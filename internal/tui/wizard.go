package tui

import (
	"context"
	"errors"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PiniPP/credorbit/internal/engine"
	"github.com/PiniPP/credorbit/internal/model"
)

const (
	wzSession = iota
	wzMode
	wzScope
	wzOptions
	wzRun
	wzReport
)

var wizardSteps = []string{"Session", "Mode", "Scope", "Options", "Run", "Report"}

type stepState struct {
	id, msg string
	state   int // 0 running, 1 ok, 2 failed
}

type wizard struct {
	e        *env
	step     int
	sessions []model.SSOSession
	cur      int
	compat   bool
	scope    engine.Scope
	scopeLbl string
	picker   *scopePicker
	opts     []toggle
	opID     int
	steps    []stepState
	prog     progressPanel
	report   *engine.Report
	err      error
	scroll   int
	started  time.Time
}

func newWizard(e *env) *wizard {
	w := &wizard{e: e, compat: e.svc.Mode() == "compat"}
	w.picker = newScopePicker(e, func(s engine.Scope, label string) tea.Cmd {
		w.scope, w.scopeLbl = s, label
		w.step = wzOptions
		w.cur = 0
		return nil
	})
	w.loadSessions()
	return w
}

func (w *wizard) loadSessions() {
	w.sessions, _ = w.e.svc.Sessions()
	for i, s := range w.sessions {
		if s.Name == w.e.session {
			w.cur = i
		}
	}
}

func (w *wizard) Init() tea.Cmd { return nil }
func (w *wizard) Title() string { return "Full Setup & Sync" }

func (w *wizard) KeyHints() []hint {
	g := w.e.g
	switch w.step {
	case wzOptions:
		return []hint{{g.up + g.down, "move"}, {"space", "toggle"}, {"enter", "toggle / start"}, {"esc", "back"}}
	case wzRun:
		return []hint{{"ctrl+c", "cancel run"}}
	case wzReport:
		return []hint{{g.up + g.down, "scroll"}, {"esc", "done"}}
	}
	return []hint{{g.up + g.down, "move"}, {"enter", "next"}, {"esc", "back"}}
}

func (w *wizard) back() (bool, tea.Cmd) {
	switch w.step {
	case wzSession, wzReport:
		return false, nil
	case wzRun:
		return true, toast("press ctrl+c to cancel the run")
	}
	w.step--
	w.cur = 0
	if w.step == wzMode && w.compat {
		w.cur = 1
	}
	return true, nil
}

func (w *wizard) defaultOptions() {
	rot := len(w.e.svc.Rotation().Accounts) > 0
	w.opts = []toggle{
		{label: "Export credentials", help: "write ~/.aws/credentials", on: true},
		{label: "Update roles in ~/.aws/config", help: "persist the role that worked (reviewed)", on: w.compat},
		{label: "Rotate Claude / Bedrock account", help: "pick the next eligible account", on: rot && w.compat},
		{label: "Remove stale profiles (reviewed)", help: "never on failed discovery", on: false},
		{label: "Dry run", help: "show what would change, write nothing", on: false},
	}
}

func (w *wizard) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case eventsMsg:
		if w.step == wzRun {
			for _, ev := range m {
				w.applyEvent(ev)
			}
		}
		return nil
	case opDoneMsg:
		if m.id != w.opID {
			return nil
		}
		w.opID = 0
		if m.err != nil && w.step != wzRun {
			return toast("failed: " + m.err.Error())
		}
		if w.step == wzRun {
			w.report, _ = m.val.(*engine.Report)
			w.err = m.err
			w.step = wzReport
			w.prog.running = false
		} else {
			w.loadSessions()
		}
		return nil
	case tea.KeyMsg:
		return w.key(m)
	}
	return nil
}

func (w *wizard) key(k tea.KeyMsg) tea.Cmd {
	e := w.e
	s := k.String()
	switch w.step {
	case wzSession:
		n := len(w.sessions) + 1
		switch s {
		case "up", "k":
			w.cur = (w.cur + n - 1) % n
		case "down", "j":
			w.cur = (w.cur + 1) % n
		case "enter":
			if w.cur == len(w.sessions) {
				return w.newSessionForm()
			}
			e.session = w.sessions[w.cur].Name
			w.step, w.cur = wzMode, 0
			if w.compat {
				w.cur = 1
			}
			return e.refreshLogin()
		}
	case wzMode:
		switch s {
		case "up", "k", "down", "j", "tab":
			w.cur = 1 - w.cur
		case "enter":
			w.compat = w.cur == 1
			w.step = wzScope
		}
	case wzScope:
		return w.picker.update(k)
	case wzOptions:
		if w.opts == nil {
			w.defaultOptions()
		}
		n := len(w.opts) + 1
		switch s {
		case "up", "k":
			w.cur = (w.cur + n - 1) % n
		case "down", "j", "tab":
			w.cur = (w.cur + 1) % n
		case " ", "enter":
			if w.cur < len(w.opts) {
				w.opts[w.cur].on = !w.opts[w.cur].on
				return nil
			}
			if s == "enter" {
				return w.start()
			}
		case "s":
			return w.start()
		}
	case wzReport:
		switch s {
		case "up", "k":
			w.scroll = max(0, w.scroll-1)
		case "down", "j":
			w.scroll++
		case "enter":
			return pop()
		}
	}
	return nil
}

func (w *wizard) newSessionForm() tea.Cmd {
	e := w.e
	f := []*field{newField("Name", "e.g. my-org", ""), newField("Start URL", "https://my-org.awsapps.com/start", ""), newField("Region", "us-east-1", "us-east-1")}
	return openDialog(newForm(e, "Configure new SSO session",
		"Adds an [sso-session] block to ~/.aws/config (you will be asked to approve the write).", f,
		func(v []string) (tea.Cmd, error) {
			if v[0] == "" || strings.ContainsAny(v[0], " []") {
				return nil, errors.New("name is required and may not contain spaces or brackets")
			}
			if !strings.HasPrefix(v[1], "https://") {
				return nil, errors.New("start URL must begin with https://")
			}
			if v[2] == "" {
				return nil, errors.New("region is required")
			}
			sess := model.SSOSession{Name: v[0], StartURL: v[1], Region: v[2]}
			id, cmd := e.run("Adding session", func(context.Context) (any, error) { return nil, e.svc.AddSession(sess) })
			w.opID = id
			return cmd, nil
		}))
}

func (w *wizard) start() tea.Cmd {
	e := w.e
	if w.opts == nil {
		w.defaultOptions()
	}
	o := engine.FullOptions{
		Session:     e.session,
		Compat:      w.compat,
		Scope:       w.scope,
		Export:      boolp(w.opts[0].on),
		UpdateRoles: boolp(w.opts[1].on),
		Rotate:      boolp(w.opts[2].on),
		// stale removal is opt-in here and still shown through ReviewPlan
		AllowRemove: w.opts[3].on,
		DryRun:      w.opts[4].on,
	}
	if w.compat {
		o.Policy = model.PolicyScript
		o.Scope.IncludeNew = o.Scope.Kind == engine.ScopeGroup && o.Scope.Group == "main"
	}
	w.step = wzRun
	w.steps = nil
	w.prog.reset("export")
	w.prog.running = true
	w.started = e.now()
	id, cmd := e.run("Full setup", func(ctx context.Context) (any, error) {
		return e.svc.RunFull(ctx, o)
	})
	w.opID = id
	e.live[e.session] = true
	return cmd
}

func (w *wizard) applyEvent(ev model.Event) {
	if ev.Step == "export" {
		w.prog.apply(ev)
	}
	switch ev.Kind {
	case model.EventStepStart:
		for i := range w.steps {
			if w.steps[i].id == ev.Step {
				w.steps[i].state, w.steps[i].msg = 0, ev.Message
				return
			}
		}
		w.steps = append(w.steps, stepState{id: ev.Step, msg: ev.Message})
	case model.EventStepDone:
		for i := range w.steps {
			if w.steps[i].id == ev.Step {
				w.steps[i].msg = ev.Message
				w.steps[i].state = 1
				if ev.Err != nil {
					w.steps[i].state = 2
				}
			}
		}
	}
}

// stepper renders "Session › Mode › Scope …" with the current step highlighted.
func (w *wizard) stepper() string {
	e, t := w.e, w.e.th
	var parts []string
	for i, s := range wizardSteps {
		label := itoa(i+1) + " " + s
		switch {
		case i == w.step:
			parts = append(parts, t.Accent.Bold(true).Render(label))
		case i < w.step:
			parts = append(parts, t.Success.Render(e.g.ok+" "+s))
		default:
			parts = append(parts, t.Muted.Render(label))
		}
	}
	return strings.Join(parts, t.Muted.Render("  "+e.g.right+"  "))
}

func (w *wizard) View(width, h int) string {
	e, t, g := w.e, w.e.th, w.e.g
	out := []string{w.stepper(), e.hrule(width), ""}
	switch w.step {
	case wzSession:
		out = append(out, t.Bold.Render("Choose the IAM Identity Center session"), "")
		for i, s := range w.sessions {
			line := padRight(s.Name, 22) + padRight(s.Region, 14) + s.StartURL
			if i == w.cur {
				out = append(out, t.Sel.Render(e.fit(" "+g.cursor+" "+line, width)))
			} else {
				out = append(out, "   "+t.Text.Render(padRight(s.Name, 22))+t.Muted.Render(padRight(s.Region, 14)+s.StartURL))
			}
		}
		nl := "+ Configure new session" + g.ellipsis
		if w.cur == len(w.sessions) {
			out = append(out, t.Sel.Render(e.fit(" "+g.cursor+" "+nl, width)))
		} else {
			out = append(out, "   "+t.Accent.Render(nl))
		}
	case wzMode:
		out = append(out, t.Bold.Render("How should CredOrbit behave?"), "")
		modes := [][2]string{
			{"Normal (recommended)", "Uses each profile's configured role, never persists role changes silently, and reviews every removal. Safest default."},
			{"Script-compatible", "Reproduces the legacy populate-aws-creds.sh: strongest assigned role first, role updates written back, newly discovered accounts included in Main exports."},
		}
		for i, m := range modes {
			if i == w.cur {
				out = append(out, t.Sel.Render(e.fit(" "+g.cursor+" "+m[0], width)))
			} else {
				out = append(out, "   "+t.Text.Render(m[0]))
			}
			for _, l := range wrap(m[1], width-6) {
				out = append(out, "     "+t.Muted.Render(l))
			}
			out = append(out, "")
		}
	case wzScope:
		out = append(out, w.picker.view(width))
	case wzOptions:
		if w.opts == nil {
			w.defaultOptions()
		}
		mode := "Normal"
		if w.compat {
			mode = "Script-compatible"
		}
		out = append(out, t.Muted.Render("Session ")+t.Text.Render(e.session)+t.Muted.Render("   Mode ")+t.Text.Render(mode)+
			t.Muted.Render("   Scope ")+t.Accent.Render(w.scopeLbl), "")
		out = append(out, e.toggles(w.opts, w.cur, width)...)
		out = append(out, "")
		btn := " " + g.right + " Start setup "
		if w.cur == len(w.opts) {
			out = append(out, "  "+t.Sel.Render(btn))
		} else {
			out = append(out, "  "+t.Badge.Render(strings.TrimSpace(btn)))
		}
	case wzRun:
		out = append(out, w.runView(width, h-len(out))...)
	case wzReport:
		rl := reportLines(e, w.report, w.err, width)
		w.scroll = min(w.scroll, max(0, len(rl)-(h-len(out))))
		out = append(out, rl[w.scroll:]...)
	}
	return strings.Join(out, "\n")
}

func (w *wizard) runView(width, h int) []string {
	e, t, g := w.e, w.e.th, w.e.g
	leftW := 44
	var left []string
	for _, s := range w.steps {
		var mark string
		switch s.state {
		case 0:
			mark = e.spin.View()
		case 1:
			mark = t.Success.Render(g.ok)
		default:
			mark = t.Danger.Render(g.fail)
		}
		left = append(left, mark+" "+e.fit(t.Text.Render(s.id), 18)+" "+t.Muted.Render(e.trunc(s.msg, leftW-21)))
	}
	if len(left) == 0 {
		left = append(left, e.spin.View()+" "+t.Muted.Render("starting"+g.ellipsis))
	}
	if len(left) > h {
		left = left[len(left)-h:]
	}
	elapsed := t.Muted.Render("elapsed " + humanDur(e.now().Sub(w.started)))
	if width < 90 {
		out := append([]string{}, left...)
		if w.prog.total > 0 {
			out = append(out, "")
			out = append(out, strings.Split(w.prog.view(e, width, 6), "\n")...)
		}
		return append(out, "", elapsed)
	}
	rightW := width - leftW - 3
	right := []string{t.Bold.Render("Credential export"), ""}
	if w.prog.total > 0 || w.prog.ok+w.prog.fail > 0 {
		right = append(right, strings.Split(w.prog.view(e, rightW, h-4), "\n")...)
	} else {
		right = append(right, t.Muted.Render("waiting for the export step"+g.ellipsis))
	}
	right = append(right, "", elapsed)
	var out []string
	for i := 0; i < max(len(left), len(right)) && i < h; i++ {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		out = append(out, e.fit(l, leftW)+" "+t.Muted.Render(g.border.Left)+" "+r)
	}
	return out
}

// reportLines renders a workflow report.
func reportLines(e *env, rep *engine.Report, err error, w int) []string {
	t, g := e.th, e.g
	var out []string
	switch {
	case err != nil && errors.Is(err, context.Canceled), err != nil && errors.Is(err, model.ErrCanceled):
		out = append(out, t.Warning.Render(g.warn+" Canceled")+t.Muted.Render(" "+g.sep+" partial report below; files are only replaced atomically"))
	case err != nil:
		out = append(out, t.Danger.Render(g.fail+" Setup failed: ")+t.Text.Render(err.Error()))
	default:
		out = append(out, t.Success.Render(g.ok+" Setup complete"))
	}
	if rep == nil {
		return out
	}
	out = append(out, t.Muted.Render("exported ")+t.Success.Render(itoa(rep.Exported))+
		t.Muted.Render("   failed ")+t.Danger.Render(itoa(rep.Failed))+
		t.Muted.Render("   duration "+rep.Duration.Round(100*time.Millisecond).String()), "")
	cols := 1
	if w >= 90 && len(rep.Steps) > 10 {
		cols = 2
	}
	cw := w/cols - 1
	var cells []string
	for _, s := range rep.Steps {
		mark := t.Success.Render(g.ok)
		switch {
		case s.Skipped:
			mark = t.Muted.Render(g.dot)
		case !s.OK:
			mark = t.Danger.Render(g.fail)
		}
		name := e.fit(s.Step, 18)
		if s.Skipped {
			name = t.Muted.Render(name)
		} else {
			name = t.Text.Render(name)
		}
		cells = append(cells, e.fit(" "+mark+" "+name+" "+t.Muted.Render(e.trunc(s.Summary, cw-23)), cw))
		if cols == 1 {
			for _, d := range s.Details {
				cells = append(cells, "     "+t.Muted.Render(e.trunc(d, w-6)))
			}
		}
	}
	if cols == 2 {
		half := (len(cells) + 1) / 2
		for i := 0; i < half; i++ {
			l := cells[i]
			if i+half < len(cells) {
				l += "  " + cells[i+half]
			}
			out = append(out, l)
		}
	} else {
		out = append(out, cells...)
	}
	if len(rep.Backups) > 0 {
		out = append(out, "", e.label("Backups"))
		for _, b := range rep.Backups {
			out = append(out, "  "+t.Text.Render(e.trunc(b, w-2)))
		}
	}
	if len(rep.Warnings) > 0 {
		out = append(out, "", e.label("Warnings"))
		for _, wn := range rep.Warnings {
			out = append(out, "  "+t.Warning.Render(g.warn+" ")+t.Text.Render(e.trunc(wn, w-4)))
		}
	}
	return out
}
