package demo

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/PiniPP/credorbit/internal/engine"
	"github.com/PiniPP/credorbit/internal/model"
)

// --- sync & export ---

func (s *Service) plan(o engine.SyncOptions) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	addNew := o.AddNew == nil || *o.AddNew
	filter := map[string]bool{}
	for _, id := range o.AccountIDs {
		filter[id] = true
	}
	var b strings.Builder
	for _, a := range s.accounts {
		if len(filter) > 0 && !filter[a.ID] {
			continue
		}
		switch {
		case a.status == model.StatusNew && addNew:
			role := ""
			if len(a.Roles) > 0 {
				role = a.Roles[len(a.Roles)-1]
			}
			fmt.Fprintf(&b, "+ [profile p_%s]  sso_account_id=%s  sso_role_name=%s\n", strings.TrimPrefix(a.Name, "acme-"), a.ID, role)
		case a.status == model.StatusMissing && o.RemoveStale:
			fmt.Fprintf(&b, "- [profile %s]  account %s no longer visible in SSO\n", a.profile, a.ID)
		}
	}
	for p, r := range o.RoleUpdates {
		if a := s.find(p); a != nil {
			fmt.Fprintf(&b, "~ [profile %s]  sso_role_name: %s -> %s\n", a.profile, a.role, r)
		}
	}
	return b.String()
}

func (s *Service) PlanSync(ctx context.Context, session string, o engine.SyncOptions) (string, bool, error) {
	if err := s.sleep(ctx, 300*time.Millisecond); err != nil {
		return "", true, err
	}
	p := s.plan(o)
	return p, p == "", nil
}

func (s *Service) ApplySync(ctx context.Context, session string, o engine.SyncOptions) (*engine.SyncResult, error) {
	p := s.plan(o)
	if p == "" {
		return &engine.SyncResult{}, nil
	}
	ok, err := s.ask().ReviewPlan(ctx, "Apply changes to ~/.aws/config", p)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("sync: %w", model.ErrCanceled)
	}
	if err := s.sleep(ctx, 400*time.Millisecond); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := strings.Count(p, "\n")
	addNew := o.AddNew == nil || *o.AddNew
	kept := s.accounts[:0]
	for _, a := range s.accounts {
		if a.status == model.StatusNew && addNew && (len(o.AccountIDs) == 0 || contains(o.AccountIDs, a.ID)) {
			a.status = model.StatusAvailable
			a.profile = "p_" + strings.TrimPrefix(a.Name, "acme-")
			if len(a.Roles) > 0 {
				a.role = a.Roles[len(a.Roles)-1]
			}
		}
		if a.status == model.StatusMissing && o.RemoveStale {
			continue
		}
		kept = append(kept, a)
	}
	s.accounts = kept
	return &engine.SyncResult{Applied: n, Backup: "~/.aws/config.credorbit-" + s.now().Format("20060102-150405") + ".bak (demo)"}, nil
}

func (s *Service) targets(scope engine.Scope) []*acct {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*acct
	for _, a := range s.accounts {
		if a.profile == "" {
			continue
		}
		switch scope.Kind {
		case engine.ScopeGroup:
			g := s.groups[scope.Group]
			if g == nil || !contains(g.ids, a.ID) {
				if !scope.IncludeNew || a.status != model.StatusNew {
					continue
				}
			}
		case engine.ScopeSelected, engine.ScopeSingle:
			if !contains(scope.AccountIDs, a.ID) && !contains(scope.Profiles, a.profile) {
				continue
			}
		}
		out = append(out, a)
		if scope.Kind == engine.ScopeSingle {
			break
		}
	}
	return out
}

func (s *Service) Export(ctx context.Context, session string, scope engine.Scope, o engine.ExportOptions) (*engine.ExportResult, error) {
	if !s.LoginStatus(ctx, session).LoggedIn {
		return nil, fmt.Errorf("export: %w", model.ErrUnauthorized)
	}
	tg := s.targets(scope)
	if len(tg) == 0 {
		return nil, fmt.Errorf("export: no profiles in scope: %w", model.ErrNotFound)
	}
	s.emit(model.Event{Kind: model.EventStepStart, Step: "export", Message: fmt.Sprintf("Fetching credentials for %d profiles", len(tg)), Total: len(tg)})
	res := &engine.ExportResult{RoleChanges: map[string]string{}}
	write := o.WriteFile == nil || *o.WriteFile
	par := o.Parallel
	if par <= 0 {
		par = 8
	}
	for i, a := range tg {
		ln := engine.ExportLine{Profile: a.profile, Section: strings.TrimPrefix(a.profile, "p_"), AccountID: a.ID}
		if ctx.Err() != nil {
			ln.Err, ln.ErrClass = "canceled", "canceled"
		} else if err := s.sleep(ctx, time.Duration(320/par+40)*time.Millisecond); err != nil {
			ln.Err, ln.ErrClass = "canceled", "canceled"
		} else {
			s.mu.Lock()
			role := a.role
			if a.pref != "" {
				role = a.pref
			}
			s.mu.Unlock()
			if m, ok := o.Manual[a.ID]; ok {
				role = m
			}
			if o.Policy == model.PolicyStrongest && len(a.Roles) > 0 {
				role = strongest(a.Roles)
			}
			switch {
			case a.status == model.StatusMissing:
				ln.Err, ln.ErrClass = "ForbiddenException: No access", "no_access"
			case a.Name == "acme-ledger-dev":
				ln.Err, ln.ErrClass = "TooManyRequestsException: rate exceeded (retried 3x)", "transient"
			default:
				ln.Role = role
				ln.Expiration = s.now().Add(12 * time.Hour)
				if role != a.role {
					res.RoleChanges[a.profile] = role
				}
			}
		}
		if ln.Err == "" {
			res.Succeeded++
			if write && !o.DryRun {
				res.Written = append(res.Written, ln.Section)
				s.mu.Lock()
				a.exp = ln.Expiration
				s.mu.Unlock()
			}
			s.emit(model.Event{Kind: model.EventItemOK, Step: "export", Subject: ln.Profile, Message: ln.Role})
		} else {
			res.Failed++
			if !a.exp.IsZero() {
				res.Kept = append(res.Kept, ln.Section)
			}
			s.emit(model.Event{Kind: model.EventItemFailed, Step: "export", Subject: ln.Profile, Message: ln.Err, Err: errors.New(ln.ErrClass)})
		}
		res.Lines = append(res.Lines, ln)
		s.emit(model.Event{Kind: model.EventProgress, Step: "export", Done: i + 1, Total: len(tg)})
	}
	if write && !o.DryRun && res.Succeeded > 0 {
		res.Backup = "~/.aws/credentials.credorbit-" + s.now().Format("20060102-150405") + ".bak (demo)"
	}
	if len(res.RoleChanges) > 0 && o.UpdateRoles != nil && *o.UpdateRoles {
		res.RolesSaved = true
	}
	if res.Failed > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%d profiles failed; their previous credentials were kept", res.Failed))
	}
	s.emit(model.Event{Kind: model.EventStepDone, Step: "export", Message: fmt.Sprintf("%d ok, %d failed", res.Succeeded, res.Failed)})
	if ctx.Err() != nil {
		return res, fmt.Errorf("export: %w", model.ErrCanceled)
	}
	return res, nil
}

func strongest(roles []string) string {
	rank := map[string]int{"AdministratorAccess": 0, "PowerUserAccess": 1, "DevOps": 2, "ReadOnlyAccess": 3}
	best := roles[0]
	for _, r := range roles {
		if rank[r] < rank[best] {
			best = r
		}
	}
	return best
}

// FullSteps are the step identifiers RunFull reports, in order.
var FullSteps = []string{
	"preflight", "config", "session", "network", "login", "discover", "inventory",
	"stale-check", "plan-sync", "review-sync", "apply-sync", "groups", "new-accounts",
	"roles", "export", "write-credentials", "role-updates", "default-profile",
	"rotate", "verify-bedrock", "report",
}

func (s *Service) RunFull(ctx context.Context, o engine.FullOptions) (*engine.Report, error) {
	start := s.now()
	rep := &engine.Report{}
	if o.Session == "" {
		o.Session = s.DefaultSession()
	}
	step := func(id string, f func() (string, bool, error)) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.emit(model.Event{Kind: model.EventStepStart, Step: id, Message: id})
		sum, skipped, err := "", false, error(nil)
		if f != nil {
			sum, skipped, err = f()
		}
		if err == nil {
			err = s.sleep(ctx, 120*time.Millisecond)
		}
		r := engine.StepResult{Step: id, OK: err == nil, Skipped: skipped, Summary: sum}
		if err != nil {
			r.Summary = err.Error()
		}
		rep.Steps = append(rep.Steps, r)
		s.emit(model.Event{Kind: model.EventStepDone, Step: id, Message: r.Summary, Err: err})
		return err
	}
	exportOn := o.Export == nil || *o.Export
	rotateOn := o.Rotate != nil && *o.Rotate
	var exp *engine.ExportResult
	var syncRes *engine.SyncResult
	steps := []struct {
		id string
		f  func() (string, bool, error)
	}{
		{"preflight", func() (string, bool, error) { return "demo environment ready", false, nil }},
		{"config", func() (string, bool, error) { return "config loaded (" + s.Mode() + " mode)", false, nil }},
		{"session", func() (string, bool, error) { return "using session " + o.Session, false, nil }},
		{"network", func() (string, bool, error) { return "TLS ok · no interception detected", false, nil }},
		{"login", func() (string, bool, error) {
			st, err := s.Login(ctx, o.Session, o.LoginFlow, o.ForceLogin)
			if err != nil {
				return "", false, err
			}
			return "token valid until " + st.ExpiresAt.Format("15:04"), false, nil
		}},
		{"discover", func() (string, bool, error) {
			d, err := s.Discover(ctx, o.Session)
			if err != nil {
				return "", false, err
			}
			return fmt.Sprintf("%d accounts discovered", len(d.Accounts)), false, nil
		}},
		{"inventory", func() (string, bool, error) {
			n := len(s.NewAccounts(o.Session))
			return fmt.Sprintf("%d new · 2 missing", n), false, nil
		}},
		{"stale-check", func() (string, bool, error) { return "2 stale profiles (removal needs review)", false, nil }},
		{"plan-sync", func() (string, bool, error) {
			if o.NoSync {
				return "sync disabled", true, nil
			}
			return "plan built", false, nil
		}},
		{"review-sync", nil},
		{"apply-sync", func() (string, bool, error) {
			if o.NoSync || o.DryRun {
				return "dry run: no changes written", true, nil
			}
			r, err := s.ApplySync(ctx, o.Session, engine.SyncOptions{RemoveStale: o.AllowRemove})
			if err != nil {
				if errors.Is(err, model.ErrCanceled) {
					return "changes declined", true, nil
				}
				return "", false, err
			}
			syncRes = r
			if r.Backup != "" {
				rep.Backups = append(rep.Backups, r.Backup)
			}
			return fmt.Sprintf("%d changes applied", r.Applied), false, nil
		}},
		{"groups", func() (string, bool, error) {
			return "main: " + strconv.Itoa(len(s.groups["main"].ids)) + " accounts", false, nil
		}},
		{"new-accounts", func() (string, bool, error) {
			if !o.EditMain {
				return "kept ungrouped", true, nil
			}
			rows := s.Accounts(o.Session)
			g, _ := s.Group("main")
			var pre []string
			for _, m := range g.Members {
				pre = append(pre, m.AccountID)
			}
			ids, err := s.ask().SelectAccounts(ctx, "Select Main Accounts", rows, pre)
			if err != nil {
				return "", false, err
			}
			return fmt.Sprintf("main: %d accounts", len(ids)), false, s.GroupSet("main", ids)
		}},
		{"roles", func() (string, bool, error) { return "policy " + string(orDefault(o.Policy)), false, nil }},
		{"export", func() (string, bool, error) {
			if !exportOn {
				return "export disabled", true, nil
			}
			r, err := s.Export(ctx, o.Session, o.Scope, engine.ExportOptions{Policy: o.Policy, Parallel: o.Parallel, DryRun: o.DryRun, UpdateRoles: o.UpdateRoles})
			exp = r
			if r != nil {
				rep.Exported, rep.Failed = r.Succeeded, r.Failed
				rep.Warnings = append(rep.Warnings, r.Warnings...)
			}
			if err != nil {
				return "", false, err
			}
			return fmt.Sprintf("%d ok · %d failed", r.Succeeded, r.Failed), false, nil
		}},
		{"write-credentials", func() (string, bool, error) {
			if exp == nil || o.DryRun {
				return "nothing written", true, nil
			}
			if exp.Backup != "" {
				rep.Backups = append(rep.Backups, exp.Backup)
			}
			return fmt.Sprintf("%d sections written · %d kept", len(exp.Written), len(exp.Kept)), false, nil
		}},
		{"role-updates", func() (string, bool, error) {
			if exp == nil || len(exp.RoleChanges) == 0 {
				return "no role changes", true, nil
			}
			return fmt.Sprintf("%d role changes", len(exp.RoleChanges)), false, nil
		}},
		{"default-profile", func() (string, bool, error) {
			return "[default] ← " + s.setting("export.default_profile"), false, nil
		}},
		{"rotate", func() (string, bool, error) {
			if !rotateOn {
				return "rotation disabled", true, nil
			}
			from, to, err := s.Rotate(ctx, engine.RotateOptions{DryRun: o.DryRun})
			if err != nil {
				return "", false, err
			}
			return from + " → " + to, false, nil
		}},
		{"verify-bedrock", func() (string, bool, error) {
			if !rotateOn {
				return "skipped", true, nil
			}
			return "bedrock:InvokeModel ok", false, nil
		}},
		{"report", nil},
	}
	_ = syncRes
	for _, st := range steps {
		if err := step(st.id, st.f); err != nil {
			rep.Duration = s.now().Sub(start)
			if ctx.Err() != nil {
				return rep, fmt.Errorf("setup: %w", model.ErrCanceled)
			}
			rep.Warnings = append(rep.Warnings, st.id+": "+err.Error())
			if st.id == "login" || st.id == "discover" {
				return rep, err
			}
		}
	}
	s.mu.Lock()
	s.lastRun = s.now()
	s.mu.Unlock()
	rep.Duration = s.now().Sub(start)
	return rep, nil
}

func orDefault(p model.RolePolicy) model.RolePolicy {
	if p == "" {
		return model.PolicyConfigured
	}
	return p
}
