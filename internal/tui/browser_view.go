package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/PiniPP/credorbit/internal/engine"
	"github.com/PiniPP/credorbit/internal/model"
)

type colSpec struct {
	key, title string
	w          int
}

// columns lays out the table for width w (responsive: drops columns when narrow).
func (b *browser) columns(w int) (prefix int, cols []colSpec) {
	sw := width(b.e.g.on)
	prefix = 1 + 1 + sw + 1 + 1 + 1 // cursor, sel, status
	if b.mode == modeGroup {
		prefix += 2
	}
	if b.mode == modeSingle {
		prefix -= sw + 1
	}
	show := map[string]bool{"name": true, "roles": true, "creds": true}
	if w >= 64 {
		show["id"], show["env"] = true, true
	}
	if w >= 80 {
		show["profile"] = true
	}
	if w >= 90 {
		show["groups"] = true
	}
	fixed := map[string]int{"id": 12, "env": 4, "creds": 9, "groups": 9}
	used, n := prefix, 0
	for _, k := range []string{"name", "id", "env", "roles", "profile", "groups", "creds"} {
		if show[k] {
			used += fixed[k]
			n++
		}
	}
	used += n - 1 // single-space gaps
	flex := max(12, w-used)
	parts := map[string]float64{"name": 0.42, "roles": 0.32, "profile": 0.26}
	tot := 0.0
	for k, p := range parts {
		if show[k] {
			tot += p
		}
	}
	fw := map[string]int{}
	left := flex
	for _, k := range []string{"roles", "profile"} {
		if show[k] {
			fw[k] = int(float64(flex) * parts[k] / tot)
			left -= fw[k]
		}
	}
	fw["name"] = left
	titles := map[string]string{"name": "ACCOUNT", "id": "ID", "env": "ENV", "roles": "ROLES", "profile": "PROFILE", "groups": "GROUPS", "creds": "CREDS"}
	for _, k := range []string{"name", "id", "env", "roles", "profile", "groups", "creds"} {
		if !show[k] {
			continue
		}
		cw := fixed[k]
		if cw == 0 {
			cw = fw[k]
		}
		cols = append(cols, colSpec{key: k, title: titles[k], w: cw})
	}
	return prefix, cols
}

func envShort(e string) string {
	if e == "sandbox" {
		return "sbx"
	}
	return e
}

func (b *browser) envStyle(env string) lipgloss.Style {
	t := b.e.th
	switch env {
	case "prd":
		return t.Warning
	case "stg":
		return t.Accent
	case "dev", "test":
		return t.Success
	}
	return t.Muted
}

func (b *browser) statusGlyph(s model.AccountStatus, plain bool) string {
	g, t := b.e.g, b.e.th
	var sym string
	var st lipgloss.Style
	switch s {
	case model.StatusAvailable:
		sym, st = g.stAvail, t.Success
	case model.StatusNew:
		sym, st = g.stNew, t.Accent
	case model.StatusMissing:
		sym, st = g.stMissing, t.Danger
	default:
		sym, st = g.stUnknown, t.Warning
	}
	if plain {
		return sym
	}
	return st.Render(sym)
}

// rolesCell shows the configured role highlighted and the others as "+N".
func (b *browser) rolesCell(r engine.AccountRow, w int, plain bool) string {
	e := b.e
	t := e.th
	roles := r.Account.Roles
	if len(roles) == 0 {
		switch {
		case r.Account.RolesErr != "":
			s := e.trunc("roles unknown", w)
			if plain {
				return s
			}
			return t.Warning.Render(s)
		case r.Role != "":
			s := e.trunc(r.Role, w)
			if plain {
				return s
			}
			return t.Muted.Render(s)
		}
		if plain {
			return e.g.expNone
		}
		return t.Muted.Render(e.g.expNone)
	}
	main := r.Role
	if main == "" {
		main = roles[0]
	}
	others := 0
	for _, x := range roles {
		if x != main {
			others++
		}
	}
	suffix := ""
	if others > 0 {
		suffix = " +" + itoa(others)
	}
	room := max(1, w-width(suffix))
	if width(main) > room {
		main = shortRole(main)
	}
	base := e.trunc(main, room)
	if plain {
		return base + suffix
	}
	st := t.Accent
	if r.Role == "" {
		st = t.Text
	}
	return st.Render(base) + t.Muted.Render(suffix)
}

// shortRole drops the AWS managed-policy "Access" suffix for narrow cells
// (the detail line and dialogs always show the full name).
func shortRole(r string) string {
	if s := strings.TrimSuffix(r, "Access"); s != "" {
		return s
	}
	return r
}

func (b *browser) groupsCell(r engine.AccountRow, w int) string {
	if len(r.Groups) == 0 {
		return ""
	}
	s := r.Groups[0]
	if len(r.Groups) > 1 {
		s += " +" + itoa(len(r.Groups)-1)
	}
	return b.e.trunc(s, w)
}

func (b *browser) credsPlain(r engine.AccountRow) string {
	return ansiStrip(b.e.expiry(r.CredsExp))
}

// chips renders the filter / sort state.
func (b *browser) chips() string {
	t, g := b.e.th, b.e.g
	chip := func(name, v string, active bool) string {
		if active {
			return t.Muted.Render(name+" ") + t.Accent.Bold(true).Render(v)
		}
		return t.Muted.Render(name + " " + v)
	}
	arrow := g.up
	if b.reverse {
		arrow = g.down
	}
	if g.ascii {
		arrow = "^"
		if b.reverse {
			arrow = "v"
		}
	}
	return strings.Join([]string{
		chip("env", envOpts[b.envF], b.envF != 0),
		chip("status", statusOpts[b.statusF], b.statusF != 0),
		chip("group", b.groupOpts[b.groupF], b.groupF != 0),
		chip("sort", sortOpts[b.sortF]+" "+arrow, b.sortF != 0 || b.reverse),
	}, "   ")
}

// view renders the browser into w×h.
func (b *browser) view(w, h int) string {
	e, t, g := b.e, b.e.th, b.e.g
	if h < 4 {
		h = 4
	}
	b.page = max(1, h-4)
	b.clampOffset()
	out := make([]string, 0, h)

	// search + chips
	var search string
	if b.searching || b.search.Value() != "" {
		search = t.Accent.Render("/ ") + b.search.view(e, min(34, w/2), b.searching)
	} else {
		search = t.Key.Render("/") + t.Muted.Render(" search")
	}
	out = append(out, e.spread(search, b.chips(), w))

	prefix, cols := b.columns(w)
	// header
	var hb strings.Builder
	hb.WriteString(strings.Repeat(" ", prefix))
	for i, c := range cols {
		if i > 0 {
			hb.WriteString(" ")
		}
		title := c.title
		if sk := sortOpts[b.sortF]; sk == c.key || (sk == "expiry" && c.key == "creds") {
			if b.reverse {
				title += " " + strings.TrimSpace(g.down)
			} else {
				title += " " + strings.TrimSpace(g.up)
			}
			if g.ascii {
				title = c.title + map[bool]string{false: " ^", true: " v"}[b.reverse]
			}
		}
		hb.WriteString(e.fit(title, c.w))
	}
	out = append(out, t.Primary.Bold(true).Render(e.fit(hb.String(), w)))

	if len(b.vis) == 0 {
		out = append(out, "", t.Muted.Render("  No accounts match the current search and filters."),
			t.Muted.Render("  Press ")+t.Key.Render("c")+t.Muted.Render(" to clear filters."))
	}
	end := min(len(b.vis), b.offset+b.page)
	for vi := b.offset; vi < end; vi++ {
		out = append(out, b.renderRow(b.rows[b.vis[vi]], vi == b.cursor, prefix, cols, w))
	}
	for len(out) < h-2 {
		out = append(out, "")
	}
	out = append(out[:h-2], b.detailLine(w))

	// status line
	nsel := 0
	for _, i := range b.vis {
		if b.sel[b.rows[i].Account.ID] {
			nsel++
		}
	}
	var left string
	switch b.mode {
	case modeGroup:
		a, r := b.diff()
		left = t.Text.Render(itoa(len(b.sel)) + " members")
		if a+r > 0 {
			left += t.Muted.Render("  "+g.sep+"  ") + t.Added.Render(g.plus+itoa(a)) + " " + t.Removed.Render(g.minus+itoa(r)) + t.Warning.Render(" unsaved")
		} else {
			left += t.Muted.Render("  " + g.sep + "  saved")
		}
	case modeSingle:
		left = t.Muted.Render("enter picks the highlighted account")
	default:
		left = t.Accent.Render(itoa(len(b.sel))) + t.Muted.Render(" selected")
	}
	left += t.Muted.Render("  " + g.sep + "  " + itoa(len(b.vis)) + " of " + itoa(len(b.rows)) + " shown")
	right := ""
	if len(b.vis) > b.page {
		right = t.Muted.Render(itoa(b.offset+1) + "-" + itoa(end) + " of " + itoa(len(b.vis)))
	}
	out = append(out[:h-1], e.spread(left, right, w))
	return strings.Join(out, "\n")
}

// detailLine shows the focused row untruncated: name, id, every role, profiles.
func (b *browser) detailLine(w int) string {
	e, t, g := b.e, b.e.th, b.e.g
	c := b.current()
	if c == nil {
		return ""
	}
	sep := t.Muted.Render("  " + g.sep + "  ")
	roles := strings.Join(c.Account.Roles, ", ")
	if roles == "" {
		roles = e.orDash(c.Role)
	}
	parts := []string{t.Accent.Render(c.Account.Name), t.Muted.Render(c.Account.ID), t.Text.Render(roles)}
	if len(c.Profiles) > 0 {
		parts = append(parts, t.Text.Render(strings.Join(c.Profiles, ", ")))
	}
	return e.fit(t.Muted.Render(g.cursor+" ")+strings.Join(parts, sep), w)
}

func (b *browser) renderRow(r engine.AccountRow, cur bool, prefix int, cols []colSpec, w int) string {
	e, t, g := b.e, b.e.th, b.e.g
	selOn := b.sel[r.Account.ID]
	var cells []string
	cursor := " "
	if cur {
		cursor = g.cursor
	}
	pre := cursor + " "
	if b.mode != modeSingle {
		box := g.off
		if selOn {
			box = g.on
		}
		if !cur {
			if selOn {
				box = t.Accent.Render(box)
			} else {
				box = t.Muted.Render(box)
			}
		}
		pre += box + " "
	}
	if b.mode == modeGroup {
		m := " "
		switch {
		case selOn && !b.saved[r.Account.ID]:
			m = g.plus
			if !cur {
				m = t.Added.Render(m)
			}
		case !selOn && b.saved[r.Account.ID]:
			m = g.minus
			if !cur {
				m = t.Removed.Render(m)
			}
		}
		pre += m + " "
	}
	pre += b.statusGlyph(r.Status, cur) + " "
	for _, c := range cols {
		var v string
		switch c.key {
		case "name":
			v = e.fit(r.Account.Name, c.w)
			if !cur {
				if selOn {
					v = t.Bold.Render(v)
				} else {
					v = t.Text.Render(v)
				}
			}
		case "id":
			v = e.fit(r.Account.ID, c.w)
			if !cur {
				v = t.Muted.Render(v)
			}
		case "env":
			v = e.fit(envShort(r.Env), c.w)
			if !cur {
				v = b.envStyle(r.Env).Render(v)
			}
		case "roles":
			v = e.fit(b.rolesCell(r, c.w, cur), c.w)
		case "profile":
			p := e.g.expNone
			if len(r.Profiles) > 0 {
				p = r.Profiles[0]
				if len(r.Profiles) > 1 {
					p = e.trunc(p, c.w-3) + " +" + itoa(len(r.Profiles)-1)
				}
			}
			v = e.fit(p, c.w)
			if !cur {
				if len(r.Profiles) == 0 {
					v = t.Muted.Render(v)
				} else {
					v = t.Text.Render(v)
				}
			}
		case "groups":
			v = e.fit(b.groupsCell(r, c.w), c.w)
			if !cur {
				v = t.Primary.Render(v)
			}
		case "creds":
			if cur {
				v = e.fit(b.credsPlain(r), c.w)
			} else {
				v = e.fit(e.expiry(r.CredsExp), c.w)
			}
		}
		cells = append(cells, v)
	}
	line := pre + strings.Join(cells, " ")
	if cur {
		return t.Sel.Render(e.fit(ansiStrip(line), w))
	}
	return e.fit(line, w)
}
