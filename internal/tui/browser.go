package tui

import (
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sahilm/fuzzy"

	"github.com/PiniPP/credorbit/internal/engine"
	"github.com/PiniPP/credorbit/internal/model"
)

type browseMode int

const (
	modeBrowse browseMode = iota // selection is for this run only
	modePick                     // multi-select picker (dialogs, manual scope)
	modeSingle                   // pick exactly one account (enter)
	modeGroup                    // selection is pending group membership
)

var (
	envOpts    = []string{"all", "prd", "stg", "dev", "test", "sandbox"}
	statusOpts = []string{"all", "available", "new", "missing", "unknown"}
	sortOpts   = []string{"name", "id", "env", "status", "expiry"}
)

// browser is the accounts & roles table shared by the browser screen, the
// group editor and the account pickers. Rendering is virtualised: only the
// visible window of rows is drawn.
type browser struct {
	e     *env
	mode  browseMode
	rows  []engine.AccountRow
	vis   []int // indices into rows after filter + sort
	sel   map[string]bool
	saved map[string]bool // modeGroup: membership as saved

	cursor, offset, page int

	search    *field
	searching bool
	envF      int
	statusF   int
	groupF    int
	groupOpts []string
	sortF     int
	reverse   bool
}

func newBrowser(e *env, mode browseMode) *browser {
	return &browser{e: e, mode: mode, sel: map[string]bool{}, saved: map[string]bool{},
		search: newField("/", "type to search name, id or profile", ""), page: 10}
}

func (b *browser) setRows(rows []engine.AccountRow) {
	var keep string
	if c := b.current(); c != nil {
		keep = c.Account.ID
	}
	b.rows = rows
	gs := map[string]bool{}
	for _, r := range rows {
		for _, g := range r.Groups {
			gs[g] = true
		}
	}
	opts := []string{"all"}
	if gs["main"] {
		opts = append(opts, "main")
	}
	var rest []string
	for g := range gs {
		if g != "main" {
			rest = append(rest, g)
		}
	}
	sort.Strings(rest)
	b.groupOpts = append(append(opts, rest...), "ungrouped")
	if b.groupF >= len(b.groupOpts) {
		b.groupF = 0
	}
	b.refilter()
	if keep != "" {
		for i, ix := range b.vis {
			if b.rows[ix].Account.ID == keep {
				b.cursor = i
			}
		}
	}
}

func (b *browser) setSelection(ids []string) {
	b.sel = map[string]bool{}
	for _, id := range ids {
		b.sel[id] = true
	}
}

// setSaved sets the saved group membership and resets the pending set to it.
func (b *browser) setSaved(ids []string) {
	b.saved = map[string]bool{}
	for _, id := range ids {
		b.saved[id] = true
	}
	b.restore()
}

func (b *browser) restore() {
	b.sel = map[string]bool{}
	for id := range b.saved {
		b.sel[id] = true
	}
}

// diff returns pending additions and removals relative to the saved set.
func (b *browser) diff() (add, rem int) {
	for id := range b.sel {
		if !b.saved[id] {
			add++
		}
	}
	for id := range b.saved {
		if !b.sel[id] {
			rem++
		}
	}
	return
}

func (b *browser) dirty() bool { a, r := b.diff(); return a+r > 0 }

// selected returns selected account IDs in stable (name) order; IDs that are
// not among the rows (e.g. members not discovered) are kept, sorted, at the end.
func (b *browser) selected() []string {
	out := []string{}
	seen := map[string]bool{}
	byName := append([]engine.AccountRow(nil), b.rows...)
	sort.SliceStable(byName, func(i, j int) bool { return byName[i].Account.Name < byName[j].Account.Name })
	for _, r := range byName {
		if b.sel[r.Account.ID] && !seen[r.Account.ID] {
			out = append(out, r.Account.ID)
			seen[r.Account.ID] = true
		}
	}
	var extra []string
	for id, on := range b.sel {
		if on && !seen[id] {
			extra = append(extra, id)
		}
	}
	sort.Strings(extra)
	return append(out, extra...)
}

func (b *browser) current() *engine.AccountRow {
	if b.cursor < 0 || b.cursor >= len(b.vis) {
		return nil
	}
	return &b.rows[b.vis[b.cursor]]
}

type rowSource []engine.AccountRow

func (s rowSource) String(i int) string {
	r := s[i]
	return r.Account.Name + " " + r.Account.ID + " " + strings.Join(r.Profiles, " ")
}
func (s rowSource) Len() int { return len(s) }

func (b *browser) match(r engine.AccountRow) bool {
	if f := envOpts[b.envF]; f != "all" && r.Env != f {
		return false
	}
	if f := statusOpts[b.statusF]; f != "all" && string(r.Status) != f {
		return false
	}
	if g := b.groupOpts[b.groupF]; g == "ungrouped" {
		if len(r.Groups) > 0 {
			return false
		}
	} else if g != "all" {
		found := false
		for _, x := range r.Groups {
			found = found || x == g
		}
		if !found {
			return false
		}
	}
	return true
}

func (b *browser) refilter() {
	if len(b.groupOpts) == 0 {
		b.groupOpts = []string{"all", "ungrouped"}
	}
	q := b.search.Value()
	var cand []int
	if q != "" {
		for _, m := range fuzzy.FindFrom(q, rowSource(b.rows)) {
			cand = append(cand, m.Index)
		}
	} else {
		for i := range b.rows {
			cand = append(cand, i)
		}
	}
	b.vis = b.vis[:0]
	for _, i := range cand {
		if b.match(b.rows[i]) {
			b.vis = append(b.vis, i)
		}
	}
	key := sortOpts[b.sortF]
	less := func(x, y engine.AccountRow) bool {
		switch key {
		case "id":
			return x.Account.ID < y.Account.ID
		case "env":
			if x.Env != y.Env {
				return envRank(x.Env) < envRank(y.Env)
			}
		case "status":
			if x.Status != y.Status {
				return statusRank(x.Status) < statusRank(y.Status)
			}
		case "expiry":
			xe, ye := x.CredsExp, y.CredsExp
			if xe.IsZero() != ye.IsZero() {
				return ye.IsZero()
			}
			if !xe.Equal(ye) {
				return xe.Before(ye)
			}
		}
		return x.Account.Name < y.Account.Name
	}
	if q != "" && b.sortF == 0 && !b.reverse {
		// default sort while searching: best fuzzy match first
		if b.cursor >= len(b.vis) {
			b.cursor = max(0, len(b.vis)-1)
		}
		b.clampOffset()
		return
	}
	sort.SliceStable(b.vis, func(i, j int) bool {
		x, y := b.rows[b.vis[i]], b.rows[b.vis[j]]
		if b.reverse {
			return less(y, x)
		}
		return less(x, y)
	})
	if b.cursor >= len(b.vis) {
		b.cursor = max(0, len(b.vis)-1)
	}
	b.clampOffset()
}

func envRank(e string) int {
	for i, x := range envOpts[1:] {
		if x == e {
			return i
		}
	}
	return len(envOpts)
}

func statusRank(s model.AccountStatus) int {
	for i, x := range statusOpts[1:] {
		if x == string(s) {
			return i
		}
	}
	return len(statusOpts)
}

func (b *browser) clampOffset() {
	if b.page < 1 {
		b.page = 1
	}
	if b.cursor < b.offset {
		b.offset = b.cursor
	}
	if b.cursor >= b.offset+b.page {
		b.offset = b.cursor - b.page + 1
	}
	if b.offset > max(0, len(b.vis)-b.page) {
		b.offset = max(0, len(b.vis)-b.page)
	}
	if b.offset < 0 {
		b.offset = 0
	}
}

func (b *browser) move(d int) {
	b.cursor += d
	if b.cursor < 0 {
		b.cursor = 0
	}
	if b.cursor >= len(b.vis) {
		b.cursor = len(b.vis) - 1
	}
	if b.cursor < 0 {
		b.cursor = 0
	}
	b.clampOffset()
}

func (b *browser) toggle() {
	if c := b.current(); c != nil {
		id := c.Account.ID
		if b.sel[id] {
			delete(b.sel, id)
		} else {
			b.sel[id] = true
		}
	}
}

// selectAll selects every row in the filtered view.
func (b *browser) selectAll() {
	for _, i := range b.vis {
		b.sel[b.rows[i].Account.ID] = true
	}
}

// selectNone clears the selection of the filtered view.
func (b *browser) selectNone() {
	for _, i := range b.vis {
		delete(b.sel, b.rows[i].Account.ID)
	}
}

func (b *browser) invert() {
	for _, i := range b.vis {
		id := b.rows[i].Account.ID
		if b.sel[id] {
			delete(b.sel, id)
		} else {
			b.sel[id] = true
		}
	}
}

// update handles browser keys; handled=false lets the owner act on the key.
func (b *browser) update(k tea.KeyMsg) (bool, tea.Cmd) {
	if b.searching {
		switch k.String() {
		case "esc":
			b.search.SetValue("")
			b.searching = false
			b.refilter()
		case "enter":
			b.searching = false
		case "up":
			b.move(-1)
		case "down":
			b.move(1)
		default:
			if b.search.update(k) {
				b.cursor = 0
				b.refilter()
			}
		}
		return true, nil
	}
	multi := b.mode != modeSingle
	switch k.String() {
	case "up", "k":
		b.move(-1)
	case "down", "j":
		b.move(1)
	case "pgup", "ctrl+b":
		b.move(-b.page)
	case "pgdown", "ctrl+f":
		b.move(b.page)
	case "home":
		b.move(-len(b.vis))
	case "end", "G":
		b.move(len(b.vis))
	case "/":
		b.searching = true
	case "e":
		b.envF = (b.envF + 1) % len(envOpts)
		b.refilter()
	case "s":
		b.statusF = (b.statusF + 1) % len(statusOpts)
		b.refilter()
	case "g":
		b.groupF = (b.groupF + 1) % len(b.groupOpts)
		b.refilter()
	case "o":
		b.sortF = (b.sortF + 1) % len(sortOpts)
		b.refilter()
	case "O":
		b.reverse = !b.reverse
		b.refilter()
	case "c":
		b.envF, b.statusF, b.groupF = 0, 0, 0
		b.search.SetValue("")
		b.refilter()
	case " ", "space":
		if !multi {
			return false, nil
		}
		b.toggle()
		b.move(1)
	case "a":
		if !multi {
			return false, nil
		}
		b.selectAll()
	case "n":
		if !multi {
			return false, nil
		}
		b.selectNone()
	case "i":
		if !multi {
			return false, nil
		}
		b.invert()
	default:
		return false, nil
	}
	return true, nil
}
