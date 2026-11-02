package cli

import (
	"errors"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/PiniPP/credorbit/internal/engine"
	"github.com/PiniPP/credorbit/internal/model"
)

// ---- groups ----

func (a *app) groupsCmd() *cobra.Command {
	withSvc := func(fn func(svc engine.Service, args []string) error) func(*cobra.Command, []string) error {
		return func(_ *cobra.Command, args []string) error {
			svc, err := a.service()
			if err != nil {
				return err
			}
			return fn(svc, args)
		}
	}
	list := &cobra.Command{Use: "list", Short: "List groups", Args: exactArgs(0), RunE: withSvc(func(svc engine.Service, _ []string) error {
		gs := svc.Groups()
		if a.jsonOut {
			js := []groupJSON{}
			for _, g := range gs {
				js = append(js, toGroupJSON(g))
			}
			return a.printJSON(js)
		}
		tw := tabwriter.NewWriter(a.stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "GROUP\tMEMBERS\tDESCRIPTION")
		for _, g := range gs {
			fmt.Fprintf(tw, "%s\t%d\t%s\n", g.Name, len(g.Members), g.Description)
		}
		return tw.Flush()
	})}
	show := &cobra.Command{Use: "show NAME", Short: "Show a group's members", Args: exactArgs(1), RunE: withSvc(func(svc engine.Service, args []string) error {
		g, err := svc.Group(args[0])
		if err != nil {
			return err
		}
		if a.jsonOut {
			return a.printJSON(toGroupJSON(g))
		}
		a.out("%s — %s\n", g.Name, g.Description)
		tw := tabwriter.NewWriter(a.stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "ACCOUNT\tID\tPROFILE\tROLE\tSTATUS")
		for _, m := range g.Members {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", dash(m.AccountName), dash(m.AccountID), dash(m.Profile), dash(m.Role), m.Status)
		}
		return tw.Flush()
	})}
	edit := &cobra.Command{Use: "edit NAME", Short: "Edit a group interactively (TUI)", Args: exactArgs(1), RunE: withSvc(func(svc engine.Service, args []string) error {
		if !a.tty || RunUI == nil {
			return fmt.Errorf("groups edit needs an interactive terminal with the TUI; use: credorbit groups add|remove %s KEY...", args[0])
		}
		if _, err := svc.Group(args[0]); err != nil {
			return err
		}
		return RunUI(a.ctx, svc, engine.UIOptions{Screen: "groups", Group: args[0]})
	})}
	add := &cobra.Command{Use: "add NAME KEY...", Short: "Add accounts (ID or profile) to a group", Args: minArgs(2), RunE: withSvc(func(svc engine.Service, args []string) error {
		if err := svc.GroupAdd(args[0], args[1:]...); err != nil {
			return err
		}
		a.out("Added %d to %s\n", len(args)-1, args[0])
		return nil
	})}
	remove := &cobra.Command{Use: "remove NAME KEY...", Short: "Remove accounts from a group", Args: minArgs(2), RunE: withSvc(func(svc engine.Service, args []string) error {
		if err := svc.GroupRemove(args[0], args[1:]...); err != nil {
			return err
		}
		a.out("Removed %d from %s\n", len(args)-1, args[0])
		return nil
	})}
	var desc string
	create := &cobra.Command{Use: "create NAME", Short: "Create a group", Args: exactArgs(1), RunE: withSvc(func(svc engine.Service, args []string) error {
		if err := svc.CreateGroup(args[0], desc); err != nil {
			return err
		}
		a.out("Created group %s\n", args[0])
		return nil
	})}
	create.Flags().StringVar(&desc, "description", "", "group description")
	del := &cobra.Command{Use: "delete NAME", Short: "Delete a group (main can only be emptied)", Args: exactArgs(1), RunE: withSvc(func(svc engine.Service, args []string) error {
		if err := svc.DeleteGroup(args[0]); err != nil {
			return err
		}
		a.out("Deleted group %s\n", args[0])
		return nil
	})}
	rename := &cobra.Command{Use: "rename OLD NEW", Short: "Rename a group", Args: exactArgs(2), RunE: withSvc(func(svc engine.Service, args []string) error {
		if err := svc.RenameGroup(args[0], args[1]); err != nil {
			return err
		}
		a.out("Renamed %s to %s\n", args[0], args[1])
		return nil
	})}
	syncG := &cobra.Command{Use: "sync NAME", Short: "Sync the group's accounts, then export the group", Args: exactArgs(1), RunE: withSvc(func(svc engine.Service, args []string) error {
		sc := engine.Scope{Kind: engine.ScopeGroup, Group: args[0]}
		if err := a.doSync(svc, sc, false, false); err != nil && !errors.Is(err, engine.ErrDeclined) {
			return err
		}
		return a.doExport(svc, sc, engine.ExportOptions{})
	})}
	return parent("groups", "Manage account groups (never touches AWS files)", list, show, edit, add, remove, syncG, create, del, rename)
}

// ---- roles ----

type preferredRoles interface{ PreferredRoles() map[string]string }

func (a *app) rolesCmd() *cobra.Command {
	list := &cobra.Command{Use: "list [PROFILE|ACCOUNT]", Short: "Assigned, configured and preferred roles", Args: rangeArgs(0, 1),
		RunE: func(_ *cobra.Command, args []string) error {
			svc, err := a.service()
			if err != nil {
				return err
			}
			pref := map[string]string{}
			if p, ok := svc.(preferredRoles); ok {
				pref = p.PreferredRoles()
			}
			var out []roleJSON
			for _, r := range svc.Accounts(a.session) {
				if len(args) == 1 && !matchesKey(r, args[0]) {
					continue
				}
				out = append(out, roleJSON{AccountID: r.Account.ID, AccountName: r.Account.Name, Profiles: nz(r.Profiles),
					Assigned: nz(r.Account.Roles), Configured: r.Role, Preferred: pref[r.Account.ID]})
			}
			if len(args) == 1 && len(out) == 0 {
				return fmt.Errorf("%s: %w", args[0], model.ErrNotFound)
			}
			if a.jsonOut {
				return a.printJSON(nz(out))
			}
			tw := tabwriter.NewWriter(a.stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "ACCOUNT\tID\tASSIGNED\tCONFIGURED\tPREFERRED")
			for _, r := range out {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.AccountName, r.AccountID, dash(strings.Join(r.Assigned, ",")), dash(r.Configured), dash(r.Preferred))
			}
			return tw.Flush()
		}}
	set := &cobra.Command{Use: "set ACCOUNT_OR_PROFILE ROLE", Short: "Set the preferred role for an account (validated against assigned roles)", Args: exactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			svc, err := a.service()
			if err != nil {
				return err
			}
			id := ""
			if accountIDRE.MatchString(args[0]) {
				id = args[0]
			} else {
				for _, r := range svc.Accounts(a.session) {
					if matchesKey(r, args[0]) {
						id = r.Account.ID
						break
					}
				}
			}
			if id == "" {
				return fmt.Errorf("unknown account or profile %q: %w", args[0], model.ErrNotFound)
			}
			if err := svc.SetPreferredRole(id, args[1]); err != nil {
				return err
			}
			a.out("Preferred role for %s set to %s (used by export; sso_role_name in ~/.aws/config is unchanged)\n", id, args[1])
			return nil
		}}
	return parent("roles", "Inspect and choose roles", list, set)
}

func matchesKey(r engine.AccountRow, key string) bool {
	if r.Account.ID == key || r.Account.Name == key {
		return true
	}
	for _, p := range r.Profiles {
		if p == key || strings.HasSuffix(p, "_"+key) {
			return true
		}
	}
	return false
}

// ---- rotate ----

func (a *app) rotateCmd() *cobra.Command {
	var target, set, strategy string
	var listOnly, dry bool
	c := &cobra.Command{Use: "rotate", Short: "Rotate the AWS account Claude Code uses (Bedrock)", Args: exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if target != "claude" {
				return usagef("unsupported target %q (only claude)", target)
			}
			svc, err := a.service()
			if err != nil {
				return err
			}
			if listOnly {
				info := svc.Rotation()
				if a.jsonOut {
					return a.printJSON(toRotationJSON(info))
				}
				a.out("target %s, file %s, current %s, strategy %s\n", info.Target, info.File, dash(info.Current), info.Strategy)
				for _, ac := range info.Accounts {
					st := "eligible"
					if !ac.Eligible {
						st = "skipped: " + ac.Reason
					}
					mark := " "
					if ac.Section == info.Current {
						mark = "*"
					}
					a.out(" %s %-30s %s (expires %s)\n", mark, ac.Section, st, hm(ac.Expires))
				}
				return nil
			}
			from, to, err := svc.Rotate(a.ctx, engine.RotateOptions{Strategy: strategy, Set: set, Verify: triBool(cmd, "verify", ""), DryRun: dry})
			if err != nil {
				return err
			}
			if from == "" {
				from = "<unset>"
			}
			how := strategy
			if set != "" {
				how = "manual"
			}
			if how == "" {
				how = svc.Rotation().Strategy
			}
			if dry {
				a.out("Would rotate Claude account (%s): %s -> %s\n", how, from, to)
				return nil
			}
			a.out("Claude account rotated (%s): %s -> %s\n  (restart Claude Code / open a new shell to apply)\n", how, from, to)
			return nil
		}}
	f := c.Flags()
	f.StringVar(&target, "target", "claude", "rotation target")
	f.BoolVar(&listOnly, "list", false, "show rotation accounts and eligibility")
	f.StringVar(&set, "set", "", "set the account (credentials section) explicitly")
	f.StringVar(&strategy, "strategy", "", "random|round-robin")
	f.Bool("verify", false, "verify Bedrock access before switching")
	f.BoolVar(&dry, "dry-run", false, "compute only")

	accountsSet := &cobra.Command{Use: "set a,b,c", Short: "Replace the rotation accounts", Args: minArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		return a.editRotation(func(cur []string) []string { return splitArgs(args) })
	}}
	accountsAdd := &cobra.Command{Use: "add SECTION...", Short: "Add rotation accounts", Args: minArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		return a.editRotation(func(cur []string) []string { return append(cur, splitArgs(args)...) })
	}}
	accountsRemove := &cobra.Command{Use: "remove SECTION...", Short: "Remove rotation accounts", Args: minArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		drop := map[string]bool{}
		for _, x := range splitArgs(args) {
			drop[x] = true
		}
		return a.editRotation(func(cur []string) []string {
			var out []string
			for _, x := range cur {
				if !drop[x] {
					out = append(out, x)
				}
			}
			return out
		})
	}}
	shell := &cobra.Command{Use: "shell [zsh|bash|fish|powershell]", Short: "Print the shell snippet that exports AWS_PROFILE from the rotation file", Args: rangeArgs(0, 1),
		RunE: func(_ *cobra.Command, args []string) error {
			svc, err := a.service()
			if err != nil {
				return err
			}
			sh := "zsh"
			if len(args) == 1 {
				sh = args[0]
			}
			s, err := svc.ShellIntegration(sh)
			if err != nil {
				return usageErr(err)
			}
			a.out("%s", s)
			return nil
		}}
	c.AddCommand(parent("accounts", "Manage rotation accounts (credentials sections)", accountsSet, accountsAdd, accountsRemove), shell)
	return c
}

func splitArgs(args []string) []string {
	var out []string
	for _, a := range args {
		for _, x := range strings.Split(a, ",") {
			if x = strings.TrimSpace(x); x != "" {
				out = append(out, x)
			}
		}
	}
	return out
}

func (a *app) editRotation(fn func([]string) []string) error {
	svc, err := a.service()
	if err != nil {
		return err
	}
	var cur []string
	for _, ac := range svc.Rotation().Accounts {
		cur = append(cur, ac.Section)
	}
	next := fn(cur)
	if err := svc.SetRotationAccounts(next); err != nil {
		return err
	}
	var now []string
	for _, ac := range svc.Rotation().Accounts {
		now = append(now, ac.Section)
	}
	a.out("Rotation accounts: %s\n", dash(strings.Join(now, ", ")))
	return nil
}

// ---- import ----

func (a *app) importCmd() *cobra.Command {
	var script string
	var compat bool
	c := &cobra.Command{Use: "import", Short: "One-time, read-only import of the legacy populate-aws-creds.sh", Args: exactArgs(0),
		RunE: func(*cobra.Command, []string) error {
			svc, err := a.service()
			if err != nil {
				return err
			}
			notes, err := svc.ImportLegacy(a.ctx, script, compat || a.compat)
			if err != nil {
				return err
			}
			for _, n := range notes {
				a.out("  %s\n", n)
			}
			a.out("Imported into %s (the script was not modified)\n", svc.ConfigPath())
			return nil
		}}
	c.Flags().StringVar(&script, "script", "", "path of the legacy script (default ~/populate-aws-creds.sh)")
	c.Flags().BoolVar(&compat, "compat", false, "also switch to script-compatible mode")
	return c
}

// ---- config ----

func (a *app) configCmd() *cobra.Command {
	list := &cobra.Command{Use: "list", Short: "List settings", Args: exactArgs(0), RunE: func(*cobra.Command, []string) error {
		svc, err := a.service()
		if err != nil {
			return err
		}
		ss := svc.Settings()
		if a.jsonOut {
			js := []settingJSON{}
			for _, s := range ss {
				js = append(js, settingJSON{Key: s.Key, Value: s.Value, Description: s.Description, Choices: nz(s.Choices)})
			}
			return a.printJSON(js)
		}
		tw := tabwriter.NewWriter(a.stdout, 0, 2, 2, ' ', 0)
		for _, s := range ss {
			fmt.Fprintf(tw, "%s\t%s\t# %s\n", s.Key, s.Value, s.Description)
		}
		return tw.Flush()
	}}
	get := &cobra.Command{Use: "get KEY", Short: "Print one setting", Args: exactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		svc, err := a.service()
		if err != nil {
			return err
		}
		for _, s := range svc.Settings() {
			if s.Key == args[0] {
				a.out("%s\n", s.Value)
				return nil
			}
		}
		return usagef("unknown setting %q (see credorbit config list)", args[0])
	}}
	set := &cobra.Command{Use: "set KEY VALUE", Short: "Change a setting (validated)", Args: exactArgs(2), RunE: func(_ *cobra.Command, args []string) error {
		svc, err := a.service()
		if err != nil {
			return err
		}
		if err := svc.SetSetting(args[0], args[1]); err != nil {
			if errors.Is(err, model.ErrNotFound) {
				return usageErr(err)
			}
			return err
		}
		a.out("%s = %s\n", args[0], args[1])
		return nil
	}}
	path := &cobra.Command{Use: "path", Short: "Print the settings file path", Args: exactArgs(0), RunE: func(*cobra.Command, []string) error {
		svc, err := a.service()
		if err != nil {
			return err
		}
		a.out("%s\n", svc.ConfigPath())
		return nil
	}}
	return parent("config", "View and change settings", list, get, set, path)
}
