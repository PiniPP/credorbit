package tui

import (
	"errors"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PiniPP/credorbit/internal/engine"
	"github.com/PiniPP/credorbit/internal/model"
)

// --- progress bar & panel ---

// bar renders a blue→cyan gradient progress bar of w cells.
func (e *env) bar(w int, frac float64) string {
	if w < 4 {
		return ""
	}
	if frac < 0 {
		frac = 0
	} else if frac > 1 {
		frac = 1
	}
	filled := int(frac*float64(w) + 0.5)
	var b strings.Builder
	if e.th.mono {
		b.WriteString(strings.Repeat(e.g.barFull, filled))
		b.WriteString(e.th.Muted.Render(strings.Repeat(e.g.barEmpty, w-filled)))
		return b.String()
	}
	for i := 0; i < filled; i++ {
		b.WriteString(e.th.blend(float64(i) / float64(max(1, w-1))).Render(e.g.barFull))
	}
	b.WriteString(e.th.Muted.Render(strings.Repeat(e.g.barEmpty, w-filled)))
	return b.String()
}

type progressPanel struct {
	step        string // only events of this step count ("" = all)
	done, total int
	ok, fail    int
	recent      []model.Event
	message     string
	running     bool
}

func (p *progressPanel) reset(step string) { *p = progressPanel{step: step, running: true} }

func (p *progressPanel) apply(ev model.Event) {
	if p.step != "" && ev.Step != p.step {
		return
	}
	switch ev.Kind {
	case model.EventStepStart:
		if ev.Total > 0 {
			p.total = ev.Total
		}
		p.message = ev.Message
	case model.EventProgress:
		p.done, p.total = ev.Done, ev.Total
	case model.EventItemOK:
		p.ok++
		p.push(ev)
	case model.EventItemFailed:
		p.fail++
		p.push(ev)
	case model.EventStepDone:
		p.message = ev.Message
	case model.EventInfo, model.EventWarning:
		p.message = ev.Message
	}
}

func (p *progressPanel) push(ev model.Event) {
	p.recent = append(p.recent, ev)
	if len(p.recent) > 200 {
		p.recent = p.recent[len(p.recent)-200:]
	}
}

func (e *env) counters(ok, fail int) string {
	return e.th.Success.Render(e.g.ok+" "+itoa(ok)) + "  " + e.th.Danger.Render(e.g.fail+" "+itoa(fail))
}

// view renders bar, counters and the most recent item results.
func (p *progressPanel) view(e *env, w, h int) string {
	t := e.th
	frac := 0.0
	if p.total > 0 {
		frac = float64(p.done) / float64(p.total)
	}
	pct := itoa(int(frac*100+0.5)) + "%"
	count := itoa(p.done) + "/" + itoa(p.total)
	bw := max(10, w-width(pct)-width(count)-4)
	head := e.spin.View() + " "
	if !p.running {
		head = t.Success.Render(e.g.ok) + " "
	}
	if e.canceling {
		head = t.Warning.Render(e.spin.View()+" Canceling"+e.g.ellipsis) + " "
	}
	out := []string{
		e.spread(head+t.Text.Render(e.trunc(p.message, w-30)), e.counters(p.ok, p.fail), w),
		e.bar(bw, frac) + "  " + t.Accent.Render(pct) + "  " + t.Muted.Render(count),
		"",
	}
	n := h - len(out)
	start := max(0, len(p.recent)-n)
	for _, ev := range p.recent[start:] {
		mark := t.Success.Render(e.g.ok)
		msg := t.Muted.Render(ev.Message)
		if ev.Kind == model.EventItemFailed {
			mark = t.Danger.Render(e.g.fail)
			cls := ""
			if ev.Err != nil {
				cls = classify(ev.Err)
			}
			msg = t.Danger.Render(cls) + t.Muted.Render(" "+ev.Message)
		}
		out = append(out, mark+" "+e.fit(t.Text.Render(ev.Subject), min(34, w/3))+" "+msg)
	}
	return strings.Join(out, "\n")
}

// classify maps an error to a short label (no_access, token expired, …).
func classify(err error) string {
	switch {
	case errors.Is(err, model.ErrNoAccess):
		return "no access"
	case errors.Is(err, model.ErrUnauthorized):
		return "token expired"
	case errors.Is(err, model.ErrTransient):
		return "transient"
	case errors.Is(err, model.ErrCanceled):
		return "canceled"
	}
	s := err.Error()
	switch s {
	case "no_access":
		return "no access"
	case "unauthorized":
		return "token expired"
	case "transient", "canceled":
		return s
	}
	if len(s) > 24 {
		return "error"
	}
	return s
}

func errClassLabel(c string) string {
	switch c {
	case "no_access":
		return "no access"
	case "unauthorized":
		return "token expired"
	case "":
		return "error"
	}
	return c
}

// --- scope picker ---

type scopePicker struct {
	e      *env
	cur    int
	onPick func(engine.Scope, string) tea.Cmd
}

var scopeTitles = []string{"All AWS Accounts", "Main Accounts", "Custom Group", "Select Accounts Manually", "Single Account"}

func newScopePicker(e *env, onPick func(engine.Scope, string) tea.Cmd) *scopePicker {
	return &scopePicker{e: e, cur: 1, onPick: onPick}
}

func (s *scopePicker) descs() []string {
	return cached(s.e, "scope-descs:"+s.e.session, s.computeDescs)
}

func (s *scopePicker) computeDescs() []string {
	e := s.e
	rows := e.svc.Accounts(e.session)
	nProf := 0
	for _, r := range rows {
		if len(r.Profiles) > 0 {
			nProf++
		}
	}
	mainN := 0
	if g, err := e.svc.Group("main"); err == nil {
		mainN = len(g.Members)
	}
	return []string{
		itoa(nProf) + " configured profiles in " + e.orDash(e.session),
		itoa(mainN) + " accounts in the Main group (recommended)",
		"members of a saved group: " + strings.Join(groupNames(e), ", "),
		"pick accounts for this run only (groups are not changed)",
		"exactly one profile",
	}
}

func groupNames(e *env) []string {
	var out []string
	for _, g := range e.svc.Groups() {
		out = append(out, g.Name)
	}
	return out
}

func (s *scopePicker) update(k tea.KeyMsg) tea.Cmd {
	e := s.e
	switch k.String() {
	case "up", "k":
		s.cur = (s.cur + len(scopeTitles) - 1) % len(scopeTitles)
	case "down", "j":
		s.cur = (s.cur + 1) % len(scopeTitles)
	case "enter":
		switch s.cur {
		case 0:
			return s.onPick(engine.Scope{Kind: engine.ScopeAll}, "All AWS accounts")
		case 1:
			return s.onPick(engine.Scope{Kind: engine.ScopeGroup, Group: "main"}, "Main Accounts")
		case 2:
			gs := e.svc.Groups()
			names, descs := make([]string, len(gs)), make([]string, len(gs))
			for i, g := range gs {
				names[i] = g.Name
				descs[i] = itoa(len(g.Members)) + " accounts"
			}
			return openDialog(newListDialog(e, "Choose a group", "", names, descs, 0, func(i int, ok bool) tea.Cmd {
				if !ok {
					return nil
				}
				return s.onPick(engine.Scope{Kind: engine.ScopeGroup, Group: names[i]}, "Group "+names[i])
			}))
		case 3:
			return push(newPickerScreen(e, modePick, "Select Accounts", nil, func(rows []engine.AccountRow) tea.Cmd {
				sc := engine.Scope{Kind: engine.ScopeSelected}
				for _, r := range rows {
					sc.AccountIDs = append(sc.AccountIDs, r.Account.ID)
					sc.Profiles = append(sc.Profiles, r.Profiles...)
				}
				return s.onPick(sc, itoa(len(rows))+" selected accounts")
			}))
		case 4:
			return push(newPickerScreen(e, modeSingle, "Single Account", nil, func(rows []engine.AccountRow) tea.Cmd {
				if len(rows) == 0 {
					return nil
				}
				r := rows[0]
				sc := engine.Scope{Kind: engine.ScopeSingle, AccountIDs: []string{r.Account.ID}}
				if len(r.Profiles) > 0 {
					sc.Profiles = []string{r.Profiles[0]}
				}
				return s.onPick(sc, r.Account.Name)
			}))
		}
	}
	return nil
}

func (s *scopePicker) view(w int) string {
	e, t, g := s.e, s.e.th, s.e.g
	ds := s.descs()
	var out []string
	out = append(out, t.Bold.Render("Choose Account Scope"), t.Muted.Render("A scope is a temporary selection; it never changes saved groups."), "")
	for i, title := range scopeTitles {
		if i == s.cur {
			out = append(out, t.Sel.Render(e.fit(" "+g.cursor+" "+padRight(title, 28)+ds[i], w)))
		} else {
			out = append(out, "   "+t.Text.Render(padRight(title, 28))+t.Muted.Render(e.trunc(ds[i], max(4, w-31))))
		}
	}
	return strings.Join(out, "\n")
}

// --- toggles list ---

type toggle struct {
	label, help string
	on          bool
}

func (e *env) toggles(ts []toggle, cur, w int) []string {
	t := e.th
	var out []string
	for i, x := range ts {
		box := onOff(e.g, x.on)
		line := " " + box + "  " + padRight(x.label, 34) + x.help
		if i == cur {
			out = append(out, t.Sel.Render(e.fit(e.g.cursor+line[1:], w)))
			continue
		}
		bs := t.Muted.Render(box)
		if x.on {
			bs = t.Accent.Render(box)
		}
		out = append(out, "  "+bs+"  "+t.Text.Render(padRight(x.label, 34))+t.Muted.Render(e.trunc(x.help, max(4, w-40))))
	}
	return out
}

// --- account picker screen (manual / single scope) ---

type pickerScreen struct {
	e      *env
	title  string
	b      *browser
	onDone func([]engine.AccountRow) tea.Cmd
}

func newPickerScreen(e *env, mode browseMode, title string, pre []string, onDone func([]engine.AccountRow) tea.Cmd) *pickerScreen {
	b := newBrowser(e, mode)
	b.setRows(e.svc.Accounts(e.session))
	b.setSelection(pre)
	return &pickerScreen{e: e, title: title, b: b, onDone: onDone}
}

func (p *pickerScreen) Init() tea.Cmd   { return nil }
func (p *pickerScreen) Title() string   { return p.title }
func (p *pickerScreen) capturing() bool { return p.b.searching }
func (p *pickerScreen) KeyHints() []hint {
	if p.b.mode == modeSingle {
		return []hint{{"enter", "pick"}, {"/", "search"}, {"e/s/g", "filter"}, {"esc", "cancel"}}
	}
	return []hint{{"space", "toggle"}, {"a/n/i", "all/none/invert"}, {"/", "search"}, {"e/s/g", "filter"}, {"enter", "confirm"}}
}

func (p *pickerScreen) Update(msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	if k.String() == "enter" && !p.b.searching {
		var rows []engine.AccountRow
		if p.b.mode == modeSingle {
			if c := p.b.current(); c != nil {
				rows = append(rows, *c)
			}
		} else {
			ids := map[string]bool{}
			for _, id := range p.b.selected() {
				ids[id] = true
			}
			for _, r := range p.b.rows {
				if ids[r.Account.ID] {
					rows = append(rows, r)
				}
			}
		}
		if len(rows) == 0 {
			return toast("select at least one account")
		}
		return tea.Sequence(pop(), p.onDone(rows))
	}
	_, cmd := p.b.update(k)
	return cmd
}

func (p *pickerScreen) View(w, h int) string { return p.b.view(w, h) }
