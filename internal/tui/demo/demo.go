// Package demo is an in-memory engine.Service with synthetic data, used by
// `credorbit ui --demo`, screenshots and tests. It never touches the file
// system or the network.
package demo

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PiniPP/credorbit/internal/engine"
	"github.com/PiniPP/credorbit/internal/model"
)

// Service is the fake engine. Safe for concurrent use.
type Service struct {
	mu       sync.Mutex
	speed    float64
	now      func() time.Time
	rep      model.Reporter
	ui       engine.Interaction
	mode     string
	defSess  string
	sessions []model.SSOSession
	login    map[string]time.Time // session → token expiry
	accounts []*acct
	discAt   map[string]time.Time
	groups   map[string]*group
	settings []engine.SettingInfo
	rotAccts []string
	rotCur   string
	lastRun  time.Time
}

type acct struct {
	model.DiscoveredAccount
	profile string
	role    string
	pref    string
	status  model.AccountStatus
	exp     time.Time
}

type group struct {
	name, desc string
	ids        []string
}

// New returns a demo service populated with ~40 synthetic accounts.
func New() *Service {
	s := &Service{speed: 1, now: time.Now, rep: model.Discard, mode: "normal", defSess: "acme-sso"}
	now := s.now()
	s.sessions = []model.SSOSession{
		{Name: "acme-sso", StartURL: "https://acme-demo.awsapps.example/start", Region: "us-east-1"},
		{Name: "acme-sandbox", StartURL: "https://acme-sandbox.awsapps.example/start", Region: "eu-west-1"},
	}
	s.login = map[string]time.Time{"acme-sso": now.Add(3*time.Hour + 40*time.Minute)}
	s.discAt = map[string]time.Time{"acme-sso": now.Add(-3 * time.Hour)}
	names := []string{
		"acme-payments-prd", "acme-payments-stg", "acme-payments-dev",
		"acme-ledger-prd", "acme-ledger-stg", "acme-ledger-dev",
		"acme-identity-prd", "acme-identity-dev",
		"acme-data-lake-prd", "acme-data-lake-dev",
		"acme-analytics-prd", "acme-analytics-test",
		"acme-mobile-api-prd", "acme-mobile-api-stg", "acme-mobile-api-dev",
		"acme-web-prd", "acme-web-stg", "acme-web-dev",
		"acme-billing-prd", "acme-billing-test",
		"acme-search-prd", "acme-search-dev",
		"acme-ml-platform-prd", "acme-ml-platform-sandbox",
		"acme-network-hub-prd", "acme-security-tooling-prd", "acme-log-archive-prd",
		"acme-retired-reporting-prd", "acme-shared-services-prd", "acme-old-crm-dev",
		"acme-ci-runners-dev", "acme-sandbox-alpha", "acme-sandbox-bravo", "acme-sandbox-charlie",
		"acme-claude-bedrock-01", "acme-claude-bedrock-02", "acme-claude-bedrock-03",
		"acme-genai-research-experimental-sandbox", "acme-legacy-mainframe-bridge-test", "acme-partner-gateway-stg",
	}
	newSet := map[string]bool{"acme-ci-runners-dev": true, "acme-genai-research-experimental-sandbox": true,
		"acme-legacy-mainframe-bridge-test": true, "acme-partner-gateway-stg": true}
	missing := map[string]bool{"acme-retired-reporting-prd": true, "acme-old-crm-dev": true}
	for i, n := range names {
		a := &acct{}
		a.ID = fmt.Sprintf("1000000000%02d", i+1)
		a.Name = n
		a.Email = "aws+" + strings.TrimPrefix(n, "acme-") + "@acme.example"
		a.Roles = rolesFor(n, i)
		a.status = model.StatusAvailable
		switch {
		case newSet[n]:
			a.status = model.StatusNew
		case missing[n]:
			a.status = model.StatusMissing
			a.Roles = nil
		}
		if a.status != model.StatusNew {
			a.profile = "p_" + strings.TrimPrefix(n, "acme-")
			if len(a.Roles) > 0 {
				a.role = a.Roles[0]
			} else {
				a.role = "ReadOnlyAccess"
			}
			switch i % 5 {
			case 0:
				a.exp = now.Add(3*time.Hour + 12*time.Minute)
			case 1:
				a.exp = now.Add(9 * time.Minute)
			case 2:
				a.exp = now.Add(-2 * time.Hour)
			case 3:
				a.exp = now.Add(11*time.Hour + 4*time.Minute)
			}
		}
		switch n {
		case "acme-claude-bedrock-01":
			a.exp = now.Add(5*time.Hour + 20*time.Minute)
		case "acme-claude-bedrock-03":
			a.exp = now.Add(-40 * time.Minute)
		}
		if i == 17 {
			a.RolesErr = "ListAccountRoles: throttled (transient)"
		}
		s.accounts = append(s.accounts, a)
	}
	id := func(n string) string {
		for _, a := range s.accounts {
			if a.Name == n {
				return a.ID
			}
		}
		return ""
	}
	s.groups = map[string]*group{
		"main": {name: "main", desc: "Accounts exported by default (Main Accounts)"},
		"dev":  {name: "dev", desc: "Development and test accounts"},
		"prod": {name: "prod", desc: "Production accounts (read-only by default)"},
	}
	for _, n := range []string{"acme-payments-prd", "acme-ledger-prd", "acme-identity-prd", "acme-web-prd",
		"acme-mobile-api-prd", "acme-billing-prd", "acme-data-lake-prd", "acme-shared-services-prd"} {
		s.groups["main"].ids = append(s.groups["main"].ids, id(n))
	}
	for _, a := range s.accounts {
		if a.status == model.StatusNew {
			continue
		}
		switch envOf(a.Name) {
		case "dev", "test":
			s.groups["dev"].ids = append(s.groups["dev"].ids, a.ID)
		case "prd":
			s.groups["prod"].ids = append(s.groups["prod"].ids, a.ID)
		}
	}
	s.rotAccts = []string{"claude-bedrock-01", "claude-bedrock-02", "claude-bedrock-03", "ml-platform-sandbox"}
	s.rotCur = "claude-bedrock-01"
	s.lastRun = now.Add(-3 * time.Hour)
	b := []string{"true", "false"}
	s.settings = []engine.SettingInfo{
		{Key: "ui.theme", Value: "default", Description: "Colour theme of the interactive UI", Choices: []string{"default", "light", "mono"}},
		{Key: "ui.ascii", Value: "false", Description: "Use 7-bit ASCII box drawing and glyphs", Choices: b},
		{Key: "sso.login_flow", Value: "browser", Description: "Default SSO login flow", Choices: []string{"browser", "device"}},
		{Key: "roles.policy", Value: "configured", Description: "Which assigned role is used per account", Choices: []string{"configured", "manual", "strongest", "readonly", "script"}},
		{Key: "roles.update_config", Value: "false", Description: "Persist the role that worked to ~/.aws/config (reviewed)", Choices: b},
		{Key: "profiles.prefix", Value: "p_", Description: "Prefix of generated AWS profile names"},
		{Key: "profiles.region", Value: "us-east-1", Description: "Default region written to new profiles"},
		{Key: "sync.add_new", Value: "true", Description: "Add profiles for newly discovered accounts", Choices: b},
		{Key: "sync.remove_stale", Value: "prompt", Description: "Stale-profile removal (always reviewed)", Choices: []string{"never", "prompt"}},
		{Key: "export.enabled", Value: "true", Description: "Write ~/.aws/credentials after fetching", Choices: b},
		{Key: "export.concurrency", Value: "8", Description: "Parallel credential requests (1-64)"},
		{Key: "export.strip_prefix", Value: "true", Description: "Write p_x profiles as [x] credential sections", Choices: b},
		{Key: "export.default_profile", Value: "p_payments-dev", Description: "Profile copied into [default] (empty = leave alone)"},
		{Key: "rotation.strategy", Value: "random", Description: "Claude/Bedrock rotation strategy", Choices: []string{"random", "round-robin", "manual"}},
		{Key: "rotation.verify_bedrock", Value: "true", Description: "Verify Bedrock access before switching", Choices: b},
		{Key: "update.check", Value: "true", Description: "Check for new releases on start", Choices: b},
	}
	return s
}

func rolesFor(name string, i int) []string {
	switch envOf(name) {
	case "prd":
		r := []string{"ReadOnlyAccess", "PowerUserAccess", "DevOps"}
		if i%3 == 0 {
			r = append(r, "AdministratorAccess")
		}
		return r
	case "sandbox":
		return []string{"AdministratorAccess"}
	case "dev", "test":
		return []string{"AdministratorAccess", "DevOps", "ReadOnlyAccess"}
	case "stg":
		return []string{"PowerUserAccess", "ReadOnlyAccess"}
	}
	return []string{"AdministratorAccess", "ReadOnlyAccess"}
}

func envOf(name string) string {
	for _, e := range []string{"prd", "stg", "dev", "test", "sandbox"} {
		if strings.HasSuffix(name, "-"+e) || strings.Contains(name, "-"+e+"-") {
			return e
		}
	}
	return ""
}

// SetSpeed scales every simulated delay (0 = instant, for tests).
func (s *Service) SetSpeed(f float64) { s.mu.Lock(); s.speed = f; s.mu.Unlock() }

func (s *Service) sleep(ctx context.Context, d time.Duration) error {
	s.mu.Lock()
	d = time.Duration(float64(d) * s.speed)
	s.mu.Unlock()
	if d <= 0 {
		return ctx.Err()
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

func (s *Service) emit(e model.Event) {
	s.mu.Lock()
	r := s.rep
	s.mu.Unlock()
	r.Report(e)
}

func (s *Service) ask() engine.Interaction {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ui == nil {
		return autoUI{}
	}
	return s.ui
}

type autoUI struct{}

func (autoUI) Confirm(_ context.Context, _, _ string, def bool) (bool, error) { return def, nil }
func (autoUI) ReviewPlan(context.Context, string, string) (bool, error)       { return true, nil }
func (autoUI) SelectAccounts(_ context.Context, _ string, _ []engine.AccountRow, pre []string) ([]string, error) {
	return pre, nil
}
func (autoUI) SelectRole(_ context.Context, _ model.DiscoveredAccount, cur string) (string, error) {
	return cur, nil
}
func (autoUI) LoginPrompt(string, string) {}

// --- wiring & configuration ---

func (s *Service) SetInteraction(i engine.Interaction) { s.mu.Lock(); s.ui = i; s.mu.Unlock() }
func (s *Service) SetReporter(r model.Reporter) {
	if r == nil {
		r = model.Discard
	}
	s.mu.Lock()
	s.rep = r
	s.mu.Unlock()
}
func (s *Service) ConfigPath() string { return "~/.config/credorbit/config.yaml (demo, in memory)" }
func (s *Service) Mode() string       { s.mu.Lock(); defer s.mu.Unlock(); return s.mode }
func (s *Service) SetMode(m string) error {
	if m != "normal" && m != "compat" {
		return fmt.Errorf("unknown mode %q (want normal|compat)", m)
	}
	s.mu.Lock()
	s.mode = m
	s.mu.Unlock()
	return nil
}
func (s *Service) SaveConfig() error { return nil }
func (s *Service) Version() string   { return "0.9.0-demo" }

func (s *Service) Settings() []engine.SettingInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]engine.SettingInfo, len(s.settings))
	copy(out, s.settings)
	return out
}

func (s *Service) SetSetting(key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, st := range s.settings {
		if st.Key != key {
			continue
		}
		if len(st.Choices) > 0 && !contains(st.Choices, value) {
			return fmt.Errorf("invalid value %q for %s (want %s)", value, key, strings.Join(st.Choices, "|"))
		}
		switch key {
		case "export.concurrency":
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 || n > 64 {
				return fmt.Errorf("export.concurrency must be a number between 1 and 64")
			}
		case "profiles.prefix":
			if strings.TrimSpace(value) == "" || strings.ContainsAny(value, " []") {
				return fmt.Errorf("profiles.prefix must be non-empty and contain no spaces or brackets")
			}
		}
		s.settings[i].Value = value
		return nil
	}
	return fmt.Errorf("unknown setting %q: %w", key, model.ErrNotFound)
}

func (s *Service) setting(key string) string {
	for _, st := range s.settings {
		if st.Key == key {
			return st.Value
		}
	}
	return ""
}

func contains(l []string, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

// --- sessions & login ---

func (s *Service) Sessions() ([]model.SSOSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]model.SSOSession(nil), s.sessions...), nil
}
func (s *Service) DefaultSession() string { s.mu.Lock(); defer s.mu.Unlock(); return s.defSess }
func (s *Service) SetDefaultSession(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.sessions {
		if x.Name == name {
			s.defSess = name
			return nil
		}
	}
	return fmt.Errorf("session %q: %w", name, model.ErrNotFound)
}

func (s *Service) AddSession(n model.SSOSession) error {
	if n.Name == "" || !strings.HasPrefix(n.StartURL, "https://") || n.Region == "" {
		return errors.New("name, https start URL and region are required")
	}
	ok, err := s.ask().Confirm(context.Background(), "Add SSO session "+n.Name+"?",
		"An [sso-session "+n.Name+"] block would be written to ~/.aws/config (demo: in memory only).", true)
	if err != nil {
		return err
	}
	if !ok {
		return model.ErrCanceled
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.sessions {
		if x.Name == n.Name {
			return fmt.Errorf("session %q already exists", n.Name)
		}
	}
	s.sessions = append(s.sessions, n)
	return nil
}

func (s *Service) LoginStatus(_ context.Context, session string) engine.LoginState {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := engine.LoginState{}
	for _, x := range s.sessions {
		if x.Name == session {
			st.Session = x
		}
	}
	if exp, ok := s.login[session]; ok && exp.After(s.now()) {
		st.LoggedIn, st.ExpiresAt, st.Refresh = true, exp, true
	}
	return st
}

func (s *Service) Login(ctx context.Context, session, flow string, force bool) (engine.LoginState, error) {
	st := s.LoginStatus(ctx, session)
	if st.Session.Name == "" {
		return st, fmt.Errorf("session %q: %w", session, model.ErrNotFound)
	}
	if st.LoggedIn && !force {
		s.emit(model.Event{Kind: model.EventInfo, Step: "login", Message: "token still valid"})
		return st, nil
	}
	s.emit(model.Event{Kind: model.EventStepStart, Step: "login", Message: "Signing in to " + session})
	url := strings.TrimSuffix(st.Session.StartURL, "/start") + "/device"
	code := "DEMO-7QX4"
	if flow != "device" {
		url = "http://127.0.0.1:53682/oauth/callback (demo browser flow)"
		code = "DEMO-BRWS"
	}
	s.ask().LoginPrompt(url, code)
	if err := s.sleep(ctx, 1500*time.Millisecond); err != nil {
		return st, fmt.Errorf("login: %w", model.ErrCanceled)
	}
	s.mu.Lock()
	s.login[session] = s.now().Add(8 * time.Hour)
	s.mu.Unlock()
	s.emit(model.Event{Kind: model.EventStepDone, Step: "login", Message: "Signed in"})
	return s.LoginStatus(ctx, session), nil
}

func (s *Service) Logout(_ context.Context, session string) error {
	s.mu.Lock()
	delete(s.login, session)
	s.mu.Unlock()
	return nil
}

// --- discovery & accounts ---

func (s *Service) Discover(ctx context.Context, session string) (*model.Discovery, error) {
	if !s.LoginStatus(ctx, session).LoggedIn {
		return &model.Discovery{Session: session, Err: "sso token expired or invalid"}, fmt.Errorf("discover: %w", model.ErrUnauthorized)
	}
	s.emit(model.Event{Kind: model.EventStepStart, Step: "discover", Message: "Listing accounts"})
	s.mu.Lock()
	var list []*acct
	for _, a := range s.accounts {
		if a.status != model.StatusMissing {
			list = append(list, a)
		}
	}
	s.mu.Unlock()
	for i, a := range list {
		if err := s.sleep(ctx, 30*time.Millisecond); err != nil {
			return &model.Discovery{Session: session, Err: "canceled"}, fmt.Errorf("discover: %w", model.ErrCanceled)
		}
		if a.RolesErr != "" {
			s.emit(model.Event{Kind: model.EventItemFailed, Step: "discover", Subject: a.Name, Message: a.RolesErr, Err: model.ErrTransient})
		} else {
			s.emit(model.Event{Kind: model.EventItemOK, Step: "discover", Subject: a.Name, Message: strings.Join(a.Roles, ", ")})
		}
		s.emit(model.Event{Kind: model.EventProgress, Step: "discover", Done: i + 1, Total: len(list)})
	}
	s.mu.Lock()
	s.discAt[session] = s.now()
	s.mu.Unlock()
	s.emit(model.Event{Kind: model.EventStepDone, Step: "discover", Message: fmt.Sprintf("%d accounts", len(list))})
	d := s.discovery(session)
	d.Cached = false
	return d, nil
}

func (s *Service) discovery(session string) *model.Discovery {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.discAt[session]
	if !ok {
		return nil
	}
	d := &model.Discovery{Session: session, At: at, Complete: true, Cached: true}
	for _, x := range s.sessions {
		if x.Name == session {
			d.StartURL = x.StartURL
		}
	}
	for _, a := range s.accounts {
		if a.status != model.StatusMissing {
			d.Accounts = append(d.Accounts, a.DiscoveredAccount)
		}
	}
	model.SortAccounts(d.Accounts)
	return d
}

func (s *Service) CachedDiscovery(session string) *model.Discovery { return s.discovery(session) }

func (s *Service) Accounts(session string) []engine.AccountRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.discAt[session]; !ok {
		return nil
	}
	rows := make([]engine.AccountRow, 0, len(s.accounts))
	for _, a := range s.accounts {
		rows = append(rows, s.row(a))
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Account.Name < rows[j].Account.Name })
	return rows
}

func (s *Service) row(a *acct) engine.AccountRow {
	r := engine.AccountRow{Account: a.DiscoveredAccount, Env: envOf(a.Name), Status: a.status, CredsExp: a.exp}
	r.Account.Roles = append([]string(nil), a.Roles...)
	if a.profile != "" {
		r.Profiles = []string{a.profile}
		r.Role = a.role
		if a.pref != "" {
			r.Role = a.pref
		}
	}
	names := make([]string, 0, len(s.groups))
	for n, g := range s.groups {
		if contains(g.ids, a.ID) {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	r.Groups = names
	return r
}

func (s *Service) find(key string) *acct {
	for _, a := range s.accounts {
		if a.ID == key || a.profile == key || a.Name == key {
			return a
		}
	}
	return nil
}

func (s *Service) SetPreferredRole(accountID, role string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.find(accountID)
	if a == nil {
		return fmt.Errorf("account %s: %w", accountID, model.ErrNotFound)
	}
	if len(a.Roles) > 0 && !a.HasRole(role) {
		return fmt.Errorf("role %s is not assigned to %s", role, a.Name)
	}
	a.pref = role
	return nil
}
