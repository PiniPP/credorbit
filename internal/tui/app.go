package tui

import (
	"strings"

	"github.com/PiniPP/credorbit/internal/engine"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type dialogEntry struct {
	id uint64
	d  dialog
}

// app is the root model: a screen stack, modal dialogs on top, help overlay.
type app struct {
	e       *env
	stack   []screen
	dialogs []dialogEntry
	help    bool
	nextDlg uint64
}

func newApp(e *env, initial, group string) *app {
	a := &app{e: e, stack: []screen{newMenu(e)}}
	if s := initialScreen(e, initial, group); s != nil {
		a.stack = append(a.stack, s)
	}
	return a
}

func initialScreen(e *env, name, group string) screen {
	switch name {
	case "groups":
		if group != "" {
			return newGroupEditor(e, group)
		}
		return newGroupsScreen(e)
	case "main":
		return newGroupEditor(e, "main")
	case "accounts":
		return newBrowserScreen(e)
	case "rotate":
		return newRotateScreen(e)
	case "setup":
		return newWizard(e)
	}
	return nil
}

func (a *app) top() screen { return a.stack[len(a.stack)-1] }

func (a *app) Init() tea.Cmd {
	cmds := []tea.Cmd{a.e.spin.Tick, a.e.refreshLogin()}
	for _, s := range a.stack {
		cmds = append(cmds, s.Init())
	}
	return tea.Batch(cmds...)
}

func (a *app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		a.e.w, a.e.h = m.Width, m.Height
		return a, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		a.e.spin, cmd = a.e.spin.Update(m)
		return a, cmd
	case tea.KeyMsg:
		return a, a.key(m)
	case pushMsg:
		a.stack = append(a.stack, m.s)
		return a, m.s.Init()
	case popMsg:
		a.e.invalidate()
		if len(a.stack) > 1 {
			a.stack = a.stack[:len(a.stack)-1]
			return a, a.broadcast(resumeMsg{})
		}
		return a, nil
	case replaceMsg:
		a.stack[len(a.stack)-1] = m.s
		return a, m.s.Init()
	case openDialogMsg:
		d := m.d
		if m.mk != nil {
			d = m.mk()
		}
		id := m.id
		if id == 0 {
			a.nextDlg++
			id = 1<<62 + a.nextDlg
		}
		a.dialogs = append(a.dialogs, dialogEntry{id: id, d: d})
		return a, nil
	case closeDialogMsg:
		a.removeDialog(m.id)
		return a, nil
	case loginStateMsg:
		a.e.login, a.e.loginKnown = engine.LoginState(m), true
		return a, nil
	case toastMsg:
		a.e.toast, a.e.toastAt = string(m), a.e.now()
		return a, nil
	case themeMsg:
		a.e.applyTheme()
		return a, a.e.spin.Tick
	case opDoneMsg:
		a.e.finish(m.id)
		a.e.invalidate()
		// the SSO login prompt lives exactly as long as the operation
		kept := a.dialogs[:0]
		for _, d := range a.dialogs {
			if _, ok := d.d.(*loginDialog); !ok {
				kept = append(kept, d)
			}
		}
		a.dialogs = kept
		return a, tea.Batch(a.broadcast(m), a.e.refreshLogin())
	}
	return a, a.broadcast(msg)
}

// resumeMsg tells screens they are visible again (after a pop).
type resumeMsg struct{}

func (a *app) broadcast(msg tea.Msg) tea.Cmd {
	var cmds []tea.Cmd
	for _, s := range a.stack {
		cmds = append(cmds, s.Update(msg))
	}
	return tea.Batch(cmds...)
}

func (a *app) removeDialog(id uint64) {
	for i, d := range a.dialogs {
		if d.id == id {
			a.dialogs = append(a.dialogs[:i], a.dialogs[i+1:]...)
			return
		}
	}
}

func (a *app) key(k tea.KeyMsg) tea.Cmd {
	e := a.e
	if k.String() == "ctrl+c" {
		if e.busy() {
			if e.canceling {
				return tea.Quit
			}
			e.cancelAll()
			return nil
		}
		return tea.Quit
	}
	if a.help {
		a.help = false
		return nil
	}
	if n := len(a.dialogs); n > 0 {
		top := a.dialogs[n-1]
		done, cmd := top.d.Update(k)
		if done {
			a.removeDialog(top.id)
		}
		return cmd
	}
	s := a.top()
	if c, ok := s.(capturer); ok && c.capturing() {
		return s.Update(k)
	}
	switch k.String() {
	case "q":
		if e.busy() {
			return openDialog(&confirmDialog{e: e, title: "Quit CredOrbit?", danger: true,
				detail: "An operation is still running. Quitting cancels it; files are only ever replaced atomically, so nothing is left half-written.",
				onDone: func(ok bool) tea.Cmd {
					if ok {
						e.cancelAll()
						return tea.Quit
					}
					return nil
				}})
		}
		return tea.Quit
	case "?":
		a.help = true
		return nil
	case "esc":
		if b, ok := s.(backer); ok {
			if handled, cmd := b.back(); handled {
				return cmd
			}
		}
		if len(a.stack) > 1 {
			return pop()
		}
		return nil
	}
	return s.Update(k)
}

func (a *app) View() string {
	e := a.e
	w, h := e.w, e.h
	if w == 0 || h == 0 {
		return ""
	}
	var frame string
	if w < 50 || h < 15 {
		frame = a.tooSmall(w, h)
	} else {
		frame = a.frame(w, h)
	}
	if e.g.ascii {
		frame = asciiFold(frame)
	}
	return paint(frame, w, e.th.bgSeq)
}

func (a *app) tooSmall(w, h int) string {
	e := a.e
	msg := []string{
		e.mark() + " " + e.th.Header.Render("CREDORBIT"),
		"",
		e.th.Warning.Render("Terminal too small"),
		e.th.Muted.Render(itoa(w) + "x" + itoa(h) + " (need 50x15)"),
	}
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, strings.Join(msg, "\n"), lipgloss.WithWhitespaceChars(" "))
}

func (a *app) frame(w, h int) string {
	e := a.e
	s := a.top()
	head := e.header(w)
	foot := e.footer(w, s.KeyHints())
	bodyH := h - len(head) - 1
	badge := ""
	if b, ok := s.(badger); ok {
		badge = b.badge()
	}
	title := s.Title()
	if len(a.stack) > 1 {
		title = e.breadcrumb(a.stack)
	}
	content := s.View(w-4, bodyH-2)
	panel := e.box(title, badge, content, w, bodyH, e.th.Border)
	frame := strings.Join(append(append(head, panel), foot), "\n")
	for _, d := range a.dialogs {
		frame = overlay(frame, d.d.View(w-6, h-4), w, h)
	}
	if a.help {
		frame = overlay(frame, a.helpView(w, h), w, h)
	}
	return frame
}

func (e *env) breadcrumb(stack []screen) string {
	var parts []string
	for _, s := range stack[1:] {
		parts = append(parts, s.Title())
	}
	return strings.Join(parts, " "+e.g.right+" ")
}

func (a *app) helpView(w, h int) string {
	e := a.e
	s := a.top()
	var b []string
	b = append(b, e.label(s.Title()))
	for _, hk := range s.KeyHints() {
		b = append(b, e.fit(e.th.Key.Render(hk.key), 14)+e.th.Text.Render(hk.desc))
	}
	b = append(b, "", e.label("Everywhere"))
	for _, hk := range []hint{
		{e.g.up + e.g.down + " j k", "move"}, {"enter", "open / confirm"}, {"esc", "back / cancel"},
		{"?", "toggle this help"}, {"q", "quit (asks first while an operation runs)"},
		{"ctrl+c", "cancel the running operation (twice: force quit)"},
	} {
		b = append(b, e.fit(e.th.Key.Render(hk.key), 14)+e.th.Text.Render(hk.desc))
	}
	return e.dialogBox("Keys", strings.Join(b, "\n"), e.th.Muted.Render("press any key to close"), min(w-6, 72), h-4)
}
