package tui

import (
	"context"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PiniPP/credorbit/internal/engine"
	"github.com/PiniPP/credorbit/internal/model"
)

var policies = []model.RolePolicy{model.PolicyConfigured, model.PolicyStrongest, model.PolicyReadOnly, model.PolicyScript, model.PolicyManual}

var policyHelp = map[model.RolePolicy]string{
	model.PolicyConfigured: "the profile's configured role (no substitution)",
	model.PolicyStrongest:  "highest-ranked assigned role",
	model.PolicyReadOnly:   "lowest-privilege assigned role",
	model.PolicyScript:     "legacy order: ranked, configured, fallbacks",
	model.PolicyManual:     "the role you chose per account",
}

const (
	exScope = iota
	exOptions
	exRun
	exDone
)

type exportScreen struct {
	e        *env
	phase    int
	picker   *scopePicker
	scope    engine.Scope
	scopeLbl string
	cur      int
	policy   int
	parallel int
	opts     []toggle // write file, update roles, dry run
	opID     int
	prog     progressPanel
	res      *engine.ExportResult
	err      error
	scroll   int
}

func newExportScreen(e *env) *exportScreen {
	x := &exportScreen{e: e, parallel: 8}
	for _, s := range e.svc.Settings() {
		switch s.Key {
		case "roles.policy":
			for i, p := range policies {
				if string(p) == s.Value {
					x.policy = i
				}
			}
		case "export.concurrency":
			if n, err := strconv.Atoi(s.Value); err == nil && n > 0 {
				x.parallel = n
			}
		}
	}
	x.opts = []toggle{
		{label: "Write ~/.aws/credentials", help: "off = fetch and verify only", on: true},
		{label: "Update roles in ~/.aws/config", help: "persist roles that differ (reviewed)", on: false},
		{label: "Dry run", help: "show what would happen, write nothing", on: false},
	}
	x.picker = newScopePicker(e, func(s engine.Scope, label string) tea.Cmd {
		x.scope, x.scopeLbl, x.phase, x.cur = s, label, exOptions, 0
		return nil
	})
	return x
}

func (x *exportScreen) Init() tea.Cmd { return nil }
func (x *exportScreen) Title() string { return "Export Credentials" }
func (x *exportScreen) KeyHints() []hint {
	g := x.e.g
	switch x.phase {
	case exOptions:
		return []hint{{g.up + g.down, "move"}, {g.left + g.right, "change"}, {"space", "toggle"}, {"s", "start"}, {"esc", "back"}}
	case exRun:
		return []hint{{"ctrl+c", "cancel export"}}
	case exDone:
		return []hint{{g.up + g.down, "scroll"}, {"r", "run again"}, {"esc", "done"}}
	}
	return []hint{{g.up + g.down, "move"}, {"enter", "choose scope"}}
}

func (x *exportScreen) back() (bool, tea.Cmd) {
	switch x.phase {
	case exOptions:
		x.phase = exScope
		return true, nil
	case exRun:
		return true, toast("press ctrl+c to cancel the export")
	}
	return false, nil
}

func (x *exportScreen) start() tea.Cmd {
	e := x.e
	o := engine.ExportOptions{
		Policy:      policies[x.policy],
		Parallel:    x.parallel,
		WriteFile:   boolp(x.opts[0].on),
		UpdateRoles: boolp(x.opts[1].on),
		DryRun:      x.opts[2].on,
	}
	sess, sc := e.session, x.scope
	x.phase = exRun
	x.prog.reset("export")
	x.res, x.err = nil, nil
	id, cmd := e.run("Exporting credentials", func(ctx context.Context) (any, error) { return e.svc.Export(ctx, sess, sc, o) })
	x.opID = id
	return cmd
}

func (x *exportScreen) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case eventsMsg:
		if x.phase == exRun {
			for _, ev := range m {
				x.prog.apply(ev)
			}
		}
	case opDoneMsg:
		if m.id == x.opID {
			x.opID = 0
			x.prog.running = false
			x.res, _ = m.val.(*engine.ExportResult)
			x.err = m.err
			x.phase, x.scroll = exDone, 0
		}
	case tea.KeyMsg:
		k := m.String()
		switch x.phase {
		case exScope:
			return x.picker.update(m)
		case exOptions:
			n := len(x.opts) + 3
			switch k {
			case "up", "k":
				x.cur = (x.cur + n - 1) % n
			case "down", "j", "tab":
				x.cur = (x.cur + 1) % n
			case "left", "h", "right", "l", " ", "enter":
				d := 1
				if k == "left" || k == "h" {
					d = -1
				}
				switch {
				case x.cur == 0:
					x.policy = (x.policy + d + len(policies)) % len(policies)
				case x.cur == 1:
					x.parallel = min(64, max(1, x.parallel+d))
				case x.cur < 2+len(x.opts):
					if k == " " || k == "enter" {
						x.opts[x.cur-2].on = !x.opts[x.cur-2].on
					}
				case k == "enter":
					return x.start()
				}
			case "s":
				return x.start()
			}
		case exDone:
			switch k {
			case "up", "k":
				x.scroll = max(0, x.scroll-1)
			case "down", "j":
				x.scroll++
			case "r":
				return x.start()
			case "enter":
				return pop()
			}
		}
	}
	return nil
}

func (x *exportScreen) View(w, h int) string {
	e, t, g := x.e, x.e.th, x.e.g
	var out []string
	switch x.phase {
	case exScope:
		return x.picker.view(w)
	case exOptions:
		out = append(out, t.Muted.Render("Scope ")+t.Accent.Render(x.scopeLbl)+t.Muted.Render("   Session ")+t.Text.Render(e.session), "")
		pol := policies[x.policy]
		rows := []struct{ label, val, help string }{
			{"Role policy", g.left + " " + string(pol) + " " + g.right, policyHelp[pol]},
			{"Parallel requests", g.left + " " + itoa(x.parallel) + " " + g.right, "concurrent GetRoleCredentials calls"},
		}
		for i, r := range rows {
			if x.cur == i {
				out = append(out, t.Sel.Render(e.fit(g.cursor+"  "+padRight(r.label, 20)+padRight(r.val, 18)+r.help, w)))
			} else {
				out = append(out, "   "+t.Text.Render(padRight(r.label, 20))+t.Accent.Render(padRight(r.val, 18))+t.Muted.Render(r.help))
			}
		}
		tc := x.cur - 2
		if x.cur < 2 || x.cur >= 2+len(x.opts) {
			tc = -1
		}
		out = append(out, e.toggles(x.opts, tc, w)...)
		out = append(out, "")
		btn := " " + g.right + " Start export "
		if x.cur == 2+len(x.opts) {
			out = append(out, "  "+t.Sel.Render(btn))
		} else {
			out = append(out, "  "+t.Badge.Render(strings.TrimSpace(btn)))
		}
		out = append(out, "", t.Muted.Render("Failed profiles keep their previous credentials; unrelated sections are never touched."))
	case exRun:
		out = append(out, t.Muted.Render("Scope ")+t.Accent.Render(x.scopeLbl)+t.Muted.Render("   Policy ")+t.Text.Render(string(policies[x.policy]))+
			t.Muted.Render("   Parallel ")+t.Text.Render(itoa(x.parallel)), "")
		out = append(out, strings.Split(x.prog.view(e, w, h-2), "\n")...)
	case exDone:
		sl := x.summary(w)
		x.scroll = min(x.scroll, max(0, len(sl)-h))
		out = sl[x.scroll:]
	}
	return strings.Join(out, "\n")
}

func (x *exportScreen) summary(w int) []string {
	e, t, g := x.e, x.e.th, x.e.g
	var out []string
	if x.err != nil {
		out = append(out, e.errText(x.err))
	}
	r := x.res
	if r == nil {
		return out
	}
	head := t.Success.Render(g.ok + " Export finished")
	if x.err != nil {
		head = t.Warning.Render(g.warn + " Partial result")
	}
	out = append(out, head+"   "+e.counters(r.Succeeded, r.Failed), "")
	kv := func(k, v string) string {
		return "  " + t.Muted.Render(padRight(k, 14)) + t.Text.Render(e.trunc(v, w-18))
	}
	out = append(out, kv("written", itoa(len(r.Written))+" credential sections"))
	if len(r.Kept) > 0 {
		out = append(out, kv("kept", strings.Join(r.Kept, ", ")))
	}
	if r.Backup != "" {
		out = append(out, kv("backup", r.Backup))
	}
	if len(r.RoleChanges) > 0 {
		saved := "not saved"
		if r.RolesSaved {
			saved = "saved to ~/.aws/config"
		}
		out = append(out, kv("role changes", itoa(len(r.RoleChanges))+" ("+saved+")"))
		for _, p := range sortedKeys(r.RoleChanges) {
			out = append(out, "      "+t.Changed.Render("~ "+p+" "+g.right+" "+r.RoleChanges[p]))
		}
	}
	for _, wn := range r.Warnings {
		out = append(out, "  "+t.Warning.Render(g.warn+" "+wn))
	}
	out = append(out, "", e.label("Results"))
	for _, ln := range r.Lines {
		if ln.Err == "" {
			out = append(out, "  "+t.Success.Render(g.ok)+" "+e.fit(t.Text.Render(ln.Profile), 30)+" "+e.fit(t.Accent.Render(ln.Role), 22)+" "+e.expiry(ln.Expiration))
		} else {
			out = append(out, "  "+t.Danger.Render(g.fail)+" "+e.fit(t.Text.Render(ln.Profile), 30)+" "+e.fit(t.Danger.Render(errClassLabel(ln.ErrClass)), 22)+" "+t.Muted.Render(e.trunc(ln.Err, max(4, w-58))))
		}
	}
	return out
}
