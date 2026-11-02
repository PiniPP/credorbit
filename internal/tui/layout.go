package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// width returns the display width of s (ANSI aware).
func width(s string) int { return ansi.StringWidth(s) }

// fit truncates s (ANSI aware) to w cells with the ellipsis and pads it to exactly w.
func (e *env) fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if width(s) > w {
		s = ansi.Truncate(s, w, e.g.ellipsis)
	}
	if p := w - width(s); p > 0 {
		s += strings.Repeat(" ", p)
	}
	return s
}

// trunc truncates plain text to w cells.
func (e *env) trunc(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if width(s) > w {
		return ansi.Truncate(s, w, e.g.ellipsis)
	}
	return s
}

func padRight(s string, w int) string {
	if p := w - width(s); p > 0 {
		return s + strings.Repeat(" ", p)
	}
	return s
}

// spread places left and right on one line of width w.
func (e *env) spread(left, right string, w int) string {
	gap := w - width(left) - width(right)
	if gap < 1 {
		return e.fit(left, w-width(right)-1) + " " + right
	}
	return left + strings.Repeat(" ", gap) + right
}

// box draws a bordered panel of exactly w×h with a title (and optional badge)
// embedded in the top border. body lines are truncated/padded to the interior.
func (e *env) box(title, badge, body string, w, h int, border lipgloss.Style) string {
	if w < 4 || h < 2 {
		return ""
	}
	b := e.g.border
	inner := w - 2
	var top strings.Builder
	top.WriteString(border.Render(b.TopLeft + b.Top))
	used := 1
	if title != "" {
		t := " " + e.trunc(title, inner-6) + " "
		top.WriteString(e.th.Title.Render(t))
		used += width(t)
	}
	if badge != "" && used+width(badge)+4 <= inner {
		fill := inner - used - width(badge) - 3
		top.WriteString(border.Render(strings.Repeat(b.Top, fill)) + " " + badge + " " + border.Render(b.Top))
	} else {
		top.WriteString(border.Render(strings.Repeat(b.Top, max(0, inner-used))))
	}
	top.WriteString(border.Render(b.TopRight))

	out := make([]string, 0, h)
	out = append(out, top.String())
	bl := strings.Split(body, "\n")
	for i := 0; i < h-2; i++ {
		l := ""
		if i < len(bl) {
			l = bl[i]
		}
		out = append(out, border.Render(b.Left)+" "+e.fit(l, inner-2)+" "+border.Render(b.Right))
	}
	out = append(out, border.Render(b.BottomLeft+strings.Repeat(b.Bottom, inner)+b.BottomRight))
	return strings.Join(out, "\n")
}

// overlay centres fg over bg (both w×h frames).
func overlay(bg, fg string, w, h int) string {
	bl := strings.Split(bg, "\n")
	for len(bl) < h {
		bl = append(bl, "")
	}
	fl := strings.Split(fg, "\n")
	fw := 0
	for _, l := range fl {
		fw = max(fw, width(l))
	}
	x := max(0, (w-fw)/2)
	y := max(0, (h-len(fl))/2)
	for i, l := range fl {
		row := y + i
		if row >= len(bl) {
			break
		}
		b := bl[row]
		left := ansi.Truncate(b, x, "")
		if p := x - width(left); p > 0 {
			left += strings.Repeat(" ", p)
		}
		right := ""
		if width(b) > x+fw {
			right = ansi.TruncateLeft(b, x+fw, "")
		}
		bl[row] = left + sgrReset + padRight(l, fw) + sgrReset + right
	}
	return strings.Join(bl, "\n")
}

// --- header & footer ---

func (e *env) pill() (string, string) {
	if !e.loginKnown {
		return e.th.PillOff.Render(e.g.pillOff + " Checking…"), e.th.PillOff.Render(e.g.pillOff)
	}
	if !e.login.LoggedIn {
		return e.th.PillOff.Render(e.g.pillOff + " Signed out"), e.th.PillOff.Render(e.g.pillOff + " off")
	}
	left := e.login.ExpiresAt.Sub(e.now())
	if !e.login.ExpiresAt.IsZero() && left < 30*time.Minute {
		return e.th.PillWarn.Render(e.g.pillSoon + " Expiring in " + humanDur(left)), e.th.PillWarn.Render(e.g.pillSoon + " " + humanDur(left))
	}
	return e.th.PillOK.Render(e.g.pillOn + " Connected"), e.th.PillOK.Render(e.g.pillOn + " on")
}

func (e *env) mark() string {
	m := e.g.mini
	if e.g.ascii {
		return e.th.Primary.Render("(") + e.th.Accent.Render("@") + e.th.Primary.Render(")")
	}
	r := []rune(m)
	return e.th.Primary.Render(string(r[0])) + e.th.Accent.Render(string(r[1])) + e.th.Primary.Render(string(r[2]))
}

func (e *env) modeBadge() string {
	m := strings.ToUpper(e.svc.Mode())
	if m == "" {
		m = "NORMAL"
	}
	return e.th.Badge.Render(m)
}

// header renders 2 lines (wide) or 1 line (narrow < 70 cols).
func (e *env) header(w int) []string {
	long, short := e.pill()
	sess := e.th.Muted.Render(e.session)
	if w < 70 {
		left := " " + e.mark() + " " + e.th.Header.Render("CREDORBIT")
		right := short + " " + e.modeBadge() + " "
		if w >= 56 {
			right = long + "  " + sess + " " + e.modeBadge() + " "
		}
		return []string{e.spread(left, right, w)}
	}
	left := " " + e.mark() + "  " + e.th.Header.Render(wordmark)
	right := long + "  " + e.th.Muted.Render(e.g.sep) + "  " + sess + "  " + e.modeBadge() + " "
	if w-width(left)-width(right) > len(tagline)+6 {
		left += "   " + e.th.Muted.Render(tagline)
	}
	rule := e.th.Border.Render(strings.Repeat(e.g.rule, w))
	return []string{e.spread(left, right, w), rule}
}

func (e *env) renderHints(hs []hint) []string {
	out := make([]string, 0, len(hs))
	for _, h := range hs {
		out = append(out, e.th.Key.Render(h.key)+" "+e.th.Muted.Render(h.desc))
	}
	return out
}

func (e *env) footer(w int, hs []hint) string {
	right := ""
	if e.canceling {
		right = e.th.Warning.Render(e.spin.View() + " Canceling…")
	} else if a := e.activity(); a != "" {
		right = e.spin.View() + " " + e.th.Accent.Render(a)
	} else if e.toast != "" && e.now().Sub(e.toastAt) < 6*time.Second {
		right = e.th.Success.Render(e.toast)
	}
	tail := strings.Join(e.renderHints([]hint{{"?", "help"}, {"q", "quit"}}), "  ")
	avail := w - width(right) - width(tail) - 6
	var b strings.Builder
	b.WriteString(" ")
	for _, p := range e.renderHints(hs) {
		if width(b.String())+width(p)+2 > avail {
			break
		}
		b.WriteString(p + "  ")
	}
	b.WriteString(tail)
	return e.spread(b.String(), right+" ", w)
}

// rule draws a muted horizontal separator.
func (e *env) hrule(w int) string { return e.th.Muted.Render(strings.Repeat(e.g.rule, max(0, w))) }

// section renders a small uppercase section label.
func (e *env) label(s string) string { return e.th.Primary.Bold(true).Render(strings.ToUpper(s)) }
