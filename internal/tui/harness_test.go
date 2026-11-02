package tui

import (
	"context"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/muesli/termenv"

	"github.com/PiniPP/credorbit/internal/engine"
	"github.com/PiniPP/credorbit/internal/tui/demo"
)

// recSvc wraps the demo service and records mutating calls.
type recSvc struct {
	*demo.Service
	mu        sync.Mutex
	groupSets map[string][]string
	roles     map[string]string
	settings  map[string]string
}

func newRec() *recSvc {
	d := demo.New()
	d.SetSpeed(0)
	return &recSvc{Service: d, groupSets: map[string][]string{}, roles: map[string]string{}, settings: map[string]string{}}
}

func (r *recSvc) GroupSet(name string, ids []string) error {
	r.mu.Lock()
	r.groupSets[name] = append([]string(nil), ids...)
	r.mu.Unlock()
	return r.Service.GroupSet(name, ids)
}

func (r *recSvc) SetPreferredRole(id, role string) error {
	r.mu.Lock()
	r.roles[id] = role
	r.mu.Unlock()
	return r.Service.SetPreferredRole(id, role)
}

func (r *recSvc) SetSetting(k, v string) error {
	err := r.Service.SetSetting(k, v)
	if err == nil {
		r.mu.Lock()
		r.settings[k] = v
		r.mu.Unlock()
	}
	return err
}

// harness is a minimal Bubble Tea event loop for driving the app in tests.
type harness struct {
	t    *testing.T
	a    *app
	e    *env
	br   *bridge
	msgs chan tea.Msg
	quit bool
	auto bool // auto-answer dialogs with enter
}

func newTestEnv(svc engine.Service, look lookupFunc) *env {
	prof := termenv.TrueColor
	if look == nil {
		look = staticLook
	}
	e := newEnv(context.Background(), svc, io.Discard, &prof, look)
	e.w, e.h = 100, 32
	return e
}

func newHarness(t *testing.T, svc engine.Service, screen string) *harness {
	t.Helper()
	h := &harness{t: t, msgs: make(chan tea.Msg, 4096)}
	h.e = newTestEnv(svc, nil)
	h.a = newApp(h.e, screen, "")
	h.br = newBridge(func(m tea.Msg) { h.msgs <- m }, h.e)
	h.e.flush = h.br.flush
	svc.SetReporter(h.br)
	svc.SetInteraction(h.br)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); h.br.close() })
	go h.br.loop(ctx)
	h.exec(h.a.Init())
	h.settle()
	return h
}

var cmdType = reflect.TypeOf(tea.Cmd(nil))

func (h *harness) exec(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() {
		if m := cmd(); m != nil {
			h.msgs <- m
		}
	}()
}

func (h *harness) update(msg tea.Msg) {
	switch m := msg.(type) {
	case spinner.TickMsg:
		return
	case tea.QuitMsg:
		h.quit = true
		return
	case tea.BatchMsg:
		for _, c := range m {
			h.exec(c)
		}
		return
	}
	if v := reflect.ValueOf(msg); v.Kind() == reflect.Slice && v.Type().Elem() == cmdType {
		cmds := make([]tea.Cmd, v.Len())
		for i := range cmds {
			cmds[i], _ = v.Index(i).Interface().(tea.Cmd)
		}
		go func() {
			for _, c := range cmds {
				if c != nil {
					if m := c(); m != nil {
						h.msgs <- m
					}
				}
			}
		}()
		return
	}
	_, cmd := h.a.Update(msg)
	h.exec(cmd)
	if h.auto && len(h.a.dialogs) > 0 {
		if _, ok := h.a.dialogs[len(h.a.dialogs)-1].d.(*loginDialog); !ok {
			h.update(key("enter"))
		}
	}
}

// settle processes messages until the queue is quiet for 40ms.
func (h *harness) settle() {
	for {
		select {
		case m := <-h.msgs:
			h.update(m)
		case <-time.After(40 * time.Millisecond):
			return
		}
	}
}

// until processes messages until cond holds or the timeout expires.
func (h *harness) until(cond func() bool, d time.Duration) bool {
	deadline := time.After(d)
	for !cond() {
		select {
		case m := <-h.msgs:
			h.update(m)
		case <-time.After(20 * time.Millisecond):
		case <-deadline:
			return cond()
		}
	}
	return true
}

func (h *harness) press(keys ...string) {
	for _, k := range keys {
		h.update(key(k))
		h.settle()
	}
}

func (h *harness) typeText(s string) {
	for _, r := range s {
		h.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	h.settle()
}

func (h *harness) view() string { return ansiStrip(h.a.View()) }

func key(k string) tea.KeyMsg {
	switch k {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "space", " ":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	case "ctrl+s":
		return tea.KeyMsg{Type: tea.KeyCtrlS}
	case "ctrl+u":
		return tea.KeyMsg{Type: tea.KeyCtrlU}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}
