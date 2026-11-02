package tui

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PiniPP/credorbit/internal/engine"
	"github.com/PiniPP/credorbit/internal/model"
)

type eventItem = model.Event

// bridge connects the engine (running in background goroutines) to the Bubble
// Tea program. It implements model.Reporter (events are batched and flushed at
// ~20 fps) and engine.Interaction (each decision opens a dialog and blocks
// until the user answers or the context is canceled).
type bridge struct {
	send func(tea.Msg)
	env  *env

	mu     sync.Mutex
	buf    []model.Event
	closed bool
	nextID atomic.Uint64
}

func newBridge(send func(tea.Msg), e *env) *bridge { return &bridge{send: send, env: e} }

// Report implements model.Reporter. Safe for concurrent use.
func (b *bridge) Report(ev model.Event) {
	b.mu.Lock()
	if !b.closed {
		b.buf = append(b.buf, ev)
	}
	b.mu.Unlock()
}

// flush sends buffered events (if any) to the program.
func (b *bridge) flush() {
	b.mu.Lock()
	evs := b.buf
	b.buf = nil
	closed := b.closed
	b.mu.Unlock()
	if len(evs) > 0 && !closed {
		b.send(eventsMsg(evs))
	}
}

// loop flushes events every 50 ms until ctx is done.
func (b *bridge) loop(ctx context.Context) {
	t := time.NewTicker(50 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.flush()
		}
	}
}

func (b *bridge) close() {
	b.mu.Lock()
	b.closed = true
	b.buf = nil
	b.mu.Unlock()
}

func (b *bridge) isClosed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closed
}

type reply struct {
	v   any
	err error
}

// ask opens the dialog built by mk and waits for its answer.
func (b *bridge) ask(ctx context.Context, mk func(answer func(any, error)) dialog) (any, error) {
	if b.isClosed() {
		return nil, model.ErrCanceled
	}
	ch := make(chan reply, 1)
	id := b.nextID.Add(1)
	answer := func(v any, err error) {
		select {
		case ch <- reply{v, err}:
		default:
		}
	}
	b.send(openDialogMsg{id: id, mk: func() dialog { return mk(answer) }})
	select {
	case r := <-ch:
		return r.v, r.err
	case <-ctx.Done():
		b.send(closeDialogMsg{id: id})
		return nil, ctx.Err()
	}
}

func (b *bridge) Confirm(ctx context.Context, title, detail string, def bool) (bool, error) {
	v, err := b.ask(ctx, func(ans func(any, error)) dialog {
		return newConfirm(b.env, title, detail, def, func(ok bool) tea.Cmd { ans(ok, nil); return nil })
	})
	ok, _ := v.(bool)
	return ok, err
}

func (b *bridge) ReviewPlan(ctx context.Context, title, preview string) (bool, error) {
	v, err := b.ask(ctx, func(ans func(any, error)) dialog {
		return newPlanDialog(b.env, title, preview, func(ok bool) tea.Cmd { ans(ok, nil); return nil })
	})
	ok, _ := v.(bool)
	return ok, err
}

func (b *bridge) SelectAccounts(ctx context.Context, title string, items []engine.AccountRow, pre []string) ([]string, error) {
	v, err := b.ask(ctx, func(ans func(any, error)) dialog {
		return newPickerDialog(b.env, title, items, pre, func(ids []string, ok bool) tea.Cmd {
			if !ok {
				ans(nil, model.ErrCanceled)
			} else {
				ans(ids, nil)
			}
			return nil
		})
	})
	ids, _ := v.([]string)
	return ids, err
}

func (b *bridge) SelectRole(ctx context.Context, a model.DiscoveredAccount, current string) (string, error) {
	v, err := b.ask(ctx, func(ans func(any, error)) dialog {
		cur := 0
		for i, r := range a.Roles {
			if r == current {
				cur = i
			}
		}
		return newListDialog(b.env, "Choose role · "+a.Name, "Account "+a.ID+" "+b.env.g.sep+" current: "+b.env.orDash(current), a.Roles, nil, cur,
			func(i int, ok bool) tea.Cmd {
				if !ok {
					ans("", model.ErrCanceled)
				} else {
					ans(a.Roles[i], nil)
				}
				return nil
			})
	})
	s, _ := v.(string)
	return s, err
}

func (b *bridge) LoginPrompt(url, code string) {
	if b.isClosed() {
		return
	}
	b.send(openDialogMsg{id: b.nextID.Add(1), d: &loginDialog{e: b.env, url: url, code: code}})
}

func (e *env) orDash(s string) string {
	if s == "" {
		return e.g.expNone
	}
	return s
}

var (
	_ engine.Interaction = (*bridge)(nil)
	_ model.Reporter     = (*bridge)(nil)
)
