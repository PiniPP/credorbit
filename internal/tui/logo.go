package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/PiniPP/credorbit/internal/brand"
)

// Brand assets come from internal/brand so the terminal mark, wordmark and
// tagline stay identical to the SVG logo set in assets/brand.
var (
	wordmark = brand.Wordmark()
	tagline  = brand.Tagline
)

// brandMark returns the orbit-ring + identity-core mark (brand.Mark) with every
// line padded to the same display width so it can sit in a layout column.
func brandMark(unicode bool) []string {
	lines := brand.Mark(unicode)
	w := 0
	for _, l := range lines {
		w = max(w, ansi.StringWidth(l))
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l + strings.Repeat(" ", w-ansi.StringWidth(l))
	}
	return out
}

// colorMark styles the mark by brand role: ring in Orbit Blue (ring), the
// identity core bold cyan (core), orbital nodes and halo in cyan (node).
// lines must come from brandMark with the same unicode setting.
func colorMark(lines []string, ring, core, node func(...string) string) []string {
	unicode := false
	for _, l := range lines {
		if strings.ContainsAny(l, "╭╮╰╯─│●◦") {
			unicode = true
			break
		}
	}
	segs := brand.MarkSegments(unicode)
	out := make([]string, len(lines))
	for i := range lines {
		var b strings.Builder
		width := 0
		if i < len(segs) {
			for _, s := range segs[i] {
				width += ansi.StringWidth(s.Text)
				switch s.Role {
				case brand.RoleRing:
					b.WriteString(ring(s.Text))
				case brand.RoleCore:
					b.WriteString(core(s.Text))
				case brand.RoleNode, brand.RoleHalo:
					b.WriteString(node(s.Text))
				default:
					b.WriteString(s.Text)
				}
			}
		}
		if pad := ansi.StringWidth(lines[i]) - width; pad > 0 {
			b.WriteString(strings.Repeat(" ", pad))
		}
		out[i] = b.String()
	}
	return out
}
