package state

import (
	"fmt"
	"sort"

	"github.com/PiniPP/credorbit/internal/model"
)

// Group is a named set of accounts (e.g. "main" for fast exports).
type Group struct {
	Name        string   `yaml:"-"` // the map key in Config.Groups
	Description string   `yaml:"description,omitempty"`
	Members     []Member `yaml:"members"`
}

// Member identifies an account in a group. The stable key is AccountID; Profile
// is used when the account ID is unknown (e.g. non-SSO profiles).
type Member struct {
	AccountID string `yaml:"account_id,omitempty"`
	Profile   string `yaml:"profile,omitempty"`
	Role      string `yaml:"role,omitempty"`
}

// Key returns the member's stable key: AccountID if known, else Profile.
func (m Member) Key() string {
	if m.AccountID != "" {
		return m.AccountID
	}
	return m.Profile
}

// Matches reports whether key equals the member's AccountID or Profile.
func (m Member) Matches(key string) bool {
	return key != "" && (m.AccountID == key || m.Profile == key)
}

func (m Member) same(o Member) bool {
	if m.AccountID != "" && o.AccountID != "" {
		return m.AccountID == o.AccountID
	}
	return m.Profile != "" && m.Profile == o.Profile
}

// Add inserts m, or merges new information (account ID, profile, role) into an
// existing member with the same key. It reports whether the group changed.
func (g *Group) Add(m Member) bool {
	if m.AccountID == "" && m.Profile == "" {
		return false
	}
	for i := range g.Members {
		cur := &g.Members[i]
		if !cur.same(m) {
			continue
		}
		changed := false
		if m.AccountID != "" && cur.AccountID != m.AccountID {
			cur.AccountID, changed = m.AccountID, true
		}
		if m.Profile != "" && cur.Profile != m.Profile {
			cur.Profile, changed = m.Profile, true
		}
		if m.Role != "" && cur.Role != m.Role {
			cur.Role, changed = m.Role, true
		}
		return changed
	}
	g.Members = append(g.Members, m)
	return true
}

// Remove deletes every member whose AccountID or Profile equals key.
func (g *Group) Remove(key string) bool {
	kept := g.Members[:0]
	removed := false
	for _, m := range g.Members {
		if m.Matches(key) {
			removed = true
			continue
		}
		kept = append(kept, m)
	}
	g.Members = kept
	return removed
}

// Has reports whether any member's AccountID or Profile equals key.
func (g *Group) Has(key string) bool {
	for _, m := range g.Members {
		if m.Matches(key) {
			return true
		}
	}
	return false
}

// Keys returns the members' stable keys in member order.
func (g *Group) Keys() []string {
	out := make([]string, 0, len(g.Members))
	for _, m := range g.Members {
		out = append(out, m.Key())
	}
	return out
}

// GroupNames returns all group names sorted, with "main" first.
func (c *Config) GroupNames() []string {
	names := make([]string, 0, len(c.Groups))
	for n, g := range c.Groups {
		if g != nil && n != MainGroup {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	if c.Groups[MainGroup] != nil {
		names = append([]string{MainGroup}, names...)
	}
	return names
}

// Group returns the named group or an error wrapping model.ErrNotFound.
func (c *Config) Group(name string) (*Group, error) {
	if g := c.Groups[name]; g != nil {
		return g, nil
	}
	return nil, fmt.Errorf("group %q: %w", name, model.ErrNotFound)
}

// EnsureGroup returns the named group, creating it if needed. desc is applied
// when the group is new or has no description yet. The name is checked by
// Validate (and by Save).
func (c *Config) EnsureGroup(name, desc string) *Group {
	if c.Groups == nil {
		c.Groups = map[string]*Group{}
	}
	g := c.Groups[name]
	if g == nil {
		g = &Group{Name: name}
		c.Groups[name] = g
	}
	if g.Description == "" {
		g.Description = desc
	}
	return g
}

// DeleteGroup removes a user-defined group. "main" can only be emptied.
func (c *Config) DeleteGroup(name string) error {
	if name == MainGroup {
		return fmt.Errorf("group %q cannot be deleted (remove its members instead)", MainGroup)
	}
	if c.Groups[name] == nil {
		return fmt.Errorf("group %q: %w", name, model.ErrNotFound)
	}
	delete(c.Groups, name)
	return nil
}

// RenameGroup renames a user-defined group; "main" cannot be renamed and the
// new name must be valid and unused.
func (c *Config) RenameGroup(oldName, newName string) error {
	if oldName == MainGroup || newName == MainGroup {
		return fmt.Errorf("group %q cannot be renamed", MainGroup)
	}
	g := c.Groups[oldName]
	if g == nil {
		return fmt.Errorf("group %q: %w", oldName, model.ErrNotFound)
	}
	if oldName == newName {
		return nil
	}
	if !ValidGroupName(newName) {
		return fmt.Errorf("invalid group name %q (lowercase letters, digits, '.', '_', '-'; max 64)", newName)
	}
	if c.Groups[newName] != nil {
		return fmt.Errorf("group %q already exists", newName)
	}
	delete(c.Groups, oldName)
	g.Name = newName
	c.Groups[newName] = g
	return nil
}

// GroupsOf returns the names of groups (GroupNames order) containing a member
// matching accountID or profile. Empty arguments are ignored.
func (c *Config) GroupsOf(accountID, profile string) []string {
	var out []string
	for _, name := range c.GroupNames() {
		for _, m := range c.Groups[name].Members {
			if (accountID != "" && m.AccountID == accountID) || (profile != "" && m.Profile == profile) {
				out = append(out, name)
				break
			}
		}
	}
	return out
}
