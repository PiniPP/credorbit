package demo

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/PiniPP/credorbit/internal/engine"
	"github.com/PiniPP/credorbit/internal/model"
)

// --- groups ---

func (s *Service) groupInfo(g *group) engine.GroupInfo {
	gi := engine.GroupInfo{Name: g.name, Description: g.desc}
	for _, id := range g.ids {
		m := engine.GroupMember{AccountID: id, Status: model.StatusUnknown}
		if a := s.find(id); a != nil {
			m.AccountName, m.Profile, m.Role, m.Status = a.Name, a.profile, a.role, a.status
		}
		gi.Members = append(gi.Members, m)
	}
	return gi
}

func (s *Service) Groups() []engine.GroupInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.groups))
	for n := range s.groups {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		if (names[i] == "main") != (names[j] == "main") {
			return names[i] == "main"
		}
		return names[i] < names[j]
	})
	out := make([]engine.GroupInfo, 0, len(names))
	for _, n := range names {
		out = append(out, s.groupInfo(s.groups[n]))
	}
	return out
}

func (s *Service) Group(name string) (engine.GroupInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.groups[name]
	if g == nil {
		return engine.GroupInfo{}, fmt.Errorf("group %q: %w", name, model.ErrNotFound)
	}
	return s.groupInfo(g), nil
}

func validName(n string) error {
	if n == "" || strings.ContainsAny(n, " \t,/") {
		return errors.New("group names must be non-empty and contain no spaces, commas or slashes")
	}
	return nil
}

func (s *Service) CreateGroup(name, description string) error {
	if err := validName(name); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.groups[name] != nil {
		return fmt.Errorf("group %q already exists", name)
	}
	s.groups[name] = &group{name: name, desc: description}
	return nil
}

func (s *Service) DeleteGroup(name string) error {
	if name == "main" {
		return errors.New("the main group cannot be deleted (empty it instead)")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.groups[name] == nil {
		return fmt.Errorf("group %q: %w", name, model.ErrNotFound)
	}
	delete(s.groups, name)
	return nil
}

func (s *Service) RenameGroup(oldName, newName string) error {
	if oldName == "main" {
		return errors.New("the main group cannot be renamed")
	}
	if err := validName(newName); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.groups[oldName]
	if g == nil {
		return fmt.Errorf("group %q: %w", oldName, model.ErrNotFound)
	}
	if s.groups[newName] != nil {
		return fmt.Errorf("group %q already exists", newName)
	}
	delete(s.groups, oldName)
	g.name = newName
	s.groups[newName] = g
	return nil
}

func (s *Service) GroupAdd(name string, keys ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.groups[name]
	if g == nil {
		return fmt.Errorf("group %q: %w", name, model.ErrNotFound)
	}
	for _, k := range keys {
		a := s.find(k)
		if a == nil {
			return fmt.Errorf("account %q: %w", k, model.ErrNotFound)
		}
		if !contains(g.ids, a.ID) {
			g.ids = append(g.ids, a.ID)
		}
	}
	return nil
}

func (s *Service) GroupRemove(name string, keys ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.groups[name]
	if g == nil {
		return fmt.Errorf("group %q: %w", name, model.ErrNotFound)
	}
	for _, k := range keys {
		if a := s.find(k); a != nil {
			k = a.ID
		}
		out := g.ids[:0]
		for _, id := range g.ids {
			if id != k {
				out = append(out, id)
			}
		}
		g.ids = out
	}
	return nil
}

func (s *Service) GroupSet(name string, accountIDs []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.groups[name]
	if g == nil {
		return fmt.Errorf("group %q: %w", name, model.ErrNotFound)
	}
	g.ids = append([]string(nil), accountIDs...)
	return nil
}

func (s *Service) NewAccounts(session string) []engine.AccountRow {
	var out []engine.AccountRow
	for _, r := range s.Accounts(session) {
		if r.Status == model.StatusNew && len(r.Groups) == 0 {
			out = append(out, r)
		}
	}
	return out
}

// --- rotation ---

func (s *Service) Rotation() engine.RotationInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	ri := engine.RotationInfo{Target: "claude", File: "~/.claude-aws-account", Current: s.rotCur, Strategy: s.setting("rotation.strategy")}
	now := s.now()
	for _, sec := range s.rotAccts {
		ra := engine.RotationAccount{Section: sec}
		a := s.find("p_" + sec)
		switch {
		case a == nil:
			ra.Reason = "no such profile"
		case a.exp.IsZero():
			ra.Reason = "no exported credentials"
		case !a.exp.After(now):
			ra.Reason = "credentials expired"
			ra.Expires = a.exp
		default:
			ra.Eligible, ra.Expires = true, a.exp
		}
		ri.Accounts = append(ri.Accounts, ra)
	}
	return ri
}

func (s *Service) Rotate(ctx context.Context, o engine.RotateOptions) (string, string, error) {
	ri := s.Rotation()
	from := ri.Current
	to := o.Set
	if to == "" {
		var el []string
		for _, a := range ri.Accounts {
			if a.Eligible {
				el = append(el, a.Section)
			}
		}
		if len(el) == 0 {
			return from, "", errors.New("no eligible rotation accounts (export credentials first)")
		}
		to = el[0]
		for i, e := range el {
			if e == from {
				to = el[(i+1)%len(el)]
			}
		}
	}
	s.emit(model.Event{Kind: model.EventStepStart, Step: "rotate", Message: from + " → " + to})
	if err := s.sleep(ctx, 600*time.Millisecond); err != nil {
		return from, "", fmt.Errorf("rotate: %w", model.ErrCanceled)
	}
	if !o.DryRun {
		s.mu.Lock()
		s.rotCur = to
		s.mu.Unlock()
	}
	s.emit(model.Event{Kind: model.EventStepDone, Step: "rotate", Message: "now " + to})
	return from, to, nil
}

func (s *Service) SetRotationAccounts(sections []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rotAccts = append([]string(nil), sections...)
	return nil
}

func (s *Service) ShellIntegration(shell string) (string, error) {
	f := "$HOME/.claude-aws-account"
	switch shell {
	case "zsh", "bash":
		return fmt.Sprintf(`# CredOrbit · Claude Code / Bedrock account (add to ~/.%src)
credorbit_claude_profile() {
  if [ -r "%s" ]; then
    export AWS_PROFILE="$(cat "%s")"
    export CLAUDE_CODE_USE_BEDROCK=1
  fi
}
credorbit_claude_profile
alias claude-rotate='credorbit rotate && credorbit_claude_profile'
`, shell, f, f), nil
	case "fish":
		return `# CredOrbit · add to ~/.config/fish/config.fish
function credorbit_claude_profile
    if test -r $HOME/.claude-aws-account
        set -gx AWS_PROFILE (cat $HOME/.claude-aws-account)
        set -gx CLAUDE_CODE_USE_BEDROCK 1
    end
end
credorbit_claude_profile
alias claude-rotate 'credorbit rotate; and credorbit_claude_profile'
`, nil
	case "powershell":
		return `# CredOrbit · add to $PROFILE
function Set-CredOrbitClaudeProfile {
    $f = Join-Path $HOME ".claude-aws-account"
    if (Test-Path $f) {
        $env:AWS_PROFILE = (Get-Content $f -Raw).Trim()
        $env:CLAUDE_CODE_USE_BEDROCK = "1"
    }
}
Set-CredOrbitClaudeProfile
function claude-rotate { credorbit rotate; Set-CredOrbitClaudeProfile }
`, nil
	}
	return "", fmt.Errorf("unsupported shell %q (want zsh|bash|fish|powershell)", shell)
}

// --- diagnostics ---

func (s *Service) Status(ctx context.Context) engine.Status {
	st := engine.Status{Version: s.Version(), Mode: s.Mode(), ConfigFile: s.ConfigPath(),
		AWSConfig: "~/.aws/config", Credentials: "~/.aws/credentials", Groups: map[string]int{}}
	sess, _ := s.Sessions()
	for _, x := range sess {
		ls := s.LoginStatus(ctx, x.Name)
		ss := engine.SessionStatus{Name: x.Name, StartURL: x.StartURL, Region: x.Region, LoggedIn: ls.LoggedIn, ExpiresAt: ls.ExpiresAt, Refresh: ls.Refresh}
		if d := s.discovery(x.Name); d != nil {
			ss.Accounts, ss.DiscoveredAt = len(d.Accounts), d.At
		}
		st.Sessions = append(st.Sessions, ss)
	}
	for _, g := range s.Groups() {
		st.Groups[g.Name] = len(g.Members)
	}
	s.mu.Lock()
	now := s.now()
	for _, a := range s.accounts {
		if a.profile != "" {
			st.Profiles++
		}
		if !a.exp.IsZero() {
			st.Exported = append(st.Exported, engine.CredentialStatus{Section: strings.TrimPrefix(a.profile, "p_"), Expires: a.exp, Expired: !a.exp.After(now), Managed: true})
		}
	}
	st.LastRun = s.lastRun
	s.mu.Unlock()
	st.Exported = append(st.Exported, engine.CredentialStatus{Section: "default", Expires: now.Add(3 * time.Hour), Managed: true},
		engine.CredentialStatus{Section: "legacy-static-user"})
	ri := s.Rotation()
	st.Rotation = &ri
	return st
}

func (s *Service) Doctor(ctx context.Context) []engine.Check {
	_ = s.sleep(ctx, 500*time.Millisecond)
	return []engine.Check{
		{Area: "app", Name: "version", Status: "info", Detail: "CredOrbit " + s.Version() + " (demo mode, nothing is written)"},
		{Area: "aws-config", Name: "~/.aws/config", Status: "ok", Detail: "38 SSO profiles, 2 sso-session blocks, parses cleanly"},
		{Area: "aws-config", Name: "duplicate profiles", Status: "ok", Detail: "none"},
		{Area: "sso", Name: "session acme-sso", Status: "ok", Detail: "token valid, refresh token available"},
		{Area: "sso", Name: "session acme-sandbox", Status: "warn", Detail: "signed out", Remedy: "credorbit login --session acme-sandbox"},
		{Area: "network", Name: "TLS to portal.sso.us-east-1.amazonaws.com", Status: "ok", Detail: "public Amazon CA chain"},
		{Area: "network", Name: "proxy", Status: "info", Detail: "no HTTPS_PROXY set"},
		{Area: "credentials", Name: "~/.aws/credentials permissions", Status: "ok", Detail: "0600"},
		{Area: "credentials", Name: "expired sections", Status: "warn", Detail: "8 managed sections expired", Remedy: "credorbit export --group main"},
		{Area: "permissions", Name: "config directory", Status: "ok", Detail: "owner-only"},
		{Area: "claude", Name: "rotation account file", Status: "ok", Detail: "~/.claude-aws-account → claude-bedrock-01"},
		{Area: "claude", Name: "rotation candidate claude-bedrock-03", Status: "fail", Detail: "credentials expired", Remedy: "credorbit export --profile p_claude-bedrock-03"},
	}
}

// --- migration & updates ---

func (s *Service) ImportLegacy(_ context.Context, path string, compat bool) ([]string, error) {
	return []string{"demo: legacy import is simulated; nothing was read from " + path}, nil
}

func (s *Service) CheckUpdate(ctx context.Context) (string, string, bool, error) {
	if err := s.sleep(ctx, 700*time.Millisecond); err != nil {
		return "", "", false, err
	}
	return "1.0.0", "https://github.com/PiniPP/credorbit/releases/tag/v1.0.0", true, nil
}

var _ engine.Service = (*Service)(nil)
