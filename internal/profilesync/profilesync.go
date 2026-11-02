// Package profilesync plans and applies changes to ~/.aws/config so that the
// configured SSO profiles of one session follow what SSO discovery reports:
// new accounts are added, stale profiles removed (only after a complete, live
// discovery that passes the 80% safeguard) and roles updated.
package profilesync

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/PiniPP/credorbit/internal/awsini"
	"github.com/PiniPP/credorbit/internal/fsutil"
	"github.com/PiniPP/credorbit/internal/inventory"
	"github.com/PiniPP/credorbit/internal/model"
	"github.com/PiniPP/credorbit/internal/roles"
)

// ChangeKind is the type of a planned change.
type ChangeKind string

const (
	Add     ChangeKind = "add"
	Remove  ChangeKind = "remove"
	SetRole ChangeKind = "set-role"
)

// Change is one planned modification of the config file. For Add, Profile is
// the profile to create; for Remove, the profile as planned (its AccountID is
// re-checked at apply time); for SetRole, Profile.Role is the new role.
type Change struct {
	Kind        ChangeKind
	Profile     model.Profile
	OldRole     string
	Reason      string
	AccountName string // informational (Add/Remove)
}

// PlanOptions controls BuildPlan.
type PlanOptions struct {
	Session        string
	Prefix         string // e.g. "p_"; removals are limited to profiles with this prefix
	Region, Output string // written to added profiles when non-empty
	AddNew         bool
	RemoveStale    bool
	Threshold      float64                                // stale safeguard ratio; <= 0 → 0.8
	AccountFilter  map[string]bool                        // account IDs to consider; nil = all
	ChooseRole     func(a model.DiscoveredAccount) string // role for new profiles; nil = strongest assigned (DefaultRanker)
	RoleUpdates    map[string]string                      // profile → new role (from successful exports)
}

// Plan is the set of changes BuildPlan proposes.
type Plan struct {
	Changes      []Change
	Warnings     []string
	StaleBlocked bool
}

// Empty reports whether the plan changes nothing.
func (p *Plan) Empty() bool { return p == nil || len(p.Changes) == 0 }

// ErrConflict marks changes skipped by Apply because the file changed in a way
// that contradicts the plan.
var ErrConflict = errors.New("config changed since the plan was made")

func (o PlanOptions) allowed(id string) bool { return o.AccountFilter == nil || o.AccountFilter[id] }

// BuildPlan computes the changes for f given discovery d. It never modifies f.
func BuildPlan(f *awsini.File, d *model.Discovery, o PlanOptions) *Plan {
	p := &Plan{}
	profiles := awsini.Profiles(f)
	byName := map[string]model.Profile{}
	for _, pr := range profiles {
		byName[pr.Name] = pr
	}
	taken := map[string]bool{}
	for _, s := range f.Sections() {
		taken[s.Name()] = true
		if n, ok := awsini.ProfileNameFromSection(s.Name()); ok {
			taken[n] = true
		}
	}
	removed := map[string]bool{}

	if o.RemoveStale {
		st := inventory.Stale(d, profiles, o.Session, o.Threshold)
		if st.Blocked {
			p.StaleBlocked = true
			p.Warnings = append(p.Warnings, st.Reason)
		} else {
			for _, pr := range st.Stale {
				if !o.allowed(pr.AccountID) || pr.Name == "default" || !strings.HasPrefix(pr.Name, o.Prefix) {
					continue
				}
				removed[pr.Name] = true
				p.Changes = append(p.Changes, Change{Kind: Remove, Profile: pr, Reason: "no longer assigned"})
			}
		}
	}

	if o.AddNew && d != nil {
		choose := o.ChooseRole
		if choose == nil {
			choose = func(a model.DiscoveredAccount) string {
				if s := roles.DefaultRanker().Sort(a.Roles); len(s) > 0 {
					return s[0]
				}
				return ""
			}
		}
		for _, it := range inventory.Build(d, profiles, o.Session) {
			if it.Status != model.StatusNew || !o.allowed(it.Account.ID) {
				continue
			}
			a := it.Account
			role := choose(a)
			if role == "" {
				p.Warnings = append(p.Warnings, fmt.Sprintf("account %s (%s) has no usable role; not added", displayName(a), a.ID))
				continue
			}
			name := ProfileName(o.Prefix, a.Name, a.ID, taken)
			taken[name] = true
			taken[awsini.ProfileSectionName(name)] = true
			p.Changes = append(p.Changes, Change{
				Kind:        Add,
				Profile:     model.Profile{Name: name, Session: o.Session, AccountID: a.ID, Role: role, Region: o.Region, Output: o.Output},
				Reason:      "new account",
				AccountName: a.Name,
			})
		}
	}

	for name, role := range o.RoleUpdates {
		cur, ok := byName[name]
		if !ok || role == "" || cur.Role == role || removed[name] || !o.allowed(cur.AccountID) {
			continue
		}
		np := cur
		np.Role = role
		p.Changes = append(p.Changes, Change{Kind: SetRole, Profile: np, OldRole: cur.Role, Reason: "role that worked differs from configured"})
	}

	sort.SliceStable(p.Changes, func(i, j int) bool {
		a, b := p.Changes[i], p.Changes[j]
		if a.Profile.Name != b.Profile.Name {
			return a.Profile.Name < b.Profile.Name
		}
		return a.Kind < b.Kind
	})
	return p
}

func displayName(a model.DiscoveredAccount) string {
	if a.Name == "" {
		return a.ID
	}
	return a.Name
}

// Preview renders the plan as a deterministic, human-readable diff. It never
// contains secrets.
func (p *Plan) Preview() string {
	var b strings.Builder
	if p.Empty() {
		b.WriteString("no changes\n")
	}
	if p != nil {
		for _, c := range p.Changes {
			sec := "[" + awsini.ProfileSectionName(c.Profile.Name) + "]"
			switch c.Kind {
			case Add:
				name := ""
				if c.AccountName != "" {
					name = " (" + c.AccountName + ")"
				}
				fmt.Fprintf(&b, "+ %s  account %s%s  role %s\n", sec, c.Profile.AccountID, name, c.Profile.Role)
			case Remove:
				fmt.Fprintf(&b, "- %s  account %s (%s)\n", sec, c.Profile.AccountID, c.Reason)
			case SetRole:
				fmt.Fprintf(&b, "~ %s  role %s -> %s\n", sec, c.OldRole, c.Profile.Role)
			}
		}
		for _, w := range p.Warnings {
			fmt.Fprintf(&b, "! %s\n", w)
		}
	}
	return b.String()
}

// DefaultBackupKeep is used when Apply is called with backupKeep <= 0.
const DefaultBackupKeep = 5

// Apply applies p to the CURRENT content of configPath under a lock: the file
// is re-read and re-parsed (it may have changed since planning), changes are
// applied in memory, the result is validated by re-parsing, the old file is
// backed up and the new one written atomically with the existing file mode
// (0600 if the file is new).
//
// Changes that contradict the current file (an Add whose name now exists for a
// different account, a Remove/SetRole whose profile now points elsewhere) are
// skipped; the remaining changes are still written and the returned error
// wraps ErrConflict. A Remove whose profile is already gone is a no-op. When
// nothing changes, nothing is written and no backup is made.
func Apply(configPath string, p *Plan, backupKeep int) (backup string, err error) {
	if p.Empty() {
		return "", nil
	}
	if backupKeep <= 0 {
		backupKeep = DefaultBackupKeep
	}
	lock, err := fsutil.AcquireLock(configPath, 10*time.Second)
	if err != nil {
		return "", err
	}
	defer lock.Release()

	orig, err := fsutil.ReadFileOptional(configPath)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", configPath, err)
	}
	f, err := awsini.Parse(orig)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", configPath, err)
	}

	var conflicts []error
	conflict := func(c Change, format string, args ...any) {
		conflicts = append(conflicts, fmt.Errorf("%s %s: %s: %w", c.Kind, c.Profile.Name, fmt.Sprintf(format, args...), ErrConflict))
	}
	var applied []Change
	for _, c := range p.Changes {
		secName := awsini.ProfileSectionName(c.Profile.Name)
		s := f.Section(secName)
		cur := ""
		if s != nil {
			cur, _ = s.Get("sso_account_id")
		}
		switch c.Kind {
		case Add:
			if s != nil {
				if cur == c.Profile.AccountID {
					continue // already added (e.g. by a concurrent run)
				}
				conflict(c, "profile already exists for account %q", cur)
				continue
			}
			s = f.AddSection(secName)
			s.Set("sso_session", c.Profile.Session)
			s.Set("sso_account_id", c.Profile.AccountID)
			s.Set("sso_role_name", c.Profile.Role)
			if c.Profile.Region != "" {
				s.Set("region", c.Profile.Region)
			}
			if c.Profile.Output != "" {
				s.Set("output", c.Profile.Output)
			}
		case Remove:
			if s == nil {
				continue // already gone
			}
			if cur != c.Profile.AccountID {
				conflict(c, "profile now points to account %q, not %q", cur, c.Profile.AccountID)
				continue
			}
			for f.Section(secName) != nil { // duplicates too
				f.RemoveSection(secName)
			}
		case SetRole:
			if s == nil {
				conflict(c, "profile no longer exists")
				continue
			}
			if cur != c.Profile.AccountID {
				conflict(c, "profile now points to account %q, not %q", cur, c.Profile.AccountID)
				continue
			}
			if r, _ := s.Get("sso_role_name"); r == c.Profile.Role {
				continue
			}
			s.Set("sso_role_name", c.Profile.Role)
		default:
			conflicts = append(conflicts, fmt.Errorf("unknown change kind %q", c.Kind))
			continue
		}
		applied = append(applied, c)
	}
	cerr := errors.Join(conflicts...)
	out := f.Bytes()
	if len(applied) == 0 || string(out) == string(orig) {
		return "", cerr
	}
	if err := validate(out, applied); err != nil {
		return "", fmt.Errorf("refusing to write %s: %w", configPath, err)
	}

	perm := os.FileMode(0o600)
	if st, err := os.Stat(configPath); err == nil {
		perm = st.Mode().Perm()
	}
	if backup, err = fsutil.Backup(configPath, backupKeep); err != nil {
		return "", err
	}
	if err := fsutil.AtomicWrite(configPath, out, perm); err != nil {
		return backup, err
	}
	return backup, cerr
}

func validate(out []byte, applied []Change) error {
	g, err := awsini.Parse(out)
	if err != nil {
		return err
	}
	for _, c := range applied {
		s := g.Section(awsini.ProfileSectionName(c.Profile.Name))
		switch c.Kind {
		case Remove:
			if s != nil {
				return fmt.Errorf("validation: %s still present", c.Profile.Name)
			}
		case Add, SetRole:
			if s == nil {
				return fmt.Errorf("validation: %s missing", c.Profile.Name)
			}
			id, _ := s.Get("sso_account_id")
			role, _ := s.Get("sso_role_name")
			if id != c.Profile.AccountID || role != c.Profile.Role {
				return fmt.Errorf("validation: %s has account %q role %q", c.Profile.Name, id, role)
			}
		}
	}
	return nil
}

// ProfileName returns prefix + accountName with spaces, '_' and '+' replaced
// by '-' (as the legacy script did; other whitespace, control characters and
// brackets are replaced too so the header stays valid). An empty account name
// yields prefix+accountID. If the name is taken, "-"+accountID is appended
// (and "-2", "-3", ... if that is taken as well).
func ProfileName(prefix, accountName, accountID string, taken map[string]bool) string {
	clean := strings.Map(func(r rune) rune {
		if r == '_' || r == '+' || r == '[' || r == ']' || unicode.IsSpace(r) || unicode.IsControl(r) {
			return '-'
		}
		return r
	}, accountName)
	if clean == "" {
		clean = accountID
	}
	name := prefix + clean
	if !taken[name] {
		return name
	}
	name += "-" + accountID
	if !taken[name] {
		return name
	}
	for i := 2; ; i++ {
		if n := fmt.Sprintf("%s-%d", name, i); !taken[n] {
			return n
		}
	}
}
