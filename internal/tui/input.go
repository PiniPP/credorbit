package tui

import (
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
)

// field is a minimal single-line text input (bubbles/textinput needs a
// clipboard dependency we do not ship).
type field struct {
	label, placeholder string
	val                []rune
	pos                int
}

func newField(label, placeholder, value string) *field {
	f := &field{label: label, placeholder: placeholder, val: []rune(value)}
	f.pos = len(f.val)
	return f
}

func (f *field) Value() string { return strings.TrimSpace(string(f.val)) }

func (f *field) SetValue(s string) { f.val = []rune(s); f.pos = len(f.val) }

// update applies an editing key; it reports whether the key was consumed.
func (f *field) update(k tea.KeyMsg) bool {
	switch k.Type {
	case tea.KeyRunes, tea.KeySpace:
		r := k.Runes
		if k.Type == tea.KeySpace {
			r = []rune{' '}
		}
		ins := make([]rune, 0, len(r))
		for _, c := range r {
			if unicode.IsPrint(c) {
				ins = append(ins, c)
			}
		}
		f.val = append(f.val[:f.pos], append(ins, f.val[f.pos:]...)...)
		f.pos += len(ins)
	case tea.KeyBackspace:
		if f.pos > 0 {
			f.val = append(f.val[:f.pos-1], f.val[f.pos:]...)
			f.pos--
		}
	case tea.KeyDelete:
		if f.pos < len(f.val) {
			f.val = append(f.val[:f.pos], f.val[f.pos+1:]...)
		}
	case tea.KeyLeft:
		if f.pos > 0 {
			f.pos--
		}
	case tea.KeyRight:
		if f.pos < len(f.val) {
			f.pos++
		}
	case tea.KeyHome, tea.KeyCtrlA:
		f.pos = 0
	case tea.KeyEnd, tea.KeyCtrlE:
		f.pos = len(f.val)
	case tea.KeyCtrlU:
		f.val, f.pos = nil, 0
	case tea.KeyCtrlW:
		i := f.pos
		for i > 0 && f.val[i-1] == ' ' {
			i--
		}
		for i > 0 && f.val[i-1] != ' ' {
			i--
		}
		f.val = append(f.val[:i], f.val[f.pos:]...)
		f.pos = i
	default:
		return false
	}
	return true
}

// view renders the input of width w; the cursor is shown when focused.
func (f *field) view(e *env, w int, focused bool) string {
	if w < 4 {
		w = 4
	}
	v := f.val
	start := 0
	if f.pos >= w-1 {
		start = f.pos - (w - 2)
	}
	if start > len(v) {
		start = len(v)
	}
	vis := v[start:]
	if len(vis) > w-1 {
		vis = vis[:w-1]
	}
	if len(v) == 0 && !focused {
		return e.fit(e.th.Muted.Render(f.placeholder), w)
	}
	if !focused {
		return e.fit(e.th.Text.Render(string(vis)), w)
	}
	c := f.pos - start
	var b strings.Builder
	if len(v) == 0 && f.placeholder != "" {
		b.WriteString(e.th.Sel.Render(" "))
		b.WriteString(e.th.Muted.Render(e.trunc(f.placeholder, w-2)))
		return e.fit(b.String(), w)
	}
	b.WriteString(e.th.Text.Render(string(vis[:min(c, len(vis))])))
	cur := " "
	if c < len(vis) {
		cur = string(vis[c])
	}
	b.WriteString(e.th.Sel.Render(cur))
	if c+1 < len(vis) {
		b.WriteString(e.th.Text.Render(string(vis[c+1:])))
	}
	return e.fit(b.String(), w)
}
