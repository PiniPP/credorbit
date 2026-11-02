package tui

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/muesli/termenv"

	"github.com/PiniPP/credorbit/internal/engine"
)

// hint is one key binding shown in the footer and the help overlay.
type hint struct{ key, desc string }

// screen is one entry of the navigation stack.
type screen interface {
	Init() tea.Cmd
	Update(msg tea.Msg) tea.Cmd
	View(w, h int) string
	Title() string
	KeyHints() []hint
}

// backer screens handle Esc themselves (e.g. step back in a wizard).
type backer interface {
	back() (handled bool, cmd tea.Cmd)
}

// capturer screens consume every key while a text field is focused.
type capturer interface{ capturing() bool }

// badger screens show a badge in the panel's top border.
type badger interface{ badge() string }

// navigation & lifecycle messages
type (
	pushMsg       struct{ s screen }
	popMsg        struct{}
	replaceMsg    struct{ s screen }
	openDialogMsg struct {
		id uint64
		d  dialog
		mk func() dialog // built on the UI goroutine when set
	}
	closeDialogMsg struct{ id uint64 }
	opDoneMsg      struct {
		id  int
		val any
		err error
	}
	eventsMsg     []eventItem
	loginStateMsg engine.LoginState
	themeMsg      struct{}
	toastMsg      string
)

func push(s screen) tea.Cmd    { return func() tea.Msg { return pushMsg{s} } }
func pop() tea.Cmd             { return func() tea.Msg { return popMsg{} } }
func replace(s screen) tea.Cmd { return func() tea.Msg { return replaceMsg{s} } }
func openDialog(d dialog) tea.Cmd {
	return func() tea.Msg { return openDialogMsg{d: d} }
}
func toast(s string) tea.Cmd { return func() tea.Msg { return toastMsg(s) } }

// env is the state shared by every screen: service, theme, session, ops.
type env struct {
	svc     engine.Service
	ctx     context.Context
	th      *theme
	g       glyphs
	out     io.Writer
	profile *termenv.Profile
	look    lookupFunc

	w, h       int
	session    string
	login      engine.LoginState
	loginKnown bool
	live       map[string]bool

	cancels   map[int]context.CancelFunc
	labels    map[int]string
	nextOp    int
	canceling bool

	flush func() // delivers pending progress events before an op result

	memo map[string]memoEntry

	spin    spinner.Model
	now     func() time.Time
	toast   string
	toastAt time.Time
}

func newEnv(ctx context.Context, svc engine.Service, out io.Writer, profile *termenv.Profile, look lookupFunc) *env {
	e := &env{svc: svc, ctx: ctx, out: out, profile: profile, look: look,
		live: map[string]bool{}, cancels: map[int]context.CancelFunc{}, labels: map[int]string{}, now: time.Now}
	e.session = svc.DefaultSession()
	if e.session == "" {
		if ss, err := svc.Sessions(); err == nil && len(ss) > 0 {
			e.session = ss[0].Name
		}
	}
	e.applyTheme()
	return e
}

// applyTheme (re)reads ui.theme / ui.ascii and the environment.
func (e *env) applyTheme() {
	var st, sa string
	for _, s := range e.svc.Settings() {
		switch s.Key {
		case "ui.theme":
			st = s.Value
		case "ui.ascii":
			sa = s.Value
		}
	}
	name, ascii := detectDisplay(st, sa, e.look)
	r := newRenderer(e.out, name == "mono", e.profile)
	e.th = newTheme(name, r)
	e.g = newGlyphs(ascii)
	e.spin = spinner.New(spinner.WithSpinner(spinner.Spinner{Frames: e.g.spin, FPS: time.Second / 8}))
	e.spin.Style = e.th.Accent
}

// run starts f in the background under a cancelable context. The result
// arrives as opDoneMsg with the returned id.
func (e *env) run(label string, f func(ctx context.Context) (any, error)) (int, tea.Cmd) {
	e.nextOp++
	id := e.nextOp
	ctx, cancel := context.WithCancel(e.ctx)
	e.cancels[id] = cancel
	e.labels[id] = label
	return id, func() (msg tea.Msg) {
		defer func() {
			if r := recover(); r != nil {
				msg = opDoneMsg{id: id, err: fmt.Errorf("internal error: %v", r)}
			}
		}()
		v, err := f(ctx)
		if e.flush != nil {
			e.flush()
		}
		return opDoneMsg{id: id, val: v, err: err}
	}
}

type memoEntry struct {
	at time.Time
	v  any
}

// cached memoises cheap-but-not-free service reads used while rendering
// (views re-render at spinner frame rate). Invalidated after every operation.
func cached[T any](e *env, key string, f func() T) T {
	if e.memo == nil {
		e.memo = map[string]memoEntry{}
	}
	if m, ok := e.memo[key]; ok && e.now().Sub(m.at) < 2*time.Second {
		if v, ok := m.v.(T); ok {
			return v
		}
	}
	v := f()
	e.memo[key] = memoEntry{at: e.now(), v: v}
	return v
}

func (e *env) invalidate() { e.memo = nil }

func (e *env) busy() bool { return len(e.cancels) > 0 }

func (e *env) cancelAll() {
	for _, c := range e.cancels {
		c()
	}
	if len(e.cancels) > 0 {
		e.canceling = true
	}
}

func (e *env) finish(id int) {
	if c, ok := e.cancels[id]; ok {
		c()
		delete(e.cancels, id)
		delete(e.labels, id)
	}
	if len(e.cancels) == 0 {
		e.canceling = false
	}
}

func (e *env) activity() string {
	if len(e.labels) == 0 {
		return ""
	}
	ids := make([]int, 0, len(e.labels))
	for id := range e.labels {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	return e.labels[ids[len(ids)-1]]
}

func (e *env) refreshLogin() tea.Cmd {
	svc, ctx, s := e.svc, e.ctx, e.session
	return func() tea.Msg { return loginStateMsg(svc.LoginStatus(ctx, s)) }
}

// --- formatting helpers ---

func humanDur(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		if int(d.Minutes())%60 == 0 {
			return fmt.Sprintf("%dh", int(d.Hours()))
		}
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func ago(now, t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := now.Sub(t)
	if d < time.Minute {
		return "just now"
	}
	return humanDur(d) + " ago"
}

// expiry renders a credential expiry indicator (plain text + style).
func (e *env) expiry(t time.Time) string {
	if t.IsZero() {
		return e.th.Muted.Render(e.g.expNone)
	}
	d := t.Sub(e.now())
	switch {
	case d <= 0:
		return e.th.Danger.Render(e.g.expGone + " expired")
	case d < 30*time.Minute:
		return e.th.Warning.Render(e.g.expSoon + " " + humanDur(d))
	}
	return e.th.Success.Render(e.g.expOK + " " + humanDur(d))
}

func (e *env) statusMark(status string) string {
	switch status {
	case "ok":
		return e.th.Success.Render(e.g.ok)
	case "warn":
		return e.th.Warning.Render(e.g.warn)
	case "fail":
		return e.th.Danger.Render(e.g.fail)
	}
	return e.th.Muted.Render(e.g.dot)
}

func (e *env) errText(err error) string {
	if err == nil {
		return ""
	}
	return e.th.Danger.Render(e.g.fail + " " + err.Error())
}

func boolp(b bool) *bool { return &b }

func onOff(g glyphs, b bool) string {
	if b {
		return g.on
	}
	return g.off
}

func sortedKeys(m map[string]string) []string {
	k := make([]string, 0, len(m))
	for x := range m {
		k = append(k, x)
	}
	sort.Strings(k)
	return k
}

func lines(s string) []string { return strings.Split(strings.TrimRight(s, "\n"), "\n") }
