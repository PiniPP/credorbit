package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/PiniPP/credorbit/internal/awsini"
	"github.com/PiniPP/credorbit/internal/model"
	"github.com/PiniPP/credorbit/internal/state"
)

func profileRole(t *testing.T, cfgText, profile string) string {
	t.Helper()
	f, err := awsini.Parse([]byte(cfgText))
	if err != nil {
		t.Fatal(err)
	}
	s := f.Section("profile " + profile)
	if s == nil {
		return ""
	}
	v, _ := s.Get("sso_role_name")
	return v
}

func credSection(t *testing.T, text, section string) map[string]string {
	t.Helper()
	f, err := awsini.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	s := f.Section(section)
	if s == nil {
		return nil
	}
	m := map[string]string{}
	for _, k := range s.Keys() {
		m[k], _ = s.Get(k)
	}
	return m
}

func TestSessionsAndDefault(t *testing.T) {
	te := newTestEnv(t)
	e := te.engine(nil)
	ss, err := e.Sessions()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range ss {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "alpha,beta,legacy:legacy-example.awsapps.com" {
		t.Fatalf("sessions = %v", names)
	}
	if d := e.DefaultSession(); d != "" {
		t.Errorf("default with 3 sessions = %q", d)
	}
	te.env["SSO_SESSION"] = "beta"
	if d := e.DefaultSession(); d != "beta" {
		t.Errorf("env default = %q", d)
	}
	delete(te.env, "SSO_SESSION")
	if err := e.SetDefaultSession("alpha"); err != nil {
		t.Fatal(err)
	}
	if d := te.engine(nil).DefaultSession(); d != "alpha" {
		t.Errorf("persisted default = %q", d)
	}
	if err := e.SetDefaultSession("ghost"); !errors.Is(err, model.ErrNotFound) {
		t.Errorf("unknown session err = %v", err)
	}
}

func TestAddSession(t *testing.T) {
	te := newTestEnv(t)
	ui := &fakeUI{review: false}
	e := te.engine(ui)
	s := model.SSOSession{Name: "gamma", StartURL: "https://gamma-example.awsapps.com/start", Region: "eu-north-1"}
	before := te.read(te.paths.AWSConfig)
	if err := e.AddSession(s); !errors.Is(err, ErrDeclined) {
		t.Fatalf("declined err = %v", err)
	}
	if te.read(te.paths.AWSConfig) != before {
		t.Fatal("config changed after decline")
	}
	ui.review = true
	if err := e.AddSession(s); err != nil {
		t.Fatal(err)
	}
	after := te.read(te.paths.AWSConfig)
	if !strings.HasPrefix(after, before) || !strings.Contains(after, "[sso-session gamma]") {
		t.Fatalf("config not appended:\n%s", after)
	}
	if !strings.Contains(ui.reviews[0], "+ [sso-session gamma]") {
		t.Errorf("preview = %q", ui.reviews[0])
	}
	if err := e.AddSession(s); err == nil {
		t.Error("duplicate session accepted")
	}
	if err := e.AddSession(model.SSOSession{Name: "x", StartURL: "http://insecure", Region: "eu-west-1"}); err == nil {
		t.Error("http start URL accepted")
	}
}

func TestLoginStatusAndRefresh(t *testing.T) {
	te := newTestEnv(t)
	e := te.engine(nil)
	ls := e.LoginStatus(context.Background(), "alpha")
	if !ls.LoggedIn || ls.Refresh {
		t.Fatalf("status = %+v", ls)
	}
	// Expired token with a refresh token: status reports refreshable, no network call.
	te.login("alpha", alphaURL, "expired-token", time.Now().Add(-time.Hour), "refresh-ok")
	ls = e.LoginStatus(context.Background(), "alpha")
	if ls.LoggedIn || !ls.Refresh || te.oidc.refreshes.Load() != 0 {
		t.Fatalf("status = %+v refreshes=%d", ls, te.oidc.refreshes.Load())
	}
	// A real operation refreshes silently through the OIDC endpoint.
	if _, err := e.Discover(context.Background(), "alpha"); err != nil {
		t.Fatal(err)
	}
	if te.oidc.refreshes.Load() != 1 {
		t.Errorf("refreshes = %d", te.oidc.refreshes.Load())
	}
	if !e.LoginStatus(context.Background(), "alpha").LoggedIn {
		t.Error("not logged in after refresh")
	}
}

func TestLoginDeviceFlow(t *testing.T) {
	te := newTestEnv(t)
	te.logout("alpha", alphaURL)
	ui := &fakeUI{}
	e := te.engine(ui)
	ls, err := e.Login(context.Background(), "alpha", "device", false)
	if err != nil {
		t.Fatal(err)
	}
	if !ls.LoggedIn || !ls.Refresh {
		t.Fatalf("state = %+v", ls)
	}
	if len(ui.prompts) != 1 || !strings.Contains(ui.prompts[0], "ABCD-EFGH") {
		t.Errorf("prompts = %v", ui.prompts)
	}
	// Valid token: no new prompt unless forced.
	if _, err := e.Login(context.Background(), "alpha", "device", false); err != nil || len(ui.prompts) != 1 {
		t.Errorf("second login prompted: %v %v", err, ui.prompts)
	}
}

func TestDiscoverCacheRules(t *testing.T) {
	te := newTestEnv(t)
	e := te.engine(nil)
	ctx := context.Background()
	d, err := e.Discover(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if !d.Complete || len(d.Accounts) != 12 || d.Cached {
		t.Fatalf("discovery = complete %v, %d accounts", d.Complete, len(d.Accounts))
	}
	c := e.CachedDiscovery("alpha")
	if c == nil || !c.Cached || len(c.Accounts) != 12 {
		t.Fatalf("cache = %+v", c)
	}
	// A failed discovery is returned but never overwrites the good cache.
	te.portal.failList.Store(true)
	d2, err := e.Discover(ctx, "alpha")
	if err == nil || d2 == nil || d2.Complete {
		t.Fatalf("failed discovery: %v %+v", err, d2)
	}
	if c := te.engine(nil).CachedDiscovery("alpha"); c == nil || len(c.Accounts) != 12 {
		t.Fatalf("cache overwritten: %+v", c)
	}
	// Accounts falls back to the cache: configured-only accounts are unknown, not missing.
	rows := e.Accounts("alpha")
	for _, r := range rows {
		if r.Account.ID == "111100000099" && r.Status != model.StatusUnknown {
			t.Errorf("gone account status from cache = %s", r.Status)
		}
	}
	te.portal.failList.Store(false)
	// Unauthorized: clear login error.
	te.logout("alpha", alphaURL)
	_, err = e.Discover(ctx, "alpha")
	if !errors.Is(err, model.ErrUnauthorized) || !strings.Contains(err.Error(), "credorbit login") {
		t.Errorf("unauthorized err = %v", err)
	}
}

func TestAccountsRows(t *testing.T) {
	te := newTestEnv(t)
	e := te.engine(nil)
	// No discovery: configured profiles with unknown status.
	rows := e.Accounts("alpha")
	if len(rows) == 0 {
		t.Fatal("no rows")
	}
	for _, r := range rows {
		if r.Status != model.StatusUnknown {
			t.Fatalf("status without discovery = %s", r.Status)
		}
	}
	if _, err := e.Discover(context.Background(), "alpha"); err != nil {
		t.Fatal(err)
	}
	if err := e.GroupAdd("main", "p_team-dev"); err != nil {
		t.Fatal(err)
	}
	got := map[string]AccountRow{}
	for _, r := range e.Accounts("alpha") {
		got[r.Account.ID] = r
	}
	if r := got["111100000001"]; r.Status != model.StatusAvailable || r.Env != "dev" || !slices.Contains(r.Groups, "main") || len(r.Profiles) != 2 || r.Role != "ReadOnly" {
		t.Errorf("team-dev row = %+v", r)
	}
	if r := got["111100000005"]; r.Status != model.StatusNew || r.Env != "test" {
		t.Errorf("new-qa row = %+v", r)
	}
	if r := got["111100000099"]; r.Status != model.StatusMissing {
		t.Errorf("gone row = %+v", r)
	}
	if r := got["111100000004"]; r.Env != "sandbox" {
		t.Errorf("sandbox env = %q", r.Env)
	}
	news := e.NewAccounts("alpha")
	if len(news) == 0 || slices.ContainsFunc(news, func(r AccountRow) bool { return r.Account.ID == "111100000001" }) {
		t.Errorf("new accounts = %d", len(news))
	}
}

func TestInferEnv(t *testing.T) {
	for in, want := range map[string]string{"Team-PRD": "prd", "web_production": "prd", "Shared Services STG": "stg", "x-dev": "dev",
		"qa-tools": "test", "sbx-1": "sandbox", "poc": "sandbox", "billing": "", "devtools": ""} {
		if got := InferEnv(in); got != want {
			t.Errorf("InferEnv(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSyncAddRemoveApproval(t *testing.T) {
	ctx := context.Background()
	t.Run("approve all", func(t *testing.T) {
		te := newTestEnv(t)
		ui := &fakeUI{review: true, confirm: bp(true), role: "ReadOnly"}
		e := te.engine(ui)
		if _, err := e.Discover(ctx, "alpha"); err != nil {
			t.Fatal(err)
		}
		preview, empty, err := e.PlanSync(ctx, "alpha", SyncOptions{RemoveStale: true})
		if err != nil || empty {
			t.Fatal(err, empty)
		}
		if !strings.Contains(preview, "+ [profile p_new-qa]") || !strings.Contains(preview, "- [profile p_gone-dev]") {
			t.Fatalf("preview:\n%s", preview)
		}
		res, err := e.ApplySync(ctx, "alpha", SyncOptions{RemoveStale: true})
		if err != nil {
			t.Fatal(err)
		}
		cfg := te.read(te.paths.AWSConfig)
		if res.Applied != 2 || res.Backup == "" || !strings.Contains(cfg, "[profile p_new-qa]") || strings.Contains(cfg, "[profile p_gone-dev]") {
			t.Fatalf("applied %d backup %q\n%s", res.Applied, res.Backup, cfg)
		}
		if profileRole(t, cfg, "p_new-qa") != "ReadOnly" {
			t.Errorf("new profile role = %q (normal mode must not pick the strongest)", profileRole(t, cfg, "p_new-qa"))
		}
		if !strings.Contains(cfg, "# my admin profile, keep it") || !strings.Contains(cfg, "region = eu-west-1 ; inline comment") {
			t.Error("hand-written content lost")
		}
		if len(ui.confirms) != 1 || !strings.Contains(ui.confirms[0], "Remove 1 stale") {
			t.Errorf("confirms = %v", ui.confirms)
		}
	})
	t.Run("decline review", func(t *testing.T) {
		te := newTestEnv(t)
		e := te.engine(&fakeUI{review: false})
		_, _ = e.Discover(ctx, "alpha")
		before := te.read(te.paths.AWSConfig)
		if _, err := e.ApplySync(ctx, "alpha", SyncOptions{RemoveStale: true}); !errors.Is(err, ErrDeclined) {
			t.Fatalf("err = %v", err)
		}
		if te.read(te.paths.AWSConfig) != before {
			t.Fatal("config changed")
		}
	})
	t.Run("removal not confirmed keeps profiles", func(t *testing.T) {
		te := newTestEnv(t)
		e := te.engine(&fakeUI{review: true, role: "ReadOnly"}) // Confirm → default (false)
		_, _ = e.Discover(ctx, "alpha")
		res, err := e.ApplySync(ctx, "alpha", SyncOptions{RemoveStale: true})
		if err != nil {
			t.Fatal(err)
		}
		cfg := te.read(te.paths.AWSConfig)
		if !strings.Contains(cfg, "[profile p_gone-dev]") || !strings.Contains(cfg, "[profile p_new-qa]") {
			t.Fatalf("removal applied or add missing:\n%s", cfg)
		}
		if !slices.ContainsFunc(res.Warnings, func(w string) bool { return strings.Contains(w, "--allow-remove") }) {
			t.Errorf("warnings = %v", res.Warnings)
		}
	})
	t.Run("non-interactive multiple roles skipped", func(t *testing.T) {
		te := newTestEnv(t)
		e := te.engine(nil)
		_, _ = e.Discover(ctx, "alpha")
		preview, _, err := e.PlanSync(ctx, "alpha", SyncOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(preview, "p_new-qa") || !strings.Contains(preview, "multiple roles; choose with credorbit roles set") {
			t.Fatalf("preview:\n%s", preview)
		}
		// A preferred role resolves it.
		if err := e.SetPreferredRole("111100000005", "ReadOnly"); err != nil {
			t.Fatal(err)
		}
		preview, _, _ = e.PlanSync(ctx, "alpha", SyncOptions{})
		if !strings.Contains(preview, "+ [profile p_new-qa]  account 111100000005 (new-qa)  role ReadOnly") {
			t.Fatalf("preview with preferred role:\n%s", preview)
		}
	})
	t.Run("safeguard blocks mass removal", func(t *testing.T) {
		te := newTestEnv(t)
		te.portal.accounts[alphaTok] = te.portal.accounts[alphaTok][:3]
		e := te.engine(&fakeUI{review: true, confirm: bp(true)})
		_, _ = e.Discover(ctx, "alpha")
		preview, _, err := e.PlanSync(ctx, "alpha", SyncOptions{RemoveStale: true})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(preview, "- [profile") || !strings.Contains(preview, "refusing to remove") {
			t.Fatalf("preview:\n%s", preview)
		}
	})
	t.Run("cached discovery blocks removals", func(t *testing.T) {
		te := newTestEnv(t)
		_, _ = te.engine(nil).Discover(ctx, "alpha")
		e := te.engine(&fakeUI{review: true, confirm: bp(true)}) // fresh process: cache only
		preview, _, err := e.PlanSync(ctx, "alpha", SyncOptions{RemoveStale: true})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(preview, "- [profile") || !strings.Contains(preview, cachedRemovalMsg) {
			t.Fatalf("preview:\n%s", preview)
		}
	})
	t.Run("remove_stale never", func(t *testing.T) {
		te := newTestEnv(t)
		e := te.engine(&fakeUI{review: true, confirm: bp(true)})
		te.mustSet(e, "sync.remove_stale", "never")
		_, _ = e.Discover(ctx, "alpha")
		preview, _, _ := e.PlanSync(ctx, "alpha", SyncOptions{RemoveStale: true})
		if strings.Contains(preview, "- [profile") {
			t.Fatalf("preview:\n%s", preview)
		}
	})
}

func TestExportScopes(t *testing.T) {
	ctx := context.Background()
	setup := func(t *testing.T) (*testEnv, *Engine) {
		te := newTestEnv(t)
		e := te.engine(&fakeUI{review: true})
		te.mustSet(e, "export.enabled", "true", "sso.default_session", "alpha")
		if _, err := e.Discover(ctx, "alpha"); err != nil {
			t.Fatal(err)
		}
		return te, e
	}
	t.Run("all", func(t *testing.T) {
		te, e := setup(t)
		res, err := e.Export(ctx, "", Scope{Kind: ScopeAll}, ExportOptions{})
		if err != nil {
			t.Fatal(err)
		}
		creds := te.read(te.paths.AWSCredentials)
		if !strings.HasPrefix(creds, "# hand-written static keys, must survive\n[static-keys]\naws_access_key_id = AKIAEXAMPLESTATIC01\naws_secret_access_key = static-secret-value\n") {
			t.Fatalf("unrelated section changed:\n%s", creds)
		}
		if s := credSection(t, creds, "team-dev"); s["aws_access_key_id"] != "ASIA111100000001" || s["x_credorbit_expiration"] == "" {
			t.Errorf("team-dev = %v", s)
		}
		if credSection(t, creds, "work-admin") != nil {
			t.Error("profile without prefix exported under ScopeAll")
		}
		// Partial failure keeps the old section.
		if s := credSection(t, creds, "flaky-test"); s["aws_access_key_id"] != "ASIAOLDFLAKY0000001" {
			t.Errorf("flaky-test replaced: %v", s)
		}
		byProfile := map[string]ExportLine{}
		for _, l := range res.Lines {
			byProfile[l.Profile] = l
		}
		if l := byProfile["p_flaky-test"]; l.ErrClass != "transient" {
			t.Errorf("flaky = %+v", l)
		}
		if l := byProfile["p_revoked-dev"]; l.ErrClass != "no_access" || !strings.Contains(l.Err, "credorbit roles set") {
			t.Errorf("revoked = %+v", l)
		}
		if l := byProfile["p_team-prd"]; l.Role != "ReadOnly" {
			t.Errorf("normal mode escalated team-prd to %s", l.Role)
		}
		if len(res.RoleChanges) != 0 || res.RolesSaved {
			t.Errorf("role changes in normal mode: %v", res.RoleChanges)
		}
		if !slices.Contains(res.Kept, "flaky-test") || res.Backup == "" || res.Failed < 3 {
			t.Errorf("kept=%v backup=%q failed=%d", res.Kept, res.Backup, res.Failed)
		}
		st := te.engine(nil).Status(ctx)
		managed := 0
		for _, c := range st.Exported {
			if c.Managed {
				managed++
			}
		}
		if managed != res.Succeeded {
			t.Errorf("managed = %d, succeeded = %d", managed, res.Succeeded)
		}
		if fi, _ := os.Stat(te.paths.AWSCredentials); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
			t.Errorf("perm = %v", fi.Mode().Perm())
		}
	})
	t.Run("group", func(t *testing.T) {
		te, e := setup(t)
		if err := e.CreateGroup("core", "core accounts"); err != nil {
			t.Fatal(err)
		}
		if err := e.GroupAdd("core", "p_team-dev", "111100000003"); err != nil {
			t.Fatal(err)
		}
		res, err := e.Export(ctx, "alpha", Scope{Kind: ScopeGroup, Group: "core"}, ExportOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(res.Written, ",") != "team-dev,shared-stg" {
			t.Errorf("written = %v", res.Written)
		}
		_ = te
	})
	t.Run("selected and single", func(t *testing.T) {
		_, e := setup(t)
		res, err := e.Export(ctx, "alpha", Scope{Kind: ScopeSelected, AccountIDs: []string{"111100000004"}, Profiles: []string{"web-dev"}}, ExportOptions{})
		if err != nil || res.Succeeded != 2 {
			t.Fatalf("selected: %v %+v", err, res)
		}
		if _, err := e.Export(ctx, "alpha", Scope{Kind: ScopeSelected, Profiles: []string{"nope", "web-dev"}}, ExportOptions{}); !errors.Is(err, model.ErrNotFound) || !strings.Contains(err.Error(), "nope") {
			t.Errorf("unknown err = %v", err)
		}
		for _, name := range []string{"team-prd", "p_team-prd"} {
			res, err := e.Export(ctx, "alpha", Scope{Kind: ScopeSingle, Profiles: []string{name}}, ExportOptions{})
			if err != nil || len(res.Written) != 1 || res.Written[0] != "team-prd" {
				t.Errorf("single %s: %v %+v", name, err, res)
			}
		}
	})
	t.Run("default mapping", func(t *testing.T) {
		te, e := setup(t)
		te.mustSet(e, "export.default_profile", "p_team-dev")
		if _, err := e.Export(ctx, "alpha", Scope{Kind: ScopeSelected, Profiles: []string{"p_web-dev"}}, ExportOptions{}); err != nil {
			t.Fatal(err)
		}
		creds := te.read(te.paths.AWSCredentials)
		if s := credSection(t, creds, "default"); s["aws_access_key_id"] != "ASIA111100000001" {
			t.Fatalf("[default] = %v\n%s", s, creds)
		}
		if credSection(t, creds, "team-dev") != nil {
			t.Error("default profile's own section written although not in scope")
		}
		// Single mode leaves [default] alone.
		before := credSection(t, creds, "default")
		te.portal.exp = time.Now().Add(3 * time.Hour)
		if _, err := e.Export(ctx, "alpha", Scope{Kind: ScopeSingle, Profiles: []string{"web-stg"}}, ExportOptions{}); err != nil {
			t.Fatal(err)
		}
		if after := credSection(t, te.read(te.paths.AWSCredentials), "default"); after["x_credorbit_expiration"] != before["x_credorbit_expiration"] {
			t.Error("[default] touched in single mode")
		}
	})
	t.Run("no write verifies only", func(t *testing.T) {
		te, e := setup(t)
		before := te.read(te.paths.AWSCredentials)
		res, err := e.Export(ctx, "alpha", Scope{Kind: ScopeSingle, Profiles: []string{"team-dev"}}, ExportOptions{WriteFile: bp(false)})
		if err != nil || res.Succeeded != 1 || len(res.Written) != 0 {
			t.Fatalf("%v %+v", err, res)
		}
		if te.read(te.paths.AWSCredentials) != before || !slices.Contains(res.Warnings, NativeSSOHint) {
			t.Error("file written or hint missing")
		}
	})
	t.Run("dry run", func(t *testing.T) {
		te, e := setup(t)
		before := te.read(te.paths.AWSCredentials)
		calls := te.portal.credCalls.Load()
		res, err := e.Export(ctx, "alpha", Scope{Kind: ScopeAll}, ExportOptions{DryRun: true})
		if err != nil || len(res.Lines) == 0 {
			t.Fatal(err)
		}
		if te.portal.credCalls.Load() != calls || te.read(te.paths.AWSCredentials) != before {
			t.Error("dry run fetched or wrote")
		}
	})
	t.Run("unauthorized", func(t *testing.T) {
		te, e := setup(t)
		te.logout("alpha", alphaURL)
		before := te.read(te.paths.AWSCredentials)
		_, err := e.Export(ctx, "alpha", Scope{Kind: ScopeAll}, ExportOptions{})
		if !errors.Is(err, model.ErrUnauthorized) {
			t.Fatalf("err = %v", err)
		}
		if te.read(te.paths.AWSCredentials) != before {
			t.Error("file changed")
		}
	})
	t.Run("preferred role under configured policy", func(t *testing.T) {
		_, e := setup(t)
		if err := e.SetPreferredRole("111100000012", "Nope"); err == nil {
			t.Error("unassigned preferred role accepted")
		}
		if err := e.SetPreferredRole("111100000012", "ReadOnly"); err != nil {
			t.Fatal(err)
		}
		res, err := e.Export(ctx, "alpha", Scope{Kind: ScopeSingle, Profiles: []string{"p_revoked-dev"}}, ExportOptions{})
		if err != nil || res.Succeeded != 1 || res.Lines[0].Role != "ReadOnly" {
			t.Fatalf("%v %+v", err, res)
		}
	})
}

func TestExportCancelLeavesFilesIntact(t *testing.T) {
	te := newTestEnv(t)
	e := te.engine(nil)
	te.mustSet(e, "export.enabled", "true", "export.concurrency", "2")
	before := te.read(te.paths.AWSCredentials)
	ctx, cancel := context.WithCancel(context.Background())
	te.portal.credDelay = 200 * time.Millisecond
	te.portal.credHook = func() { cancel() }
	res, err := e.Export(ctx, "alpha", Scope{Kind: ScopeAll}, ExportOptions{})
	if !errors.Is(err, model.ErrCanceled) {
		t.Fatalf("err = %v", err)
	}
	if res == nil || te.read(te.paths.AWSCredentials) != before {
		t.Fatal("credentials changed after cancel")
	}
	matches, _ := filepath.Glob(te.paths.AWSCredentials + ".credorbit-*.bak")
	if len(matches) != 0 {
		t.Errorf("backup made on cancel: %v", matches)
	}
}

func TestCompatExportReproducesScript(t *testing.T) {
	te := newTestEnv(t)
	te.env["FALLBACK_ROLES"] = "AdministratorAccess Fallback-Admin"
	e := te.engine(nil)
	if err := e.SetMode("compat"); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := e.Discover(ctx, "alpha"); err != nil {
		t.Fatal(err)
	}
	res, err := e.Export(ctx, "alpha", Scope{Kind: ScopeAll}, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.RoleChanges["p_team-prd"] != "DevOps" || res.RoleChanges["p_team-dev"] != "AdministratorAccess" || !res.RolesSaved {
		t.Fatalf("role changes = %v saved=%v", res.RoleChanges, res.RolesSaved)
	}
	cfg := te.read(te.paths.AWSConfig)
	if profileRole(t, cfg, "p_team-prd") != "DevOps" || profileRole(t, cfg, "work-admin") != "AdministratorAccess" {
		t.Errorf("roles not persisted:\n%s", cfg)
	}
	if !strings.Contains(te.read(te.paths.AWSCredentials), "[team-prd]") {
		t.Error("compat mode did not write credentials")
	}
	// Normal mode never changes roles.
	if err := e.SetMode("normal"); err != nil {
		t.Fatal(err)
	}
	if e.conf().Export.Enabled || e.conf().Roles.UpdateConfig || e.conf().Roles.Policy != model.PolicyConfigured {
		t.Error("normal mode not restored")
	}
	if len(e.conf().Roles.Fallbacks) != 0 {
		t.Error("compat invented fallback roles")
	}
}

func TestRunFullDryRunWritesNothing(t *testing.T) {
	te := newTestEnv(t)
	e := te.engine(&fakeUI{review: true, confirm: bp(true), role: "ReadOnly"})
	te.mustSet(e, "sso.default_session", "alpha")
	cfgBefore, credBefore := te.read(te.paths.AWSConfig), te.read(te.paths.AWSCredentials)
	stateBefore := te.read(te.paths.AppStateFile)
	rep, err := e.RunFull(context.Background(), FullOptions{DryRun: true, Compat: true})
	if err != nil {
		t.Fatal(err)
	}
	if te.read(te.paths.AWSConfig) != cfgBefore || te.read(te.paths.AWSCredentials) != credBefore || te.read(te.paths.AppStateFile) != stateBefore {
		t.Fatal("dry run wrote files")
	}
	var ids []string
	for _, s := range rep.Steps {
		ids = append(ids, s.Step)
	}
	if strings.Join(ids, ",") != "environment,session,network,login,discover,roles,compare,safeguards,main-group,scope,role-policy,preview,approve,report" {
		t.Errorf("steps = %v", ids)
	}
	if te.portal.credCalls.Load() != 0 {
		t.Error("dry run fetched credentials")
	}
}

func TestRunFullCompatHappyPath(t *testing.T) {
	te := newTestEnv(t)
	e := te.engine(&fakeUI{review: true, confirm: bp(true)})
	te.mustSet(e, "sso.default_session", "alpha", "export.default_profile", "p_team-dev")
	if err := e.GroupAdd("main", "p_team-dev", "p_team-prd"); err != nil {
		t.Fatal(err)
	}
	if err := e.SetRotationAccounts([]string{"team-dev", "team-prd"}); err != nil {
		t.Fatal(err)
	}
	var events []model.Event
	e.SetReporter(model.ReporterFunc(func(ev model.Event) {
		if ev.Kind == model.EventStepDone {
			events = append(events, ev)
		}
	}))
	rep, err := e.RunFull(context.Background(), FullOptions{Compat: true, AllowRemove: true})
	if err != nil {
		t.Fatal(err)
	}
	cfg := te.read(te.paths.AWSConfig)
	if !strings.Contains(cfg, "[profile p_new-qa]") || profileRole(t, cfg, "p_new-qa") != "AdministratorAccess" {
		t.Errorf("new account not added with strongest role:\n%s", cfg)
	}
	if strings.Contains(cfg, "[profile p_gone-dev]") {
		t.Error("stale profile not removed although --allow-remove")
	}
	if profileRole(t, cfg, "p_team-prd") != "DevOps" {
		t.Error("role update not persisted")
	}
	creds := te.read(te.paths.AWSCredentials)
	for _, sec := range []string{"team-dev", "team-prd", "new-qa", "default", "static-keys"} {
		if credSection(t, creds, sec) == nil {
			t.Errorf("section %s missing:\n%s", sec, creds)
		}
	}
	if credSection(t, creds, "web-dev") != nil {
		t.Error("main mode exported a non-main account")
	}
	cur, _ := os.ReadFile(te.paths.ClaudeAccountFile)
	if c := strings.TrimSpace(string(cur)); c != "team-dev" && c != "team-prd" {
		t.Errorf("rotation file = %q", cur)
	}
	if rep.Exported != 3 || len(rep.Backups) == 0 || len(events) < len(rep.Steps) {
		t.Errorf("report exported=%d backups=%v events=%d steps=%d", rep.Exported, rep.Backups, len(events), len(rep.Steps))
	}
	for _, s := range rep.Steps {
		if !s.OK && !s.Skipped {
			t.Errorf("step %s failed: %s", s.Step, s.Summary)
		}
	}
	// Main group membership was not modified by compat include-new.
	if g, _ := e.Group("main"); len(g.Members) != 2 {
		t.Errorf("main members = %+v", g.Members)
	}
}

func TestRunFullNonInteractiveNeedsLogin(t *testing.T) {
	te := newTestEnv(t)
	te.logout("alpha", alphaURL)
	e := te.engine(nil)
	rep, err := e.RunFull(context.Background(), FullOptions{Session: "alpha"})
	if !errors.Is(err, model.ErrUnauthorized) {
		t.Fatalf("err = %v", err)
	}
	if last := rep.Steps[len(rep.Steps)-1]; last.Step != "report" {
		t.Errorf("last step = %s", last.Step)
	}
}

func TestRotation(t *testing.T) {
	te := newTestEnv(t)
	e := te.engine(nil)
	ctx := context.Background()
	if _, _, err := e.Rotate(ctx, RotateOptions{}); err == nil || !strings.Contains(err.Error(), "no rotation accounts") {
		t.Errorf("no accounts err = %v", err)
	}
	if err := e.SetRotationAccounts([]string{"team-dev", "team-prd", "web-dev"}); err != nil {
		t.Fatal(err)
	}
	// 0 eligible: no credentials yet.
	_, _, err := e.Rotate(ctx, RotateOptions{})
	if err == nil || !strings.Contains(err.Error(), "team-dev: no credentials section") {
		t.Fatalf("0 eligible err = %v", err)
	}
	te.mustSet(e, "export.enabled", "true")
	if _, err := e.Export(ctx, "alpha", Scope{Kind: ScopeSelected, Profiles: []string{"team-dev"}}, ExportOptions{}); err != nil {
		t.Fatal(err)
	}
	// 1 eligible: chosen even when current.
	for range 2 {
		_, to, err := e.Rotate(ctx, RotateOptions{})
		if err != nil || to != "team-dev" {
			t.Fatalf("1 eligible: %q %v", to, err)
		}
	}
	if _, err := e.Export(ctx, "alpha", Scope{Kind: ScopeSelected, Profiles: []string{"team-prd", "web-dev"}}, ExportOptions{}); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		from, to, err := e.Rotate(ctx, RotateOptions{})
		if err != nil || to == from {
			t.Fatalf("many: %q→%q %v", from, to, err)
		}
	}
	info := e.Rotation()
	if len(info.Accounts) != 3 || !info.Accounts[0].Eligible || info.Accounts[0].Expires.IsZero() {
		t.Errorf("info = %+v", info)
	}
	// Set: listed, unlisted-but-existing, unknown.
	if _, to, err := e.Rotate(ctx, RotateOptions{Set: "web-dev"}); err != nil || to != "web-dev" {
		t.Errorf("set: %q %v", to, err)
	}
	if _, to, err := e.Rotate(ctx, RotateOptions{Set: "static-keys"}); err != nil || to != "static-keys" {
		t.Errorf("set unlisted: %q %v", to, err)
	}
	if _, _, err := e.Rotate(ctx, RotateOptions{Set: "nope"}); err == nil {
		t.Error("unknown set accepted")
	}
	cur := te.read(te.paths.ClaudeAccountFile)
	if _, _, err := e.Rotate(ctx, RotateOptions{DryRun: true}); err != nil || te.read(te.paths.ClaudeAccountFile) != cur {
		t.Errorf("dry run wrote: %v", err)
	}
	if _, _, err := e.Rotate(ctx, RotateOptions{Verify: bp(true)}); err == nil || !strings.Contains(err.Error(), "bedrock_region") {
		t.Errorf("verify without region err = %v", err)
	}
	if sh, err := e.ShellIntegration("zsh"); err != nil || !strings.Contains(sh, "AWS_PROFILE") {
		t.Errorf("shell = %q %v", sh, err)
	}
}

func TestGroupsCRUDPersisted(t *testing.T) {
	te := newTestEnv(t)
	e := te.engine(nil)
	if err := e.CreateGroup("ops", "operations"); err != nil {
		t.Fatal(err)
	}
	if err := e.CreateGroup("Bad Name", ""); err == nil {
		t.Error("invalid name accepted")
	}
	if err := e.GroupAdd("ops", "ops-dev", "111100000008", "p_gone-dev"); err != nil {
		t.Fatal(err)
	}
	if err := e.GroupAdd("ops", "nope"); !errors.Is(err, model.ErrNotFound) || !strings.Contains(err.Error(), "nope") {
		t.Errorf("unknown key err = %v", err)
	}
	if err := e.GroupAdd("ghost", "ops-dev"); !errors.Is(err, model.ErrNotFound) {
		t.Errorf("unknown group err = %v", err)
	}
	e2 := te.engine(nil)
	g, err := e2.Group("ops")
	if err != nil || len(g.Members) != 3 || g.Description != "operations" {
		t.Fatalf("persisted group = %+v %v", g, err)
	}
	if err := e2.RenameGroup("ops", "operations"); err != nil {
		t.Fatal(err)
	}
	if err := e2.GroupRemove("operations", "p_ops-dev"); err != nil {
		t.Fatal(err)
	}
	// Roles of kept members survive GroupSet.
	if err := e2.mutateConfig(func(c *state.Config) error {
		g := c.Groups["operations"]
		for i := range g.Members {
			if g.Members[i].AccountID == "111100000008" {
				g.Members[i].Role = "ReadOnly"
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e2.GroupSet("operations", []string{"111100000008", "111100000009"}); err != nil {
		t.Fatal(err)
	}
	g, _ = te.engine(nil).Group("operations")
	if len(g.Members) != 2 || g.Members[0].Role != "ReadOnly" || g.Members[0].Profile != "p_ops-prd" {
		t.Errorf("after set = %+v", g.Members)
	}
	if err := e2.DeleteGroup("main"); err == nil {
		t.Error("main deleted")
	}
	if err := e2.DeleteGroup("operations"); err != nil {
		t.Fatal(err)
	}
	if _, err := te.engine(nil).Group("operations"); !errors.Is(err, model.ErrNotFound) {
		t.Error("deleted group still present")
	}
	// Missing account status: member whose profile was removed from the config.
	if err := e2.GroupAdd("main", "p_gone-dev"); err != nil {
		t.Fatal(err)
	}
	te.write(te.paths.AWSConfig, strings.Replace(te.read(te.paths.AWSConfig), "[profile p_gone-dev]", "[profile p_renamed]", 1), 0o600)
	g, _ = te.engine(nil).Group("main")
	if len(g.Members) != 1 {
		t.Fatalf("main = %+v", g.Members)
	}
	if g.Members[0].Status == model.StatusAvailable {
		t.Errorf("status = %s", g.Members[0].Status)
	}
}

func TestSettings(t *testing.T) {
	te := newTestEnv(t)
	e := te.engine(nil)
	keys := map[string]SettingInfo{}
	for _, s := range e.Settings() {
		keys[s.Key] = s
	}
	for _, k := range []string{"mode", "sso.default_session", "sso.login_flow", "profiles.prefix", "profiles.region", "profiles.output", "roles.policy", "roles.fallbacks", "roles.update_config", "sync.add_new", "sync.remove_stale", "sync.threshold", "export.enabled", "export.credentials_file", "export.strip_prefix", "export.default_profile", "export.concurrency", "export.backup_keep", "export.include_new_in_main", "rotation.strategy", "rotation.verify_bedrock", "rotation.bedrock_region", "rotation.bedrock_model", "rotation.file", "network.ca_bundle", "network.extra_cas", "network.probe_url", "ui.theme", "ui.ascii", "update.repo", "update.check"} {
		if _, ok := keys[k]; !ok {
			t.Errorf("missing setting %s", k)
		}
	}
	if !slices.Equal(keys["export.enabled"].Choices, []string{"true", "false"}) {
		t.Errorf("bool choices = %v", keys["export.enabled"].Choices)
	}
	te.mustSet(e, "roles.fallbacks", "A, B", "export.concurrency", "4", "sync.threshold", "0.5")
	for _, bad := range [][2]string{{"export.concurrency", "0"}, {"export.concurrency", "x"}, {"roles.policy", "bogus"}, {"sync.threshold", "2"}, {"ui.theme", "pink"}, {"nope.key", "1"}, {"export.enabled", "maybe"}} {
		if err := e.SetSetting(bad[0], bad[1]); err == nil {
			t.Errorf("accepted %s=%s", bad[0], bad[1])
		}
	}
	e2 := te.engine(nil)
	got := map[string]string{}
	for _, s := range e2.Settings() {
		got[s.Key] = s.Value
	}
	if got["roles.fallbacks"] != "A,B" || got["export.concurrency"] != "4" || got["sync.threshold"] != "0.5" {
		t.Errorf("persisted = %v", got)
	}
	if err := e2.SetSetting("mode", "compat"); err != nil || e2.Mode() != "compat" || !e2.conf().Export.Enabled {
		t.Errorf("mode compat: %v", err)
	}
}

func TestStatusJSONShape(t *testing.T) {
	te := newTestEnv(t)
	e := te.engine(nil)
	_, _ = e.Discover(context.Background(), "alpha")
	st := e.Status(context.Background())
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for _, k := range []string{"version", "mode", "config_file", "aws_config", "credentials_file", "sessions", "profiles", "groups", "exported"} {
		if _, ok := m[k]; !ok {
			t.Errorf("status json lacks %q: %s", k, b)
		}
	}
	if strings.Contains(string(b), alphaTok) || strings.Contains(string(b), "secret") {
		t.Error("status leaks secrets")
	}
	sess := m["sessions"].([]any)[0].(map[string]any)
	if sess["name"] != "alpha" || sess["logged_in"] != true || sess["accounts_discovered"].(float64) != 12 {
		t.Errorf("session = %v", sess)
	}
}

func TestDoctorBrokenSetup(t *testing.T) {
	te := newTestEnv(t)
	cfg := te.read(te.paths.AWSConfig) + "\n[profile p_orphan]\nsso_session = ghost\nsso_account_id = 444400000001\nsso_role_name = ReadOnly\n"
	te.write(te.paths.AWSConfig, cfg, 0o600)
	te.write(te.paths.AWSCredentials, credsFixture+"\n[old]\naws_access_key_id = ASIAOLD\naws_secret_access_key = s\naws_session_token = t\nx_credorbit_expiration = 2020-01-01T00:00:00Z\n", 0o644)
	te.logout("beta", betaURL)
	te.write(te.paths.LegacyScript, "#!/bin/bash\n", 0o600)
	e := te.engine(nil)
	checks := e.Doctor(context.Background())
	find := func(area, name string) Check {
		for _, c := range checks {
			if c.Area == area && strings.HasPrefix(c.Name, name) {
				return c
			}
		}
		return Check{}
	}
	if c := find("aws-config", "profiles without session"); c.Status != "fail" || !strings.Contains(c.Detail, "ghost") || c.Remedy == "" {
		t.Errorf("orphan check = %+v", c)
	}
	if c := find("credentials", "expired"); c.Status != "warn" || !strings.Contains(c.Detail, "old") {
		t.Errorf("expired check = %+v", c)
	}
	if c := find("sso", "token beta"); c.Status != "fail" || !strings.Contains(c.Remedy, "credorbit login") {
		t.Errorf("token check = %+v", c)
	}
	if c := find("app", "legacy script"); c.Status != "info" {
		t.Errorf("legacy = %+v", c)
	}
	if c := find("permissions", "credentials file"); c.Status != "fail" && runtime.GOOS != "windows" {
		t.Errorf("perm check = %+v", c)
	}
	for _, c := range checks {
		if c.Status != "ok" && c.Status != "info" && c.Remedy == "" {
			t.Errorf("check without remedy: %+v", c)
		}
		if strings.Contains(c.Detail, alphaTok) {
			t.Error("doctor leaked token")
		}
	}
}

func TestImportLegacy(t *testing.T) {
	te := newTestEnv(t)
	src, err := os.ReadFile(filepath.Join("..", "importer", "testdata", "legacy.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(te.dir, "legacy.sh")
	te.write(script, string(src), 0o700)
	ui := &fakeUI{review: false}
	e := te.engine(ui)
	if _, err := e.ImportLegacy(context.Background(), script, true); !errors.Is(err, ErrDeclined) {
		t.Fatalf("declined err = %v", err)
	}
	if e.Mode() != "normal" {
		t.Fatal("declined import applied")
	}
	ui.review = true
	notes, err := e.ImportLegacy(context.Background(), script, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) == 0 || e.Mode() != "compat" {
		t.Errorf("notes = %v mode %s", notes, e.Mode())
	}
	c := te.engine(nil).conf()
	if c.SSO.DefaultSession != "example" || !slices.Contains(c.Roles.Fallbacks, "Example-Admin-Limited") || c.Roles.Policy != model.PolicyScript {
		t.Errorf("imported config = %+v", c.Roles)
	}
	if after, _ := os.ReadFile(script); string(after) != string(src) {
		t.Error("script modified")
	}
	for _, chk := range te.engine(nil).Doctor(context.Background()) {
		if chk.Name == "legacy script" {
			t.Error("legacy hint after import")
		}
	}
}

func TestCheckUpdateUnavailable(t *testing.T) {
	te := newTestEnv(t)
	if _, _, _, err := te.engine(nil).CheckUpdate(context.Background()); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Errorf("err = %v", err)
	}
}

func TestLegacyPseudoSession(t *testing.T) {
	te := newTestEnv(t)
	e := te.engine(nil)
	// Token cached by start URL (AWS CLI legacy scheme).
	tok := model.Token{StartURL: legacyURL, AccessToken: alphaTok, ExpiresAt: time.Now().Add(time.Hour)}
	if err := saveToken(te, model.SSOSession{StartURL: legacyURL}, tok); err != nil {
		t.Fatal(err)
	}
	if !e.LoginStatus(context.Background(), "legacy:legacy-example.awsapps.com").LoggedIn {
		t.Error("legacy session not logged in")
	}
}
