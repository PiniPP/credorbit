package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/credorbit/credorbit/internal/model"
	"github.com/credorbit/credorbit/internal/netx"
	"github.com/credorbit/credorbit/internal/paths"
	"github.com/credorbit/credorbit/internal/sso"
)

// Synthetic fixture: two sessions, ~20 profiles, unrelated hand-written ones.
const (
	alphaURL  = "https://alpha-example.awsapps.com/start"
	betaURL   = "https://beta-example.awsapps.com/start"
	legacyURL = "https://legacy-example.awsapps.com/start"
	alphaTok  = "tok-alpha-0123456789"
	betaTok   = "tok-beta-0123456789"
)

const awsConfigFixture = `# my hand-written AWS config
[default]
region = eu-west-1

[sso-session alpha]
sso_start_url = https://alpha-example.awsapps.com/start
sso_region = eu-west-1
sso_registration_scopes = sso:account:access

[sso-session beta]
sso_start_url = https://beta-example.awsapps.com/start
sso_region = us-east-1

# my admin profile, keep it
[profile work-admin]
sso_session = alpha
sso_account_id = 111100000001
sso_role_name = AdministratorAccess
region = eu-west-1 ; inline comment

[profile static]
region = us-west-2
output = json

[profile p_team-dev]
sso_session = alpha
sso_account_id = 111100000001
sso_role_name = ReadOnly
region = eu-west-1

[profile p_team-prd]
sso_session = alpha
sso_account_id = 111100000002
sso_role_name = ReadOnly

[profile p_shared-stg]
sso_session = alpha
sso_account_id = 111100000003
sso_role_name = PowerUser

[profile p_data-sandbox]
sso_session = alpha
sso_account_id = 111100000004
sso_role_name = AdministratorAccess

[profile p_flaky-test]
sso_session = alpha
sso_account_id = 111100000500
sso_role_name = ReadOnly

[profile p_ops-dev]
sso_session = alpha
sso_account_id = 111100000007
sso_role_name = ReadOnly

[profile p_ops-prd]
sso_session = alpha
sso_account_id = 111100000008
sso_role_name = ReadOnly

[profile p_web-dev]
sso_session = alpha
sso_account_id = 111100000009
sso_role_name = ReadOnly

[profile p_web-stg]
sso_session = alpha
sso_account_id = 111100000010
sso_role_name = ReadOnly

[profile p_web-prd]
sso_session = alpha
sso_account_id = 111100000011
sso_role_name = ReadOnly

[profile p_revoked-dev]
sso_session = alpha
sso_account_id = 111100000012
sso_role_name = Revoked

# account removed from SSO
[profile p_gone-dev]
sso_session = alpha
sso_account_id = 111100000099
sso_role_name = ReadOnly

[profile p_beta-one]
sso_session = beta
sso_account_id = 222200000001
sso_role_name = ReadOnly

[profile p_beta-two]
sso_session = beta
sso_account_id = 222200000002
sso_role_name = ReadOnly

[profile legacy-old]
sso_start_url = https://legacy-example.awsapps.com/start
sso_region = eu-central-1
sso_account_id = 333300000001
sso_role_name = ReadOnly

[profile assume-x]
role_arn = arn:aws:iam::111100000001:role/x
source_profile = work-admin
`

const credsFixture = `# hand-written static keys, must survive
[static-keys]
aws_access_key_id = AKIAEXAMPLESTATIC01
aws_secret_access_key = static-secret-value

[flaky-test]
aws_access_key_id = ASIAOLDFLAKY0000001
aws_secret_access_key = old-flaky-secret
aws_session_token = old-flaky-token
`

type fakePortal struct {
	mu        sync.Mutex
	accounts  map[string][]model.Account // token → accounts
	roles     map[string][]string
	pageSize  int
	failList  atomic.Bool
	credCalls atomic.Int32
	credDelay time.Duration
	credHook  func() // called on each credentials request
	exp       time.Time
}

func newFakePortal() *fakePortal {
	f := &fakePortal{accounts: map[string][]model.Account{}, roles: map[string][]string{}, pageSize: 3, exp: time.Now().Add(2 * time.Hour)}
	add := func(tok, id, name string, roles ...string) {
		f.accounts[tok] = append(f.accounts[tok], model.Account{ID: id, Name: name, Email: id + "@example.invalid"})
		f.roles[id] = roles
	}
	add(alphaTok, "111100000001", "team-dev", "AdministratorAccess", "ReadOnly")
	add(alphaTok, "111100000002", "team-prd", "ReadOnly", "DevOps")
	add(alphaTok, "111100000003", "shared-stg", "PowerUser")
	add(alphaTok, "111100000004", "data-sandbox", "AdministratorAccess")
	add(alphaTok, "111100000005", "new-qa", "ReadOnly", "AdministratorAccess")
	add(alphaTok, "111100000500", "flaky-test", "ReadOnly")
	add(alphaTok, "111100000007", "ops-dev", "ReadOnly")
	add(alphaTok, "111100000008", "ops-prd", "ReadOnly")
	add(alphaTok, "111100000009", "web-dev", "ReadOnly")
	add(alphaTok, "111100000010", "web-stg", "ReadOnly")
	add(alphaTok, "111100000011", "web-prd", "ReadOnly")
	add(alphaTok, "111100000012", "revoked-dev", "ReadOnly")
	add(betaTok, "222200000001", "beta-one", "ReadOnly")
	add(betaTok, "222200000002", "beta-two", "ReadOnly")
	return f
}

func apiErr(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("x-amzn-ErrorType", code)
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"__type":%q,"message":"synthetic"}`, code)
}

func jsonResp(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func pageOf[T any](items []T, next string, size int) ([]T, any) {
	start, _ := strconv.Atoi(next)
	end := min(start+size, len(items))
	if end < len(items) {
		return items[start:end], strconv.Itoa(end)
	}
	return items[start:end], nil
}

func (f *fakePortal) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tok := r.Header.Get("x-amz-sso_bearer_token")
	f.mu.Lock()
	accts, ok := f.accounts[tok]
	f.mu.Unlock()
	if !ok {
		apiErr(w, 401, "UnauthorizedException")
		return
	}
	q := r.URL.Query()
	switch r.URL.Path {
	case "/assignment/accounts":
		if f.failList.Load() {
			apiErr(w, 500, "InternalServerException")
			return
		}
		items, next := pageOf(accts, q.Get("next_token"), f.pageSize)
		list := []map[string]string{}
		for _, a := range items {
			list = append(list, map[string]string{"accountId": a.ID, "accountName": a.Name, "emailAddress": a.Email})
		}
		jsonResp(w, map[string]any{"accountList": list, "nextToken": next})
	case "/assignment/roles":
		f.mu.Lock()
		roles := f.roles[q.Get("account_id")]
		f.mu.Unlock()
		items, next := pageOf(roles, q.Get("next_token"), 1)
		list := []map[string]string{}
		for _, rn := range items {
			list = append(list, map[string]string{"accountId": q.Get("account_id"), "roleName": rn})
		}
		jsonResp(w, map[string]any{"roleList": list, "nextToken": next})
	case "/federation/credentials":
		f.credCalls.Add(1)
		if f.credHook != nil {
			f.credHook()
		}
		if f.credDelay > 0 {
			select {
			case <-time.After(f.credDelay):
			case <-r.Context().Done():
				return
			}
		}
		id, role := q.Get("account_id"), q.Get("role_name")
		if strings.HasSuffix(id, "500") {
			apiErr(w, 500, "InternalServerException")
			return
		}
		f.mu.Lock()
		assigned := false
		for _, x := range f.roles[id] {
			assigned = assigned || x == role
		}
		f.mu.Unlock()
		if !assigned {
			apiErr(w, 403, "ForbiddenException")
			return
		}
		jsonResp(w, map[string]any{"roleCredentials": map[string]any{
			"accessKeyId": "ASIA" + id, "secretAccessKey": "secret-" + id + "-" + role, "sessionToken": "session-" + id,
			"expiration": f.exp.UnixMilli(),
		}})
	case "/logout":
		w.WriteHeader(200)
	default:
		http.NotFound(w, r)
	}
}

// fakeOIDC emulates the SSO OIDC REST API (register, device auth, token).
type fakeOIDC struct {
	refreshes atomic.Int32
	issue     string // access token to issue
}

func (o *fakeOIDC) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var in map[string]any
	_ = json.Unmarshal(body, &in)
	switch r.URL.Path {
	case "/client/register":
		jsonResp(w, map[string]any{"clientId": "cid", "clientSecret": "csecret", "clientIdIssuedAt": time.Now().Unix(), "clientSecretExpiresAt": time.Now().Add(90 * 24 * time.Hour).Unix()})
	case "/device_authorization":
		jsonResp(w, map[string]any{"deviceCode": "dev-code", "userCode": "ABCD-EFGH", "verificationUri": "https://device.example.invalid/", "verificationUriComplete": "https://device.example.invalid/?code=ABCD-EFGH", "expiresIn": 600, "interval": 1})
	case "/token":
		if in["grantType"] == "refresh_token" {
			o.refreshes.Add(1)
			if in["refreshToken"] != "refresh-ok" {
				apiErr(w, 400, "InvalidGrantException")
				return
			}
		}
		jsonResp(w, map[string]any{"accessToken": o.issue, "expiresIn": 3600, "refreshToken": "refresh-ok", "tokenType": "Bearer"})
	default:
		http.NotFound(w, r)
	}
}

// fakeUI records interactions and answers with configured values.
type fakeUI struct {
	mu        sync.Mutex
	review    bool
	confirm   *bool // nil → default
	selectIDs []string
	role      string
	reviews   []string
	confirms  []string
	prompts   []string
}

func (u *fakeUI) Confirm(_ context.Context, title, _ string, def bool) (bool, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.confirms = append(u.confirms, title)
	if u.confirm != nil {
		return *u.confirm, nil
	}
	return def, nil
}
func (u *fakeUI) ReviewPlan(_ context.Context, title, preview string) (bool, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.reviews = append(u.reviews, title+"\n"+preview)
	return u.review, nil
}
func (u *fakeUI) SelectAccounts(_ context.Context, _ string, _ []AccountRow, pre []string) ([]string, error) {
	if u.selectIDs != nil {
		return u.selectIDs, nil
	}
	return pre, nil
}
func (u *fakeUI) SelectRole(_ context.Context, a model.DiscoveredAccount, _ string) (string, error) {
	if u.role == "" {
		return "", fmt.Errorf("no role")
	}
	return u.role, nil
}
func (u *fakeUI) LoginPrompt(url, code string) {
	u.mu.Lock()
	u.prompts = append(u.prompts, url+" "+code)
	u.mu.Unlock()
}

type testEnv struct {
	t      *testing.T
	dir    string
	paths  paths.Paths
	portal *fakePortal
	srv    *httptest.Server
	oidc   *fakeOIDC
	osrv   *httptest.Server
	env    map[string]string
	now    time.Time
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	te := &testEnv{t: t, dir: dir, portal: newFakePortal(), oidc: &fakeOIDC{issue: alphaTok}, env: map[string]string{}, now: time.Now()}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(home, ".aws", "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(home, ".aws", "credentials"))
	t.Setenv("CREDORBIT_CONFIG_DIR", filepath.Join(home, ".config", "credorbit"))
	t.Setenv("AWS_CA_BUNDLE", "")
	te.paths = paths.Paths{
		Home:              home,
		AWSConfig:         filepath.Join(home, ".aws", "config"),
		AWSCredentials:    filepath.Join(home, ".aws", "credentials"),
		SSOCacheDir:       filepath.Join(home, ".aws", "sso", "cache"),
		AppConfigDir:      filepath.Join(home, ".config", "credorbit"),
		AppConfigFile:     filepath.Join(home, ".config", "credorbit", "config.yaml"),
		AppStateFile:      filepath.Join(home, ".config", "credorbit", "state.json"),
		AppLogFile:        filepath.Join(home, ".config", "credorbit", "credorbit.log"),
		ClaudeAccountFile: filepath.Join(home, ".claude-aws-account"),
		LegacyScript:      filepath.Join(home, "populate-aws-creds.sh"),
	}
	te.write(te.paths.AWSConfig, awsConfigFixture, 0o600)
	te.write(te.paths.AWSCredentials, credsFixture, 0o600)
	te.srv = httptest.NewServer(te.portal)
	t.Cleanup(te.srv.Close)
	te.osrv = httptest.NewServer(te.oidc)
	t.Cleanup(te.osrv.Close)
	te.login("alpha", alphaURL, alphaTok, time.Now().Add(8*time.Hour), "")
	te.login("beta", betaURL, betaTok, time.Now().Add(8*time.Hour), "")
	return te
}

func (te *testEnv) write(path, data string, perm os.FileMode) {
	te.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		te.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), perm); err != nil {
		te.t.Fatal(err)
	}
	_ = os.Chmod(path, perm)
}

func (te *testEnv) read(path string) string {
	te.t.Helper()
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		te.t.Fatal(err)
	}
	return string(b)
}

// login seeds the token cache.
func (te *testEnv) login(name, url, tok string, exp time.Time, refresh string) {
	te.t.Helper()
	t := model.Token{StartURL: url, Region: "eu-west-1", AccessToken: tok, ExpiresAt: exp}
	if refresh != "" {
		t.RefreshToken, t.ClientID, t.ClientSecret = refresh, "cid", "csecret"
		t.RegistrationExpiresAt = time.Now().Add(30 * 24 * time.Hour)
	}
	if err := (sso.TokenCache{Dir: te.paths.SSOCacheDir}).Save(model.SSOSession{Name: name, StartURL: url}, t); err != nil {
		te.t.Fatal(err)
	}
}

func (te *testEnv) logout(name, url string) {
	_ = (sso.TokenCache{Dir: te.paths.SSOCacheDir}).Delete(model.SSOSession{Name: name, StartURL: url})
}

func (te *testEnv) options(ui Interaction) Options {
	return Options{
		Paths:       te.paths,
		Version:     "1.2.3-test",
		Interaction: ui,
		Env: func(k string) string {
			return te.env[k]
		},
		SSOClient: func(region string, _ *http.Client) sso.Client {
			return sso.NewClientWithBackoff(region, te.srv.Client(), te.srv.URL, 2*time.Millisecond)
		},
		OIDCEndpoint:   te.osrv.URL,
		PortalEndpoint: te.srv.URL,
		Probe: func(context.Context, *http.Client, string) (netx.ProbeResult, netx.ProbeResult) {
			ok := netx.ProbeResult{URL: "https://portal.invalid/", OK: true, Status: 200}
			return ok, ok
		},
	}
}

func (te *testEnv) engine(ui Interaction) *Engine {
	te.t.Helper()
	e, err := New(te.options(ui))
	if err != nil {
		te.t.Fatal(err)
	}
	return e
}

func (te *testEnv) mustSet(e *Engine, kv ...string) {
	te.t.Helper()
	for i := 0; i+1 < len(kv); i += 2 {
		if err := e.SetSetting(kv[i], kv[i+1]); err != nil {
			te.t.Fatalf("set %s=%s: %v", kv[i], kv[i+1], err)
		}
	}
}

func bp(b bool) *bool { return &b }

func saveToken(te *testEnv, s model.SSOSession, t model.Token) error {
	return sso.TokenCache{Dir: te.paths.SSOCacheDir}.Save(s, t)
}
