package credexport

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/credorbit/credorbit/internal/fsutil"
	"github.com/credorbit/credorbit/internal/model"
)

var exp = time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)

func fakeCreds(tag string) model.Credentials {
	return model.Credentials{AccessKeyID: "ASIA" + tag, SecretAccessKey: "SECRET-" + tag, SessionToken: "TOKEN-" + tag, Expiration: exp}
}

// fakeAPI implements RoleCredentialsAPI: allowed["acct/role"] → creds; errs["acct/role"] → error.
type fakeAPI struct {
	mu      sync.Mutex
	allowed map[string]bool
	errs    map[string]error
	calls   []string
	delay   time.Duration
}

func (f *fakeAPI) GetRoleCredentials(ctx context.Context, tok, acct, role string) (model.Credentials, error) {
	key := acct + "/" + role
	f.mu.Lock()
	f.calls = append(f.calls, key)
	f.mu.Unlock()
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return model.Credentials{}, ctx.Err()
		}
	}
	if tok != "tok" {
		return model.Credentials{}, model.ErrUnauthorized
	}
	if err := f.errs[key]; err != nil {
		return model.Credentials{}, err
	}
	if f.allowed[key] {
		return fakeCreds(acct + role), nil
	}
	return model.Credentials{}, fmt.Errorf("ForbiddenException: %w", model.ErrNoAccess)
}

func tokens(model.Profile) (model.Token, error) { return model.Token{AccessToken: "tok"}, nil }

func target(name, acct, role string) Target {
	return Target{Profile: model.Profile{Name: name, Session: "acme", AccountID: acct, Role: role}, Section: SectionName(name, "p_", true)}
}

func TestSSOFetcherCandidates(t *testing.T) {
	api := &fakeAPI{allowed: map[string]bool{"111111111111/B": true},
		errs: map[string]error{"222222222222/T": fmt.Errorf("throttled: %w", model.ErrTransient)}}
	f := NewSSOFetcher(api, tokens)
	p := model.Profile{Name: "p_a", Session: "acme", AccountID: "111111111111"}
	c, role, err := f.Fetch(context.Background(), p, []string{"A", "B", "C"})
	if err != nil || role != "B" || c.AccessKeyID == "" {
		t.Fatalf("got %v %q %v", c, role, err)
	}
	_, _, err = f.Fetch(context.Background(), p, []string{"A", "C"})
	if !errors.Is(err, model.ErrNoAccess) || err.Error() != "A: no access; C: no access" {
		t.Fatalf("err = %v", err)
	}
	p.AccountID = "222222222222"
	_, _, err = f.Fetch(context.Background(), p, []string{"A", "T"})
	if !errors.Is(err, model.ErrTransient) || errors.Is(err, model.ErrNoAccess) {
		t.Fatalf("transient err = %v", err)
	}
	bad := NewSSOFetcher(api, func(model.Profile) (model.Token, error) { return model.Token{}, nil })
	if _, _, err := bad.Fetch(context.Background(), p, []string{"A"}); !errors.Is(err, model.ErrUnauthorized) {
		t.Fatalf("empty token err = %v", err)
	}
}

type recorder struct {
	mu     sync.Mutex
	events []model.Event
}

func (r *recorder) Report(e model.Event) { r.mu.Lock(); r.events = append(r.events, e); r.mu.Unlock() }

func TestFetchPoliciesAndOrder(t *testing.T) {
	api := &fakeAPI{allowed: map[string]bool{
		"111111111111/ReadOnlyAccess":      true,
		"222222222222/AdministratorAccess": true,
		"333333333333/Fallback":            true,
	}}
	targets := []Target{
		target("p_a", "111111111111", "ReadOnlyAccess"),
		target("p_b", "222222222222", "ReadOnlyAccess"),
		target("p_c", "333333333333", "Gone"),
		target("p_d", "444444444444", ""),
		{Profile: model.Profile{Name: "static"}},
	}
	assigned := map[string][]string{
		"111111111111": {"ReadOnlyAccess"},
		"222222222222": {"ReadOnlyAccess", "AdministratorAccess"},
		"333333333333": {},
	}
	rec := &recorder{}
	r := Fetch(context.Background(), targets, NewSSOFetcher(api, tokens),
		Options{Policy: model.PolicyScript, Assigned: assigned, Fallbacks: []string{"Fallback"}, Concurrency: 2}, rec)
	if len(r.Items) != 5 {
		t.Fatal("items")
	}
	for i, it := range r.Items {
		if it.Target.Profile.Name != targets[i].Profile.Name {
			t.Fatal("order not preserved")
		}
	}
	if !r.Items[0].OK() || r.Items[0].Role != "ReadOnlyAccess" || r.Items[0].Target.Section != "a" {
		t.Fatalf("a = %+v", r.Items[0])
	}
	if r.Items[1].Role != "AdministratorAccess" || r.Items[2].Role != "Fallback" {
		t.Fatalf("roles: %q %q", r.Items[1].Role, r.Items[2].Role)
	}
	if r.Items[3].OK() || r.Items[4].OK() {
		t.Fatal("d/static should fail")
	}
	if r.Succeeded() != 3 {
		t.Fatalf("succeeded = %d", r.Succeeded())
	}
	rc := r.RoleChanges()
	if len(rc) != 2 || rc["p_b"] != "AdministratorAccess" || rc["p_c"] != "Fallback" {
		t.Fatalf("role changes = %v", rc)
	}
	var ok, failed, progress int
	for _, e := range rec.events {
		switch e.Kind {
		case model.EventItemOK:
			ok++
			if e.Step != "export" || !strings.HasPrefix(e.Message, "role ") || !strings.Contains(e.Message, ", expires ") {
				t.Fatalf("ok event = %+v", e)
			}
		case model.EventItemFailed:
			failed++
		case model.EventProgress:
			progress++
		}
	}
	if ok != 3 || failed != 2 || progress != 5 {
		t.Fatalf("events ok=%d failed=%d progress=%d", ok, failed, progress)
	}

	// configured policy: role not assigned → explanatory error, no API call.
	api.calls = nil
	r = Fetch(context.Background(), targets[2:3], NewSSOFetcher(api, tokens), Options{Assigned: assigned}, nil)
	if err := r.Items[0].Err; err == nil || !strings.Contains(err.Error(), "no longer assigned") || !errors.Is(err, model.ErrNoAccess) {
		t.Fatalf("err = %v", err)
	}
	if len(api.calls) != 0 {
		t.Fatal("API called without candidates")
	}
}

func TestFetchUnauthorizedCancelsRest(t *testing.T) {
	var calls atomic.Int64
	f := fetcherFunc(func(ctx context.Context, p model.Profile, c []string) (model.Credentials, string, error) {
		calls.Add(1)
		if p.Name == "p_002" {
			return model.Credentials{}, "", fmt.Errorf("UnauthorizedException: %w", model.ErrUnauthorized)
		}
		select {
		case <-time.After(5 * time.Millisecond):
		case <-ctx.Done():
			return model.Credentials{}, "", ctx.Err()
		}
		return fakeCreds(p.Name), c[0], nil
	})
	var targets []Target
	for i := 0; i < 200; i++ {
		targets = append(targets, target(fmt.Sprintf("p_%03d", i), fmt.Sprintf("%012d", i), "R"))
	}
	r := Fetch(context.Background(), targets, f, Options{Concurrency: 1}, nil)
	if calls.Load() != 3 {
		t.Fatalf("calls = %d, want 3 (work after unauthorized must stop)", calls.Load())
	}
	for i, it := range r.Items {
		switch {
		case i < 2:
			if !it.OK() {
				t.Fatalf("item %d: %v", i, it.Err)
			}
		default:
			if !errors.Is(it.Err, model.ErrUnauthorized) {
				t.Fatalf("item %d err = %v", i, it.Err)
			}
		}
	}
}

type fetcherFunc func(ctx context.Context, p model.Profile, c []string) (model.Credentials, string, error)

func (f fetcherFunc) Fetch(ctx context.Context, p model.Profile, c []string) (model.Credentials, string, error) {
	return f(ctx, p, c)
}

func TestFetchConcurrency(t *testing.T) {
	var inflight, peak atomic.Int64
	f := fetcherFunc(func(ctx context.Context, p model.Profile, c []string) (model.Credentials, string, error) {
		n := inflight.Add(1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(2 * time.Millisecond)
		inflight.Add(-1)
		if strings.HasSuffix(p.Name, "7") {
			return model.Credentials{}, "", fmt.Errorf("x: %w", model.ErrNoAccess)
		}
		return fakeCreds(p.Name), c[0], nil
	})
	var targets []Target
	for i := 0; i < 500; i++ {
		targets = append(targets, target(fmt.Sprintf("p_%03d", i), fmt.Sprintf("%012d", i), "R"))
	}
	r := Fetch(context.Background(), targets, f, Options{Concurrency: 8}, &recorder{})
	if peak.Load() > 8 || peak.Load() < 2 {
		t.Fatalf("peak concurrency = %d", peak.Load())
	}
	if r.Succeeded() != 450 {
		t.Fatalf("succeeded = %d", r.Succeeded())
	}
	for i, it := range r.Items {
		if it.Target.Profile.Name != targets[i].Profile.Name {
			t.Fatal("order")
		}
		if it.OK() && it.Credentials().AccessKeyID != "ASIA"+it.Target.Profile.Name {
			t.Fatal("credentials mismatched to item")
		}
	}
}

func TestFetchCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var n atomic.Int64
	f := fetcherFunc(func(c context.Context, p model.Profile, cands []string) (model.Credentials, string, error) {
		if n.Add(1) == 5 {
			cancel()
		}
		return fakeCreds(p.Name), cands[0], nil
	})
	var targets []Target
	for i := 0; i < 50; i++ {
		targets = append(targets, target(fmt.Sprintf("p_%02d", i), fmt.Sprintf("%012d", i), "R"))
	}
	r := Fetch(ctx, targets, f, Options{Concurrency: 1}, nil)
	if r.Succeeded() != 5 {
		t.Fatalf("succeeded = %d", r.Succeeded())
	}
	for _, it := range r.Items[5:] {
		if !errors.Is(it.Err, model.ErrCanceled) || !errors.Is(it.Err, context.Canceled) {
			t.Fatalf("err = %v", it.Err)
		}
	}
	// Write still only touches succeeded sections.
	path := filepath.Join(t.TempDir(), "credentials")
	old := "[40]\naws_access_key_id = OLD\n"
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err := Write(path, r, WriteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := readFile(t, path)
	if !strings.HasPrefix(got, old+"\n[00]\n") || len(rep.Written) != 5 || strings.Join(rep.Kept, ",") != "40" {
		t.Fatalf("unattempted section changed or wrong report %+v:\n%s", rep, got)
	}
}

func TestCredentialsNeverPrinted(t *testing.T) {
	r := Fetch(context.Background(), []Target{target("p_a", "111111111111", "R")},
		fetcherFunc(func(ctx context.Context, p model.Profile, c []string) (model.Credentials, string, error) {
			return fakeCreds("x"), "R", nil
		}), Options{}, nil)
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%d", "%x", "%q"} {
		for _, v := range []any{r, *r, r.Items[0], r.Items} {
			out := fmt.Sprintf(verb, v)
			if strings.Contains(out, "SECRET-x") || strings.Contains(out, "TOKEN-x") ||
				strings.Contains(out, fmt.Sprintf("%x", "SECRET-x")) {
				t.Fatalf("%s leaked secrets: %s", verb, out)
			}
		}
	}
	if r.Items[0].Credentials().SecretAccessKey != "SECRET-x" {
		t.Fatal("accessor")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// result builds a Result directly (ok → creds tagged by section).
func result(items ...Item) *Result { return &Result{Items: items} }

func okItem(profile, section string) Item {
	c := fakeCreds(section)
	return Item{Target: Target{Profile: model.Profile{Name: profile}, Section: section}, Role: "R", Expiration: c.Expiration, creds: &c}
}

func failItem(profile, section string) Item {
	return Item{Target: Target{Profile: model.Profile{Name: profile}, Section: section}, Err: fmt.Errorf("A: %w", model.ErrNoAccess)}
}

const existingCreds = `# my static keys — keep me
[personal]
aws_access_key_id = AKIAPERSONAL
aws_secret_access_key = keep
region = us-east-1

[default]
aws_access_key_id = OLDDEFAULT
aws_secret_access_key = old
aws_session_token = old

[acme-dev]
aws_access_key_id = OLDDEV
aws_secret_access_key = old
aws_session_token = old
x_credorbit_expiration = 2020-01-01T00:00:00Z

[acme-prd]
aws_access_key_id = OLDPRD
aws_secret_access_key = old
aws_session_token = old

[acme-gone]
aws_access_key_id = OLDGONE
aws_secret_access_key = old
aws_session_token = old
`

func TestWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials")
	if err := os.WriteFile(path, []byte(existingCreds), 0o644); err != nil {
		t.Fatal(err)
	}
	r := result(okItem("p_acme-dev", "acme-dev"), failItem("p_acme-prd", "acme-prd"), okItem("p_acme-new", "acme-new"))
	rep, err := Write(path, r, WriteOptions{DefaultFrom: "p_acme-dev", Prune: []string{"acme-gone", "never-existed", "acme-dev"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(rep.Written, ",") != "acme-dev,acme-new,default" || strings.Join(rep.Kept, ",") != "acme-prd" ||
		strings.Join(rep.Pruned, ",") != "acme-gone" || rep.Backup == "" || len(rep.Warnings) != 0 {
		t.Fatalf("report = %+v", rep)
	}
	if readFile(t, rep.Backup) != existingCreds {
		t.Fatal("backup content")
	}
	want := `# my static keys — keep me
[personal]
aws_access_key_id = AKIAPERSONAL
aws_secret_access_key = keep
region = us-east-1

[default]
aws_access_key_id = ASIAacme-dev
aws_secret_access_key = SECRET-acme-dev
aws_session_token = TOKEN-acme-dev
x_credorbit_expiration = 2030-01-02T03:04:05Z

[acme-dev]
aws_access_key_id = ASIAacme-dev
aws_secret_access_key = SECRET-acme-dev
aws_session_token = TOKEN-acme-dev
x_credorbit_expiration = 2030-01-02T03:04:05Z

[acme-prd]
aws_access_key_id = OLDPRD
aws_secret_access_key = old
aws_session_token = old

[acme-new]
aws_access_key_id = ASIAacme-new
aws_secret_access_key = SECRET-acme-new
aws_session_token = TOKEN-acme-new
x_credorbit_expiration = 2030-01-02T03:04:05Z
`
	if got := readFile(t, path); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v", st.Mode().Perm())
		}
	}
	exps := ReadExpirations(path)
	if len(exps) != 3 || !exps["acme-new"].Equal(exp) || !exps["default"].Equal(exp) {
		t.Fatalf("expirations = %v", exps)
	}
}

func TestWriteDefaultFromFailed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials")
	if err := os.WriteFile(path, []byte(existingCreds), 0o600); err != nil {
		t.Fatal(err)
	}
	r := result(okItem("p_acme-dev", "acme-dev"), failItem("p_acme-prd", "acme-prd"))
	rep, err := Write(path, r, WriteOptions{DefaultFrom: "acme-prd"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], "left unchanged") {
		t.Fatalf("warnings = %v", rep.Warnings)
	}
	if !strings.Contains(readFile(t, path), "[default]\naws_access_key_id = OLDDEFAULT\n") {
		t.Fatal("[default] must be untouched")
	}
	rep, err = Write(path, r, WriteOptions{DefaultFrom: "p_not-in-run"})
	if err != nil || len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], "not exported") {
		t.Fatalf("rep=%+v err=%v", rep, err)
	}
}

func TestWriteZeroSuccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials")
	if err := os.WriteFile(path, []byte(existingCreds), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Write(path, result(failItem("p_acme-dev", "acme-dev")), WriteOptions{Prune: []string{"acme-gone"}})
	if !errors.Is(err, ErrNothingToWrite) {
		t.Fatalf("err = %v", err)
	}
	if readFile(t, path) != existingCreds || len(fsutil.ListBackups(path)) != 0 {
		t.Fatal("file touched on zero success")
	}
	if _, err := Write(path, nil, WriteOptions{}); !errors.Is(err, ErrNothingToWrite) {
		t.Fatal("nil result")
	}
}

func TestWriteNewFileAndDuplicates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aws", "credentials")
	if _, err := Write(path, result(okItem("p_a", "a")), WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path); !strings.HasPrefix(got, "[a]\naws_access_key_id = ASIAa\n") {
		t.Fatalf("got %q", got)
	}
	// A broken file with a duplicated section we own is repaired.
	dup := "[a]\naws_access_key_id = X\n\n[b]\nk = v\n\n[a]\naws_access_key_id = Y\n"
	if err := os.WriteFile(path, []byte(dup), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(path, result(okItem("p_a", "a")), WriteOptions{BackupKeep: 2}); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, path)
	if strings.Count(got, "[a]") != 1 || !strings.Contains(got, "[b]\nk = v\n") {
		t.Fatalf("got %q", got)
	}
}

func TestWritePreservesCRLFAndUnrelated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials")
	in := "; c\r\n[other]\r\naws_access_key_id=AKIA1\r\n\r\n[x]\r\naws_access_key_id = OLD\r\nregion = eu-west-1\r\n"
	if err := os.WriteFile(path, []byte(in), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(path, result(okItem("p_x", "x")), WriteOptions{}); err != nil {
		t.Fatal(err)
	}
	want := "; c\r\n[other]\r\naws_access_key_id=AKIA1\r\n\r\n[x]\r\naws_access_key_id = ASIAx\r\nregion = eu-west-1\r\n" +
		"aws_secret_access_key = SECRET-x\r\naws_session_token = TOKEN-x\r\nx_credorbit_expiration = 2030-01-02T03:04:05Z\r\n"
	if got := readFile(t, path); got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestSectionName(t *testing.T) {
	cases := []struct {
		p, prefix string
		strip     bool
		want      string
	}{
		{"p_acme", "p_", true, "acme"},
		{"p_acme", "p_", false, "p_acme"},
		{"acme", "p_", true, "acme"},
		{"p_", "p_", true, "p_"},
		{"x", "", true, "x"},
	}
	for _, c := range cases {
		if got := SectionName(c.p, c.prefix, c.strip); got != c.want {
			t.Errorf("SectionName(%q) = %q", c.p, got)
		}
	}
	if len(ReadExpirations(filepath.Join(t.TempDir(), "missing"))) != 0 {
		t.Fatal("missing file")
	}
}
