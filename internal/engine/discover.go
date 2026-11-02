package engine

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/credorbit/credorbit/internal/credexport"
	"github.com/credorbit/credorbit/internal/inventory"
	"github.com/credorbit/credorbit/internal/model"
	"github.com/credorbit/credorbit/internal/sso"
	"github.com/credorbit/credorbit/internal/state"
)

// Discover runs a live discovery. Only a complete discovery replaces the
// cached one; a partial/failed one is returned (and kept in memory) but never
// overwrites a good cache.
func (e *Engine) Discover(ctx context.Context, session string) (*model.Discovery, error) {
	return e.discover(ctx, session, true)
}

func (e *Engine) discover(ctx context.Context, session string, persist bool) (*model.Discovery, error) {
	s, err := e.session(session)
	if err != nil {
		return nil, err
	}
	tok, err := e.token(ctx, s)
	if err != nil {
		return nil, err
	}
	c := e.conf()
	d := sso.Discover(ctx, e.ssoClient(s.Region), tok, s, e.parallel(c, 0), e.reporter())
	d.At = e.now().UTC()
	e.mu.Lock()
	e.live[s.Name] = d
	e.mu.Unlock()
	if ctx.Err() != nil {
		d.Complete = false
		if d.Err == "" {
			d.Err = ctx.Err().Error()
		}
	}
	if !d.Complete {
		e.log.Warn("discovery incomplete", "session", s.Name, "err", d.Err)
		if ctx.Err() != nil {
			return d, fmt.Errorf("discovery canceled: %w: %w", model.ErrCanceled, ctx.Err())
		}
		return d, fmt.Errorf("discovery of %q incomplete: %s", s.Name, d.Err)
	}
	e.log.Info("discovery complete", "session", s.Name, "accounts", len(d.Accounts))
	if persist {
		cp := *d
		cp.Accounts = slices.Clone(d.Accounts)
		if err := e.mutateState(func(st *state.State) { st.SetDiscovery(&cp) }); err != nil {
			return d, fmt.Errorf("save discovery cache: %w", err)
		}
	}
	return d, nil
}

// CachedDiscovery returns the offline discovery cache (Cached=true) or nil.
func (e *Engine) CachedDiscovery(session string) *model.Discovery {
	if session == "" {
		session = e.DefaultSession()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	d := e.st.Discoveries[session]
	if d == nil {
		return nil
	}
	cp := *d
	cp.Accounts = slices.Clone(d.Accounts)
	cp.Cached = true
	return &cp
}

// latest returns the live discovery if complete, else the cache, else the
// failed live one, else nil.
func (e *Engine) latest(session string) *model.Discovery {
	e.mu.Lock()
	live := e.live[session]
	e.mu.Unlock()
	if live != nil && live.Complete {
		return live
	}
	if c := e.CachedDiscovery(session); c != nil {
		return c
	}
	return live
}

// assignedMap converts a discovery into accountID → roles (nil when unknown).
func assignedMap(d *model.Discovery) map[string][]string {
	if d == nil {
		return nil
	}
	m := map[string][]string{}
	for _, a := range d.Accounts {
		if a.RolesErr == "" || a.RolesErr == model.ErrNoAccess.Error() {
			m[a.ID] = a.Roles
		}
	}
	return m
}

// InferEnv derives an environment label from an account name.
func InferEnv(name string) string {
	toks := strings.FieldsFunc(strings.ToLower(name), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	envs := map[string]string{
		"prd": "prd", "prod": "prd", "production": "prd",
		"stg": "stg", "stage": "stg", "staging": "stg",
		"dev": "dev", "development": "dev",
		"tst": "test", "test": "test", "qa": "test",
		"sandbox": "sandbox", "sbx": "sandbox", "poc": "sandbox",
	}
	for i := len(toks) - 1; i >= 0; i-- {
		if v, ok := envs[toks[i]]; ok {
			return v
		}
	}
	return ""
}

func (e *Engine) Accounts(session string) []AccountRow {
	s, err := e.session(session)
	if err != nil {
		return nil
	}
	return e.accountRows(s, e.latest(s.Name))
}

func (e *Engine) accountRows(s model.SSOSession, d *model.Discovery) []AccountRow {
	profiles, _ := e.profiles()
	var mine []model.Profile
	for _, p := range profiles {
		if inSession(p, s) {
			mine = append(mine, p)
		}
	}
	c := e.conf()
	exp := credexport.ReadExpirations(e.credsFile(c))
	strip := c.Export.StripPrefix || e.compat()
	var rows []AccountRow
	for _, it := range inventory.Build(d, mine, s.Name) {
		r := AccountRow{Account: it.Account, Status: it.Status, Env: InferEnv(it.Account.Name)}
		gs := map[string]bool{}
		for _, g := range c.GroupsOf(it.Account.ID, "") {
			gs[g] = true
		}
		for _, p := range it.Profiles {
			r.Profiles = append(r.Profiles, p.Name)
			for _, g := range c.GroupsOf("", p.Name) {
				gs[g] = true
			}
		}
		if pp := primary(it.Profiles, c.Profiles.Prefix); pp != nil {
			r.Role = pp.Role
			r.CredsExp = exp[credexport.SectionName(pp.Name, c.Profiles.Prefix, strip)]
		}
		for _, g := range c.GroupNames() {
			if gs[g] {
				r.Groups = append(r.Groups, g)
			}
		}
		rows = append(rows, r)
	}
	return rows
}

// primary picks the profile used for an account: the first (by name) with the
// prefix, else the first.
func primary(ps []model.Profile, prefix string) *model.Profile {
	if len(ps) == 0 {
		return nil
	}
	for i := range ps {
		if prefix != "" && strings.HasPrefix(ps[i].Name, prefix) {
			return &ps[i]
		}
	}
	return &ps[0]
}

// SetPreferredRole stores the preferred/manual role for an account. When a
// discovery knows the account's roles, the role must be among them.
func (e *Engine) SetPreferredRole(accountID, role string) error {
	if !state.ValidAccountID(accountID) {
		return fmt.Errorf("%q is not a 12-digit account ID", accountID)
	}
	if role != "" {
		for _, s := range e.sessionNames() {
			if d := e.latest(s); d != nil {
				if a, ok := d.ByID()[accountID]; ok && a.RolesErr == "" && !a.HasRole(role) {
					return fmt.Errorf("role %s is not assigned on account %s (assigned: %s)", role, accountID, strings.Join(a.Roles, ", "))
				}
			}
		}
	}
	return e.mutateConfig(func(c *state.Config) error {
		p := c.Accounts[accountID]
		p.Role = role
		if p == (state.AccountPref{}) {
			delete(c.Accounts, accountID)
		} else {
			c.Accounts[accountID] = p
		}
		return nil
	})
}

func (e *Engine) sessionNames() []string {
	ss, _ := e.Sessions()
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.Name
	}
	return out
}

// NewAccounts lists discovered accounts that are unconfigured or in no group.
func (e *Engine) NewAccounts(session string) []AccountRow {
	var out []AccountRow
	for _, r := range e.Accounts(session) {
		if r.Status == model.StatusMissing {
			continue
		}
		if r.Status == model.StatusNew || len(r.Groups) == 0 {
			out = append(out, r)
		}
	}
	return out
}

// PreferredRoles returns accountID → preferred/manual role (not part of
// Service; the CLI uses it through an interface assertion).
func (e *Engine) PreferredRoles() map[string]string {
	c := e.conf()
	out := map[string]string{}
	for id, p := range c.Accounts {
		if p.Role != "" {
			out[id] = p.Role
		}
	}
	return out
}
