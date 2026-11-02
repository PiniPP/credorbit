package tui

import (
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	colorful "github.com/lucasb-eyer/go-colorful"
	"github.com/muesli/termenv"
)

// palette is a set of brand colors. Empty strings mean "no color" (mono).
type palette struct {
	bg, surface, primary, accent, success, text, muted, warning, danger string
}

var (
	paletteDefault = palette{
		bg: "#0B1020", surface: "#111C31", primary: "#2563EB", accent: "#22D3EE",
		success: "#34D399", text: "#F8FAFC", muted: "#94A3B8", warning: "#FBBF24", danger: "#F87171",
	}
	paletteLight = palette{
		bg: "#F8FAFC", surface: "#E2E8F0", primary: "#1D4ED8", accent: "#0891B2",
		success: "#047857", text: "#0B1020", muted: "#475569", warning: "#B45309", danger: "#B91C1C",
	}
)

// theme bundles every lipgloss style the TUI uses.
type theme struct {
	name string
	mono bool
	r    *lipgloss.Renderer
	p    palette

	Text, Muted, Accent, Primary, Success, Warning, Danger lipgloss.Style
	Bold, Title, Key, Border, Sel, SelMuted, Header, Badge lipgloss.Style
	PillOK, PillWarn, PillOff                              lipgloss.Style
	Added, Removed, Changed                                lipgloss.Style

	bgSeq, surfaceSeq string
}

// newTheme builds a theme on renderer r. Unknown names fall back to default.
func newTheme(name string, r *lipgloss.Renderer) *theme {
	t := &theme{name: name, r: r}
	switch name {
	case "light":
		t.p = paletteLight
	case "mono":
		t.mono = true
	default:
		t.name = "default"
		t.p = paletteDefault
	}
	s := func() lipgloss.Style { return r.NewStyle() }
	if t.mono {
		t.Text = s()
		t.Muted = s().Faint(true)
		t.Accent = s().Bold(true)
		t.Primary = s()
		t.Success = s()
		t.Warning = s().Bold(true)
		t.Danger = s().Bold(true).Underline(true)
		t.Bold = s().Bold(true)
		t.Title = s().Bold(true)
		t.Key = s().Bold(true)
		t.Border = s()
		t.Sel = s().Reverse(true).Bold(true)
		t.SelMuted = s().Reverse(true)
		t.Header = s().Bold(true)
		t.Badge = s().Reverse(true).Padding(0, 1)
		t.PillOK = s().Bold(true)
		t.PillWarn = s().Bold(true).Underline(true)
		t.PillOff = s()
		t.Added = s().Bold(true)
		t.Removed = s().Underline(true)
		t.Changed = s()
		return t
	}
	c := func(h string) lipgloss.Color { return lipgloss.Color(h) }
	p := t.p
	t.Text = s().Foreground(c(p.text))
	t.Muted = s().Foreground(c(p.muted))
	t.Accent = s().Foreground(c(p.accent))
	t.Primary = s().Foreground(c(p.primary))
	t.Success = s().Foreground(c(p.success))
	t.Warning = s().Foreground(c(p.warning))
	t.Danger = s().Foreground(c(p.danger))
	t.Bold = s().Foreground(c(p.text)).Bold(true)
	t.Title = s().Foreground(c(p.accent)).Bold(true)
	t.Key = s().Foreground(c(p.accent)).Bold(true)
	t.Border = s().Foreground(c(p.primary))
	selFG := "#F8FAFC"
	t.Sel = s().Background(c(p.primary)).Foreground(c(selFG)).Bold(true)
	t.SelMuted = s().Background(c(p.primary)).Foreground(c("#BFDBFE"))
	t.Header = s().Foreground(c(p.text)).Bold(true)
	t.Badge = s().Background(c(p.primary)).Foreground(c(selFG)).Bold(true).Padding(0, 1)
	t.PillOK = s().Foreground(c(p.success)).Bold(true)
	t.PillWarn = s().Foreground(c(p.warning)).Bold(true)
	t.PillOff = s().Foreground(c(p.muted))
	t.Added = s().Foreground(c(p.success))
	t.Removed = s().Foreground(c(p.danger))
	t.Changed = s().Foreground(c(p.warning))
	prof := r.ColorProfile()
	if prof != termenv.Ascii {
		t.bgSeq = prof.Color(p.bg).Sequence(true)
		t.surfaceSeq = prof.Color(p.surface).Sequence(true)
	}
	return t
}

// blend returns the color at fraction f between the primary and accent colors.
func (t *theme) blend(f float64) lipgloss.Style {
	if t.mono {
		return t.Text
	}
	a, err1 := colorful.Hex(t.p.primary)
	b, err2 := colorful.Hex(t.p.accent)
	if err1 != nil || err2 != nil {
		return t.Accent
	}
	if f < 0 {
		f = 0
	} else if f > 1 {
		f = 1
	}
	return t.r.NewStyle().Foreground(lipgloss.Color(a.BlendLab(b, f).Clamped().Hex()))
}

const sgrReset = "\x1b[0m"

// paint fills every line of s to width w with the background sequence seq,
// re-applying it after every SGR reset so nested styles do not punch holes.
func paint(s string, w int, seq string) string {
	if seq == "" {
		return s
	}
	pre := "\x1b[" + seq + "m"
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		l = strings.ReplaceAll(l, sgrReset, sgrReset+pre)
		if pad := w - ansi.StringWidth(l); pad > 0 {
			l += strings.Repeat(" ", pad)
		}
		lines[i] = pre + l + sgrReset
	}
	return strings.Join(lines, "\n")
}

// glyphs are the symbols used by the UI, in Unicode or 7-bit ASCII.
type glyphs struct {
	ascii                                bool
	on, off, cursor, ok, fail, warn, dot string
	stAvail, stNew, stMissing, stUnknown string
	expOK, expSoon, expGone, expNone     string
	pillOn, pillSoon, pillOff            string
	sep, ellipsis, up, down, left, right string
	barFull, barEmpty, rule, plus, minus string
	spin                                 []string
	border                               lipgloss.Border
	mini                                 string
}

func newGlyphs(ascii bool) glyphs {
	if ascii {
		return glyphs{ascii: true,
			on: "[x]", off: "[ ]", cursor: ">", ok: "+", fail: "x", warn: "!", dot: "*",
			stAvail: "o", stNew: "+", stMissing: "x", stUnknown: "?",
			expOK: "*", expSoon: "~", expGone: "x", expNone: "-",
			pillOn: "*", pillSoon: "~", pillOff: "o",
			sep: "|", ellipsis: "...", up: "^", down: "v", left: "<", right: ">",
			barFull: "#", barEmpty: ".", rule: "-", plus: "+", minus: "-",
			spin:   []string{"|", "/", "-", "\\"},
			border: lipgloss.ASCIIBorder(),
			mini:   "(@)",
		}
	}
	return glyphs{
		on: "☑", off: "☐", cursor: "▸", ok: "✓", fail: "✗", warn: "!", dot: "•",
		stAvail: "●", stNew: "✦", stMissing: "✗", stUnknown: "?",
		expOK: "●", expSoon: "◐", expGone: "○", expNone: "—",
		pillOn: "●", pillSoon: "◐", pillOff: "○",
		sep: "·", ellipsis: "…", up: "↑", down: "↓", left: "←", right: "→",
		barFull: "█", barEmpty: "░", rule: "─", plus: "+", minus: "−",
		spin:   []string{"◜", "◝", "◞", "◟"},
		border: lipgloss.RoundedBorder(),
		mini:   "◜●◞",
	}
}

// lookupFunc reads an environment variable (injectable for tests).
type lookupFunc func(string) (string, bool)

// detectDisplay resolves theme name and ASCII mode from settings and environment.
// NO_COLOR (any value) forces mono; CREDORBIT_ASCII=1 or a non-UTF-8 locale
// forces ASCII (Windows Terminal is assumed UTF-8 capable).
func detectDisplay(settingTheme, settingASCII string, look lookupFunc) (string, bool) {
	if look == nil {
		look = os.LookupEnv
	}
	name := settingTheme
	if name == "" {
		name = "default"
	}
	if _, ok := look("NO_COLOR"); ok {
		name = "mono"
	}
	ascii := settingASCII == "true"
	if v, _ := look("CREDORBIT_ASCII"); v == "1" || strings.EqualFold(v, "true") {
		ascii = true
	}
	if !ascii && !utf8Locale(look) {
		ascii = true
	}
	return name, ascii
}

func utf8Locale(look lookupFunc) bool {
	if v, ok := look("WT_SESSION"); ok && v != "" {
		return true
	}
	for _, k := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if v, ok := look(k); ok && v != "" {
			l := strings.ToLower(v)
			return strings.Contains(l, "utf-8") || strings.Contains(l, "utf8")
		}
	}
	// No locale information: modern macOS/Windows consoles are UTF-8.
	return runtime.GOOS == "darwin" || runtime.GOOS == "windows"
}

// newRenderer returns a renderer for w; profile overrides detection when forced.
func newRenderer(w io.Writer, mono bool, force *termenv.Profile) *lipgloss.Renderer {
	r := lipgloss.NewRenderer(w)
	if force != nil {
		r.SetColorProfile(*force)
	}
	if mono {
		// ANSI (not Ascii) keeps bold/underline/reverse; mono styles set no colours.
		r.SetColorProfile(termenv.ANSI)
	}
	return r
}
