package rotate

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PiniPP/credorbit/internal/model"
)

func seeded() *rand.Rand { return rand.New(rand.NewPCG(1, 2)) }

func TestNextZeroOne(t *testing.T) {
	if _, err := Next("a", nil, StrategyRandom, seeded()); !errors.Is(err, ErrNoEligible) {
		t.Fatalf("err = %v", err)
	}
	for _, s := range []Strategy{StrategyRandom, StrategyRoundRobin} {
		got, err := Next("a", []string{"a", "a", " "}, s, seeded())
		if err != nil || got != "a" {
			t.Fatalf("%s: got %q err %v", s, got, err)
		}
	}
	if _, err := Next("a", []string{"a", "b"}, StrategyManual, nil); !errors.Is(err, ErrManual) || !strings.Contains(err.Error(), "use --set") {
		t.Fatalf("err = %v", err)
	}
	if _, err := Next("a", []string{"a"}, "bogus", nil); err == nil {
		t.Fatal("unknown strategy accepted")
	}
}

func TestNextRandomNeverRepeats(t *testing.T) {
	r := seeded()
	elig := []string{"a", "b", "c", "d"}
	counts := map[string]int{}
	cur := "b"
	for i := 0; i < 1000; i++ {
		got, err := Next(cur, elig, StrategyRandom, r)
		if err != nil {
			t.Fatal(err)
		}
		if got == cur {
			t.Fatalf("iteration %d repeated current %q", i, cur)
		}
		counts[got]++
		cur = got
	}
	for _, a := range elig {
		if counts[a] < 200 { // ~333 expected each
			t.Errorf("%s picked %d times; distribution skewed: %v", a, counts[a], counts)
		}
	}
	// current not eligible: any eligible may be picked
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		got, _ := Next("zzz", []string{"x", "y"}, StrategyRandom, r)
		seen[got] = true
	}
	if !seen["x"] || !seen["y"] {
		t.Fatalf("seen = %v", seen)
	}
	if _, err := Next("x", []string{"x", "y"}, StrategyRandom, nil); err != nil {
		t.Fatal(err)
	}
}

func TestNextRoundRobin(t *testing.T) {
	elig := []string{"a", "b", "c"}
	for cur, want := range map[string]string{"a": "b", "b": "c", "c": "a", "": "a", "gone": "a"} {
		got, err := Next(cur, elig, StrategyRoundRobin, nil)
		if err != nil || got != want {
			t.Errorf("Next(%q) = %q, %v; want %q", cur, got, err, want)
		}
	}
}

func TestCurrentFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude-aws-account")
	if got, err := ReadCurrent(path); err != nil || got != "" {
		t.Fatalf("missing: %q %v", got, err)
	}
	if err := WriteCurrent(path, "acct-1"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "acct-1\n" {
		t.Fatalf("raw = %q", raw)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
			t.Fatalf("perm = %v", fi.Mode().Perm())
		}
	}
	if err := os.WriteFile(path, []byte("  acct-2 \r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := ReadCurrent(path); got != "acct-2" {
		t.Fatalf("got %q", got)
	}
	for _, bad := range []string{"", "a\nb"} {
		if err := WriteCurrent(path, bad); err == nil {
			t.Errorf("WriteCurrent(%q) accepted", bad)
		}
	}
}

func TestEligible(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	creds := `# comment
[default]
aws_access_key_id = AKIAFAKE
aws_secret_access_key = fake
aws_session_token = fake

[good]
aws_access_key_id = AKIAFAKE
aws_secret_access_key = fake
aws_session_token = fake
x_credorbit_expiration = ` + now.Add(time.Hour).Format(time.RFC3339) + `

[nodate]
aws_access_key_id=AKIAFAKE
aws_secret_access_key=fake
aws_session_token=fake

[soon]
aws_access_key_id = AKIAFAKE
aws_secret_access_key = fake
aws_session_token = fake
x_credorbit_expiration = ` + now.Add(2*time.Minute).Format(time.RFC3339) + `

[partial]
aws_access_key_id = AKIAFAKE
aws_secret_access_key = fake

[baddate]
aws_access_key_id = AKIAFAKE
aws_secret_access_key = fake
aws_session_token = fake
x_credorbit_expiration = tomorrow
`
	path := filepath.Join(t.TempDir(), "credentials")
	if err := os.WriteFile(path, []byte(creds), 0o600); err != nil {
		t.Fatal(err)
	}
	ok, skipped := Eligible([]string{"good", "missing", "nodate", "soon", "partial", "baddate", "good"}, path, now)
	if strings.Join(ok, ",") != "good,nodate" {
		t.Fatalf("ok = %v", ok)
	}
	want := map[string]string{"missing": "no credentials section", "soon": "expired at", "partial": "incomplete credentials", "baddate": "unreadable"}
	for k, prefix := range want {
		if !strings.HasPrefix(skipped[k], prefix) {
			t.Errorf("skipped[%s] = %q, want prefix %q", k, skipped[k], prefix)
		}
	}
	if len(skipped) != len(want) {
		t.Errorf("skipped = %v", skipped)
	}
	ok, skipped = Eligible([]string{"good"}, filepath.Join(t.TempDir(), "none"), now)
	if len(ok) != 0 || skipped["good"] != "no credentials section" {
		t.Fatalf("missing file: %v %v", ok, skipped)
	}
}

func fakeCreds() model.Credentials {
	return model.Credentials{AccessKeyID: "AKIAFAKEFAKEFAKE", SecretAccessKey: "fake-secret", SessionToken: "fake-token", Expiration: time.Now().Add(time.Hour)}
}

func bedrockServer(t *testing.T, status int, errType, msg string, hits *int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/converse") || !strings.Contains(r.URL.Path, "test-model") {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if !strings.Contains(r.Header.Get("Authorization"), "AKIAFAKEFAKEFAKE") {
			t.Errorf("request not signed with provided credentials")
		}
		body, _ := io.ReadAll(r.Body)
		var in struct {
			InferenceConfig struct{ MaxTokens int } `json:"inferenceConfig"`
		}
		_ = json.Unmarshal(body, &in)
		if in.InferenceConfig.MaxTokens != 1 {
			t.Errorf("maxTokens = %d, body %s", in.InferenceConfig.MaxTokens, body)
		}
		w.Header().Set("Content-Type", "application/json")
		if status != http.StatusOK {
			w.Header().Set("X-Amzn-ErrorType", errType)
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"message":"`+msg+`"}`)
			return
		}
		_, _ = io.WriteString(w, `{"output":{"message":{"role":"assistant","content":[{"text":"p"}]}},"stopReason":"max_tokens","usage":{"inputTokens":1,"outputTokens":1,"totalTokens":2},"metrics":{"latencyMs":5}}`)
	}))
}

func TestVerifyBedrock(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		errType string
		msg     string
		want    error
	}{
		{"ok", 200, "", "", nil},
		{"denied", 403, "AccessDeniedException", "explicit deny in a service control policy", ErrBedrockAccessDenied},
		{"model", 400, "ValidationException", "The provided model identifier is invalid.", ErrModelNotAvailable},
		{"notfound", 404, "ResourceNotFoundException", "Model use case details have not been submitted", ErrBedrockNotFound},
		{"throttle", 429, "ThrottlingException", "Too many requests", ErrBedrockThrottled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hits int32
			srv := bedrockServer(t, tc.status, tc.errType, tc.msg, &hits)
			defer srv.Close()
			v := BedrockVerifier{HTTP: srv.Client(), BaseEndpoint: srv.URL}
			err := v.Verify(context.Background(), fakeCreds(), "eu-west-1", "test-model")
			if tc.want == nil {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
			} else if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if tc.name == "throttle" && !errors.Is(err, model.ErrTransient) {
				t.Errorf("throttling should be transient: %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "fake-secret") {
				t.Fatal("secret leaked into error")
			}
			if hits != 1 {
				t.Errorf("hits = %d (retries must be disabled)", hits)
			}
		})
	}
}

func TestVerifyBedrockArgs(t *testing.T) {
	ctx := context.Background()
	if err := VerifyBedrock(ctx, nil, fakeCreds(), "eu-west-1", ""); !errors.Is(err, ErrBedrockModelRequired) {
		t.Fatalf("err = %v", err)
	}
	if err := VerifyBedrock(ctx, nil, fakeCreds(), "", "m"); err == nil {
		t.Fatal("empty region accepted")
	}
	if err := VerifyBedrock(ctx, nil, model.Credentials{}, "eu-west-1", "m"); !errors.Is(err, ErrBedrockInvalidCreds) {
		t.Fatalf("err = %v", err)
	}
}

func TestShellIntegration(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home")
	}
	file := filepath.Join(home, ".claude-aws-account")
	cases := map[string]string{
		"zsh":        `[ -f "$HOME/.claude-aws-account" ] && export AWS_PROFILE="$(cat "$HOME/.claude-aws-account")"`,
		"bash":       `[ -f "$HOME/.claude-aws-account" ] && export AWS_PROFILE="$(cat "$HOME/.claude-aws-account")"`,
		"fish":       `set -gx AWS_PROFILE (string trim < "$HOME/.claude-aws-account")`,
		"powershell": `$env:AWS_PROFILE = (Get-Content '` + file + `' -Raw).Trim()`,
	}
	for sh, want := range cases {
		got, err := ShellIntegration(sh, file)
		if err != nil {
			t.Fatalf("%s: %v", sh, err)
		}
		if !strings.Contains(got, want) || !strings.HasPrefix(got, "# ") {
			t.Errorf("%s snippet:\n%s\nwant to contain:\n%s", sh, got, want)
		}
	}
	got, _ := ShellIntegration("bash", "/opt/it's here")
	if !strings.Contains(got, `'/opt/it'\''s here'`) {
		t.Errorf("quoting wrong: %s", got)
	}
	if _, err := ShellIntegration("tcsh", file); err == nil {
		t.Fatal("unknown shell accepted")
	}
}
