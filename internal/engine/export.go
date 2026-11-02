package engine

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/credorbit/credorbit/internal/awsini"
	"github.com/credorbit/credorbit/internal/credexport"
	"github.com/credorbit/credorbit/internal/model"
	"github.com/credorbit/credorbit/internal/profilesync"
	"github.com/credorbit/credorbit/internal/roles"
	"github.com/credorbit/credorbit/internal/state"
)

// NativeSSOHint is shown when credentials are verified but not written.
const NativeSSOHint = "credentials verified but not written (export.enabled=false). Prefer native SSO resolution: your profiles work with the AWS CLI/SDK directly via the shared token cache"

// exportPlan is the resolved work of one export.
type exportPlan struct {
	session  model.SSOSession
	targets  []credexport.Target
	original map[string]string // profile → sso_role_name in the AWS config
	all      []model.Profile   // every profile in the AWS config
	warnings []string
}

// ClassifyErr maps an error to an ExportLine.ErrClass.
func ClassifyErr(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, model.ErrUnauthorized):
		return "unauthorized"
	case errors.Is(err, model.ErrCanceled), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "canceled"
	case errors.Is(err, model.ErrTransient):
		return "transient"
	case errors.Is(err, model.ErrNoAccess):
		return "no_access"
	}
	return "other"
}

// resolveTargets turns a scope into credential targets.
func (e *Engine) resolveTargets(s model.SSOSession, scope Scope, c *state.Config, compat bool, extraNew []string) (*exportPlan, error) {
	all, err := e.profiles()
	if err != nil {
		return nil, err
	}
	ep := &exportPlan{session: s, original: map[string]string{}, all: all}
	byName := map[string]model.Profile{}
	byAccount := map[string][]model.Profile{}
	var mine []model.Profile
	for _, p := range all {
		byName[p.Name] = p
		ep.original[p.Name] = p.Role
		if inSession(p, s) {
			mine = append(mine, p)
			byAccount[p.AccountID] = append(byAccount[p.AccountID], p)
		}
	}
	for _, ps := range byAccount {
		sort.SliceStable(ps, func(i, j int) bool { return ps[i].Name < ps[j].Name })
	}
	prefix := c.Profiles.Prefix
	var picked []model.Profile
	seen := map[string]bool{}
	add := func(p model.Profile) {
		if !seen[p.Name] {
			seen[p.Name] = true
			picked = append(picked, p)
		}
	}
	lookup := func(name string) (model.Profile, bool) {
		if p, ok := byName[name]; ok {
			return p, true
		}
		if prefix != "" && !strings.HasPrefix(name, prefix) {
			if p, ok := byName[prefix+name]; ok {
				return p, true
			}
		}
		return model.Profile{}, false
	}
	var unknown []string
	switch scope.Kind {
	case ScopeAll, "":
		for _, p := range mine {
			if scope.AnyPrefix || prefix == "" || strings.HasPrefix(p.Name, prefix) {
				add(p)
			}
		}
	case ScopeGroup:
		g, err := c.Group(scope.Group)
		if err != nil {
			return nil, err
		}
		for _, m := range g.Members {
			if m.Profile != "" {
				if p, ok := byName[m.Profile]; ok && (m.AccountID == "" || p.AccountID == m.AccountID) {
					add(p)
					continue
				}
			}
			if pp := primary(byAccount[m.AccountID], prefix); m.AccountID != "" && pp != nil {
				add(*pp)
				continue
			}
			ep.warnings = append(ep.warnings, fmt.Sprintf("group %s: member %s has no configured profile (status missing); skipped", scope.Group, m.Key()))
		}
		includeNew := scope.IncludeNew || (compat && scope.Group == state.MainGroup && includeNewInMain(c, compat))
		if includeNew {
			for _, n := range extraNew {
				if p, ok := byName[n]; ok {
					add(p)
				}
			}
		}
	case ScopeSelected:
		for _, id := range scope.AccountIDs {
			if pp := primary(byAccount[id], prefix); pp != nil {
				add(*pp)
			} else {
				unknown = append(unknown, id)
			}
		}
		for _, n := range scope.Profiles {
			if p, ok := lookup(n); ok {
				add(p)
			} else {
				unknown = append(unknown, n)
			}
		}
	case ScopeSingle:
		names := scope.Profiles
		if len(names) == 0 && len(scope.AccountIDs) == 1 {
			if pp := primary(byAccount[scope.AccountIDs[0]], prefix); pp != nil {
				names = []string{pp.Name}
			} else {
				unknown = append(unknown, scope.AccountIDs[0])
			}
		}
		if len(names)+len(unknown) != 1 {
			return nil, fmt.Errorf("single scope needs exactly one profile")
		}
		if len(names) == 1 {
			if p, ok := lookup(names[0]); ok {
				add(p)
			} else {
				unknown = append(unknown, names[0])
			}
		}
	default:
		return nil, fmt.Errorf("unknown scope %q", scope.Kind)
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("not configured in %s: %s: %w", e.paths.AWSConfig, strings.Join(unknown, ", "), model.ErrNotFound)
	}
	strip := c.Export.StripPrefix || compat
	for _, p := range picked {
		if !p.IsSSO() {
			ep.warnings = append(ep.warnings, fmt.Sprintf("%s is not an SSO profile; skipped", p.Name))
			continue
		}
		ep.targets = append(ep.targets, credexport.Target{Profile: p, Section: credexport.SectionName(p.Name, prefix, strip)})
	}
	// [default] mapping (script: written in all/main modes, not single).
	if dp := c.Export.DefaultProfile; dp != "" && scope.Kind != ScopeSingle {
		if p, ok := lookup(dp); ok && p.IsSSO() {
			if !seen[p.Name] {
				ep.targets = append(ep.targets, credexport.Target{Profile: p, Section: "default"})
			}
		} else {
			ep.warnings = append(ep.warnings, fmt.Sprintf("export.default_profile %s is not a configured SSO profile; [default] left alone", dp))
		}
	}
	return ep, nil
}

func (e *Engine) Export(ctx context.Context, session string, scope Scope, o ExportOptions) (*ExportResult, error) {
	return e.export(ctx, session, scope, o, runOpts{compat: e.compat(), dryRun: o.DryRun}, nil)
}

// fetchOptions builds credexport options and the per-target role overrides.
func (e *Engine) fetchOptions(c *state.Config, ep *exportPlan, o ExportOptions, compat bool) (credexport.Options, model.RolePolicy) {
	pol := e.policy(c, o.Policy, compat)
	manual := map[string]string{}
	for id, p := range c.Accounts {
		if p.Role != "" {
			manual[id] = p.Role
		}
	}
	for id, r := range o.Manual {
		manual[id] = r
	}
	// Under the configured policy a preferred role (credorbit roles set) stands
	// in for the profile's sso_role_name.
	if pol == model.PolicyConfigured {
		for i := range ep.targets {
			if r := manual[ep.targets[i].Profile.AccountID]; r != "" {
				ep.targets[i].Profile.Role = r
			}
		}
	}
	d := e.latest(ep.session.Name)
	return credexport.Options{
		Concurrency: e.parallel(c, o.Parallel),
		Policy:      pol,
		Ranker:      ranker(c),
		Fallbacks:   fallbacksOr(o.Fallbacks, e.fallbacks(c)),
		Manual:      manual,
		Assigned:    assignedMap(d),
	}, pol
}

func (e *Engine) export(ctx context.Context, session string, scope Scope, o ExportOptions, ro runOpts, extraNew []string) (*ExportResult, error) {
	s, err := e.session(session)
	if err != nil {
		return nil, err
	}
	c := e.conf()
	if extraNew == nil {
		e.mu.Lock()
		extraNew = append([]string(nil), e.added[s.Name]...)
		e.mu.Unlock()
	}
	ep, err := e.resolveTargets(s, scope, c, ro.compat, extraNew)
	if err != nil {
		return nil, err
	}
	fo, pol := e.fetchOptions(c, ep, o, ro.compat)
	res := &ExportResult{Warnings: ep.warnings, RoleChanges: map[string]string{}}
	if len(ep.targets) == 0 {
		res.Warnings = append(res.Warnings, "no profiles selected")
		return res, nil
	}
	writeFile := c.Export.Enabled
	if o.WriteFile != nil {
		writeFile = *o.WriteFile
	} else if ro.compat {
		writeFile = true
	}
	path := e.credsFile(c)

	if ro.dryRun || o.DryRun {
		for _, t := range ep.targets {
			assigned, known := fo.Assigned[t.Profile.AccountID]
			cands := roles.Candidates(roles.Request{
				Policy: pol, Assigned: assigned, AssignedKnown: fo.Assigned != nil && known,
				Configured: t.Profile.Role, Manual: fo.Manual[t.Profile.AccountID], Fallbacks: fo.Fallbacks,
			}, fo.Ranker)
			line := ExportLine{Profile: t.Profile.Name, Section: t.Section, AccountID: t.Profile.AccountID}
			if len(cands) > 0 {
				line.Role = cands[0]
			} else {
				line.Err = "no candidate role"
				line.ErrClass = "no_access"
			}
			res.Lines = append(res.Lines, line)
		}
		where := "verify only (not written)"
		if writeFile {
			where = "write to " + path
		}
		res.Warnings = append(res.Warnings, fmt.Sprintf("dry run: would fetch %d profile(s) with policy %s and %s", len(ep.targets), pol, where))
		return res, nil
	}

	tok, err := e.token(ctx, s)
	if err != nil {
		return nil, err
	}
	fetcher := credexport.NewSSOFetcher(e.ssoClient(s.Region), func(model.Profile) (model.Token, error) { return tok, nil })
	r := credexport.Fetch(ctx, ep.targets, fetcher, fo, e.reporter())

	unauthorized := false
	for _, it := range r.Items {
		line := ExportLine{Profile: it.Target.Profile.Name, Section: it.Target.Section, AccountID: it.Target.Profile.AccountID, Role: it.Role, Expiration: it.Expiration}
		if it.Err != nil {
			line.Err = it.Err.Error()
			line.ErrClass = ClassifyErr(it.Err)
			if line.ErrClass == "unauthorized" {
				unauthorized = true
			}
			if pol == model.PolicyConfigured && strings.Contains(line.Err, "no longer assigned") {
				line.Err += fmt.Sprintf(" — run `credorbit roles set %s ROLE`", it.Target.Profile.Name)
			}
			res.Failed++
		} else {
			res.Succeeded++
			if orig := ep.original[it.Target.Profile.Name]; it.Role != orig && it.Target.Section != "default" {
				res.RoleChanges[it.Target.Profile.Name] = it.Role
			}
		}
		res.Lines = append(res.Lines, line)
	}
	if err := ctx.Err(); err != nil {
		return res, fmt.Errorf("export interrupted; %s left unchanged: %w: %w", path, model.ErrCanceled, err)
	}
	if unauthorized && res.Succeeded == 0 {
		return res, needsLogin(s.Name, model.ErrUnauthorized)
	}

	if writeFile {
		if err := e.writeCreds(path, r, ep, c, res, ro.compat); err != nil {
			return res, err
		}
	} else {
		res.Warnings = append(res.Warnings, NativeSSOHint)
	}

	if ro.deferRoles {
		return res, nil // RunFull persists role changes in its own step
	}
	// Role updates.
	update, auto := e.roleUpdatePolicy(c, ro.compat, o.UpdateRoles)
	if len(res.RoleChanges) > 0 {
		if update {
			saved, err := e.persistRoles(ctx, s, res.RoleChanges, ro, auto)
			if err != nil {
				res.Warnings = append(res.Warnings, "role updates not saved: "+err.Error())
			}
			res.RolesSaved = saved
		} else {
			res.Warnings = append(res.Warnings, fmt.Sprintf("%d profile(s) used a different role than configured; not saved (roles.update_config=false; use --update-roles)", len(res.RoleChanges)))
		}
	}
	return res, nil
}

// writeCreds writes the fetched credentials and records managed sections.
func (e *Engine) writeCreds(path string, r *credexport.Result, ep *exportPlan, c *state.Config, res *ExportResult, compat bool) error {
	strip := c.Export.StripPrefix || compat
	exists := map[string]bool{"default": true}
	for _, p := range ep.all {
		exists[credexport.SectionName(p.Name, c.Profiles.Prefix, strip)] = true
		exists[p.Name] = true
	}
	e.mu.Lock()
	managed := append([]string(nil), e.st.ManagedSections...)
	e.mu.Unlock()
	var prune []string
	for _, m := range managed {
		if !exists[m] {
			prune = append(prune, m)
		}
	}
	wo := credexport.WriteOptions{Prune: prune, BackupKeep: c.Export.BackupKeep}
	if dp := c.Export.DefaultProfile; dp != "" {
		for _, t := range ep.targets {
			if t.Section != "default" && (t.Profile.Name == dp || t.Profile.Name == c.Profiles.Prefix+dp) {
				wo.DefaultFrom = t.Section
			}
		}
	}
	wr, err := credexport.Write(path, r, wo)
	if wr != nil {
		res.Written = wr.Written
		res.Kept = wr.Kept
		res.Backup = wr.Backup
		res.Warnings = append(res.Warnings, wr.Warnings...)
	}
	if err != nil {
		return err
	}
	return e.mutateState(func(st *state.State) {
		st.AddManaged(wr.Written...)
		st.RemoveManaged(wr.Pruned...)
		for _, it := range r.Items {
			if it.OK() {
				st.LastRole[it.Target.Profile.Name] = it.Role
			}
		}
		st.LastRun = e.now().UTC()
	})
}

// roleUpdatePolicy decides whether role changes are persisted (update) and
// whether without review (auto: configured/compat behaviour). Precedence:
// explicit option > env ROLE_UPDATE > compat mode > roles.update_config.
func (e *Engine) roleUpdatePolicy(c *state.Config, compat bool, override *bool) (update, auto bool) {
	update = c.Roles.UpdateConfig || compat
	auto = update
	if v, set := e.roleUpdateEnv(); set {
		update, auto = v, v
	}
	if override != nil {
		update = *override
		auto = auto && update
	}
	return update, auto
}

// persistRoles writes role changes to the AWS config. auto skips the review.
func (e *Engine) persistRoles(ctx context.Context, s model.SSOSession, changes map[string]string, ro runOpts, auto bool) (bool, error) {
	f, err := e.readAWSConfig()
	if err != nil {
		return false, err
	}
	d := e.latest(s.Name)
	if d == nil {
		d = &model.Discovery{Session: s.Name, StartURL: s.StartURL}
	}
	p := profilesync.BuildPlan(f, d, profilesync.PlanOptions{Session: s.Name, Prefix: e.conf().Profiles.Prefix, RoleUpdates: changes})
	if p.Empty() {
		return false, nil
	}
	if !auto {
		ok, err := e.review(ctx, ro, "Save the roles that worked to "+e.paths.AWSConfig, p.Preview())
		if err != nil {
			return false, err
		}
		if !ok {
			return false, ErrDeclined
		}
	}
	if _, err := profilesync.Apply(e.paths.AWSConfig, p, e.conf().Export.BackupKeep); err != nil {
		return false, err
	}
	return true, nil
}

// sectionCreds reads one section's credentials from a credentials file.
func sectionCreds(data []byte, section string) (model.Credentials, bool) {
	f, err := awsini.Parse(data)
	if err != nil {
		return model.Credentials{}, false
	}
	sec := f.Section(section)
	if sec == nil {
		return model.Credentials{}, false
	}
	get := func(k string) string { v, _ := sec.Get(k); return v }
	c := model.Credentials{AccessKeyID: get("aws_access_key_id"), SecretAccessKey: get("aws_secret_access_key"), SessionToken: get("aws_session_token")}
	return c, c.AccessKeyID != "" && c.SecretAccessKey != ""
}

func fallbacksOr(o, def []string) []string {
	if o != nil {
		return o
	}
	return def
}

// includeNewInMain: compat mode includes new accounts in main exports; a
// one-off --compat run implies the compat defaults.
func includeNewInMain(c *state.Config, compat bool) bool {
	return c.Export.IncludeNewInMain || (compat && c.Mode != state.ModeCompat)
}
