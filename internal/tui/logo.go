package tui

import "strings"

// Brand assets. Kept isolated so internal/brand can replace them later.

const (
	wordmark = "C R E D O R B I T"
	tagline  = "YOUR CLOUD ACCESS. IN ORBIT."
)

// brandMark returns the orbit-ring + identity-core mark: a tilted ring open on
// the right like a "C", a core at its centre and small nodes riding the ring.
// Every line has the same display width (≤ 22 columns, ≤ 7 lines).
func brandMark(unicode bool) []string {
	if unicode {
		return []string{
			"     ╭───────◦──╮   ",
			"  ╭──╯          ╰─  ",
			" ╭╯                 ",
			" │       ●        ◦ ",
			" ╰╮                 ",
			"  ╰──╮          ╭─  ",
			"     ╰──◦───────╯   ",
		}
	}
	return []string{
		"     .-------o--.   ",
		"  .--'          '-  ",
		" .'                 ",
		" |       @        o ",
		" '.                 ",
		"  '--.          .-  ",
		"     '--o-------'   ",
	}
}

// colorMark colours the ring, the core and the ring nodes separately.
func colorMark(lines []string, ring, core, node func(...string) string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		var b strings.Builder
		var run strings.Builder
		flush := func() {
			if run.Len() > 0 {
				b.WriteString(ring(run.String()))
				run.Reset()
			}
		}
		for _, r := range l {
			switch r {
			case '●', '@':
				flush()
				b.WriteString(core(string(r)))
			case '◦', 'o':
				flush()
				b.WriteString(node(string(r)))
			case ' ':
				flush()
				b.WriteRune(r)
			default:
				run.WriteRune(r)
			}
		}
		flush()
		out[i] = b.String()
	}
	return out
}
