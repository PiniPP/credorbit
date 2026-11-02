package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/credorbit/credorbit/internal/engine"
	"github.com/credorbit/credorbit/internal/model"
	"github.com/credorbit/credorbit/internal/netx"
	"github.com/credorbit/credorbit/internal/sso"
)

const (
	startURL = "https://cli-example.awsapps.com/start"
	token    = "tok-cli-0123456789"
)

const cliConfig = `[sso-session demo]
sso_start_url = https://cli-example.awsapps.com/start
sso_region = eu-west-1

# hand-written
[profile p_app-dev]
sso_session = demo
sso_account_id = 555500000001
sso_role_name = ReadOnly

[profile p_app-prd]
sso_session = demo
sso_account_id = 555500000002
sso_role_name = ReadOnly

[profile p_broken-test]
sso_session = demo
sso_account_id = 555500000500
sso_role_name = ReadOnly

[profile p_gone-dev]
sso_session = demo
sso_account_id = 555500000099
sso_role_name = ReadOnly
`

type portal struct{}

func (portal) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("x-amz-sso_bearer_token") != token {
		w.Header().Set("x-amzn-ErrorType", "UnauthorizedException")
		w.WriteHeader(401)
		_, _ = io.WriteString(w, `{"message":"no"}`)
		return
	}
	q := r.URL.Query()
	switch r.URL.Path {
	case "/assignment/accounts":
		_, _ = io.WriteString(w, `{"accountList":[
			{"accountId":"555500000001","accountName":"app-dev"},
			{"accountId":"555500000002","accountName":"app-prd"},
			{"accountId":"555500000500","accountName":"broken-test"},
			{"accountId":"555500000003","accountName":"new-stg"}]}`)
	case "/assignment/roles":
		_, _ = fmt.Fprintf(w, `{"roleList":[{"accountId":%q,"roleName":"ReadOnly"}]}`, q.Get("account_id"))
	case "/federation/credentials":
		if strings.HasSuffix(q.Get("account_id"), "500") {
			w.Header().Set("x-amzn-ErrorType", "InternalServerException")
			w.WriteHeader(500)
			_, _ = io.WriteString(w, `{"message":"boom"}`)
			return
		}
		_, _ = fmt.Fprintf(w, `{"roleCredentials":{"accessKeyId":"ASIA%s","secretAccessKey":"s","sessionToken":"t","expiration":%d}}`,
			q.Get("account_id"), time.Now().Add(time.Hour).UnixMilli())
	default:
		http.NotFound(w, r)
	}
}

type cliEnv struct {
	t    *testing.T
	home string
}

func newCLIEnv(t *testing.T) *cliEnv {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(home, ".aws", "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(home, ".aws", "credentials"))
	t.Setenv("CREDORBIT_CONFIG_DIR", filepath.Join(home, "cfg"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	for _, k := range []string{"SSO_SESSION", "FORCE_LOGIN", "PARALLEL", "CREDS_FILE", "SYNC", "ROLE_UPDATE", "FALLBACK_ROLES", "AWS_CA_BUNDLE", "AWS_PROFILE"} {
		t.Setenv(k, "")
	}
	ce := &cliEnv{t: t, home: home}
	ce.write(".aws/config", cliConfig)
	ce.write(".aws/credentials", "[static]\naws_access_key_id = AKIAEXAMPLE0000001\naws_secret_access_key = x\n")
	ce.login()
	srv := httptest.NewServer(portal{})
	t.Cleanup(srv.Close)
	engineHook = func(o *engine.Options) {
		o.SSOClient = func(region string, _ *http.Client) sso.Client {
			return sso.NewClientWithBackoff(region, srv.Client(), srv.URL, time.Millisecond)
		}
		o.PortalEndpoint = srv.URL
		o.OpenBrowser = func(string) error { return nil }
		o.Probe = func(context.Context, *http.Client, string) (netx.ProbeResult, netx.ProbeResult) {
			r := netx.ProbeResult{OK: true}
			return r, r
		}
	}
	isTTY = func(io.Writer) bool { return false }
	stdin = strings.NewReader("")
	t.Cleanup(func() { engineHook = nil; RunUI = nil })
	return ce
}

func (ce *cliEnv) write(rel, data string) {
	p := filepath.Join(ce.home, rel)
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		ce.t.Fatal(err)
	}
}

func (ce *cliEnv) read(rel string) string {
	b, _ := os.ReadFile(filepath.Join(ce.home, rel))
	return string(b)
}

func (ce *cliEnv) login() {
	err := sso.TokenCache{Dir: filepath.Join(ce.home, ".aws", "sso", "cache")}.Save(model.SSOSession{Name: "demo", StartURL: startURL},
		model.Token{StartURL: startURL, Region: "eu-west-1", AccessToken: token, ExpiresAt: time.Now().Add(8 * time.Hour)})
	if err != nil {
		ce.t.Fatal(err)
	}
}

func (ce *cliEnv) logout() {
	_ = sso.TokenCache{Dir: filepath.Join(ce.home, ".aws", "sso", "cache")}.Delete(model.SSOSession{Name: "demo"})
}

func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Execute(context.Background(), args, &out, &errb)
	return code, out.String(), errb.String()
}

func keysOf(t *testing.T, raw string) []string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("not a JSON object: %v\n%s", err, raw)
	}
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

func hasKeys(t *testing.T, raw string, want ...string) {
	t.Helper()
	got := map[string]bool{}
	for _, k := range keysOf(t, raw) {
		got[k] = true
	}
	for _, k := range want {
		if !got[k] {
			t.Errorf("JSON lacks %q: %s", k, raw)
		}
	}
}

func TestUsageAndBasics(t *testing.T) {
	newCLIEnv(t)
	if code, out, _ := run(t, "version"); code != 0 || !strings.HasPrefix(out, "credorbit ") {
		t.Errorf("version: %d %q", code, out)
	}
	if code, out, _ := run(t, "version", "--json"); code != 0 {
		t.Errorf("version json: %d", code)
	} else {
		hasKeys(t, out, "version", "os", "arch", "go")
	}
	for _, args := range [][]string{{"bogus"}, {"export", "--nope"}, {"export", "--policy", "bogus"}, {"export", "--all", "--group", "main"},
		{"groups", "show"}, {"config", "set", "nope.key", "1"}, {"rotate", "--target", "x"}, {"login", "--start-url", "https://x"}} {
		if code, _, stderr := run(t, args...); code != ExitUsage {
			t.Errorf("%v: exit %d (%s)", args, code, stderr)
		}
	}
	if code, out, _ := run(t); code != 0 || !strings.Contains(out, "Usage:") {
		t.Errorf("no args non-TTY: %d %q", code, out)
	}
	if code, _, stderr := run(t, "ui"); code != ExitError || !strings.Contains(stderr, "no TUI") {
		t.Errorf("ui without TUI: %d %q", code, stderr)
	}
	if code, out, _ := run(t, "config", "path"); code != 0 || !strings.HasSuffix(strings.TrimSpace(out), filepath.Join("cfg", "config.yaml")) {
		t.Errorf("config path: %q", out)
	}
	if code, out, _ := run(t, "rotate", "shell", "fish"); code != 0 || !strings.Contains(out, "set -gx AWS_PROFILE") {
		t.Errorf("rotate shell: %d %q", code, out)
	}
	if code, _, _ := run(t, "completion", "zsh"); code != 0 {
		t.Errorf("completion: %d", code)
	}
}

func TestUIWiring(t *testing.T) {
	newCLIEnv(t)
	var got engine.UIOptions
	RunUI = func(_ context.Context, svc engine.Service, o engine.UIOptions) error {
		got = o
		if svc == nil {
			t.Error("nil service")
		}
		return nil
	}
	if code, _, _ := run(t, "ui", "--screen", "groups"); code != 0 || got.Screen != "groups" {
		t.Errorf("ui: %d %+v", code, got)
	}
	// groups edit on a non-TTY explains the alternative.
	if code, _, stderr := run(t, "groups", "edit", "main"); code != ExitError || !strings.Contains(stderr, "groups add|remove") {
		t.Errorf("groups edit: %d %q", code, stderr)
	}
}

func TestConfigCommands(t *testing.T) {
	newCLIEnv(t)
	if code, _, e := run(t, "config", "set", "export.concurrency", "3"); code != 0 {
		t.Fatalf("set: %d %s", code, e)
	}
	if code, out, _ := run(t, "config", "get", "export.concurrency"); code != 0 || strings.TrimSpace(out) != "3" {
		t.Errorf("get: %q", out)
	}
	if code, _, _ := run(t, "config", "set", "export.concurrency", "999"); code != ExitError {
		t.Errorf("invalid value exit %d", code)
	}
	code, out, _ := run(t, "config", "list", "--json")
	var ss []settingJSON
	if code != 0 || json.Unmarshal([]byte(out), &ss) != nil || len(ss) < 30 {
		t.Fatalf("config list json: %d %s", code, out)
	}
	hasKeys(t, mustJSON(t, ss[0]), "key", "value", "description", "choices")
}

func mustJSON(t *testing.T, v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestStatusAndAccountsJSON(t *testing.T) {
	newCLIEnv(t)
	code, out, _ := run(t, "status", "--json")
	if code != 0 {
		t.Fatalf("status: %d", code)
	}
	hasKeys(t, out, "version", "mode", "config_file", "aws_config", "credentials_file", "sessions", "profiles", "groups", "exported")
	if strings.Contains(out, token) {
		t.Error("status leaks the token")
	}
	if code, _, e := run(t, "discover", "--json"); code != 0 {
		t.Fatalf("discover: %d %s", code, e)
	}
	code, out, _ = run(t, "accounts", "list", "--json", "--status", "new")
	var rows []map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 1 {
		t.Fatalf("accounts: %d %s", code, out)
	}
	for _, k := range []string{"id", "name", "status", "env", "profiles", "role", "roles", "groups"} {
		if _, ok := rows[0][k]; !ok {
			t.Errorf("account JSON lacks %q: %v", k, rows[0])
		}
	}
	if rows[0]["env"] != "stg" || rows[0]["status"] != "new" {
		t.Errorf("row = %v", rows[0])
	}
}

func TestExportExitCodesAndProgress(t *testing.T) {
	ce := newCLIEnv(t)
	run(t, "config", "set", "export.enabled", "true")
	code, out, stderr := run(t, "export", "--all")
	if code != ExitPartial {
		t.Fatalf("partial failure exit %d\n%s\n%s", code, out, stderr)
	}
	if !strings.Contains(stderr, "p_app-dev  done (ReadOnly, expires ") || !strings.Contains(stderr, "p_broken-test  skipped: transient") {
		t.Errorf("progress:\n%s", stderr)
	}
	if !strings.Contains(out, "Done — 3 sections written to ") || !strings.Contains(out, "Backup: ") {
		t.Errorf("summary:\n%s", out)
	}
	creds := ce.read(".aws/credentials")
	if !strings.Contains(creds, "[static]\naws_access_key_id = AKIAEXAMPLE0000001") || !strings.Contains(creds, "[app-dev]") {
		t.Errorf("creds:\n%s", creds)
	}
	code, out, _ = run(t, "export", "--profile", "app-dev", "--json")
	if code != 0 {
		t.Fatalf("single json: %d", code)
	}
	hasKeys(t, out, "succeeded", "failed", "written", "kept", "role_changes", "roles_saved", "warnings", "lines")
	if strings.Contains(out, "secretAccessKey") || strings.Contains(out, `"s"`) {
		t.Error("export JSON leaks secrets")
	}
	if code, _, _ := run(t, "export", "--profile", "nope"); code != ExitError {
		t.Errorf("unknown profile exit %d", code)
	}
	ce.logout()
	if code, _, stderr := run(t, "export", "--all"); code != ExitNeedsLogin || !strings.Contains(stderr, "credorbit login") {
		t.Errorf("logged out exit %d %s", code, stderr)
	}
}

func TestSyncNonTTYApprovals(t *testing.T) {
	ce := newCLIEnv(t)
	before := ce.read(".aws/config")
	code, _, stderr := run(t, "sync", "--remove-stale")
	if code != ExitError || !strings.Contains(stderr, "--yes") {
		t.Fatalf("non-TTY sync: %d %s", code, stderr)
	}
	if ce.read(".aws/config") != before {
		t.Fatal("config changed without approval")
	}
	code, _, stderr = run(t, "--yes", "sync", "--remove-stale")
	cfg := ce.read(".aws/config")
	if code != 0 || !strings.Contains(cfg, "[profile p_new-stg]") || !strings.Contains(cfg, "[profile p_gone-dev]") {
		t.Fatalf("--yes sync: %d %s\n%s", code, stderr, cfg)
	}
	if !strings.Contains(stderr, "--allow-remove") {
		t.Errorf("no hint about --allow-remove: %s", stderr)
	}
	code, _, stderr = run(t, "--yes", "sync", "--allow-remove")
	if code != 0 || strings.Contains(ce.read(".aws/config"), "[profile p_gone-dev]") {
		t.Fatalf("--allow-remove: %d %s", code, stderr)
	}
	if !strings.Contains(ce.read(".aws/config"), "# hand-written") {
		t.Error("comment lost")
	}
}

func TestGroupsAndDoctorJSON(t *testing.T) {
	newCLIEnv(t)
	if code, _, e := run(t, "groups", "create", "web", "--description", "web apps"); code != 0 {
		t.Fatalf("create: %s", e)
	}
	if code, _, e := run(t, "groups", "add", "web", "app-dev", "555500000002"); code != 0 {
		t.Fatalf("add: %s", e)
	}
	if code, _, _ := run(t, "groups", "add", "web", "nope"); code != ExitError {
		t.Errorf("unknown member exit %d", code)
	}
	code, out, _ := run(t, "groups", "show", "web", "--json")
	if code != 0 {
		t.Fatal(code)
	}
	hasKeys(t, out, "name", "description", "members")
	var g groupJSON
	_ = json.Unmarshal([]byte(out), &g)
	if len(g.Members) != 2 {
		t.Errorf("members = %+v", g.Members)
	}
	code, out, _ = run(t, "doctor", "--json")
	var checks []map[string]any
	if json.Unmarshal([]byte(out), &checks) != nil || len(checks) == 0 {
		t.Fatalf("doctor json (%d): %s", code, out)
	}
	for _, k := range []string{"area", "name", "status", "detail"} {
		if _, ok := checks[0][k]; !ok {
			t.Errorf("check lacks %q", k)
		}
	}
}

func TestSetupDryRunJSON(t *testing.T) {
	ce := newCLIEnv(t)
	before := ce.read(".aws/config")
	code, out, stderr := run(t, "setup", "--dry-run", "--compat", "--json")
	if code != 0 {
		t.Fatalf("setup dry-run: %d %s", code, stderr)
	}
	hasKeys(t, out, "steps", "exported", "failed", "backups", "warnings", "duration_ms")
	if ce.read(".aws/config") != before {
		t.Error("dry run changed config")
	}
	if !strings.Contains(stderr, "[ok  ] discover") {
		t.Errorf("step lines:\n%s", stderr)
	}
}

func TestParseSelection(t *testing.T) {
	for in, want := range map[string]string{"1,3-5": "[0 2 3 4]", "all": "[0 1 2 3 4 5]", "none": "[]", " 2 , 2 ": "[1]"} {
		got, err := parseSelection(in, 6)
		if err != nil || fmt.Sprint(got) != want {
			t.Errorf("%q → %v %v", in, got, err)
		}
	}
	for _, bad := range []string{"0", "7", "3-1", "x"} {
		if _, err := parseSelection(bad, 6); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if got, _ := parseSelection("", 3); got != nil {
		t.Error("empty must keep preselection")
	}
}
