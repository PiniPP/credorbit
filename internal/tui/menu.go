package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PiniPP/credorbit/internal/engine"
	"github.com/PiniPP/credorbit/internal/model"
)

type menuItem struct {
	title, desc string
	open        func(e *env) screen // nil = exit
}

func menuItems() []menuItem {
	return []menuItem{
		{"Full Setup & Sync", "Login, discover, sync profiles, export credentials and rotate Claude, end to end.", func(e *env) screen { return newWizard(e) }},
		{"AWS SSO Login", "Sign in to IAM Identity Center (browser or device code) and refresh the token.", func(e *env) screen { return newLoginScreen(e) }},
		{"Discover AWS Accounts", "List every account and role assigned to you and compare with your AWS config.", func(e *env) screen { return newDiscoverScreen(e) }},
		{"Browse Accounts & Roles", "Search, filter and inspect accounts, roles, profiles, groups and credential expiry.", func(e *env) screen { return newBrowserScreen(e) }},
		{"Manage Main Accounts", "Choose the accounts in the Main group (the default export scope).", func(e *env) screen { return newGroupEditor(e, "main") }},
		{"Manage Account Groups", "Create, rename and edit custom account groups such as dev or prod.", func(e *env) screen { return newGroupsScreen(e) }},
		{"Sync AWS Profiles", "Preview and apply profile additions, role updates and reviewed stale removals.", func(e *env) screen { return newSyncScreen(e) }},
		{"Export Credentials", "Fetch temporary role credentials into ~/.aws/credentials with live progress.", func(e *env) screen { return newExportScreen(e) }},
		{"Manage IAM Roles", "Pick the preferred role per account and the role selection policy.", func(e *env) screen { return newRolesScreen(e) }},
		{"Claude / Bedrock Rotation", "See the active Claude Code account, rotate, and manage rotation candidates.", func(e *env) screen { return newRotateScreen(e) }},
		{"Credentials Status", "Sessions, exported credential sections and their expiry at a glance.", func(e *env) screen { return newStatusScreen(e) }},
		{"Diagnostics", "Check AWS config, SSO, network/TLS, file permissions and Claude integration.", func(e *env) screen { return newDoctorScreen(e) }},
		{"Settings", "Theme, ASCII mode, role policy, export and rotation preferences.", func(e *env) screen { return newSettingsScreen(e) }},
		{"Check for Updates", "Compare this version with the latest release (never installs anything).", func(e *env) screen { return newUpdateScreen(e) }},
		{"Exit", "Leave CredOrbit.", nil},
	}
}

type menu struct {
	e         *env
	items     []menuItem
	cur       int
	filter    *field
	filtering bool
}

func newMenu(e *env) *menu {
	return &menu{e: e, items: menuItems(), filter: newField("/", "filter", "")}
}

func (m *menu) Init() tea.Cmd   { return nil }
func (m *menu) Title() string   { return "Main Menu" }
func (m *menu) capturing() bool { return m.filtering }

func (m *menu) KeyHints() []hint {
	g := m.e.g
	if m.filtering {
		return []hint{{"type", "filter"}, {"enter", "open"}, {"esc", "clear"}}
	}
	return []hint{{g.up + g.down, "move"}, {"enter", "open"}, {"1-9", "jump"}, {"/", "filter"}}
}

// visible returns the indices of items matching the filter.
func (m *menu) visible() []int {
	q := strings.ToLower(m.filter.Value())
	var out []int
	for i, it := range m.items {
		if q == "" || strings.Contains(strings.ToLower(it.title), q) {
			out = append(out, i)
		}
	}
	if len(out) == 0 { // fall back to descriptions
		for i, it := range m.items {
			if strings.Contains(strings.ToLower(it.desc), q) {
				out = append(out, i)
			}
		}
	}
	return out
}

func (m *menu) back() (bool, tea.Cmd) {
	if m.filter.Value() != "" {
		m.filter.SetValue("")
		m.cur = 0
		return true, nil
	}
	return false, nil
}

func (m *menu) activate(i int) tea.Cmd {
	it := m.items[i]
	if it.open == nil {
		e := m.e
		if e.busy() {
			return openDialog(newConfirm(e, "Quit CredOrbit?", "An operation is still running. Quitting cancels it.", false,
				func(ok bool) tea.Cmd {
					if ok {
						e.cancelAll()
						return tea.Quit
					}
					return nil
				}))
		}
		return tea.Quit
	}
	return push(it.open(m.e))
}

func (m *menu) Update(msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	vis := m.visible()
	if m.filtering {
		switch k.String() {
		case "esc":
			m.filtering = false
			m.filter.SetValue("")
			m.cur = 0
		case "enter":
			m.filtering = false
			if len(vis) > 0 {
				i := vis[min(m.cur, len(vis)-1)]
				m.filter.SetValue("")
				m.cur = i
				return m.activate(i)
			}
		case "up":
			m.cur = max(0, m.cur-1)
		case "down":
			m.cur = min(len(vis)-1, m.cur+1)
		default:
			if m.filter.update(k) {
				m.cur = 0
			}
		}
		return nil
	}
	switch s := k.String(); s {
	case "up", "k":
		m.cur = (m.cur - 1 + len(vis)) % max(1, len(vis))
	case "down", "j", "tab":
		m.cur = (m.cur + 1) % max(1, len(vis))
	case "home", "g":
		m.cur = 0
	case "end", "G":
		m.cur = len(vis) - 1
	case "/":
		m.filtering = true
	case "enter", " ":
		if len(vis) > 0 {
			return m.activate(vis[m.cur])
		}
	default:
		if len(s) == 1 && s[0] >= '1' && s[0] <= '9' {
			n := int(s[0] - '1')
			if n < len(m.items) {
				m.filter.SetValue("")
				m.cur = n
				return m.activate(n)
			}
		}
	}
	return nil
}

func (m *menu) View(w, h int) string {
	e, t, g := m.e, m.e.th, m.e.g
	vis := m.visible()
	if m.cur >= len(vis) {
		m.cur = max(0, len(vis)-1)
	}
	listW := min(w, 46)
	var list []string
	if m.filtering || m.filter.Value() != "" {
		list = append(list, t.Accent.Render("/ ")+m.filter.view(e, listW-2, m.filtering), "")
	}
	for vi, i := range vis {
		num := "  "
		if i < 9 {
			num = itoa(i+1) + " "
		}
		title := m.items[i].title
		if vi == m.cur {
			list = append(list, t.Sel.Render(e.fit(" "+g.cursor+" "+num+" "+title, listW)))
		} else {
			list = append(list, "   "+t.Muted.Render(num)+" "+t.Text.Render(title))
		}
		if h >= 24 && (i == 0 || i == 3 || i == 7 || i == 9 || i == 13) {
			if vi < len(vis)-1 && m.filter.Value() == "" {
				list = append(list, "   "+t.Muted.Render(strings.Repeat(g.rule, listW-6)))
			}
		}
	}
	if len(vis) == 0 {
		list = append(list, t.Muted.Render("   no menu item matches"))
	}
	desc := ""
	if len(vis) > 0 {
		desc = m.items[vis[m.cur]].desc
	}
	if w >= 96 {
		left := m.brandPanel()
		lw := 34
		right := append([]string{}, list...)
		right = append(right, "")
		for _, l := range wrap(desc, w-lw-7) {
			right = append(right, "   "+t.Accent.Render(l))
		}
		var out []string
		top := max(0, (h-max(len(left), len(right)))/2)
		for i := 0; i < top; i++ {
			out = append(out, "")
		}
		for i := 0; i < max(len(left), len(right)); i++ {
			l, r := "", ""
			if i < len(left) {
				l = left[i]
			}
			if i < len(right) {
				r = right[i]
			}
			out = append(out, e.fit(l, lw)+"  "+r)
		}
		return strings.Join(out, "\n")
	}
	dl := wrap(desc, w-2)
	room := h - len(dl) - 1
	if len(list) > room && room > 0 {
		// keep the focused row visible (no separators below 24 rows)
		curLine := m.cur
		if m.filtering || m.filter.Value() != "" {
			curLine += 2
		}
		start := min(max(0, curLine-room/2), len(list)-room)
		list = list[start : start+room]
	}
	out := append([]string{}, list...)
	out = append(out, "")
	for _, l := range dl {
		out = append(out, t.Accent.Render(l))
	}
	return strings.Join(out, "\n")
}

// brandPanel is the logo column shown beside the menu on wide terminals.
func (m *menu) brandPanel() []string {
	e, t := m.e, m.e.th
	mark := colorMark(brandMark(!e.g.ascii), t.Primary.Render, t.Accent.Bold(true).Render, t.Accent.Render)
	out := []string{}
	for _, l := range mark {
		out = append(out, "  "+l)
	}
	out = append(out, "", "  "+t.Header.Render(wordmark), "  "+t.Muted.Render(tagline), "")
	rows := cached(e, "rows:"+e.session, func() []engine.AccountRow { return e.svc.Accounts(e.session) })
	valid, newN := 0, 0
	for _, r := range rows {
		if !r.CredsExp.IsZero() && r.CredsExp.After(e.now()) {
			valid++
		}
		if r.Status == model.StatusNew {
			newN++
		}
	}
	mainN := cached(e, "main-size", func() int {
		if gi, err := e.svc.Group("main"); err == nil {
			return len(gi.Members)
		}
		return 0
	})
	stat := func(k, v string) string { return "  " + t.Muted.Render(padRight(k, 13)) + t.Text.Render(v) }
	out = append(out,
		stat("Session", e.orDash(e.session)),
		stat("Accounts", itoa(len(rows))+t.Muted.Render(" ("+itoa(newN)+" new)")),
		stat("Main", itoa(mainN)+" accounts"),
		stat("Valid creds", itoa(valid)+" of "+itoa(len(rows))),
		stat("Version", e.svc.Version()),
	)
	return out
}
