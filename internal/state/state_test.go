package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/credorbit/credorbit/internal/model"
)

func TestDefaultIsValid(t *testing.T) {
	c := Default()
	if err := c.Validate(); err != nil {
		t.Fatalf("default config invalid: %v", err)
	}
	if c.Groups[MainGroup] == nil {
		t.Fatal("default has no main group")
	}
	if !c.Sync.AddNew || !c.Export.StripPrefix || !c.Update.Check || c.Export.Enabled {
		t.Fatalf("unexpected bool defaults: %+v %+v", c.Sync, c.Export)
	}
	if c.Roles.Policy != model.PolicyConfigured || c.Sync.RemoveStale != RemoveStalePrompt {
		t.Fatalf("unexpected defaults: %+v %+v", c.Roles, c.Sync)
	}
}

func TestLoadMissingReturnsDefault(t *testing.T) {
	c, warns, err := LoadWithWarnings(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil || len(warns) != 0 {
		t.Fatalf("err=%v warns=%v", err, warns)
	}
	if c.Version != CurrentVersion || c.Groups[MainGroup] == nil {
		t.Fatalf("not default: %+v", c)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.yaml")
	c := Default()
	c.SSO.DefaultSession = "corp"
	c.Export.Enabled = true
	c.Sync.AddNew = false
	c.Update.Check = false
	c.Roles.Ranking = []RankRule{{Match: "admin*", Weight: 1}}
	c.Accounts["111111111111"] = AccountPref{Profile: "p_a", Role: "ReadOnly"}
	g := c.EnsureGroup("team-x", "team")
	g.Add(Member{AccountID: "222222222222", Profile: "p_b"})
	c.Groups[MainGroup].Add(Member{AccountID: "333333333333", Profile: "p_z"})
	c.Groups[MainGroup].Add(Member{AccountID: "111111111111", Profile: "p_a"})
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("perm = %v", fi.Mode().Perm())
		}
	}
	raw, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(raw), "# CredOrbit settings") {
		t.Fatalf("missing header:\n%s", raw)
	}
	if strings.Index(string(raw), "p_a") > strings.Index(string(raw), "p_z") {
		t.Fatalf("members not sorted:\n%s", raw)
	}
	got, warns, err := LoadWithWarnings(path)
	if err != nil || len(warns) > 0 {
		t.Fatalf("err=%v warns=%v", err, warns)
	}
	if got.SSO.DefaultSession != "corp" || !got.Export.Enabled || got.Sync.AddNew || got.Update.Check {
		t.Fatalf("round trip lost values: %+v", got)
	}
	if got.Groups["team-x"].Name != "team-x" || len(got.Groups[MainGroup].Members) != 2 {
		t.Fatalf("groups lost: %+v", got.Groups)
	}
	again, _ := got.Marshal()
	if string(again) != string(raw) {
		t.Fatalf("not deterministic:\n%s\n---\n%s", raw, again)
	}
}

func TestLoadV0MigratesAndFillsDefaults(t *testing.T) {
	c, warns, err := Parse([]byte("mode: compat\nexport:\n  concurrency: 0\n  enabled: true\nsync:\n  threshold: 0\ntypo_key: 1\nexport2: {}\nrotation:\n  strategyy: x\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Version != 1 || c.Mode != ModeCompat || c.Export.Concurrency != 10 || c.Sync.Threshold != 0.8 {
		t.Fatalf("bad migration/defaults: %+v", c)
	}
	if !c.Export.StripPrefix || !c.Sync.AddNew {
		t.Fatal("absent bools must keep their true defaults")
	}
	if c.Groups[MainGroup] == nil {
		t.Fatal("main group not created on load")
	}
	joined := strings.Join(warns, "\n")
	for _, k := range []string{`"typo_key"`, `"export2"`, `"rotation.strategyy"`} {
		if !strings.Contains(joined, k) {
			t.Errorf("missing warning for %s in %q", k, joined)
		}
	}
	if len(warns) != 3 {
		t.Errorf("warnings = %v", warns)
	}
}

func TestUnknownKeysInGroups(t *testing.T) {
	_, warns, err := Parse([]byte("version: 1\ngroups:\n  main:\n    members:\n      - account_id: \"111111111111\"\n        colour: red\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "groups.main.members[0].colour") {
		t.Fatalf("warns = %v", warns)
	}
}

func TestLoadNewerVersionFails(t *testing.T) {
	_, _, err := Parse([]byte("version: 99\n"))
	if !errors.Is(err, ErrNewerConfig) || !strings.Contains(err.Error(), "config written by newer CredOrbit") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadEmptyFile(t *testing.T) {
	c, _, err := Parse(nil)
	if err != nil || c.Groups[MainGroup] == nil {
		t.Fatalf("err=%v", err)
	}
}

func TestValidate(t *testing.T) {
	cases := map[string]func(c *Config){
		"mode":        func(c *Config) { c.Mode = "x" },
		"policy":      func(c *Config) { c.Roles.Policy = "bogus" },
		"threshold0":  func(c *Config) { c.Sync.Threshold = 0 },
		"threshold>1": func(c *Config) { c.Sync.Threshold = 1.5 },
		"concurrency": func(c *Config) { c.Export.Concurrency = 65 },
		"strategy":    func(c *Config) { c.Rotation.Strategy = "lottery" },
		"theme":       func(c *Config) { c.UI.Theme = "neon" },
		"stale":       func(c *Config) { c.Sync.RemoveStale = "always" },
		"groupname":   func(c *Config) { c.EnsureGroup("Bad Name", "") },
		"member-id":   func(c *Config) { c.Groups[MainGroup].Members = []Member{{AccountID: "12"}} },
		"member-none": func(c *Config) { c.Groups[MainGroup].Members = []Member{{Role: "x"}} },
		"no-main":     func(c *Config) { delete(c.Groups, MainGroup) },
		"account-key": func(c *Config) { c.Accounts["abc"] = AccountPref{} },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			c := Default()
			mut(c)
			if err := c.Validate(); err == nil {
				t.Fatal("expected error")
			}
		})
	}
	c := Default()
	c.Groups[MainGroup].Members = []Member{{Profile: "legacy-profile"}}
	c.Sync.Threshold = 1
	if err := c.Validate(); err != nil {
		t.Fatalf("profile-only member should be valid: %v", err)
	}
}

func TestSaveRejectsInvalid(t *testing.T) {
	c := Default()
	c.Mode = "nope"
	path := filepath.Join(t.TempDir(), "c.yaml")
	if err := c.Save(path); err == nil {
		t.Fatal("expected error")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("invalid config was written")
	}
}

func TestGroupAddDedupAndUpdate(t *testing.T) {
	g := &Group{Name: "x"}
	if !g.Add(Member{AccountID: "111111111111", Profile: "p_a"}) {
		t.Fatal("first add should change")
	}
	if g.Add(Member{AccountID: "111111111111", Profile: "p_a"}) {
		t.Fatal("duplicate add should not change")
	}
	if !g.Add(Member{AccountID: "111111111111", Profile: "p_a2", Role: "Admin"}) {
		t.Fatal("update should change")
	}
	if len(g.Members) != 1 || g.Members[0].Profile != "p_a2" || g.Members[0].Role != "Admin" {
		t.Fatalf("members = %+v", g.Members)
	}
	// profile-only member is matched by profile and gains the account ID
	g.Add(Member{Profile: "p_b"})
	if !g.Add(Member{AccountID: "222222222222", Profile: "p_b"}) || len(g.Members) != 2 || g.Members[1].AccountID != "222222222222" {
		t.Fatalf("members = %+v", g.Members)
	}
	// re-add without role keeps role
	g.Add(Member{AccountID: "111111111111"})
	if g.Members[0].Role != "Admin" {
		t.Fatal("role cleared by re-add")
	}
	if g.Add(Member{}) {
		t.Fatal("empty member must be ignored")
	}
	if !g.Has("p_b") || !g.Has("111111111111") || g.Has("nope") {
		t.Fatal("Has wrong")
	}
	if k := g.Keys(); len(k) != 2 || k[0] != "111111111111" {
		t.Fatalf("keys = %v", k)
	}
	if !g.Remove("p_b") || g.Remove("p_b") || len(g.Members) != 1 {
		t.Fatalf("remove wrong: %+v", g.Members)
	}
}

func TestGroupManagement(t *testing.T) {
	c := Default()
	c.EnsureGroup("zeta", "")
	c.EnsureGroup("alpha", "a")
	if n := c.GroupNames(); strings.Join(n, ",") != "main,alpha,zeta" {
		t.Fatalf("names = %v", n)
	}
	if err := c.DeleteGroup(MainGroup); err == nil {
		t.Fatal("main deleted")
	}
	if err := c.RenameGroup(MainGroup, "x"); err == nil {
		t.Fatal("main renamed")
	}
	if err := c.RenameGroup("alpha", "zeta"); err == nil {
		t.Fatal("rename onto existing")
	}
	if err := c.RenameGroup("alpha", "Bad!"); err == nil {
		t.Fatal("rename to invalid")
	}
	if err := c.RenameGroup("alpha", "beta"); err != nil {
		t.Fatal(err)
	}
	if g, err := c.Group("beta"); err != nil || g.Name != "beta" || g.Description != "a" {
		t.Fatalf("g=%+v err=%v", g, err)
	}
	if _, err := c.Group("alpha"); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if err := c.DeleteGroup("missing"); !errors.Is(err, model.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	c.Groups[MainGroup].Add(Member{AccountID: "111111111111", Profile: "p_a"})
	c.Groups["zeta"].Add(Member{Profile: "p_a"})
	if got := c.GroupsOf("111111111111", ""); strings.Join(got, ",") != "main" {
		t.Fatalf("GroupsOf = %v", got)
	}
	if got := c.GroupsOf("", "p_a"); strings.Join(got, ",") != "main,zeta" {
		t.Fatalf("GroupsOf = %v", got)
	}
	if err := c.DeleteGroup("zeta"); err != nil {
		t.Fatal(err)
	}
}

func TestStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := LoadState(path)
	if err != nil || s.Discoveries == nil || s.LastRole == nil {
		t.Fatalf("s=%+v err=%v", s, err)
	}
	s.SetDiscovery(&model.Discovery{Session: "corp", Complete: true, Accounts: []model.DiscoveredAccount{{Account: model.Account{ID: "111111111111", Name: "a"}}}})
	s.AddManaged("b", "a", "b", "")
	s.LastRole["p_a"] = "Admin"
	s.LastRun = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for i := 0; i < 60; i++ {
		s.RecordRotation("x", "y", s.LastRun)
	}
	if len(s.Rotation.History) != MaxRotationHistory || s.Rotation.Current != "y" {
		t.Fatalf("history len = %d", len(s.Rotation.History))
	}
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.ManagedSections, ",") != "a,b" || !got.IsManaged("a") || got.IsManaged("c") {
		t.Fatalf("managed = %v", got.ManagedSections)
	}
	if d := got.Discoveries["corp"]; d == nil || !d.Cached || len(d.Accounts) != 1 {
		t.Fatalf("discovery = %+v", d)
	}
	if !got.RemoveManaged("a") || got.RemoveManaged("zz") || got.IsManaged("a") {
		t.Fatal("RemoveManaged wrong")
	}
	if !got.LastRun.Equal(s.LastRun) || got.LastRole["p_a"] != "Admin" {
		t.Fatal("fields lost")
	}
}

func TestLoadStateCorrupt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(path); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("err = %v", err)
	}
	s, warn := LoadStateOrReset(path)
	if s == nil || warn == "" {
		t.Fatalf("s=%v warn=%q", s, warn)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("corrupt file not moved aside")
	}
	m, _ := filepath.Glob(filepath.Join(dir, "state.json.corrupt-*"))
	if len(m) != 1 {
		t.Fatalf("corrupt backups = %v", m)
	}
	s2, warn2 := LoadStateOrReset(filepath.Join(dir, "missing.json"))
	if s2 == nil || warn2 != "" {
		t.Fatalf("missing file: warn=%q", warn2)
	}
}

// TestNoSecretsPersisted guards the "no secrets in config/state" invariant: no
// serialized field name may look like a credential or token.
func TestNoSecretsPersisted(t *testing.T) {
	c := Default()
	c.SSO.DefaultSession = "s"
	c.Accounts["111111111111"] = AccountPref{Profile: "p", Role: "r"}
	c.Roles.Ranking = []RankRule{{Match: "a", Weight: 1}}
	c.Roles.Fallbacks = []string{"x"}
	c.Network.ExtraCAs = []string{"/ca.pem"}
	c.Rotation.Accounts = []string{"a"}
	c.Groups[MainGroup].Add(Member{AccountID: "111111111111", Profile: "p", Role: "r"})
	y, err := c.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	s := NewState()
	s.SetDiscovery(&model.Discovery{Session: "s", StartURL: "https://x", Accounts: []model.DiscoveredAccount{{Account: model.Account{ID: "1", Name: "n", Email: "e"}, Roles: []string{"r"}, RolesErr: "e"}}, Err: "x"})
	s.AddManaged("a")
	s.LastRole["p"] = "r"
	s.RecordRotation("a", "b", time.Now())
	j, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	keys := collectKeys(t, y, j)
	for _, k := range keys {
		l := strings.ToLower(k)
		for _, bad := range []string{"secret", "token", "password", "access_key", "credential_value", "session_token"} {
			if strings.Contains(l, bad) {
				t.Errorf("persisted field %q looks secret-bearing", k)
			}
		}
	}
	if len(keys) < 20 {
		t.Fatalf("too few keys collected (%d); test is not exercising the structs", len(keys))
	}
}

func collectKeys(t *testing.T, y, j []byte) []string {
	t.Helper()
	var keys []string
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, vv := range x {
				keys = append(keys, k)
				walk(vv)
			}
		case []any:
			for _, vv := range x {
				walk(vv)
			}
		}
	}
	var yv, jv any
	if err := yaml.Unmarshal(y, &yv); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(j, &jv); err != nil {
		t.Fatal(err)
	}
	walk(yv)
	walk(jv)
	return keys
}
