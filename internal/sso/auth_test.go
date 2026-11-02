package sso

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssooidc"
	"github.com/aws/aws-sdk-go-v2/service/ssooidc/types"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/PiniPP/credorbit/internal/model"
)

// fakeOIDC is an in-memory SSO OIDC service.
type fakeOIDC struct {
	mu            sync.Mutex
	now           func() time.Time
	registrations []*ssooidc.RegisterClientInput
	starts        int
	tokenCalls    []*ssooidc.CreateTokenInput
	// deviceSeq is consumed by device_code CreateToken calls; nil entry = success.
	deviceSeq  []error
	refreshErr error
	authCode   string
	nextID     int
}

func (f *fakeOIDC) RegisterClient(_ context.Context, in *ssooidc.RegisterClientInput, _ ...func(*ssooidc.Options)) (*ssooidc.RegisterClientOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.registrations = append(f.registrations, in)
	f.nextID++
	return &ssooidc.RegisterClientOutput{
		ClientId:              aws.String("client-" + string(rune('0'+f.nextID))),
		ClientSecret:          aws.String("CLIENT-SECRET"),
		ClientSecretExpiresAt: f.now().Add(90 * 24 * time.Hour).Unix(),
	}, nil
}

func (f *fakeOIDC) StartDeviceAuthorization(_ context.Context, in *ssooidc.StartDeviceAuthorizationInput, _ ...func(*ssooidc.Options)) (*ssooidc.StartDeviceAuthorizationOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	return &ssooidc.StartDeviceAuthorizationOutput{
		DeviceCode:              aws.String("DEVICE-CODE"),
		UserCode:                aws.String("ABCD-EFGH"),
		VerificationUri:         aws.String("https://device.sso.example/"),
		VerificationUriComplete: aws.String("https://device.sso.example/?user_code=ABCD-EFGH"),
		Interval:                1,
		ExpiresIn:               600,
	}, nil
}

func (f *fakeOIDC) CreateToken(_ context.Context, in *ssooidc.CreateTokenInput, _ ...func(*ssooidc.Options)) (*ssooidc.CreateTokenOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokenCalls = append(f.tokenCalls, in)
	ok := &ssooidc.CreateTokenOutput{AccessToken: aws.String("ACCESS-" + aws.ToString(in.GrantType)), ExpiresIn: 3600,
		RefreshToken: aws.String("REFRESH-NEW")}
	switch aws.ToString(in.GrantType) {
	case grantDeviceCode:
		if len(f.deviceSeq) > 0 {
			e := f.deviceSeq[0]
			f.deviceSeq = f.deviceSeq[1:]
			if e != nil {
				return nil, e
			}
		}
		return ok, nil
	case grantAuthCode:
		if aws.ToString(in.Code) != f.authCode {
			return nil, &types.InvalidGrantException{Message: aws.String("bad code")}
		}
		return ok, nil
	case grantRefresh:
		if f.refreshErr != nil {
			return nil, f.refreshErr
		}
		ok.RefreshToken = nil // service may omit a new refresh token
		return ok, nil
	}
	return nil, &types.UnsupportedGrantTypeException{}
}

func (f *fakeOIDC) grants() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var g []string
	for _, c := range f.tokenCalls {
		g = append(g, aws.ToString(c.GrantType))
	}
	return g
}

var testSession = model.SSOSession{Name: "corp", StartURL: testStartURL, Region: "eu-west-1"}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time                    { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration)               { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }
func newClock() *clock                             { return &clock{t: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)} }
func noSleep(context.Context, time.Duration) error { return nil }

func newTestAuth(t *testing.T) (*Authenticator, *fakeOIDC, *clock) {
	t.Helper()
	clk := newClock()
	f := &fakeOIDC{now: clk.Now, authCode: "AUTH-CODE"}
	a := &Authenticator{
		Cache:   TokenCache{Dir: filepath.Join(t.TempDir(), "cache")},
		Now:     clk.Now,
		newOIDC: func(string) oidcAPI { return f },
		sleep:   noSleep,
	}
	return a, f, clk
}

// browser simulates the user's browser following the authorize URL: it first
// replays a forged callback (wrong state) which must be rejected, then the
// genuine one.
type browser struct {
	t         *testing.T
	code      string
	errParam  string
	wg        sync.WaitGroup
	mu        sync.Mutex
	authURL   *url.URL
	forged    int
	genuine   int
	genuineOK bool
	page      string
}

func (b *browser) open(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.authURL = u
	b.mu.Unlock()
	q := u.Query()
	redirect := q.Get("redirect_uri")
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		get := func(v url.Values) (int, string) {
			resp, err := http.Get(redirect + "?" + v.Encode())
			if err != nil {
				return 0, err.Error()
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			return resp.StatusCode, string(body)
		}
		st, _ := get(url.Values{"code": {"EVIL"}, "state": {"forged-state"}})
		v := url.Values{"state": {q.Get("state")}}
		if b.errParam != "" {
			v.Set("error", b.errParam)
		} else {
			v.Set("code", b.code)
		}
		st2, page := get(v)
		b.mu.Lock()
		b.forged, b.genuine, b.page = st, st2, page
		b.mu.Unlock()
	}()
	return nil
}

func TestLoginBrowserFlow(t *testing.T) {
	a, f, _ := newTestAuth(t)
	br := &browser{t: t, code: "AUTH-CODE"}
	a.OpenBrowser = br.open
	var prompts []Prompt
	a.OnPrompt = func(p Prompt) { prompts = append(prompts, p) }

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tok, err := a.Login(ctx, testSession, FlowBrowser)
	if err != nil {
		t.Fatal(err)
	}
	br.wg.Wait()

	// Authorize URL.
	q := br.authURL.Query()
	if br.authURL.Scheme != "https" || br.authURL.Host != "oidc.eu-west-1.amazonaws.com" || br.authURL.Path != "/authorize" {
		t.Errorf("authorize URL = %s", br.authURL)
	}
	for k, v := range map[string]string{"response_type": "code", "client_id": "client-1", "code_challenge_method": "S256", "scopes": "sso:account:access"} {
		if q.Get(k) != v {
			t.Errorf("authorize %s = %q, want %q", k, q.Get(k), v)
		}
	}
	redirect, _ := url.Parse(q.Get("redirect_uri"))
	if redirect.Hostname() != "127.0.0.1" || redirect.Path != callbackPath || redirect.Port() == "" {
		t.Errorf("redirect_uri = %s", redirect)
	}
	if len(q.Get("state")) < 32 {
		t.Errorf("weak state %q", q.Get("state"))
	}
	if len(prompts) != 1 || prompts[0].VerificationURL != br.authURL.String() {
		t.Errorf("prompts = %+v", prompts)
	}

	// Forged callback rejected, genuine accepted with a secret-free page.
	if br.forged != http.StatusBadRequest || br.genuine != http.StatusOK {
		t.Errorf("forged=%d genuine=%d", br.forged, br.genuine)
	}
	if !strings.Contains(br.page, "Signed in") || strings.Contains(br.page, "AUTH-CODE") {
		t.Errorf("success page: %s", br.page)
	}

	// Registration and token exchange.
	reg := f.registrations[0]
	if aws.ToString(reg.ClientName) != "credorbit" || aws.ToString(reg.ClientType) != "public" ||
		!slices.Equal(reg.GrantTypes, []string{"authorization_code", "refresh_token"}) ||
		!slices.Equal(reg.RedirectUris, []string{registeredRedirect}) || aws.ToString(reg.IssuerUrl) != testStartURL {
		t.Errorf("RegisterClient input = %+v", reg)
	}
	ct := f.tokenCalls[0]
	sum := sha256.Sum256([]byte(aws.ToString(ct.CodeVerifier)))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != q.Get("code_challenge") {
		t.Error("code_verifier does not match code_challenge")
	}
	if aws.ToString(ct.RedirectUri) != q.Get("redirect_uri") || aws.ToString(ct.Code) != "AUTH-CODE" {
		t.Errorf("CreateToken input = %+v", ct)
	}

	// Token persisted, CLI-compatible, with refresh + registration.
	cached, err := a.Cache.Load(testSession)
	if err != nil {
		t.Fatal(err)
	}
	if cached != tok || tok.RefreshToken != "REFRESH-NEW" || tok.ClientID != "client-1" || tok.ClientSecret != "CLIENT-SECRET" ||
		tok.RegistrationExpiresAt.IsZero() || tok.AccessToken != "ACCESS-authorization_code" {
		t.Errorf("token = %#v cached = %#v", tok, cached)
	}

	// The loopback listener is closed.
	if c, err := net.DialTimeout("tcp", redirect.Host, 200*time.Millisecond); err == nil {
		c.Close()
		t.Error("callback listener still open after Login")
	}
}

func TestLoginBrowserErrors(t *testing.T) {
	t.Run("authorization error", func(t *testing.T) {
		a, _, _ := newTestAuth(t)
		br := &browser{t: t, errParam: "access_denied"}
		a.OpenBrowser = br.open
		_, err := a.Login(context.Background(), testSession, FlowBrowser)
		br.wg.Wait()
		if !errors.Is(err, model.ErrUnauthorized) || !strings.Contains(err.Error(), "access_denied") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("wrong code rejected by token endpoint", func(t *testing.T) {
		a, _, _ := newTestAuth(t)
		br := &browser{t: t, code: "STALE"}
		a.OpenBrowser = br.open
		_, err := a.Login(context.Background(), testSession, FlowBrowser)
		br.wg.Wait()
		if !errors.Is(err, model.ErrUnauthorized) {
			t.Errorf("err = %v", err)
		}
		if _, lerr := a.Cache.Load(testSession); lerr == nil {
			t.Error("token cached after failed login")
		}
	})
	t.Run("timeout closes listener", func(t *testing.T) {
		a, _, _ := newTestAuth(t)
		var redirect string
		a.OnPrompt = func(p Prompt) {
			u, _ := url.Parse(p.VerificationURL)
			redirect = u.Query().Get("redirect_uri")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_, err := a.Login(ctx, testSession, FlowBrowser)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v", err)
		}
		u, _ := url.Parse(redirect)
		if c, err := net.DialTimeout("tcp", u.Host, 200*time.Millisecond); err == nil {
			c.Close()
			t.Error("listener still open")
		}
	})
}

func TestLoginDeviceFlow(t *testing.T) {
	a, f, _ := newTestAuth(t)
	f.deviceSeq = []error{
		&types.AuthorizationPendingException{},
		&types.SlowDownException{},
		&types.AuthorizationPendingException{},
		nil,
	}
	var waits []time.Duration
	var mu sync.Mutex
	a.sleep = func(_ context.Context, d time.Duration) error {
		mu.Lock()
		waits = append(waits, d)
		mu.Unlock()
		return nil
	}
	var prompt Prompt
	a.OnPrompt = func(p Prompt) { prompt = p }
	var opened string
	a.OpenBrowser = func(u string) error { opened = u; return errors.New("no browser") }

	tok, err := a.Login(context.Background(), testSession, FlowDevice)
	if err != nil {
		t.Fatal(err)
	}
	if prompt.UserCode != "ABCD-EFGH" || prompt.VerificationURL != "https://device.sso.example/?user_code=ABCD-EFGH" || opened != prompt.VerificationURL {
		t.Errorf("prompt = %+v opened = %q", prompt, opened)
	}
	want := []time.Duration{time.Second, time.Second, 6 * time.Second, 6 * time.Second}
	if !slices.Equal(waits, want) {
		t.Errorf("waits = %v, want %v", waits, want)
	}
	if tok.AccessToken != "ACCESS-"+grantDeviceCode || tok.RefreshToken != "REFRESH-NEW" {
		t.Errorf("token = %#v", tok)
	}
	reg := f.registrations[0]
	if !slices.Contains(reg.GrantTypes, grantDeviceCode) || len(reg.RedirectUris) != 0 {
		t.Errorf("device registration = %+v", reg)
	}
	if _, err := a.Cache.Load(testSession); err != nil {
		t.Errorf("token not cached: %v", err)
	}
}

func TestLoginDeviceFailures(t *testing.T) {
	tests := []struct {
		name string
		seq  []error
		want error
	}{
		{"expired", []error{&types.AuthorizationPendingException{}, &types.ExpiredTokenException{}}, model.ErrUnauthorized},
		{"denied", []error{&types.AccessDeniedException{}}, model.ErrCanceled},
		{"network", []error{&smithyhttp.RequestSendError{Err: errors.New("connection reset")}}, model.ErrTransient},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, f, _ := newTestAuth(t)
			f.deviceSeq = tt.seq
			_, err := a.Login(context.Background(), testSession, FlowDevice)
			if !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
		})
	}

	t.Run("deadline from ExpiresIn", func(t *testing.T) {
		a, f, clk := newTestAuth(t)
		pending := make([]error, 100)
		for i := range pending {
			pending[i] = &types.AuthorizationPendingException{}
		}
		f.deviceSeq = pending
		a.sleep = func(context.Context, time.Duration) error { clk.Add(2 * time.Minute); return nil }
		_, err := a.Login(context.Background(), testSession, FlowDevice)
		if !errors.Is(err, model.ErrUnauthorized) || !strings.Contains(err.Error(), "expired") {
			t.Errorf("err = %v", err)
		}
	})

	t.Run("context canceled while polling", func(t *testing.T) {
		a, f, _ := newTestAuth(t)
		f.deviceSeq = []error{&types.AuthorizationPendingException{}}
		ctx, cancel := context.WithCancel(context.Background())
		a.sleep = nil // real timer, interrupted by cancel
		a.OnPrompt = func(Prompt) { cancel() }
		_, err := a.Login(ctx, testSession, FlowDevice)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestRegistrationCaching(t *testing.T) {
	a, f, clk := newTestAuth(t)
	login := func(s model.SSOSession) {
		t.Helper()
		if _, err := a.Login(context.Background(), s, FlowDevice); err != nil {
			t.Fatal(err)
		}
	}
	login(testSession)
	login(testSession)
	if len(f.registrations) != 1 {
		t.Fatalf("registrations = %d, want 1 (cached)", len(f.registrations))
	}
	p := a.registrationPath(testSession, FlowDevice)
	if !strings.HasPrefix(filepath.Base(p), "credorbit-client-") {
		t.Errorf("registration file = %s", p)
	}
	if runtime.GOOS != "windows" {
		if st, err := os.Stat(p); err != nil || st.Mode().Perm() != 0o600 {
			t.Errorf("registration file stat = %v, %v", st, err)
		}
	}

	// Different flow → separate registration.
	if a.registrationPath(testSession, FlowBrowser) == p {
		t.Error("browser and device share a registration file")
	}

	// Scope change → re-register.
	s2 := testSession
	s2.Scopes = "sso:account:access codewhisperer:completions"
	login(s2)
	if len(f.registrations) != 2 {
		t.Fatalf("registrations = %d after scope change", len(f.registrations))
	}

	// Within 1h of expiry → re-register.
	clk.Add(90*24*time.Hour - 30*time.Minute)
	login(s2)
	if len(f.registrations) != 3 {
		t.Fatalf("registrations = %d near expiry", len(f.registrations))
	}
}

func saveToken(t *testing.T, a *Authenticator, tok model.Token) {
	t.Helper()
	if err := a.Cache.Save(testSession, tok); err != nil {
		t.Fatal(err)
	}
}

func TestValid(t *testing.T) {
	base := func(clk *clock) model.Token {
		return model.Token{
			StartURL: testStartURL, Region: "eu-west-1", AccessToken: "OLD", ExpiresAt: clk.Now().Add(-time.Minute),
			RefreshToken: "REFRESH-OLD", ClientID: "cid", ClientSecret: "csecret", RegistrationExpiresAt: clk.Now().Add(24 * time.Hour),
		}
	}
	t.Run("fresh token returned as is", func(t *testing.T) {
		a, f, clk := newTestAuth(t)
		tok := base(clk)
		tok.ExpiresAt = clk.Now().Add(time.Hour)
		saveToken(t, a, tok)
		got, err := a.Valid(context.Background(), testSession)
		if err != nil || got.AccessToken != "OLD" || len(f.grants()) != 0 {
			t.Errorf("got %v err %v grants %v", got, err, f.grants())
		}
	})
	t.Run("expired is refreshed and saved", func(t *testing.T) {
		a, f, clk := newTestAuth(t)
		saveToken(t, a, base(clk))
		got, err := a.Valid(context.Background(), testSession)
		if err != nil {
			t.Fatal(err)
		}
		if got.AccessToken != "ACCESS-refresh_token" || got.RefreshToken != "REFRESH-OLD" || !got.ExpiresAt.Equal(clk.Now().Add(time.Hour)) {
			t.Errorf("refreshed = %#v", got)
		}
		in := f.tokenCalls[0]
		if aws.ToString(in.RefreshToken) != "REFRESH-OLD" || aws.ToString(in.ClientId) != "cid" {
			t.Errorf("refresh input = %+v", in)
		}
		cached, _ := a.Cache.Load(testSession)
		if cached != got {
			t.Errorf("cache not updated: %#v", cached)
		}
	})
	t.Run("concurrent callers refresh once", func(t *testing.T) {
		a, f, clk := newTestAuth(t)
		saveToken(t, a, base(clk))
		var wg sync.WaitGroup
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := a.Valid(context.Background(), testSession); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		if n := len(f.grants()); n != 1 {
			t.Errorf("refresh calls = %d, want 1", n)
		}
	})

	failures := []struct {
		name   string
		mutate func(*model.Token, *fakeOIDC, *clock)
		want   error
	}{
		{"missing refresh token", func(tk *model.Token, _ *fakeOIDC, _ *clock) { tk.RefreshToken = "" }, model.ErrUnauthorized},
		{"registration expired", func(tk *model.Token, _ *fakeOIDC, c *clock) { tk.RegistrationExpiresAt = c.Now().Add(-time.Second) }, model.ErrUnauthorized},
		{"refresh rejected", func(_ *model.Token, f *fakeOIDC, _ *clock) {
			f.refreshErr = &types.InvalidGrantException{}
		}, model.ErrUnauthorized},
		{"unclassified refresh error", func(_ *model.Token, f *fakeOIDC, _ *clock) {
			f.refreshErr = &types.InvalidRequestException{}
		}, model.ErrUnauthorized},
		{"refresh network failure", func(_ *model.Token, f *fakeOIDC, _ *clock) {
			f.refreshErr = &smithyhttp.RequestSendError{Err: errors.New("dial tcp: i/o timeout")}
		}, model.ErrTransient},
	}
	for _, tt := range failures {
		t.Run(tt.name, func(t *testing.T) {
			a, f, clk := newTestAuth(t)
			tok := base(clk)
			tt.mutate(&tok, f, clk)
			saveToken(t, a, tok)
			_, err := a.Valid(context.Background(), testSession)
			if !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
			if err != nil && (strings.Contains(err.Error(), "REFRESH-OLD") || strings.Contains(err.Error(), "csecret")) {
				t.Errorf("error leaks secret: %v", err)
			}
		})
	}
	t.Run("no cached token", func(t *testing.T) {
		a, _, _ := newTestAuth(t)
		if _, err := a.Valid(context.Background(), testSession); !errors.Is(err, model.ErrUnauthorized) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestLogout(t *testing.T) {
	a, _, clk := newTestAuth(t)
	fp := newFakePortal(0)
	srv := httptest.NewServer(fp)
	defer srv.Close()
	a.portalEndpoint = srv.URL
	a.HTTP = srv.Client()

	// Nothing cached: fine.
	if err := a.Logout(context.Background(), testSession); err != nil {
		t.Fatal(err)
	}
	saveToken(t, a, model.Token{StartURL: testStartURL, AccessToken: testToken, ExpiresAt: clk.Now().Add(time.Hour)})
	if err := a.Logout(context.Background(), testSession); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.Cache.Path(testSession)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("token file still present: %v", err)
	}
	if fp.logouts.Load() != 1 {
		t.Errorf("server logout calls = %d", fp.logouts.Load())
	}
}

func TestLoginValidation(t *testing.T) {
	a, _, _ := newTestAuth(t)
	for _, s := range []model.SSOSession{
		{Name: "x", Region: "eu-west-1"},
		{Name: "x", StartURL: "http://insecure.example/start", Region: "eu-west-1"},
		{Name: "x", StartURL: testStartURL},
	} {
		if _, err := a.Login(context.Background(), s, FlowDevice); err == nil {
			t.Errorf("Login(%+v) succeeded", s)
		}
	}
	if _, err := a.Login(context.Background(), testSession, Flow("carrier-pigeon")); err == nil {
		t.Error("unknown flow accepted")
	}
	if f, err := ParseFlow(""); err != nil || f != FlowBrowser {
		t.Errorf("ParseFlow(\"\") = %v, %v", f, err)
	}
	if _, err := ParseFlow("nope"); err == nil {
		t.Error("ParseFlow accepted junk")
	}
	if OIDCEndpoint("cn-north-1") != "https://oidc.cn-north-1.amazonaws.com.cn" {
		t.Error("China endpoint")
	}
}
