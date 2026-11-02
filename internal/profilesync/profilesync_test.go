package profilesync

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/credorbit/credorbit/internal/awsini"
	"github.com/credorbit/credorbit/internal/fsutil"
	"github.com/credorbit/credorbit/internal/model"
)

const baseConfig = `# my hand-written notes
[default]
region = eu-west-1

[sso-session acme]
sso_start_url = https://acme-fake.awsapps.com/start
sso_region = eu-west-1

# dev account
[profile p_acme-dev]
sso_session = acme
sso_account_id = 111111111111
sso_role_name = ReadOnlyAccess
region = eu-west-1

[profile p_acme-old]
sso_session = acme
sso_account_id = 222222222222
sso_role_name = AdministratorAccess

[profile personal]
aws_access_key_id = fake
`

func disc(accts ...model.DiscoveredAccount) *model.Discovery {
	return &model.Discovery{Session: "acme", StartURL: "https://acme-fake.awsapps.com/start", Complete: true, Accounts: accts}
}

func acct(id, name string, roles ...string) model.DiscoveredAccount {
	return model.DiscoveredAccount{Account: model.Account{ID: id, Name: name}, Roles: roles}
}

func parse(t *testing.T, s string) *awsini.File {
	t.Helper()
	f, err := awsini.Parse([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func opts() PlanOptions {
	return PlanOptions{Session: "acme", Prefix: "p_", Region: "eu-west-1", Output: "yaml", AddNew: true, RemoveStale: true, Threshold: 0.5}
}

func TestBuildPlan(t *testing.T) {
	f := parse(t, baseConfig)
	d := disc(
		acct("111111111111", "acme-dev", "ReadOnlyAccess", "AdministratorAccess"),
		acct("333333333333", "acme new+stuff", "ReadOnlyAccess", "AdministratorAccess"),
		acct("444444444444", "acme_dev", "Developer"), // p_acme-dev taken → -<id> appended
		acct("555555555555", "acme-empty"),            // no roles → warning
	)
	o := opts()
	o.RoleUpdates = map[string]string{"p_acme-dev": "AdministratorAccess", "p_unknown": "X", "p_acme-old": "AdministratorAccess"}
	p := BuildPlan(f, d, o)
	got := p.Preview()
	want := strings.Join([]string{
		"~ [profile p_acme-dev]  role ReadOnlyAccess -> AdministratorAccess",
		"+ [profile p_acme-dev-444444444444]  account 444444444444 (acme_dev)  role Developer",
		"+ [profile p_acme-new-stuff]  account 333333333333 (acme new+stuff)  role AdministratorAccess",
		"- [profile p_acme-old]  account 222222222222 (no longer assigned)",
		"! account acme-empty (555555555555) has no usable role; not added",
	}, "\n") + "\n"
	if got != want {
		t.Fatalf("preview:\n%s\nwant:\n%s", got, want)
	}
	if p.StaleBlocked || p.Empty() {
		t.Fatal("unexpected plan state")
	}
	if p2 := BuildPlan(f, d, o); p2.Preview() != got {
		t.Fatal("plan not deterministic")
	}
}

func TestBuildPlanFilterAndChooser(t *testing.T) {
	f := parse(t, baseConfig)
	d := disc(acct("111111111111", "acme-dev", "R"), acct("333333333333", "acme-a", "R"), acct("444444444444", "acme-b", "R"))
	o := opts()
	o.AccountFilter = map[string]bool{"444444444444": true}
	o.ChooseRole = func(a model.DiscoveredAccount) string { return "Chosen" }
	p := BuildPlan(f, d, o)
	if len(p.Changes) != 1 || p.Changes[0].Profile.Name != "p_acme-b" || p.Changes[0].Profile.Role != "Chosen" {
		t.Fatalf("changes = %+v", p.Changes)
	}
	o.AddNew, o.RemoveStale = false, false
	if !BuildPlan(f, d, o).Empty() {
		t.Fatal("disabled plan should be empty")
	}
}

func TestBuildPlanStaleSafeguards(t *testing.T) {
	// 10 configured accounts; discovery returns 7 (< 80%) → blocked.
	var b strings.Builder
	var accts []model.DiscoveredAccount
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("%012d", 100000000000+i)
		fmt.Fprintf(&b, "[profile p_acme-%02d]\nsso_session = acme\nsso_account_id = %s\nsso_role_name = R\n\n", i, id)
		accts = append(accts, acct(id, fmt.Sprintf("acme-%02d", i), "R"))
	}
	f := parse(t, b.String())
	o := opts()
	o.Threshold = 0
	cases := []struct {
		name    string
		d       *model.Discovery
		removes int
		blocked bool
	}{
		{"80% ok", disc(accts[:8]...), 2, false},
		{"70% blocked", disc(accts[:7]...), 0, true},
		{"incomplete never removes", &model.Discovery{Session: "acme", Accounts: accts[:1], Err: "throttled"}, 0, true},
		{"cached never removes", func() *model.Discovery { d := disc(accts[:9]...); d.Cached = true; return d }(), 0, true},
		{"nil discovery", nil, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := BuildPlan(f, c.d, o)
			n := 0
			for _, ch := range p.Changes {
				if ch.Kind == Remove {
					n++
				}
			}
			if n != c.removes || p.StaleBlocked != c.blocked {
				t.Fatalf("removes=%d blocked=%v warnings=%v", n, p.StaleBlocked, p.Warnings)
			}
			if c.blocked && len(p.Warnings) == 0 {
				t.Fatal("blocked without warning")
			}
		})
	}
	// Profiles without the managed prefix are never removed.
	f2 := parse(t, "[profile mine]\nsso_session = acme\nsso_account_id = 999999999999\nsso_role_name = R\n"+b.String())
	p := BuildPlan(f2, disc(accts...), o)
	if !p.Empty() {
		t.Fatalf("unprefixed profile must not be removed: %s", p.Preview())
	}
}

func writeFile(t *testing.T, path, content string, perm os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestApply(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	writeFile(t, path, baseConfig, 0o640)
	f := parse(t, baseConfig)
	d := disc(acct("111111111111", "acme-dev", "AdministratorAccess"), acct("333333333333", "acme-new", "PowerUserAccess"))
	o := opts()
	o.RoleUpdates = map[string]string{"p_acme-dev": "AdministratorAccess"}
	p := BuildPlan(f, d, o)
	backup, err := Apply(path, p, 3)
	if err != nil {
		t.Fatal(err)
	}
	if backup == "" || read(t, backup) != baseConfig {
		t.Fatal("backup missing or wrong")
	}
	got := read(t, path)
	want := strings.Replace(baseConfig, "sso_role_name = ReadOnlyAccess", "sso_role_name = AdministratorAccess", 1)
	want = strings.Replace(want, `[profile p_acme-old]
sso_session = acme
sso_account_id = 222222222222
sso_role_name = AdministratorAccess

`, "", 1)
	want += "\n[profile p_acme-new]\nsso_session = acme\nsso_account_id = 333333333333\nsso_role_name = PowerUserAccess\nregion = eu-west-1\noutput = yaml\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(path); st.Mode().Perm() != 0o640 {
			t.Fatalf("mode = %v, want preserved 0640", st.Mode().Perm())
		}
	}
	// Re-planning against the new file is a no-op → no write, no backup.
	before := fsutil.ListBackups(path)
	p2 := BuildPlan(parse(t, got), d, opts())
	if !p2.Empty() {
		t.Fatalf("second plan not empty: %s", p2.Preview())
	}
	if b, err := Apply(path, p2, 3); err != nil || b != "" {
		t.Fatalf("no-op apply wrote: %q %v", b, err)
	}
	if len(fsutil.ListBackups(path)) != len(before) {
		t.Fatal("no-op apply made a backup")
	}
}

func TestApplyNewFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config")
	p := &Plan{Changes: []Change{{Kind: Add, Profile: model.Profile{Name: "p_x", Session: "acme", AccountID: "111111111111", Role: "R"}}}}
	if _, err := Apply(path, p, 0); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != "[profile p_x]\nsso_session = acme\nsso_account_id = 111111111111\nsso_role_name = R\n" {
		t.Fatalf("got %q", got)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v", st.Mode().Perm())
		}
	}
}

func TestApplyFileChangedSincePlan(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	writeFile(t, path, baseConfig, 0o600)
	d := disc(acct("111111111111", "acme-dev", "R"), acct("333333333333", "acme-new", "R"))
	o := opts()
	o.RoleUpdates = map[string]string{"p_acme-dev": "AdministratorAccess"}
	p := BuildPlan(parse(t, baseConfig), d, o) // add p_acme-new, remove p_acme-old, set-role p_acme-dev

	// Meanwhile the user edits the file: re-points p_acme-old to another account,
	// adds a profile of their own and a comment.
	changed := strings.Replace(baseConfig, "sso_account_id = 222222222222", "sso_account_id = 777777777777", 1) +
		"\n# added by hand\n[profile hand]\nregion = us-east-1\n"
	writeFile(t, path, changed, 0o600)

	_, err := Apply(path, p, 3)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
	got := read(t, path)
	if !strings.Contains(got, "[profile p_acme-old]\nsso_session = acme\nsso_account_id = 777777777777") {
		t.Fatal("re-pointed profile must not be removed")
	}
	if !strings.Contains(got, "# added by hand\n[profile hand]\nregion = us-east-1\n") {
		t.Fatal("concurrent edit lost")
	}
	if !strings.Contains(got, "[profile p_acme-new]") || !strings.Contains(got, "sso_role_name = AdministratorAccess\nregion = eu-west-1") {
		t.Fatalf("non-conflicting changes not applied:\n%s", got)
	}
}

func TestApplyRemoveAlreadyGone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	writeFile(t, path, "[default]\nregion = x\n", 0o600)
	p := &Plan{Changes: []Change{{Kind: Remove, Profile: model.Profile{Name: "p_gone", AccountID: "1"}}}}
	if b, err := Apply(path, p, 3); err != nil || b != "" {
		t.Fatalf("expected silent no-op, got %q %v", b, err)
	}
}

func TestApplyLargeConfigPreservesUnrelated(t *testing.T) {
	var b strings.Builder
	b.WriteString("; top comment\r\n")
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&b, "# c%d\r\n[profile p_acme-%03d]\r\nsso_session = acme\r\nsso_account_id = %012d\r\nsso_role_name=R\r\n\r\n", i, i, 100000000000+i)
	}
	in := b.String()
	path := filepath.Join(t.TempDir(), "config")
	writeFile(t, path, in, 0o600)
	p := &Plan{Changes: []Change{
		{Kind: Remove, Profile: model.Profile{Name: "p_acme-100", AccountID: "100000000100"}},
		{Kind: SetRole, Profile: model.Profile{Name: "p_acme-005", AccountID: "100000000005", Role: "Admin"}, OldRole: "R"},
		{Kind: Add, Profile: model.Profile{Name: "p_new", Session: "acme", AccountID: "999999999999", Role: "R"}},
	}}
	if _, err := Apply(path, p, 3); err != nil {
		t.Fatal(err)
	}
	got := read(t, path)
	want := strings.Replace(in, "# c100\r\n[profile p_acme-100]\r\nsso_session = acme\r\nsso_account_id = 100000000100\r\nsso_role_name=R\r\n\r\n", "", 1)
	want = strings.Replace(want, "sso_account_id = 100000000005\r\nsso_role_name=R", "sso_account_id = 100000000005\r\nsso_role_name=Admin", 1)
	want += "[profile p_new]\r\nsso_session = acme\r\nsso_account_id = 999999999999\r\nsso_role_name = R\r\n"
	if got != want {
		t.Fatal("unrelated content not preserved byte-for-byte")
	}
}

func TestProfileName(t *testing.T) {
	taken := map[string]bool{"p_acme-dev": true, "p_acme-dev-111111111111": true}
	cases := []struct{ prefix, name, id, want string }{
		{"p_", "acme dev+x_y", "1", "p_acme-dev-x-y"},
		{"p_", "acme-dev", "222222222222", "p_acme-dev-222222222222"},
		{"p_", "acme_dev", "111111111111", "p_acme-dev-111111111111-2"},
		{"p_", "", "333333333333", "p_333333333333"},
		{"", "Acme [Prod]\t", "1", "Acme--Prod--"},
	}
	for _, c := range cases {
		if got := ProfileName(c.prefix, c.name, c.id, taken); got != c.want {
			t.Errorf("ProfileName(%q,%q) = %q want %q", c.prefix, c.name, got, c.want)
		}
	}
}

func TestPreviewEmpty(t *testing.T) {
	if (&Plan{}).Preview() != "no changes\n" {
		t.Fatal("empty preview")
	}
}
