package cli

import (
	"errors"
	"fmt"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/credorbit/credorbit/internal/engine"
	"github.com/credorbit/credorbit/internal/model"
	"github.com/credorbit/credorbit/internal/netx"
)

var accountIDRE = regexp.MustCompile(`^\d{12}$`)

// ---- scope flags ----

type scopeFlags struct {
	all       bool
	group     string
	profile   string
	selected  bool
	accounts  []string
	anyPrefix bool
}

func (s *scopeFlags) register(fs *pflag.FlagSet, profile bool) {
	fs.BoolVar(&s.all, "all", false, "every SSO profile of the session with the prefix")
	fs.StringVar(&s.group, "group", "", "members of a saved group (e.g. main)")
	fs.BoolVar(&s.selected, "selected", false, "only the accounts given with --accounts")
	fs.StringSliceVar(&s.accounts, "accounts", nil, "comma-separated account IDs or profile names (temporary selection)")
	fs.BoolVar(&s.anyPrefix, "any-prefix", false, "with --all: include SSO profiles without the prefix")
	if profile {
		fs.StringVar(&s.profile, "profile", "", "exactly one profile (with or without prefix)")
	}
}

func (s *scopeFlags) scope(def engine.ScopeKind) (engine.Scope, error) {
	n := 0
	for _, b := range []bool{s.all, s.group != "", s.profile != "", s.selected || len(s.accounts) > 0} {
		if b {
			n++
		}
	}
	if n > 1 {
		return engine.Scope{}, usagef("choose one of --all, --group, --profile, --selected/--accounts")
	}
	if s.selected && len(s.accounts) == 0 {
		return engine.Scope{}, usagef("--selected needs --accounts a,b")
	}
	switch {
	case s.group != "":
		return engine.Scope{Kind: engine.ScopeGroup, Group: s.group}, nil
	case s.profile != "":
		return engine.Scope{Kind: engine.ScopeSingle, Profiles: []string{s.profile}}, nil
	case len(s.accounts) > 0:
		sc := engine.Scope{Kind: engine.ScopeSelected}
		for _, a := range s.accounts {
			if a = strings.TrimSpace(a); a == "" {
				continue
			} else if accountIDRE.MatchString(a) {
				sc.AccountIDs = append(sc.AccountIDs, a)
			} else {
				sc.Profiles = append(sc.Profiles, a)
			}
		}
		return sc, nil
	case s.all:
		return engine.Scope{Kind: engine.ScopeAll, AnyPrefix: s.anyPrefix}, nil
	}
	return engine.Scope{Kind: def, AnyPrefix: s.anyPrefix}, nil
}

func parsePolicy(p string) (model.RolePolicy, error) {
	if p == "" {
		return "", nil
	}
	pol, err := model.ParseRolePolicy(p)
	if err != nil {
		return "", usageErr(err)
	}
	return pol, nil
}

// triBool returns nil unless one of the two flags was given.
func triBool(cmd *cobra.Command, on, off string) *bool {
	if cmd.Flags().Changed(on) {
		v, _ := cmd.Flags().GetBool(on)
		return &v
	}
	if off != "" && cmd.Flags().Changed(off) {
		v, _ := cmd.Flags().GetBool(off)
		v = !v
		return &v
	}
	return nil
}

func hm(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("15:04")
}

// ---- ui ----

func (a *app) uiCmd() *cobra.Command {
	var o engine.UIOptions
	c := &cobra.Command{Use: "ui", Short: "Open the interactive terminal UI", Args: exactArgs(0), RunE: func(*cobra.Command, []string) error {
		if RunUI == nil {
			return errors.New("this build has no TUI")
		}
		svc, err := a.service()
		if err != nil && !o.Demo {
			return err
		}
		return RunUI(a.ctx, svc, o)
	}}
	c.Flags().BoolVar(&o.Demo, "demo", false, "demo mode with fake data (no AWS calls, no file writes)")
	c.Flags().StringVar(&o.Screen, "screen", "", "initial screen: groups|main|accounts|rotate|setup")
	return c
}

// ---- setup ----

func (a *app) setupCmd() *cobra.Command {
	var (
		full, dry, allowRemove, forceLogin, device, noSync, editMain bool
		policy                                                       string
		parallel                                                     int
		sf                                                           scopeFlags
		fallbacks                                                    []string
	)
	c := &cobra.Command{Use: "setup", Short: "Run the complete workflow: login, discover, sync, export, rotate", Args: exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			_ = full
			pol, err := parsePolicy(policy)
			if err != nil {
				return err
			}
			sc, err := sf.scope("")
			if err != nil {
				return err
			}
			svc, err := a.service()
			if err != nil {
				return err
			}
			a.ui.allowRemove = allowRemove
			a.prog.steps = true
			o := engine.FullOptions{
				Session: a.session, Compat: a.compat, Scope: sc, ForceLogin: forceLogin, Policy: pol, NoSync: noSync,
				Export: triBool(cmd, "export", "no-export"), UpdateRoles: triBool(cmd, "update-roles", "no-update-roles"),
				Rotate: triBool(cmd, "rotate", "no-rotate"), DryRun: dry, Yes: a.yes, AllowRemove: allowRemove, EditMain: editMain, Parallel: parallel,
			}
			if cmd.Flags().Changed("fallback-role") {
				o.Fallbacks = fallbacks
			}
			if device {
				o.LoginFlow = "device"
			}
			rep, err := svc.RunFull(a.ctx, o)
			if a.jsonOut && rep != nil {
				_ = a.printJSON(toReportJSON(rep))
			} else if rep != nil {
				a.printReport(rep, dry)
			}
			if err != nil {
				return err
			}
			if rep.Failed > 0 {
				return &exitError{code: ExitPartial, err: fmt.Errorf("%d profile(s) failed", rep.Failed)}
			}
			return nil
		}}
	f := c.Flags()
	f.BoolVar(&full, "full", true, "run the full workflow (default)")
	f.BoolVar(&dry, "dry-run", false, "stop after the preview; write nothing")
	f.BoolVar(&allowRemove, "allow-remove", false, "approve removal of stale profiles")
	f.BoolVar(&forceLogin, "force-login", false, "sign in even if the token is valid")
	f.BoolVar(&device, "device", false, "use the device-code login flow")
	f.StringVar(&policy, "policy", "", "role policy: configured|manual|strongest|readonly|script")
	f.BoolVar(&noSync, "no-sync", false, "do not change ~/.aws/config")
	f.Bool("export", false, "write credentials file")
	f.Bool("no-export", false, "do not write credentials file")
	f.Bool("update-roles", false, "persist roles that worked")
	f.Bool("no-update-roles", false, "never persist role changes")
	f.Bool("rotate", false, "rotate the Claude account")
	f.Bool("no-rotate", false, "do not rotate")
	f.BoolVar(&editMain, "edit-main", false, "offer the Main-group editor")
	f.IntVar(&parallel, "parallel", 0, "parallel credential requests")
	f.StringSliceVar(&fallbacks, "fallback-role", nil, "fallback roles tried last (script policy); repeatable")
	sf.register(f, true)
	return c
}

func (a *app) printReport(rep *engine.Report, dry bool) {
	for _, w := range rep.Warnings {
		a.note("warning: %s\n", w)
	}
	if dry {
		for _, s := range rep.Steps {
			if s.Step == "preview" {
				a.out("%s\n", strings.Join(s.Details, "\n"))
			}
		}
		a.out("Dry run — nothing was written.\n")
		return
	}
	for _, s := range rep.Steps {
		if s.Step == "export" && s.OK {
			a.out("Done — %s\n", s.Summary)
		}
	}
	for _, b := range rep.Backups {
		a.out("Backup: %s\n", b)
	}
	a.out("%d exported, %d failed (%s)\n", rep.Exported, rep.Failed, rep.Duration.Round(time.Millisecond))
}

// ---- login / logout ----

func (a *app) loginCmd() *cobra.Command {
	var force, device bool
	var startURL, region, name string
	c := &cobra.Command{Use: "login", Short: "Sign in to an SSO session (or create one with --start-url/--region/--name)", Args: exactArgs(0),
		RunE: func(*cobra.Command, []string) error {
			svc, err := a.service()
			if err != nil {
				return err
			}
			session := a.session
			if startURL != "" || region != "" || name != "" {
				if startURL == "" || region == "" || name == "" {
					return usagef("creating a session needs --start-url, --region and --name")
				}
				if err := svc.AddSession(model.SSOSession{Name: name, StartURL: startURL, Region: region}); err != nil {
					return err
				}
				session = name
			}
			flow := ""
			if device {
				flow = "device"
			}
			ls, err := svc.Login(a.ctx, session, flow, force)
			if err != nil {
				return err
			}
			refresh := ""
			if ls.Refresh {
				refresh = ", silent refresh available"
			}
			a.out("Signed in to %s; token valid until %s%s\n", ls.Session.Name, ls.ExpiresAt.Local().Format("2006-01-02 15:04"), refresh)
			return nil
		}}
	f := c.Flags()
	f.BoolVar(&force, "force", false, "sign in even if the token is valid")
	f.BoolVar(&device, "device", false, "device-code flow (no local browser needed)")
	f.StringVar(&startURL, "start-url", "", "create session: SSO start URL")
	f.StringVar(&region, "region", "", "create session: SSO region")
	f.StringVar(&name, "name", "", "create session: name")
	return c
}

func (a *app) logoutCmd() *cobra.Command {
	return &cobra.Command{Use: "logout", Short: "Remove the cached SSO token", Args: exactArgs(0), RunE: func(*cobra.Command, []string) error {
		svc, err := a.service()
		if err != nil {
			return err
		}
		if err := svc.Logout(a.ctx, a.session); err != nil {
			return err
		}
		a.out("Signed out.\n")
		return nil
	}}
}

// ---- discover / accounts ----

func (a *app) discoverCmd() *cobra.Command {
	return &cobra.Command{Use: "discover", Short: "List accounts and roles assigned to you (cached for offline use)", Args: exactArgs(0),
		RunE: func(*cobra.Command, []string) error {
			svc, err := a.service()
			if err != nil {
				return err
			}
			d, err := svc.Discover(a.ctx, a.session)
			if d != nil {
				if a.jsonOut {
					_ = a.printJSON(d)
				} else {
					tw := tabwriter.NewWriter(a.stdout, 0, 2, 2, ' ', 0)
					fmt.Fprintln(tw, "ACCOUNT\tID\tROLES")
					for _, x := range d.Accounts {
						roles := strings.Join(x.Roles, ",")
						if x.RolesErr != "" {
							roles = "(" + x.RolesErr + ")"
						}
						fmt.Fprintf(tw, "%s\t%s\t%s\n", x.Name, x.ID, roles)
					}
					_ = tw.Flush()
					a.out("%d accounts discovered in session %s\n", len(d.Accounts), d.Session)
				}
			}
			return err
		}}
}

func (a *app) accountsCmd() *cobra.Command {
	var filter, env, status, group, sortBy string
	list := &cobra.Command{Use: "list", Short: "Accounts from discovery joined with your configuration", Args: exactArgs(0),
		RunE: func(*cobra.Command, []string) error {
			svc, err := a.service()
			if err != nil {
				return err
			}
			rows := svc.Accounts(a.session)
			var out []engine.AccountRow
			f := strings.ToLower(filter)
			for _, r := range rows {
				hay := strings.ToLower(r.Account.Name + " " + r.Account.ID + " " + strings.Join(r.Profiles, " "))
				switch {
				case f != "" && !strings.Contains(hay, f):
				case env != "" && r.Env != env:
				case status != "" && string(r.Status) != status:
				case group != "" && !contains(r.Groups, group):
				default:
					out = append(out, r)
				}
			}
			sort.SliceStable(out, func(i, j int) bool {
				x, y := out[i], out[j]
				switch sortBy {
				case "id":
					return x.Account.ID < y.Account.ID
				case "env":
					return x.Env < y.Env
				case "status":
					return x.Status < y.Status
				}
				return strings.ToLower(x.Account.Name) < strings.ToLower(y.Account.Name)
			})
			if a.jsonOut {
				js := []accountJSON{}
				for _, r := range out {
					js = append(js, toAccountJSON(r))
				}
				return a.printJSON(js)
			}
			tw := tabwriter.NewWriter(a.stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "ACCOUNT\tID\tENV\tSTATUS\tPROFILE\tROLE\tGROUPS\tCREDS")
			for _, r := range out {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.Account.Name, r.Account.ID, dash(r.Env), r.Status,
					dash(strings.Join(r.Profiles, ",")), dash(r.Role), dash(strings.Join(r.Groups, ",")), hm(r.CredsExp))
			}
			_ = tw.Flush()
			return nil
		}}
	fl := list.Flags()
	fl.StringVar(&filter, "filter", "", "substring of name, ID or profile")
	fl.StringVar(&env, "env", "", "prd|stg|dev|test|sandbox")
	fl.StringVar(&status, "status", "", "available|new|missing|unknown")
	fl.StringVar(&group, "group", "", "only members of this group")
	fl.StringVar(&sortBy, "sort", "name", "name|id|env|status")
	return parent("accounts", "Account inventory", list)
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// ---- sync ----

func (a *app) syncCmd() *cobra.Command {
	var dry, removeStale, allowRemove bool
	var sf scopeFlags
	c := &cobra.Command{Use: "sync", Short: "Add new accounts to ~/.aws/config and (reviewed) remove stale profiles", Args: exactArgs(0),
		RunE: func(*cobra.Command, []string) error {
			sc, err := sf.scope(engine.ScopeAll)
			if err != nil {
				return err
			}
			svc, err := a.service()
			if err != nil {
				return err
			}
			a.ui.allowRemove = allowRemove
			return a.doSync(svc, sc, dry, removeStale || allowRemove)
		}}
	f := c.Flags()
	f.BoolVar(&dry, "dry-run", false, "show the plan only")
	f.BoolVar(&removeStale, "remove-stale", false, "include removal of profiles whose account is gone (reviewed)")
	f.BoolVar(&allowRemove, "allow-remove", false, "approve the removals non-interactively")
	sf.register(f, false)
	return c
}

func (a *app) scopeAccountIDs(svc engine.Service, sc engine.Scope) ([]string, error) {
	switch sc.Kind {
	case engine.ScopeGroup:
		g, err := svc.Group(sc.Group)
		if err != nil {
			return nil, err
		}
		ids := []string{}
		for _, m := range g.Members {
			if m.AccountID != "" {
				ids = append(ids, m.AccountID)
			}
		}
		return ids, nil
	case engine.ScopeSelected, engine.ScopeSingle:
		ids := append([]string{}, sc.AccountIDs...)
		for _, r := range svc.Accounts(a.session) {
			for _, p := range r.Profiles {
				for _, want := range sc.Profiles {
					if p == want || strings.HasSuffix(p, "_"+want) {
						ids = append(ids, r.Account.ID)
					}
				}
			}
		}
		return ids, nil
	}
	return nil, nil
}

func (a *app) doSync(svc engine.Service, sc engine.Scope, dry, removeStale bool) error {
	if _, err := svc.Discover(a.ctx, a.session); err != nil {
		if errors.Is(err, model.ErrUnauthorized) {
			return err
		}
		a.note("warning: live discovery failed (%v); using the cached discovery, removals are not evaluated\n", err)
	}
	ids, err := a.scopeAccountIDs(svc, sc)
	if err != nil {
		return err
	}
	o := engine.SyncOptions{RemoveStale: removeStale, AccountIDs: ids}
	if dry {
		preview, _, err := svc.PlanSync(a.ctx, a.session, o)
		if err != nil {
			return err
		}
		a.out("%s", preview)
		return nil
	}
	res, err := svc.ApplySync(a.ctx, a.session, o)
	if res != nil {
		for _, w := range res.Warnings {
			a.note("warning: %s\n", w)
		}
	}
	if err != nil {
		return err
	}
	if res.Applied == 0 {
		a.out("~/.aws/config is up to date\n")
		return nil
	}
	a.out("Sync: %d change(s) applied", res.Applied)
	if res.Backup != "" {
		a.out(" — backup at %s", res.Backup)
	}
	a.out("\n")
	return nil
}

// ---- export ----

func (a *app) exportCmd() *cobra.Command {
	var dry, noWrite bool
	var policy string
	var parallel int
	var sf scopeFlags
	var fallbacks []string
	c := &cobra.Command{Use: "export", Short: "Fetch temporary credentials (and write the credentials file if enabled)", Args: exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			pol, err := parsePolicy(policy)
			if err != nil {
				return err
			}
			sc, err := sf.scope(engine.ScopeAll)
			if err != nil {
				return err
			}
			svc, err := a.service()
			if err != nil {
				return err
			}
			o := engine.ExportOptions{Policy: pol, Parallel: parallel, DryRun: dry, UpdateRoles: triBool(cmd, "update-roles", "")}
			if noWrite {
				o.WriteFile = boolp(false)
			}
			if cmd.Flags().Changed("fallback-role") {
				o.Fallbacks = fallbacks
			}
			return a.doExport(svc, sc, o)
		}}
	f := c.Flags()
	f.BoolVar(&dry, "dry-run", false, "show what would be fetched/written")
	f.StringVar(&policy, "policy", "", "role policy: configured|manual|strongest|readonly|script")
	f.IntVar(&parallel, "parallel", 0, "parallel credential requests")
	f.BoolVar(&noWrite, "no-write", false, "verify access only; do not write the credentials file")
	f.Bool("update-roles", false, "persist the roles that worked to ~/.aws/config (reviewed)")
	f.StringSliceVar(&fallbacks, "fallback-role", nil, "fallback roles tried last (script policy); repeatable")
	sf.register(f, true)
	return c
}

func boolp(b bool) *bool { return &b }

func (a *app) doExport(svc engine.Service, sc engine.Scope, o engine.ExportOptions) error {
	pol := o.Policy
	if pol == "" && svc.Mode() == "compat" {
		pol = model.PolicyScript
	}
	if pol == model.PolicyScript || pol == model.PolicyStrongest || pol == model.PolicyReadOnly {
		// Ranking policies need the assigned roles: refresh discovery (cache on failure).
		if _, err := svc.Discover(a.ctx, a.session); err != nil && errors.Is(err, model.ErrUnauthorized) {
			return err
		}
	}
	res, err := svc.Export(a.ctx, a.session, sc, o)
	if res != nil {
		if a.jsonOut {
			_ = a.printJSON(toExportJSON(res))
		} else {
			a.printExport(svc, res, o.DryRun)
		}
	}
	if err != nil {
		return err
	}
	switch {
	case res.Failed > 0 && res.Succeeded > 0:
		return &exitError{code: ExitPartial, err: fmt.Errorf("%d of %d profiles failed", res.Failed, res.Failed+res.Succeeded)}
	case res.Failed > 0:
		allAuth := true
		for _, l := range res.Lines {
			allAuth = allAuth && l.ErrClass == "unauthorized"
		}
		if allAuth {
			return &exitError{code: ExitNeedsLogin, err: errors.New("SSO token expired or invalid")}
		}
		return errors.New("no profile succeeded")
	}
	return nil
}

func (a *app) printExport(svc engine.Service, res *engine.ExportResult, dry bool) {
	for _, w := range res.Warnings {
		a.note("warning: %s\n", w)
	}
	if dry {
		tw := tabwriter.NewWriter(a.stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "PROFILE\tSECTION\tROLE")
		for _, l := range res.Lines {
			role := l.Role
			if l.Err != "" {
				role = "(" + l.Err + ")"
			}
			fmt.Fprintf(tw, "%s\t[%s]\t%s\n", l.Profile, l.Section, role)
		}
		_ = tw.Flush()
		return
	}
	for p, r := range res.RoleChanges {
		if res.RolesSaved {
			a.out("  role updated: %s -> %s\n", p, r)
		} else {
			a.out("  role differs (not saved): %s -> %s\n", p, r)
		}
	}
	if len(res.Written) > 0 {
		a.out("\nDone — %d sections written to %s\n", len(res.Written), svc.Status(a.ctx).Credentials)
		if res.Backup != "" {
			a.out("Backup: %s\n", res.Backup)
		}
	} else if res.Succeeded > 0 {
		a.out("\nVerified %d profile(s); credentials file not written\n", res.Succeeded)
	}
	if len(res.Kept) > 0 {
		a.out("Kept previous credentials for: %s\n", strings.Join(res.Kept, ", "))
	}
}

// ---- status / doctor ----

func (a *app) statusCmd() *cobra.Command {
	return &cobra.Command{Use: "status", Short: "Summary of sessions, profiles, groups and exported credentials (no network)", Args: exactArgs(0),
		RunE: func(*cobra.Command, []string) error {
			svc, err := a.service()
			if err != nil {
				return err
			}
			st := svc.Status(a.ctx)
			if a.jsonOut {
				return a.printJSON(st)
			}
			a.out("CredOrbit %s — mode %s\n", st.Version, st.Mode)
			a.out("settings:    %s\naws config:  %s\ncredentials: %s\n", st.ConfigFile, st.AWSConfig, st.Credentials)
			for _, s := range st.Sessions {
				state := "not logged in"
				if s.LoggedIn {
					state = "logged in until " + s.ExpiresAt.Local().Format("2006-01-02 15:04")
				} else if s.Refresh {
					state = "expired (refreshable)"
				}
				a.out("session %-20s %s; %d accounts discovered\n", s.Name, state, s.Accounts)
			}
			a.out("%d SSO profiles\n", st.Profiles)
			names := make([]string, 0, len(st.Groups))
			for n := range st.Groups {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				a.out("group %-12s %d member(s)\n", n, st.Groups[n])
			}
			expired := 0
			for _, c := range st.Exported {
				if c.Expired {
					expired++
				}
			}
			a.out("%d credential section(s) tracked, %d expired\n", len(st.Exported), expired)
			if st.Rotation != nil {
				a.out("claude rotation: current %s (%d accounts)\n", dash(st.Rotation.Current), len(st.Rotation.Accounts))
			}
			return nil
		}}
}

func (a *app) doctorCmd() *cobra.Command {
	return &cobra.Command{Use: "doctor", Short: "Diagnose configuration, login, network and permissions", Args: exactArgs(0),
		RunE: func(*cobra.Command, []string) error {
			svc, err := a.service()
			if err != nil {
				return err
			}
			checks := svc.Doctor(a.ctx)
			fails := 0
			js := []checkJSON{}
			for _, c := range checks {
				if c.Status == "fail" {
					fails++
				}
				js = append(js, checkJSON{Area: c.Area, Name: c.Name, Status: c.Status, Detail: c.Detail, Remedy: c.Remedy})
			}
			if a.jsonOut {
				_ = a.printJSON(js)
			} else {
				for _, c := range checks {
					a.out("[%-4s] %-11s %s: %s\n", c.Status, c.Area, c.Name, c.Detail)
					if c.Remedy != "" && c.Status != "ok" {
						a.out("       fix: %s\n", c.Remedy)
					}
				}
			}
			if fails > 0 {
				return &exitError{code: ExitError, err: fmt.Errorf("%d check(s) failed", fails)}
			}
			return nil
		}}
}

// ---- version / update / ca ----

func (a *app) versionCmd() *cobra.Command {
	return &cobra.Command{Use: "version", Short: "Print the version", Args: exactArgs(0), RunE: func(*cobra.Command, []string) error {
		v := versionJSON{Version: Version, OS: runtime.GOOS, Arch: runtime.GOARCH, Go: runtime.Version()}
		if a.jsonOut {
			return a.printJSON(v)
		}
		a.out("credorbit %s (%s/%s, %s)\n", v.Version, v.OS, v.Arch, v.Go)
		return nil
	}}
}

func (a *app) updateCmd() *cobra.Command {
	var check bool
	c := &cobra.Command{Use: "update", Short: "Check for a newer release", Args: exactArgs(0), RunE: func(*cobra.Command, []string) error {
		if !check {
			return usagef("only --check is supported; download releases from the project page")
		}
		svc, err := a.service()
		if err != nil {
			return err
		}
		latest, url, newer, err := svc.CheckUpdate(a.ctx)
		if err != nil {
			return err
		}
		if newer {
			a.out("CredOrbit %s is available (you have %s): %s\n", latest, Version, url)
		} else {
			a.out("CredOrbit %s is up to date\n", Version)
		}
		return nil
	}}
	c.Flags().BoolVar(&check, "check", false, "check for updates")
	return c
}

func (a *app) caCmd() *cobra.Command {
	var out string
	bundle := &cobra.Command{Use: "bundle", Short: "Write system roots + configured CAs to one PEM file (for the AWS CLI / child processes)", Args: exactArgs(0),
		RunE: func(*cobra.Command, []string) error {
			if out == "" {
				return usagef("--out PATH is required")
			}
			svc, err := a.service()
			if err != nil {
				return err
			}
			var extra []string
			for _, s := range svc.Settings() {
				switch s.Key {
				case "network.ca_bundle":
					if s.Value != "" {
						extra = append(extra, s.Value)
					}
				case "network.extra_cas":
					for _, x := range strings.Split(s.Value, ",") {
						if x = strings.TrimSpace(x); x != "" {
							extra = append(extra, x)
						}
					}
				}
			}
			n, err := netx.WriteCombinedBundle(out, extra)
			if err != nil {
				return err
			}
			a.out("Wrote %d certificates to %s\nUse it with: export AWS_CA_BUNDLE=%s\n", n, out, out)
			return nil
		}}
	bundle.Flags().StringVar(&out, "out", "", "output PEM file")
	return parent("ca", "TLS trust helpers", bundle)
}
