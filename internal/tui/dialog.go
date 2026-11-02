package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/PiniPP/credorbit/internal/engine"
)

// dialog is a modal rendered on top of the current screen. It receives keys
// until it reports done.
type dialog interface {
	Update(k tea.KeyMsg) (done bool, cmd tea.Cmd)
	View(maxW, maxH int) string
}

// dialogBox frames body as a dialog of width w (height from content).
func (e *env) dialogBox(title, body, hints string, w, maxH int) string {
	bl := strings.Split(body, "\n")
	if hints != "" {
		bl = append(bl, "", hints)
	}
	h := min(len(bl)+2, maxH)
	return paint(e.box(title, "", strings.Join(bl, "\n"), w, h, e.th.Accent), w, e.th.surfaceSeq)
}

func (e *env) keyLine(hs ...hint) string { return strings.Join(e.renderHints(hs), "   ") }

// wrap breaks plain text into lines of at most w cells (word boundaries).
func wrap(s string, w int) []string {
	if w < 8 {
		w = 8
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		cur := ""
		for _, wd := range words {
			if cur == "" {
				cur = wd
			} else if width(cur)+1+width(wd) <= w {
				cur += " " + wd
			} else {
				out = append(out, cur)
				cur = wd
			}
		}
		out = append(out, cur)
	}
	return out
}

// --- confirm ---

type confirmDialog struct {
	e             *env
	title, detail string
	yes, danger   bool
	onDone        func(bool) tea.Cmd
}

func newConfirm(e *env, title, detail string, def bool, onDone func(bool) tea.Cmd) *confirmDialog {
	return &confirmDialog{e: e, title: title, detail: detail, yes: def, onDone: onDone}
}

func (d *confirmDialog) Update(k tea.KeyMsg) (bool, tea.Cmd) {
	switch k.String() {
	case "left", "right", "tab", "shift+tab", "h", "l":
		d.yes = !d.yes
	case "y", "Y":
		return true, d.onDone(true)
	case "n", "N", "esc":
		return true, d.onDone(false)
	case "enter":
		return true, d.onDone(d.yes)
	}
	return false, nil
}

func (d *confirmDialog) View(maxW, maxH int) string {
	e := d.e
	w := min(maxW, 64)
	var b []string
	for _, l := range wrap(d.detail, w-4) {
		b = append(b, e.th.Text.Render(l))
	}
	btn := func(label string, on bool, st lipgloss.Style) string {
		if on {
			return st.Render(" " + label + " ")
		}
		return e.th.Muted.Render("[" + label + "]")
	}
	yesSt := e.th.Sel
	if d.danger && !e.th.mono {
		yesSt = yesSt.Background(lipgloss.Color(e.th.p.danger))
	}
	b = append(b, "", btn("Yes", d.yes, yesSt)+"  "+btn("No", !d.yes, e.th.Sel))
	return e.dialogBox(d.title, strings.Join(b, "\n"), e.keyLine(hint{"y/n", "answer"}, hint{e.g.left + e.g.right, "switch"}, hint{"enter", "confirm"}), w, maxH)
}

// --- plan review ---

type planDialog struct {
	e      *env
	title  string
	vp     viewport.Model
	text   string
	onDone func(bool) tea.Cmd
}

func newPlanDialog(e *env, title, preview string, onDone func(bool) tea.Cmd) *planDialog {
	d := &planDialog{e: e, title: title, text: preview, onDone: onDone, vp: viewport.New(60, 10)}
	d.vp.SetContent(e.colorDiff(preview))
	return d
}

// colorDiff colours a unified-diff-like preview: + green, - red, ~ amber.
func (e *env) colorDiff(s string) string {
	ls := lines(s)
	for i, l := range ls {
		t := strings.TrimLeft(l, " ")
		switch {
		case strings.HasPrefix(t, "+"):
			ls[i] = e.th.Added.Render(l)
		case strings.HasPrefix(t, "-"):
			ls[i] = e.th.Removed.Render(l)
		case strings.HasPrefix(t, "~"):
			ls[i] = e.th.Changed.Render(l)
		default:
			ls[i] = e.th.Muted.Render(l)
		}
	}
	return strings.Join(ls, "\n")
}

func diffCounts(s string) (add, rem, chg int) {
	for _, l := range lines(s) {
		t := strings.TrimLeft(l, " ")
		switch {
		case strings.HasPrefix(t, "+"):
			add++
		case strings.HasPrefix(t, "-"):
			rem++
		case strings.HasPrefix(t, "~"):
			chg++
		}
	}
	return
}

func (d *planDialog) Update(k tea.KeyMsg) (bool, tea.Cmd) {
	switch k.String() {
	case "a", "y", "enter":
		return true, d.onDone(true)
	case "n", "esc", "c":
		return true, d.onDone(false)
	}
	d.vp, _ = d.vp.Update(k)
	return false, nil
}

func (d *planDialog) View(maxW, maxH int) string {
	e := d.e
	w := min(maxW, 96)
	d.vp.Width = w - 4
	d.vp.Height = max(3, min(len(lines(d.text)), maxH-8))
	d.vp.SetContent(e.colorDiff(d.text))
	a, r, c := diffCounts(d.text)
	sum := e.th.Added.Render(e.g.plus+itoa(a)+" add") + "  " + e.th.Removed.Render(e.g.minus+itoa(r)+" remove") + "  " +
		e.th.Changed.Render("~"+itoa(c)+" change")
	scroll := ""
	if d.vp.TotalLineCount() > d.vp.Height {
		scroll = e.th.Muted.Render("  " + itoa(int(d.vp.ScrollPercent()*100)) + "%")
	}
	body := sum + scroll + "\n" + e.hrule(w-4) + "\n" + d.vp.View()
	return e.dialogBox(d.title, body, e.keyLine(hint{"a", "apply"}, hint{"esc", "cancel"}, hint{e.g.up + e.g.down, "scroll"}), w, maxH)
}

// --- single choice list ---

type listDialog struct {
	e             *env
	title, detail string
	items, descs  []string
	cur           int
	onDone        func(int, bool) tea.Cmd
}

func newListDialog(e *env, title, detail string, items, descs []string, cur int, onDone func(int, bool) tea.Cmd) *listDialog {
	return &listDialog{e: e, title: title, detail: detail, items: items, descs: descs, cur: cur, onDone: onDone}
}

func (d *listDialog) Update(k tea.KeyMsg) (bool, tea.Cmd) {
	switch k.String() {
	case "up", "k":
		if d.cur > 0 {
			d.cur--
		}
	case "down", "j":
		if d.cur < len(d.items)-1 {
			d.cur++
		}
	case "enter":
		if len(d.items) == 0 {
			return true, d.onDone(-1, false)
		}
		return true, d.onDone(d.cur, true)
	case "esc", "q":
		return true, d.onDone(-1, false)
	}
	return false, nil
}

func (d *listDialog) View(maxW, maxH int) string {
	e := d.e
	w := min(maxW, 72)
	var b []string
	if d.detail != "" {
		for _, l := range wrap(d.detail, w-4) {
			b = append(b, e.th.Muted.Render(l))
		}
		b = append(b, "")
	}
	if len(d.items) == 0 {
		b = append(b, e.th.Muted.Render("(nothing to choose)"))
	}
	for i, it := range d.items {
		line := "  " + it
		if i < len(d.descs) && d.descs[i] != "" {
			line = padRight(line, 26) + "  " + d.descs[i]
		}
		if i == d.cur {
			b = append(b, e.th.Sel.Render(e.fit(e.g.cursor+line[1:], w-4)))
		} else {
			b = append(b, e.th.Text.Render(line))
		}
	}
	return e.dialogBox(d.title, strings.Join(b, "\n"), e.keyLine(hint{e.g.up + e.g.down, "move"}, hint{"enter", "choose"}, hint{"esc", "cancel"}), w, maxH)
}

// --- form ---

type formDialog struct {
	e      *env
	title  string
	intro  string
	fields []*field
	focus  int
	err    string
	submit func(vals []string) (error, tea.Cmd)
	cancel func() tea.Cmd
}

func newForm(e *env, title, intro string, fields []*field, submit func([]string) (error, tea.Cmd)) *formDialog {
	return &formDialog{e: e, title: title, intro: intro, fields: fields, submit: submit}
}

func (d *formDialog) Update(k tea.KeyMsg) (bool, tea.Cmd) {
	switch k.String() {
	case "esc":
		if d.cancel != nil {
			return true, d.cancel()
		}
		return true, nil
	case "tab", "down":
		d.focus = (d.focus + 1) % len(d.fields)
		return false, nil
	case "shift+tab", "up":
		d.focus = (d.focus + len(d.fields) - 1) % len(d.fields)
		return false, nil
	case "enter":
		if d.focus < len(d.fields)-1 {
			d.focus++
			return false, nil
		}
		vals := make([]string, len(d.fields))
		for i, f := range d.fields {
			vals[i] = f.Value()
		}
		err, cmd := d.submit(vals)
		if err != nil {
			d.err = err.Error()
			return false, cmd
		}
		return true, cmd
	}
	if d.fields[d.focus].update(k) {
		d.err = ""
	}
	return false, nil
}

func (d *formDialog) View(maxW, maxH int) string {
	e := d.e
	w := min(maxW, 70)
	var b []string
	if d.intro != "" {
		for _, l := range wrap(d.intro, w-4) {
			b = append(b, e.th.Muted.Render(l))
		}
		b = append(b, "")
	}
	lw := 0
	for _, f := range d.fields {
		lw = max(lw, width(f.label))
	}
	for i, f := range d.fields {
		lab := padRight(f.label, lw)
		if i == d.focus {
			lab = e.th.Accent.Render(lab)
		} else {
			lab = e.th.Muted.Render(lab)
		}
		b = append(b, lab+"  "+f.view(e, w-4-lw-2, i == d.focus))
	}
	if d.err != "" {
		b = append(b, "")
		for _, l := range wrap(d.err, w-6) {
			b = append(b, e.th.Danger.Render(e.g.fail+" "+l))
		}
	}
	return e.dialogBox(d.title, strings.Join(b, "\n"), e.keyLine(hint{"tab", "next field"}, hint{"enter", "submit"}, hint{"esc", "cancel"}), w, maxH)
}

// --- SSO login prompt (non-blocking) ---

type loginDialog struct {
	e         *env
	url, code string
}

func (d *loginDialog) Update(k tea.KeyMsg) (bool, tea.Cmd) {
	if k.String() == "esc" {
		d.e.cancelAll()
		return true, nil
	}
	return false, nil
}

func (d *loginDialog) View(maxW, maxH int) string {
	e := d.e
	w := min(maxW, 76)
	code := e.th.Badge.Render(d.code)
	b := []string{
		e.th.Text.Render("Complete the sign-in in your browser."),
		"",
		e.th.Muted.Render("Verification URL"),
		e.th.Accent.Render(e.trunc(d.url, w-6)),
		"",
		e.th.Muted.Render("User code  ") + code,
		"",
		e.spin.View() + " " + e.th.Muted.Render("Waiting for authorization"+e.g.ellipsis),
	}
	return e.dialogBox("AWS SSO Login", strings.Join(b, "\n"), e.keyLine(hint{"esc", "cancel login"}), w, maxH)
}

// --- account picker ---

type pickerDialog struct {
	e      *env
	title  string
	b      *browser
	onDone func([]string, bool) tea.Cmd
}

func newPickerDialog(e *env, title string, rows []engine.AccountRow, pre []string, onDone func([]string, bool) tea.Cmd) *pickerDialog {
	b := newBrowser(e, modePick)
	b.setRows(rows)
	b.setSelection(pre)
	return &pickerDialog{e: e, title: title, b: b, onDone: onDone}
}

func (d *pickerDialog) Update(k tea.KeyMsg) (bool, tea.Cmd) {
	if !d.b.searching {
		switch k.String() {
		case "enter":
			return true, d.onDone(d.b.selected(), true)
		case "esc":
			return true, d.onDone(nil, false)
		}
	}
	_, cmd := d.b.update(k)
	return false, cmd
}

func (d *pickerDialog) View(maxW, maxH int) string {
	e := d.e
	w := maxW
	h := maxH - 4
	body := d.b.view(w-4, h-2)
	return e.dialogBox(d.title, body, e.keyLine(hint{"space", "toggle"}, hint{"a/n/i", "all/none/invert"}, hint{"/", "search"}, hint{"enter", "confirm"}, hint{"esc", "cancel"}), w, maxH)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
