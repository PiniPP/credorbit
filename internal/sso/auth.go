package sso

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssooidc"
	"github.com/aws/smithy-go"

	"github.com/PiniPP/credorbit/internal/fsutil"
	"github.com/PiniPP/credorbit/internal/model"
)

// Flow selects the OIDC login flow.
type Flow string

const (
	// FlowBrowser is the authorization code grant with PKCE on a loopback redirect.
	FlowBrowser Flow = "browser"
	// FlowDevice is the device authorization grant (works without a local browser).
	FlowDevice Flow = "device"
)

// ParseFlow validates a flow name ("" → browser).
func ParseFlow(s string) (Flow, error) {
	switch Flow(strings.ToLower(strings.TrimSpace(s))) {
	case "", FlowBrowser:
		return FlowBrowser, nil
	case FlowDevice:
		return FlowDevice, nil
	}
	return "", fmt.Errorf("unknown login flow %q (want browser|device)", s)
}

// Prompt is what the user must see to complete a login. It never contains tokens.
type Prompt struct {
	VerificationURL string
	UserCode        string // device flow only
}

const (
	clientName      = "credorbit"
	clientType      = "public"
	defaultScope    = "sso:account:access"
	grantAuthCode   = "authorization_code"
	grantRefresh    = "refresh_token"
	grantDeviceCode = "urn:ietf:params:oauth:grant-type:device_code"
	callbackPath    = "/oauth/callback"
	// registeredRedirect is registered without a port: per RFC 8252 §7.3 the
	// authorization server accepts any port on a loopback redirect, which lets one
	// cached registration serve every login (each uses a fresh ephemeral port).
	registeredRedirect = "http://127.0.0.1" + callbackPath
	// registrationSkew: a cached registration is replaced this long before expiry.
	registrationSkew = time.Hour
	slowDownStep     = 5 * time.Second
)

// oidcAPI is the subset of the SSO OIDC SDK client used here (injectable for tests).
type oidcAPI interface {
	RegisterClient(ctx context.Context, in *ssooidc.RegisterClientInput, optFns ...func(*ssooidc.Options)) (*ssooidc.RegisterClientOutput, error)
	StartDeviceAuthorization(ctx context.Context, in *ssooidc.StartDeviceAuthorizationInput, optFns ...func(*ssooidc.Options)) (*ssooidc.StartDeviceAuthorizationOutput, error)
	CreateToken(ctx context.Context, in *ssooidc.CreateTokenInput, optFns ...func(*ssooidc.Options)) (*ssooidc.CreateTokenOutput, error)
}

// Authenticator obtains, refreshes and removes SSO tokens. Use it through a
// pointer; the zero value plus Cache is usable. Safe for concurrent use.
type Authenticator struct {
	HTTP  *http.Client
	Cache TokenCache
	// OpenBrowser opens a URL in the user's browser; nil → the URL is only shown
	// via OnPrompt.
	OpenBrowser func(url string) error
	// OnPrompt is called with what the user must see (never contains tokens).
	OnPrompt func(Prompt)
	// Now defaults to time.Now.
	Now func() time.Time

	// OIDCEndpoint overrides the SSO OIDC endpoint (tests, VPC/FIPS endpoints);
	// "" uses the standard regional endpoint.
	OIDCEndpoint string
	// PortalEndpoint overrides the SSO portal endpoint used for logout; "" = default.
	PortalEndpoint string

	// Test/advanced hooks.
	newOIDC        func(region string) oidcAPI                      // nil → SDK client
	sleep          func(ctx context.Context, d time.Duration) error // nil → timer
	portalEndpoint string                                           // "" → default SSO portal endpoint

	refreshMu sync.Mutex // serializes refreshes so a refresh token is used once
}

func (a *Authenticator) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *Authenticator) oidc(region string) oidcAPI {
	if a.newOIDC != nil {
		return a.newOIDC(region)
	}
	o := ssooidc.Options{
		Region:      region,
		Credentials: aws.AnonymousCredentials{},
		Retryer:     newRetryer(defaultMaxBackoff),
	}
	if a.HTTP != nil {
		o.HTTPClient = a.HTTP
	}
	if a.OIDCEndpoint != "" {
		o.BaseEndpoint = aws.String(strings.TrimRight(a.OIDCEndpoint, "/"))
	}
	return ssooidc.New(o)
}

func (a *Authenticator) wait(ctx context.Context, d time.Duration) error {
	if a.sleep != nil {
		return a.sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (a *Authenticator) prompt(p Prompt) {
	if a.OnPrompt != nil {
		a.OnPrompt(p)
	}
}

func (a *Authenticator) open(u string) {
	if a.OpenBrowser != nil {
		// Failure is fine: the URL was already shown through OnPrompt.
		_ = a.OpenBrowser(u)
	}
}

// ParseScopes splits a comma/space separated scope list; empty → sso:account:access.
func ParseScopes(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' }) {
		if !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		out = []string{defaultScope}
	}
	return out
}

func validateSession(s model.SSOSession) error {
	if s.StartURL == "" || s.Region == "" {
		return fmt.Errorf("sso session %q needs sso_start_url and sso_region", s.Name)
	}
	u, err := url.Parse(s.StartURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("sso session %q: start URL must be an https URL", s.Name)
	}
	return nil
}

// Login runs the OIDC flow f for s, persists the token (with refresh token and
// client registration, AWS CLI compatible) and returns it. Bound the wait with
// ctx (e.g. a 5-minute timeout).
func (a *Authenticator) Login(ctx context.Context, s model.SSOSession, f Flow) (model.Token, error) {
	if err := validateSession(s); err != nil {
		return model.Token{}, err
	}
	if f == "" {
		f = FlowBrowser
	}
	if f != FlowBrowser && f != FlowDevice {
		return model.Token{}, fmt.Errorf("unknown login flow %q", f)
	}
	api := a.oidc(s.Region)
	scopes := ParseScopes(s.Scopes)
	reg, cached, err := a.registration(ctx, api, s, f, scopes)
	if err != nil {
		return model.Token{}, err
	}

	var out *ssooidc.CreateTokenOutput
	if f == FlowBrowser {
		out, err = a.browserFlow(ctx, api, s, reg, scopes)
	} else {
		out, err = a.deviceFlow(ctx, api, s, reg)
	}
	if err != nil {
		if cached && hasCode(err, "InvalidClientException") {
			// The cached registration was revoked server-side; drop it so the
			// next attempt registers afresh.
			_ = os.Remove(a.registrationPath(s, f))
		}
		return model.Token{}, err
	}
	tok := a.tokenFrom(s, out, reg)
	if err := a.Cache.Save(s, tok); err != nil {
		return model.Token{}, err
	}
	return tok, nil
}

func (a *Authenticator) tokenFrom(s model.SSOSession, out *ssooidc.CreateTokenOutput, reg clientRegistration) model.Token {
	return model.Token{
		StartURL:              s.StartURL,
		Region:                s.Region,
		AccessToken:           aws.ToString(out.AccessToken),
		ExpiresAt:             a.now().Add(time.Duration(out.ExpiresIn) * time.Second).UTC().Truncate(time.Second),
		RefreshToken:          aws.ToString(out.RefreshToken),
		ClientID:              reg.ClientID,
		ClientSecret:          reg.ClientSecret,
		RegistrationExpiresAt: reg.expires(),
	}
}

// ---- client registration cache ----

// clientRegistration is CredOrbit's cached OIDC client registration. The
// client secret is a credential, hence 0600 like the token cache.
type clientRegistration struct {
	ClientID     string   `json:"clientId"`
	ClientSecret string   `json:"clientSecret"`
	ExpiresAt    string   `json:"expiresAt"`
	Scopes       []string `json:"scopes"`
	Flow         Flow     `json:"flow"`
}

func (r clientRegistration) expires() time.Time {
	t, _ := parseCacheTime(r.ExpiresAt)
	return t
}

func (a *Authenticator) registrationPath(s model.SSOSession, f Flow) string {
	key := strings.Join([]string{s.Name, s.StartURL, s.Region, string(f)}, "|")
	return filepath.Join(fsutil.ExpandHome(a.Cache.Dir), "credorbit-client-"+sha1Hex(key)+".json")
}

// registration returns a cached registration valid for at least another hour
// with the same scopes, or registers a new client. cached reports reuse.
func (a *Authenticator) registration(ctx context.Context, api oidcAPI, s model.SSOSession, f Flow, scopes []string) (clientRegistration, bool, error) {
	p := a.registrationPath(s, f)
	if data, err := os.ReadFile(p); err == nil {
		var r clientRegistration
		if json.Unmarshal(data, &r) == nil && r.ClientID != "" && r.ClientSecret != "" &&
			slices.Equal(r.Scopes, scopes) && r.expires().After(a.now().Add(registrationSkew)) {
			return r, true, nil
		}
	}

	in := &ssooidc.RegisterClientInput{
		ClientName: aws.String(clientName),
		ClientType: aws.String(clientType),
		Scopes:     scopes,
		IssuerUrl:  aws.String(s.StartURL),
	}
	if f == FlowBrowser {
		in.GrantTypes = []string{grantAuthCode, grantRefresh}
		in.RedirectUris = []string{registeredRedirect}
	} else {
		in.GrantTypes = []string{grantDeviceCode, grantRefresh}
	}
	out, err := api.RegisterClient(ctx, in)
	if err != nil {
		return clientRegistration{}, false, mapErr(ctx, "RegisterClient", err)
	}
	if aws.ToString(out.ClientId) == "" || aws.ToString(out.ClientSecret) == "" {
		return clientRegistration{}, false, fmt.Errorf("sso RegisterClient: empty client credentials in response: %w", model.ErrTransient)
	}
	exp := time.Unix(out.ClientSecretExpiresAt, 0)
	if out.ClientSecretExpiresAt == 0 {
		exp = a.now().Add(24 * time.Hour) // unknown expiry: reuse briefly
	}
	r := clientRegistration{
		ClientID:     aws.ToString(out.ClientId),
		ClientSecret: aws.ToString(out.ClientSecret),
		ExpiresAt:    formatCacheTime(exp),
		Scopes:       scopes,
		Flow:         f,
	}
	// A cache write failure only costs a re-registration next time.
	_ = writeJSON(p, r)
	return r, false, nil
}

// ---- browser flow (authorization code + PKCE) ----

type callbackResult struct {
	code string
	err  error
}

func randomURLSafe(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate random value: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// OIDCEndpoint returns the SSO OIDC base URL for region.
func OIDCEndpoint(region string) string {
	suffix := "amazonaws.com"
	if strings.HasPrefix(region, "cn-") {
		suffix = "amazonaws.com.cn"
	}
	return "https://oidc." + region + "." + suffix
}

func (a *Authenticator) browserFlow(ctx context.Context, api oidcAPI, s model.SSOSession, reg clientRegistration, scopes []string) (*ssooidc.CreateTokenOutput, error) {
	verifier, err := randomURLSafe(32)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	state, err := randomURLSafe(32)
	if err != nil {
		return nil, err
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen on loopback for the login callback: %w", err)
	}
	redirect := fmt.Sprintf("http://127.0.0.1:%d%s", ln.Addr().(*net.TCPAddr).Port, callbackPath)

	results := make(chan callbackResult, 1)
	var once sync.Once
	deliver := func(r callbackResult) { once.Do(func() { results <- r }) }

	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		got := q.Get("state")
		if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(state)) != 1 {
			// Not our request (stale tab, another app, forgery): reject it but keep
			// waiting for the genuine callback.
			writePage(w, http.StatusBadRequest, false, "This sign-in link is invalid or has expired. Return to the terminal and try again.")
			return
		}
		if e := q.Get("error"); e != "" {
			writePage(w, http.StatusOK, false, "Sign-in was not completed. You can close this tab and return to the terminal.")
			deliver(callbackResult{err: fmt.Errorf("authorization failed: %s: %w", sanitizeOAuthError(e), model.ErrUnauthorized)})
			return
		}
		code := q.Get("code")
		if code == "" {
			writePage(w, http.StatusBadRequest, false, "The sign-in response was incomplete. Return to the terminal and try again.")
			deliver(callbackResult{err: fmt.Errorf("authorization callback without code: %w", model.ErrUnauthorized)})
			return
		}
		writePage(w, http.StatusOK, true, "You are signed in. You can close this tab and return to the terminal.")
		deliver(callbackResult{code: code})
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = srv.Serve(ln)
	}()
	defer func() {
		// Give the success page a moment to flush, then stop; Close also closes ln.
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if srv.Shutdown(sctx) != nil {
			_ = srv.Close()
		}
		<-served
	}()

	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", reg.ClientID)
	q.Set("redirect_uri", redirect)
	q.Set("state", state)
	q.Set("code_challenge_method", "S256")
	q.Set("scopes", strings.Join(scopes, " "))
	q.Set("code_challenge", challenge)
	authURL := OIDCEndpoint(s.Region) + "/authorize?" + q.Encode()

	a.prompt(Prompt{VerificationURL: authURL})
	a.open(authURL)

	var res callbackResult
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("waiting for browser sign-in: %w", ctx.Err())
	case res = <-results:
	}
	if res.err != nil {
		return nil, res.err
	}
	out, err := api.CreateToken(ctx, &ssooidc.CreateTokenInput{
		ClientId:     aws.String(reg.ClientID),
		ClientSecret: aws.String(reg.ClientSecret),
		GrantType:    aws.String(grantAuthCode),
		Code:         aws.String(res.code),
		CodeVerifier: aws.String(verifier),
		RedirectUri:  aws.String(redirect),
	})
	if err != nil {
		return nil, mapErr(ctx, "CreateToken", err)
	}
	return checkTokenOutput(out)
}

func checkTokenOutput(out *ssooidc.CreateTokenOutput) (*ssooidc.CreateTokenOutput, error) {
	if out == nil || aws.ToString(out.AccessToken) == "" {
		return nil, fmt.Errorf("sso CreateToken: no access token in response: %w", model.ErrTransient)
	}
	return out, nil
}

// sanitizeOAuthError keeps only a short token-like error code from the redirect.
func sanitizeOAuthError(e string) string {
	var b strings.Builder
	for _, r := range e {
		if b.Len() >= 64 {
			break
		}
		if r == '_' || r == '-' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "unknown_error"
	}
	return b.String()
}

func writePage(w http.ResponseWriter, status int, ok bool, msg string) {
	title, accent := "Sign-in failed", "#c0392b"
	if ok {
		title, accent = "Signed in", "#1e8e5a"
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	h.Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>CredOrbit — %[1]s</title>
<style>body{font-family:system-ui,-apple-system,Segoe UI,sans-serif;background:#0f1424;color:#e8ecf5;display:flex;align-items:center;justify-content:center;height:100vh;margin:0}
.card{background:#182039;border-radius:14px;padding:36px 44px;max-width:460px;box-shadow:0 8px 30px rgba(0,0,0,.35);border-top:4px solid %[2]s}
h1{margin:0 0 6px;font-size:22px}.brand{color:#8ea2d8;font-size:13px;letter-spacing:.08em;text-transform:uppercase;margin-bottom:18px}p{line-height:1.5}</style></head>
<body><div class="card"><div class="brand">CredOrbit · Your Cloud Access. In Orbit.</div><h1>%[1]s</h1><p>%[3]s</p></div></body></html>`,
		html.EscapeString(title), accent, html.EscapeString(msg))
}

// ---- device flow ----

func (a *Authenticator) deviceFlow(ctx context.Context, api oidcAPI, s model.SSOSession, reg clientRegistration) (*ssooidc.CreateTokenOutput, error) {
	da, err := api.StartDeviceAuthorization(ctx, &ssooidc.StartDeviceAuthorizationInput{
		ClientId:     aws.String(reg.ClientID),
		ClientSecret: aws.String(reg.ClientSecret),
		StartUrl:     aws.String(s.StartURL),
	})
	if err != nil {
		return nil, mapErr(ctx, "StartDeviceAuthorization", err)
	}
	verify := aws.ToString(da.VerificationUriComplete)
	if verify == "" {
		verify = aws.ToString(da.VerificationUri)
	}
	a.prompt(Prompt{VerificationURL: verify, UserCode: aws.ToString(da.UserCode)})
	a.open(verify)

	interval := time.Duration(da.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	var deadline time.Time
	if da.ExpiresIn > 0 {
		deadline = a.now().Add(time.Duration(da.ExpiresIn) * time.Second)
	}
	for {
		if err := a.wait(ctx, interval); err != nil {
			return nil, fmt.Errorf("waiting for device authorization: %w", err)
		}
		out, err := api.CreateToken(ctx, &ssooidc.CreateTokenInput{
			ClientId:     aws.String(reg.ClientID),
			ClientSecret: aws.String(reg.ClientSecret),
			GrantType:    aws.String(grantDeviceCode),
			DeviceCode:   da.DeviceCode,
		})
		switch {
		case err == nil:
			return checkTokenOutput(out)
		case hasCode(err, "AuthorizationPendingException"):
		case hasCode(err, "SlowDownException"):
			interval += slowDownStep
		case hasCode(err, "ExpiredTokenException"):
			return nil, fmt.Errorf("the device code expired before sign-in was approved: %w", model.ErrUnauthorized)
		case hasCode(err, "AccessDeniedException"):
			return nil, fmt.Errorf("sign-in was denied in the browser: %w", model.ErrCanceled)
		default:
			return nil, mapErr(ctx, "CreateToken", err)
		}
		if !deadline.IsZero() && !a.now().Before(deadline) {
			return nil, fmt.Errorf("the device code expired before sign-in was approved: %w", model.ErrUnauthorized)
		}
	}
}

func hasCode(err error, code string) bool {
	var ae smithy.APIError
	return errors.As(err, &ae) && ae.ErrorCode() == code
}

// ---- Valid / Logout ----

// Valid returns the cached token for s, refreshing it with the refresh token
// when it has expired. It wraps model.ErrUnauthorized when a new login is
// required and model.ErrTransient when a refresh failed for network reasons.
func (a *Authenticator) Valid(ctx context.Context, s model.SSOSession) (model.Token, error) {
	tok, err := a.Cache.Load(s)
	if err != nil {
		return model.Token{}, err
	}
	if !tok.Expired(a.now()) {
		return tok, nil
	}

	a.refreshMu.Lock()
	defer a.refreshMu.Unlock()
	// Another goroutine may have refreshed while we waited.
	if t2, err := a.Cache.Load(s); err == nil && !t2.Expired(a.now()) {
		return t2, nil
	} else if err == nil {
		tok = t2
	}
	if tok.RefreshToken == "" || tok.ClientID == "" || tok.ClientSecret == "" {
		return model.Token{}, fmt.Errorf("SSO token for %q expired and cannot be refreshed: %w", CacheKey(s), model.ErrUnauthorized)
	}
	if !tok.RegistrationExpiresAt.IsZero() && !tok.RegistrationExpiresAt.After(a.now()) {
		return model.Token{}, fmt.Errorf("SSO token for %q expired and its client registration too: %w", CacheKey(s), model.ErrUnauthorized)
	}
	region := s.Region
	if region == "" {
		region = tok.Region
	}
	out, err := a.oidc(region).CreateToken(ctx, &ssooidc.CreateTokenInput{
		ClientId:     aws.String(tok.ClientID),
		ClientSecret: aws.String(tok.ClientSecret),
		GrantType:    aws.String(grantRefresh),
		RefreshToken: aws.String(tok.RefreshToken),
	})
	if err != nil {
		err = mapErr(ctx, "CreateToken(refresh)", err)
		if errors.Is(err, model.ErrTransient) || ctx.Err() != nil {
			return model.Token{}, err
		}
		if !errors.Is(err, model.ErrUnauthorized) {
			err = fmt.Errorf("%w: %w", model.ErrUnauthorized, err)
		}
		return model.Token{}, fmt.Errorf("refresh SSO token for %q: %w", CacheKey(s), err)
	}
	if out, err = checkTokenOutput(out); err != nil {
		return model.Token{}, err
	}
	tok.AccessToken = aws.ToString(out.AccessToken)
	tok.ExpiresAt = a.now().Add(time.Duration(out.ExpiresIn) * time.Second).UTC().Truncate(time.Second)
	if rt := aws.ToString(out.RefreshToken); rt != "" {
		tok.RefreshToken = rt
	}
	if err := a.Cache.Save(s, tok); err != nil {
		return model.Token{}, err
	}
	return tok, nil
}

// Logout removes the cached token for s and, best effort, invalidates it
// server-side. A missing token is not an error.
func (a *Authenticator) Logout(ctx context.Context, s model.SSOSession) error {
	tok, loadErr := a.Cache.Load(s)
	if err := a.Cache.Delete(s); err != nil {
		return err
	}
	if loadErr == nil && !tok.Expired(a.now()) {
		region := s.Region
		if region == "" {
			region = tok.Region
		}
		lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		// Logout is a courtesy: short timeout and backoff, errors ignored.
		ep := a.PortalEndpoint
		if ep == "" {
			ep = a.portalEndpoint
		}
		p := newPortal(region, a.HTTP, ep, time.Second)
		_ = p.logout(lctx, tok.AccessToken)
	}
	return nil
}
