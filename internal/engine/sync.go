package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/credorbit/credorbit/internal/model"
	"github.com/credorbit/credorbit/internal/profilesync"
	"github.com/credorbit/credorbit/internal/state"
)

// cachedRemovalMsg explains why removals are not evaluated on a cached discovery.
const cachedRemovalMsg = "cached discovery: refresh to evaluate removals"

func (e *Engine) PlanSync(ctx context.Context, session string, o SyncOptions) (string, bool, error) {
	p, _, err := e.buildSyncPlan(ctx, session, o, runOpts{compat: e.compat()})
	if err != nil {
		return "", true, err
	}
	return p.Preview(), p.Empty(), nil
}

func (e *Engine) ApplySync(ctx context.Context, session string, o SyncOptions) (*SyncResult, error) {
	ro := runOpts{compat: e.compat()}
	return e.applySync(ctx, session, o, ro, false)
}

// syncDiscovery picks the discovery a sync plan is built from.
func (e *Engine) syncDiscovery(session string) (*model.Discovery, error) {
	d := e.latest(session)
	if d == nil {
		return nil, fmt.Errorf("no discovery for session %q yet: run `credorbit discover` first", session)
	}
	if !d.Complete {
		return nil, fmt.Errorf("discovery for %q is incomplete (%s) and no cached discovery exists", session, d.Err)
	}
	return d, nil
}

func (e *Engine) buildSyncPlan(ctx context.Context, session string, o SyncOptions, ro runOpts) (*profilesync.Plan, model.SSOSession, error) {
	s, err := e.session(session)
	if err != nil {
		return nil, s, err
	}
	d, err := e.syncDiscovery(s.Name)
	if err != nil {
		return nil, s, err
	}
	f, err := e.readAWSConfig()
	if err != nil {
		return nil, s, err
	}
	c := e.conf()
	addNew := c.Sync.AddNew
	if o.AddNew != nil {
		addNew = *o.AddNew
	}
	removeStale := o.RemoveStale && c.Sync.RemoveStale != state.RemoveStaleNever
	var filter map[string]bool
	if o.AccountIDs != nil {
		filter = map[string]bool{}
		for _, id := range o.AccountIDs {
			filter[id] = true
		}
	}
	region := c.Profiles.Region
	if region == "" {
		region = s.Region
	}
	skipped := map[string]string{}
	choose := o.ChooseRole
	if choose == nil {
		choose = e.roleChooser(ctx, c, ro, skipped)
	}
	po := profilesync.PlanOptions{
		Session:       s.Name,
		Prefix:        c.Profiles.Prefix,
		Region:        region,
		Output:        c.Profiles.Output,
		AddNew:        addNew,
		RemoveStale:   removeStale && !d.Cached,
		Threshold:     c.Sync.Threshold,
		AccountFilter: filter,
		ChooseRole:    choose,
		RoleUpdates:   o.RoleUpdates,
	}
	if strings.HasPrefix(s.Name, legacyPrefix) {
		// Legacy inline profiles: never add sso_session-based profiles for a pseudo-session.
		po.AddNew = false
	}
	p := profilesync.BuildPlan(f, d, po)
	if removeStale && d.Cached {
		p.StaleBlocked = true
		p.Warnings = append(p.Warnings, cachedRemovalMsg)
	}
	if len(skipped) > 0 {
		var ws []string
		for _, w := range p.Warnings {
			drop := false
			for id := range skipped {
				if strings.Contains(w, "("+id+")") && strings.Contains(w, "no usable role") {
					drop = true
				}
			}
			if !drop {
				ws = append(ws, w)
			}
		}
		for id, why := range skipped {
			ws = append(ws, fmt.Sprintf("account %s: %s", id, why))
		}
		p.Warnings = ws
	}
	return p, s, nil
}

// roleChooser returns the role for a NEW profile. Compat mode reproduces the
// script (strongest ranked); normal mode uses the preferred role if assigned,
// the only assigned role, or asks the user.
func (e *Engine) roleChooser(ctx context.Context, c *state.Config, ro runOpts, skipped map[string]string) func(model.DiscoveredAccount) string {
	rk := ranker(c)
	return func(a model.DiscoveredAccount) string {
		if ro.compat {
			if s := rk.Sort(a.Roles); len(s) > 0 {
				return s[0]
			}
			return ""
		}
		if pref := c.Accounts[a.ID].Role; pref != "" && a.HasRole(pref) {
			return pref
		}
		if len(a.Roles) == 1 {
			return a.Roles[0]
		}
		if len(a.Roles) == 0 {
			return ""
		}
		e.mu.Lock()
		prev, ok := e.roleChoice[a.ID]
		e.mu.Unlock()
		if ok {
			return prev
		}
		if !e.hasInteraction() || ro.yes {
			skipped[a.ID] = "multiple roles; choose with credorbit roles set"
			return ""
		}
		r, err := e.interaction().SelectRole(ctx, a, "")
		if err != nil || r == "" || !a.HasRole(r) {
			skipped[a.ID] = "multiple roles; choose with credorbit roles set"
			return ""
		}
		e.mu.Lock()
		e.roleChoice[a.ID] = r
		e.mu.Unlock()
		return r
	}
}

// applySync builds, reviews and applies a plan. reviewed=true skips the
// ReviewPlan prompt (the caller already showed the preview).
func (e *Engine) applySync(ctx context.Context, session string, o SyncOptions, ro runOpts, reviewed bool) (*SyncResult, error) {
	p, s, err := e.buildSyncPlan(ctx, session, o, ro)
	if err != nil {
		return nil, err
	}
	res := &SyncResult{Warnings: append([]string(nil), p.Warnings...)}
	if p.Empty() {
		return res, nil
	}
	if !reviewed {
		ok, err := e.review(ctx, ro, "Apply changes to "+e.paths.AWSConfig, p.Preview())
		if err != nil {
			return res, err
		}
		if !ok {
			return res, ErrDeclined
		}
	}
	var removals []string
	for _, ch := range p.Changes {
		if ch.Kind == profilesync.Remove {
			removals = append(removals, ch.Profile.Name)
		}
	}
	if len(removals) > 0 {
		ok, err := e.confirmDestructive(ctx, ro, fmt.Sprintf("Remove %d stale profiles?", len(removals)), strings.Join(removals, "\n"))
		if err != nil {
			return res, err
		}
		if !ok {
			kept := p.Changes[:0]
			for _, ch := range p.Changes {
				if ch.Kind != profilesync.Remove {
					kept = append(kept, ch)
				}
			}
			p.Changes = kept
			res.Warnings = append(res.Warnings, fmt.Sprintf("%d stale profile(s) kept (removal not approved; pass --allow-remove to remove)", len(removals)))
			if p.Empty() {
				return res, nil
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return res, fmt.Errorf("%w: %w", model.ErrCanceled, err)
	}
	backup, err := profilesync.Apply(e.paths.AWSConfig, p, e.conf().Export.BackupKeep)
	res.Backup = backup
	if err != nil && !errors.Is(err, profilesync.ErrConflict) {
		return res, err
	}
	if err != nil {
		res.Warnings = append(res.Warnings, err.Error())
	}
	var added []string
	for _, ch := range p.Changes {
		res.Applied++
		if ch.Kind == profilesync.Add {
			added = append(added, ch.Profile.Name)
		}
		e.log.Info("profile change", "kind", string(ch.Kind), "profile", ch.Profile.Name, "account", ch.Profile.AccountID)
	}
	e.mu.Lock()
	e.added[s.Name] = append(e.added[s.Name], added...)
	e.mu.Unlock()
	return res, nil
}
