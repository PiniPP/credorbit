package engine

import (
	"fmt"
	"strings"

	"github.com/PiniPP/credorbit/internal/model"
	"github.com/PiniPP/credorbit/internal/state"
)

// accountIndex maps profile names and discovered accounts for group resolution.
type accountIndex struct {
	profiles map[string]model.Profile // by profile name
	byID     map[string][]model.Profile
	names    map[string]string // account ID → account name (discovery)
	status   map[string]model.AccountStatus
}

func (e *Engine) index() accountIndex {
	ix := accountIndex{profiles: map[string]model.Profile{}, byID: map[string][]model.Profile{}, names: map[string]string{}, status: map[string]model.AccountStatus{}}
	ps, _ := e.profiles()
	for _, p := range ps {
		ix.profiles[p.Name] = p
		if p.AccountID != "" {
			ix.byID[p.AccountID] = append(ix.byID[p.AccountID], p)
		}
	}
	ss, _ := e.Sessions()
	for _, s := range ss {
		for _, r := range e.accountRows(s, e.latest(s.Name)) {
			if _, ok := ix.status[r.Account.ID]; !ok || r.Status == model.StatusAvailable {
				ix.status[r.Account.ID] = r.Status
			}
			if r.Status != model.StatusMissing && r.Status != model.StatusUnknown || ix.names[r.Account.ID] == "" {
				ix.names[r.Account.ID] = r.Account.Name
			}
		}
	}
	return ix
}

// member resolves a key (account ID or profile name, with or without prefix).
func (ix accountIndex) member(key, prefix string) (state.Member, bool) {
	if state.ValidAccountID(key) {
		if ps := ix.byID[key]; len(ps) > 0 {
			return state.Member{AccountID: key}, true
		}
		if _, ok := ix.names[key]; ok {
			return state.Member{AccountID: key}, true
		}
		return state.Member{}, false
	}
	for _, n := range []string{key, prefix + key} {
		if p, ok := ix.profiles[n]; ok {
			return state.Member{AccountID: p.AccountID, Profile: p.Name}, true
		}
	}
	return state.Member{}, false
}

func (e *Engine) groupInfo(c *state.Config, g *state.Group, ix accountIndex) GroupInfo {
	gi := GroupInfo{Name: g.Name, Description: g.Description}
	for _, m := range g.Members {
		gm := GroupMember{AccountID: m.AccountID, Profile: m.Profile, Role: m.Role}
		if gm.AccountID == "" {
			if p, ok := ix.profiles[m.Profile]; ok {
				gm.AccountID = p.AccountID
			}
		}
		if gm.Profile == "" {
			if pp := primary(ix.byID[gm.AccountID], c.Profiles.Prefix); pp != nil {
				gm.Profile = pp.Name
			}
		}
		if gm.Role == "" {
			if p, ok := ix.profiles[gm.Profile]; ok {
				gm.Role = p.Role
			}
		}
		gm.AccountName = ix.names[gm.AccountID]
		_, hasProfile := ix.profiles[gm.Profile]
		st, known := ix.status[gm.AccountID]
		switch {
		case known && st == model.StatusNew:
			gm.Status = st // discovered, not configured yet
		case !hasProfile && len(ix.byID[gm.AccountID]) == 0:
			gm.Status = model.StatusMissing // no profile configured for this member any more
		case known:
			gm.Status = st
		default:
			gm.Status = model.StatusUnknown
		}
		gi.Members = append(gi.Members, gm)
	}
	return gi
}

func (e *Engine) Groups() []GroupInfo {
	c := e.conf()
	ix := e.index()
	var out []GroupInfo
	for _, n := range c.GroupNames() {
		out = append(out, e.groupInfo(c, c.Groups[n], ix))
	}
	return out
}

func (e *Engine) Group(name string) (GroupInfo, error) {
	c := e.conf()
	g, err := c.Group(name)
	if err != nil {
		return GroupInfo{}, err
	}
	return e.groupInfo(c, g, e.index()), nil
}

func (e *Engine) CreateGroup(name, description string) error {
	if !state.ValidGroupName(name) {
		return fmt.Errorf("invalid group name %q (lowercase letters, digits, '.', '_', '-'; max 64)", name)
	}
	return e.mutateConfig(func(c *state.Config) error {
		if c.Groups[name] != nil {
			return fmt.Errorf("group %q already exists", name)
		}
		c.EnsureGroup(name, description)
		return nil
	})
}

func (e *Engine) DeleteGroup(name string) error {
	return e.mutateConfig(func(c *state.Config) error { return c.DeleteGroup(name) })
}

func (e *Engine) RenameGroup(oldName, newName string) error {
	return e.mutateConfig(func(c *state.Config) error { return c.RenameGroup(oldName, newName) })
}

func (e *Engine) resolveKeys(keys []string, prefix string) ([]state.Member, error) {
	ix := e.index()
	var out []state.Member
	var unknown []string
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		m, ok := ix.member(k, prefix)
		if !ok {
			unknown = append(unknown, k)
			continue
		}
		out = append(out, m)
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("unknown account or profile: %s: %w", strings.Join(unknown, ", "), model.ErrNotFound)
	}
	return out, nil
}

func (e *Engine) GroupAdd(name string, keys ...string) error {
	c := e.conf()
	if _, err := c.Group(name); err != nil {
		return err
	}
	ms, err := e.resolveKeys(keys, c.Profiles.Prefix)
	if err != nil {
		return err
	}
	return e.mutateConfig(func(c *state.Config) error {
		g, err := c.Group(name)
		if err != nil {
			return err
		}
		for _, m := range ms {
			g.Add(m)
		}
		return nil
	})
}

func (e *Engine) GroupRemove(name string, keys ...string) error {
	c := e.conf()
	return e.mutateConfig(func(nc *state.Config) error {
		g, err := nc.Group(name)
		if err != nil {
			return err
		}
		var unknown []string
		for _, k := range keys {
			removed := g.Remove(k)
			if !removed && c.Profiles.Prefix != "" {
				removed = g.Remove(c.Profiles.Prefix + k)
			}
			if !removed {
				// A profile key may resolve to a member stored by account ID.
				if m, ok := e.index().member(k, c.Profiles.Prefix); ok && m.AccountID != "" {
					removed = g.Remove(m.AccountID)
				}
			}
			if !removed {
				unknown = append(unknown, k)
			}
		}
		if len(unknown) > 0 {
			return fmt.Errorf("not in group %s: %s: %w", name, strings.Join(unknown, ", "), model.ErrNotFound)
		}
		return nil
	})
}

// GroupSet replaces membership with accountIDs, preserving existing members' roles.
func (e *Engine) GroupSet(name string, accountIDs []string) error {
	c := e.conf()
	if _, err := c.Group(name); err != nil {
		return err
	}
	ms, err := e.resolveKeys(accountIDs, c.Profiles.Prefix)
	if err != nil {
		return err
	}
	return e.mutateConfig(func(c *state.Config) error {
		g, err := c.Group(name)
		if err != nil {
			return err
		}
		old := g.Members
		g.Members = nil
		for _, m := range ms {
			for _, o := range old {
				if (m.AccountID != "" && o.AccountID == m.AccountID) || (m.Profile != "" && o.Profile == m.Profile) {
					m.Role = o.Role
					if m.Profile == "" {
						m.Profile = o.Profile
					}
				}
			}
			g.Add(m)
		}
		return nil
	})
}
