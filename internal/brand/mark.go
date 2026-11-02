package brand

import "strings"

// The terminal mark mirrors the vector logo: an orbital ring open on the
// right (reading as a "C"), orbital nodes at the ring's two ends and on the
// far side, and the identity core inside its halo. 7 lines x 18 columns.
var markUnicode = []string{
	"      ╭──────────◦",
	"   ╭──╯",
	"  ╱      ╭───╮",
	" ●       │ ● │",
	"  ╲      ╰───╯",
	"   ╰──╮",
	"      ╰──────────●",
}

// markASCII uses only 7-bit characters for terminals without Unicode.
var markASCII = []string{
	`      .----------o`,
	`   .--'`,
	`  /      .---.`,
	` O       | * |`,
	`  \      '---'`,
	`   '--.`,
	`      '----------O`,
}

// MarkWidth and MarkHeight bound the terminal mark (in cells).
const (
	MarkWidth  = 18
	MarkHeight = 7
)

// Mark returns the terminal brand mark, one string per line, without
// trailing spaces. unicode=false returns the 7-bit ASCII fallback.
func Mark(unicode bool) []string {
	src := markASCII
	if unicode {
		src = markUnicode
	}
	out := make([]string, len(src))
	copy(out, src)
	return out
}

// Wordmark returns the spaced-caps wordmark "C R E D O R B I T".
func Wordmark() string {
	return spaced("CREDORBIT")
}

// WordmarkParts returns the wordmark split into its two brand halves
// ("C R E D", "O R B I T") so renderers can colour "Orbit" as the accent.
func WordmarkParts() (cred, orbit string) {
	return spaced("CRED"), spaced("ORBIT")
}

func spaced(s string) string {
	return strings.Join(strings.Split(s, ""), " ")
}

// Role classifies a mark character for styling.
type Role int

// Mark roles.
const (
	RoleSpace Role = iota // whitespace
	RoleRing              // orbital structure  -> Primary
	RoleNode              // orbital nodes      -> Accent
	RoleCore              // identity core      -> Accent, bold
	RoleHalo              // halo around core   -> Accent, faint
)

// Segment is a run of characters sharing one Role.
type Segment struct {
	Text string
	Role Role
}

// markRoles is a per-cell role mask shared by both marks (they have an
// identical layout): r=ring n=node h=halo c=core, space=space.
var markRoles = []string{
	"      rrrrrrrrrrrn",
	"   rrrr",
	"  r      hhhhh",
	" n       h c h",
	"  r      hhhhh",
	"   rrrr",
	"      rrrrrrrrrrrn",
}

// MarkSegments returns the mark split into styled runs per line. Joining the
// Text of each line's segments reproduces Mark(unicode) exactly.
func MarkSegments(unicode bool) [][]Segment {
	lines := Mark(unicode)
	out := make([][]Segment, len(lines))
	for i, line := range lines {
		mask := []rune(markRoles[i])
		var segs []Segment
		for j, r := range []rune(line) {
			role := RoleRing
			if j < len(mask) {
				role = roleFor(mask[j])
			}
			if r == ' ' {
				role = RoleSpace
			}
			if n := len(segs); n > 0 && segs[n-1].Role == role {
				segs[n-1].Text += string(r)
				continue
			}
			segs = append(segs, Segment{Text: string(r), Role: role})
		}
		out[i] = segs
	}
	return out
}

func roleFor(m rune) Role {
	switch m {
	case 'n':
		return RoleNode
	case 'c':
		return RoleCore
	case 'h':
		return RoleHalo
	case ' ':
		return RoleSpace
	}
	return RoleRing
}
