package engine

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/credorbit/credorbit/internal/awsini"
	"github.com/credorbit/credorbit/internal/credexport"
	"github.com/credorbit/credorbit/internal/fsutil"
	"github.com/credorbit/credorbit/internal/inventory"
	"github.com/credorbit/credorbit/internal/model"
	"github.com/credorbit/credorbit/internal/state"
)

// WorkflowSubject marks RunFull step events (EventStepStart/EventStepDone) so
// front-ends can tell them from the step events of inner operations.
const WorkflowSubject = "workflow"

// Step identifiers of RunFull, in order.
var FullSteps = []string{"environment", "session", "network", "login", "discover", "roles", "compare", "safeguards",
	"main-group", "scope", "role-policy", "preview", "approve", "backup", "sync", "credentials", "export",
	"role-updates", "rotate", "validate", "report"}

type fullRun struct {
	e      *Engine
	ctx    context.Context
	o      FullOptions
	ro     runOpts
	rep    *Report
	start  time.Time
	s      model.SSOSession
	d      *model.Discovery
	cached bool
	scope  Scope
	pol    model.RolePolicy
	newIDs []string
	newIn  []string // new account IDs to include in this run's export (compat main mode)
	syncO  SyncOptions
	expO   ExportOptions
	exp    *ExportResult
}

func (r *fullRun) step(id string, fn func() (StepResult, error)) error {
	if err := r.ctx.Err(); err != nil {
		r.rep.Steps = append(r.rep.Steps, StepResult{Step: id, Summary: "canceled"})
		return fmt.Errorf("%w: %w", model.ErrCanceled, err)
	}
	r.e.report(model.Event{Kind: model.EventStepStart, Step: id, Subject: WorkflowSubject})
	sr, err := fn()
	sr.Step = id
	if err != nil && sr.Summary == "" {
		sr.Summary = err.Error()
	}
	if err == nil && !sr.Skipped {
		sr.OK = true
	}
	r.rep.Steps = append(r.rep.Steps, sr)
	ev := model.Event{Kind: model.EventStepDone, Step: id, Subject: WorkflowSubject, Message: sr.Summary, Err: err}
	if sr.Skipped {
		ev.Kind = model.EventStepDone
		ev.Message = "skipped: " + sr.Summary
	}
	r.e.report(ev)
	for _, d := range sr.Details {
		r.e.log.Debug("step detail", "step", id, "detail", d)
	}
	return err
}

func skipped(summary string) (StepResult, error) {
	return StepResult{Skipped: true, Summary: summary}, nil
}

// RunFull runs the complete setup workflow.
func (e *Engine) RunFull(ctx context.Context, o FullOptions) (*Report, error) {
	r := &fullRun{e: e, ctx: ctx, o: o, rep: &Report{}, start: e.now()}
	r.ro = runOpts{compat: o.Compat || e.compat(), yes: o.Yes, allowRemove: o.AllowRemove, dryRun: o.DryRun}
	err := r.run()
	r.rep.Duration = e.now().Sub(r.start)
	if err != nil {
		e.log.Warn("full run stopped", "err", err)
	}
	return r.rep, err
}

func (r *fullRun) run() error {
	e, o, c := r.e, r.o, r.e.conf()
	steps := []struct {
		id string
		fn func() (StepResult, error)
	}{
		{"environment", func() (StepResult, error) {
			mode := "normal"
			if r.ro.compat {
				mode = "compat"
			}
			return StepResult{Summary: fmt.Sprintf("%s/%s, mode %s", runtime.GOOS, runtime.GOARCH, mode),
				Details: []string{"aws config: " + e.paths.AWSConfig, "credentials: " + e.credsFile(c), "settings: " + e.paths.AppConfigFile}}, nil
		}},
		{"session", func() (StepResult, error) {
			s, err := e.session(o.Session)
			if err != nil {
				if errors.Is(err, model.ErrNotFound) && o.Session == "" {
					return StepResult{}, fmt.Errorf("no SSO session configured. Create one with: credorbit login --name NAME --start-url https://<id>.awsapps.com/start --region <region> (or credorbit import to read the legacy script)")
				}
				return StepResult{}, err
			}
			r.s = s
			return StepResult{Summary: s.Name + " (" + s.StartURL + ", " + s.Region + ")"}, nil
		}},
		{"network", func() (StepResult, error) {
			if e.netErr != nil {
				return StepResult{}, fmt.Errorf("network configuration: %w", e.netErr)
			}
			url := c.Network.ProbeURL
			if url == "" {
				url = portalURL(r.s.Region)
			}
			pc, ps := e.probe(r.ctx, url)
			sr := StepResult{Summary: "portal reachable"}
			switch {
			case !pc.OK:
				sr.Summary = "probe failed (continuing): " + pc.ErrClass
				sr.Details = append(sr.Details, pc.Err, pc.Advice)
				r.rep.Warnings = append(r.rep.Warnings, "network probe failed: "+pc.ErrClass+" "+pc.Advice)
			case pc.Intercepted || (!ps.OK && ps.ErrClass == "unknown_authority"):
				sr.Summary = "TLS interception detected; trusted via configured CA"
			}
			return sr, nil
		}},
		{"login", func() (StepResult, error) {
			force := o.ForceLogin || e.forceLoginEnv()
			if !force && !e.hasInteraction() {
				if _, err := e.token(r.ctx, r.s); err != nil {
					return StepResult{}, err
				}
			}
			ls, err := e.Login(r.ctx, r.s.Name, o.LoginFlow, force)
			if err != nil {
				return StepResult{}, err
			}
			return StepResult{Summary: "token valid until " + ls.ExpiresAt.Local().Format("15:04")}, nil
		}},
		{"discover", func() (StepResult, error) {
			d, err := e.discover(r.ctx, r.s.Name, !o.DryRun)
			if err != nil {
				if r.ctx.Err() != nil {
					return StepResult{}, err
				}
				if cd := e.CachedDiscovery(r.s.Name); cd != nil {
					r.d, r.cached = cd, true
					w := "live discovery failed; continuing read-only with the cached discovery from " + cd.At.Local().Format("2006-01-02 15:04") + " (no removals)"
					r.rep.Warnings = append(r.rep.Warnings, w)
					return StepResult{Summary: w, Details: []string{err.Error()}}, nil
				}
				return StepResult{}, err
			}
			r.d = d
			return StepResult{Summary: fmt.Sprintf("%d accounts", len(d.Accounts))}, nil
		}},
		{"roles", func() (StepResult, error) {
			n, failed := 0, 0
			for _, a := range r.d.Accounts {
				n += len(a.Roles)
				if a.RolesErr != "" && a.RolesErr != model.ErrNoAccess.Error() {
					failed++
				}
			}
			sr := StepResult{Summary: fmt.Sprintf("%d role assignments", n)}
			if failed > 0 {
				sr.Summary += fmt.Sprintf(", roles unknown for %d account(s)", failed)
			}
			return sr, nil
		}},
		{"compare", func() (StepResult, error) {
			rows := e.accountRows(r.s, r.d)
			cnt := map[model.AccountStatus]int{}
			for _, row := range rows {
				cnt[row.Status]++
				if row.Status == model.StatusNew {
					r.newIDs = append(r.newIDs, row.Account.ID)
				}
			}
			return StepResult{Summary: fmt.Sprintf("%d available, %d new, %d missing, %d unknown",
				cnt[model.StatusAvailable], cnt[model.StatusNew], cnt[model.StatusMissing], cnt[model.StatusUnknown])}, nil
		}},
		{"safeguards", func() (StepResult, error) {
			ps, _ := e.profiles()
			st := inventory.Stale(r.d, ps, r.s.Name, c.Sync.Threshold)
			switch {
			case st.Blocked:
				return StepResult{Summary: "removals blocked: " + st.Reason}, nil
			case len(st.Stale) > 0:
				var names []string
				for _, p := range st.Stale {
					names = append(names, p.Name)
				}
				return StepResult{Summary: fmt.Sprintf("%d stale profile(s)", len(st.Stale)), Details: names}, nil
			}
			return StepResult{Summary: "no stale profiles"}, nil
		}},
		{"main-group", r.mainGroup},
		{"scope", func() (StepResult, error) {
			sc := o.Scope
			if sc.Kind == "" {
				g, _ := e.conf().Group(state.MainGroup)
				if g != nil && len(g.Members) > 0 {
					sc = Scope{Kind: ScopeGroup, Group: state.MainGroup}
				} else {
					sc = Scope{Kind: ScopeAll}
				}
			}
			if sc.Kind == ScopeGroup && sc.Group == state.MainGroup && r.ro.compat && includeNewInMain(c, true) {
				sc.IncludeNew = true
			}
			r.scope = sc
			desc := string(sc.Kind)
			if sc.Kind == ScopeGroup {
				desc += " " + sc.Group
			}
			if sc.IncludeNew {
				desc += " + new accounts"
			}
			return StepResult{Summary: desc}, nil
		}},
		{"role-policy", func() (StepResult, error) {
			r.pol = e.policy(c, o.Policy, r.ro.compat)
			return StepResult{Summary: string(r.pol)}, nil
		}},
		{"preview", r.preview},
		{"approve", r.approve},
	}
	for _, s := range steps {
		if s.id == "approve" && o.DryRun {
			_ = r.step(s.id, func() (StepResult, error) { return skipped("dry run: nothing written") })
			r.finishReport()
			return nil
		}
		if err := r.step(s.id, s.fn); err != nil {
			r.finishReport()
			return err
		}
	}
	rest := []struct {
		id string
		fn func() (StepResult, error)
	}{
		{"backup", r.backup},
		{"sync", r.sync},
		{"credentials", r.credentials},
		{"export", r.exportStep},
		{"role-updates", r.roleUpdates},
		{"rotate", r.rotate},
		{"validate", r.validate},
	}
	for _, s := range rest {
		if err := r.step(s.id, s.fn); err != nil {
			r.finishReport()
			return err
		}
	}
	r.finishReport()
	return nil
}

func (r *fullRun) finishReport() {
	_ = r.step("report", func() (StepResult, error) {
		ok, failed := 0, 0
		for _, s := range r.rep.Steps {
			if s.OK {
				ok++
			} else if !s.Skipped {
				failed++
			}
		}
		return StepResult{Summary: fmt.Sprintf("%d steps ok, %d failed; %d exported, %d failed", ok, failed, r.rep.Exported, r.rep.Failed)}, nil
	})
	if !r.o.DryRun && r.s.Name != "" {
		_ = r.e.mutateState(func(s *state.State) { s.LastRun = r.e.now().UTC() })
	}
}

func (r *fullRun) mainGroup() (StepResult, error) {
	e, o := r.e, r.o
	c := e.conf()
	g, _ := c.Group(state.MainGroup)
	if r.ro.compat {
		if includeNewInMain(c, true) && len(r.newIDs) > 0 {
			r.newIn = r.newIDs
			return StepResult{Summary: fmt.Sprintf("%d new account(s) included in this run (compat)", len(r.newIDs))}, nil
		}
		return StepResult{Summary: fmt.Sprintf("%d member(s)", len(g.Members))}, nil
	}
	if !e.hasInteraction() || o.DryRun {
		if len(r.newIDs) > 0 {
			return StepResult{Summary: fmt.Sprintf("%d member(s); %d new account(s) left ungrouped", len(g.Members), len(r.newIDs))}, nil
		}
		return StepResult{Summary: fmt.Sprintf("%d member(s)", len(g.Members))}, nil
	}
	rows := e.accountRows(r.s, r.d)
	if len(g.Members) == 0 && o.EditMain {
		var avail []AccountRow
		for _, row := range rows {
			if row.Status != model.StatusMissing {
				avail = append(avail, row)
			}
		}
		ids, err := e.interaction().SelectAccounts(r.ctx, "Choose the accounts of your Main group", avail, nil)
		if err != nil {
			return StepResult{}, err
		}
		if len(ids) > 0 {
			if err := e.groupSetIDs(state.MainGroup, ids); err != nil {
				return StepResult{}, err
			}
			return StepResult{Summary: fmt.Sprintf("main group set to %d account(s)", len(ids))}, nil
		}
		return StepResult{Summary: "main group left empty"}, nil
	}
	if len(r.newIDs) > 0 {
		var news []AccountRow
		for _, row := range rows {
			if row.Status == model.StatusNew {
				news = append(news, row)
			}
		}
		ids, err := e.interaction().SelectAccounts(r.ctx, "Add newly discovered accounts to Main?", news, nil)
		if err != nil {
			return StepResult{}, err
		}
		if len(ids) > 0 {
			if err := e.mutateConfig(func(c *state.Config) error {
				mg := c.EnsureGroup(state.MainGroup, "")
				for _, id := range ids {
					mg.Add(state.Member{AccountID: id})
				}
				return nil
			}); err != nil {
				return StepResult{}, err
			}
			return StepResult{Summary: fmt.Sprintf("%d new account(s) added to main", len(ids))}, nil
		}
		return StepResult{Summary: fmt.Sprintf("%d new account(s) left ungrouped", len(r.newIDs))}, nil
	}
	return StepResult{Summary: fmt.Sprintf("%d member(s)", len(g.Members))}, nil
}

// groupSetIDs replaces a group's members with account IDs (discovered accounts
// need not be configured yet).
func (e *Engine) groupSetIDs(name string, ids []string) error {
	return e.mutateConfig(func(c *state.Config) error {
		g, err := c.Group(name)
		if err != nil {
			return err
		}
		old := g.Members
		g.Members = nil
		for _, id := range ids {
			m := state.Member{AccountID: id}
			for _, x := range old {
				if x.AccountID == id {
					m = x
				}
			}
			g.Add(m)
		}
		return nil
	})
}

func (r *fullRun) syncEnabled() bool {
	return !r.o.NoSync && !r.e.syncDisabledEnv()
}

func (r *fullRun) preview() (StepResult, error) {
	e, o := r.e, r.o
	var b strings.Builder
	removeStale := !r.cached
	r.syncO = SyncOptions{RemoveStale: removeStale}
	if r.syncEnabled() {
		p, _, err := e.buildSyncPlan(r.ctx, r.s.Name, r.syncO, r.ro)
		if err != nil {
			return StepResult{}, err
		}
		b.WriteString("~/.aws/config changes:\n")
		b.WriteString(indent(p.Preview()))
	} else {
		b.WriteString("~/.aws/config sync: skipped\n")
	}
	r.expO = ExportOptions{Policy: o.Policy, Parallel: o.Parallel, WriteFile: o.Export, UpdateRoles: o.UpdateRoles, Fallbacks: o.Fallbacks}
	dry := r.expO
	dry.DryRun = true
	ro := r.ro
	ro.dryRun = true
	res, err := e.export(r.ctx, r.s.Name, r.scope, dry, ro, r.newProfilesGuess())
	if err != nil {
		return StepResult{}, err
	}
	fmt.Fprintf(&b, "credentials (%s, policy %s):\n", r.scope.Kind, r.pol)
	for _, l := range res.Lines {
		fmt.Fprintf(&b, "  %s → [%s] role %s\n", l.Profile, l.Section, l.Role)
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(&b, "  ! %s\n", w)
	}
	if r.ro.compat && r.scope.IncludeNew && len(r.newIn) > 0 {
		fmt.Fprintf(&b, "  + %d newly discovered account(s) once their profiles are added\n", len(r.newIn))
	}
	r.rep.Warnings = append(r.rep.Warnings, res.Warnings...)
	return StepResult{Summary: fmt.Sprintf("%d profile(s) in scope", len(res.Lines)), Details: strings.Split(strings.TrimRight(b.String(), "\n"), "\n")}, nil
}

// newProfilesGuess: during preview the new profiles do not exist yet.
func (r *fullRun) newProfilesGuess() []string { return []string{} }

func indent(s string) string {
	var b strings.Builder
	for _, l := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		b.WriteString("  " + l + "\n")
	}
	return b.String()
}

func (r *fullRun) approve() (StepResult, error) {
	var preview string
	for _, s := range r.rep.Steps {
		if s.Step == "preview" {
			preview = strings.Join(s.Details, "\n") + "\n"
		}
	}
	ok, err := r.e.review(r.ctx, r.ro, "Apply the setup plan", preview)
	if err != nil {
		return StepResult{}, err
	}
	if !ok {
		return StepResult{}, ErrDeclined
	}
	return StepResult{Summary: "approved"}, nil
}

func (r *fullRun) backup() (StepResult, error) {
	keep := r.e.conf().Export.BackupKeep
	var made []string
	for _, p := range []string{r.e.paths.AWSConfig, r.e.credsFile(r.e.conf())} {
		b, err := fsutil.Backup(p, keep)
		if err != nil {
			return StepResult{}, err
		}
		if b != "" {
			made = append(made, b)
		}
	}
	r.rep.Backups = append(r.rep.Backups, made...)
	return StepResult{Summary: fmt.Sprintf("%d backup(s)", len(made)), Details: made}, nil
}

func (r *fullRun) sync() (StepResult, error) {
	if !r.syncEnabled() {
		return skipped("disabled (--no-sync / SYNC=0)")
	}
	res, err := r.e.applySync(r.ctx, r.s.Name, r.syncO, r.ro, true)
	if err != nil {
		return StepResult{}, err
	}
	r.rep.Warnings = append(r.rep.Warnings, res.Warnings...)
	if res.Backup != "" {
		r.rep.Backups = append(r.rep.Backups, res.Backup)
	}
	return StepResult{Summary: fmt.Sprintf("%d change(s) applied", res.Applied), Details: res.Warnings}, nil
}

func (r *fullRun) credentials() (StepResult, error) {
	e := r.e
	e.mu.Lock()
	added := append([]string(nil), e.added[r.s.Name]...)
	e.mu.Unlock()
	ro := r.ro
	ro.deferRoles = true // role updates are their own step
	res, err := e.export(r.ctx, r.s.Name, r.scope, r.expO, ro, added)
	r.exp = res
	if res != nil {
		r.rep.Exported, r.rep.Failed = res.Succeeded, res.Failed
		if res.Backup != "" {
			r.rep.Backups = append(r.rep.Backups, res.Backup)
		}
		r.rep.Warnings = append(r.rep.Warnings, res.Warnings...)
	}
	if err != nil {
		return StepResult{}, err
	}
	var details []string
	for _, l := range res.Lines {
		if l.Err != "" {
			details = append(details, l.Profile+": "+l.Err)
		}
	}
	return StepResult{Summary: fmt.Sprintf("%d fetched, %d failed", res.Succeeded, res.Failed), Details: details}, nil
}

func (r *fullRun) exportStep() (StepResult, error) {
	if r.exp == nil {
		return skipped("nothing fetched")
	}
	if len(r.exp.Written) == 0 {
		return skipped("credentials not written (export disabled; native SSO resolution)")
	}
	return StepResult{Summary: fmt.Sprintf("%d section(s) written to %s", len(r.exp.Written), r.e.credsFile(r.e.conf())), Details: r.exp.Written}, nil
}

func (r *fullRun) roleUpdates() (StepResult, error) {
	if r.exp == nil || len(r.exp.RoleChanges) == 0 {
		return skipped("no role changes")
	}
	update, auto := r.e.roleUpdatePolicy(r.e.conf(), r.ro.compat, r.o.UpdateRoles)
	if !update {
		return skipped(fmt.Sprintf("%d role change(s) not saved (roles.update_config=false)", len(r.exp.RoleChanges)))
	}
	saved, err := r.e.persistRoles(r.ctx, r.s, r.exp.RoleChanges, r.ro, auto)
	if err != nil {
		if errors.Is(err, ErrDeclined) {
			return skipped("role updates declined")
		}
		return StepResult{}, err
	}
	r.exp.RolesSaved = saved
	var d []string
	for p, role := range r.exp.RoleChanges {
		d = append(d, p+" → "+role)
	}
	return StepResult{Summary: fmt.Sprintf("%d role(s) saved to %s", len(r.exp.RoleChanges), r.e.paths.AWSConfig), Details: d}, nil
}

func (r *fullRun) rotate() (StepResult, error) {
	on := r.ro.compat
	if r.o.Rotate != nil {
		on = *r.o.Rotate
	}
	if !on {
		return skipped("rotation disabled")
	}
	if len(r.e.conf().Rotation.Accounts) == 0 {
		return skipped("no rotation accounts configured")
	}
	if r.scope.Kind == ScopeSingle {
		return skipped("single-profile run")
	}
	from, to, err := r.e.Rotate(r.ctx, RotateOptions{})
	if err != nil {
		r.rep.Warnings = append(r.rep.Warnings, "rotation: "+err.Error())
		return StepResult{Summary: err.Error()}, nil
	}
	if from == "" {
		from = "<unset>"
	}
	return StepResult{Summary: "Claude account rotated: " + from + " -> " + to}, nil
}

func (r *fullRun) validate() (StepResult, error) {
	e := r.e
	var details []string
	f, err := e.readAWSConfig()
	if err != nil {
		return StepResult{}, err
	}
	n := 0
	for _, p := range awsini.Profiles(f) {
		if p.IsSSO() {
			n++
		}
	}
	details = append(details, fmt.Sprintf("%s: %d SSO profiles", e.paths.AWSConfig, n))
	creds := e.credsFile(e.conf())
	data, err := fsutil.ReadFileOptional(creds)
	if err != nil {
		return StepResult{}, err
	}
	if data != nil {
		cf, err := awsini.Parse(data)
		if err != nil {
			return StepResult{}, fmt.Errorf("credentials file does not parse: %w", err)
		}
		details = append(details, fmt.Sprintf("%s: %d sections, %d with expiry", creds, len(cf.Sections()), len(credexport.ReadExpirations(creds))))
		if ok, detail, err := fsutil.CheckPrivate(creds); err == nil && !ok {
			r.rep.Warnings = append(r.rep.Warnings, creds+": "+detail)
		}
	}
	return StepResult{Summary: fmt.Sprintf("%d SSO profiles; files parse", n), Details: details}, nil
}
