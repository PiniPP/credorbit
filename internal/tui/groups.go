package tui

import (
	"context"
	"errors"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PiniPP/credorbit/internal/engine"
	"github.com/PiniPP/credorbit/internal/model"
)

// discoveryBadge shows whether rows come from the offline cache or a live run.
func (e *env) discoveryBadge() string {
	d := cached(e, "disc:"+e.session, func() *model.Discovery { return e.svc.CachedDiscovery(e.session) })
	if d == nil {
		return e.th.Warning.Render("NOT DISCOVERED")
	}
	if e.live[e.session] {
		return e.th.Success.Render("LIVE") + e.th.Muted.Render(" "+e.g.sep+" discovered "+ago(e.now(), d.At))
	}
	return e.th.Warning.Render("CACHED") + e.th.Muted.Render(" "+e.g.sep+" discovered "+ago(e.now(), d.At))
}

// roleDialog lets the user choose the preferred role of one account.
func (e *env) roleDialog(r engine.AccountRow, after func() tea.Cmd) tea.Cmd {
	roles := r.Account.Roles
	cur := 0
	descs := make([]string, len(roles))
	for i, x := range roles {
		if x == r.Role {
			cur, descs[i] = i, "configured"
		}
	}
	prof := e.orDash(strings.Join(r.Profiles, ", "))
	grp := e.orDash(strings.Join(r.Groups, ", "))
	detail := "ID " + r.Account.ID + "  " + e.g.sep + "  env " + e.orDash(r.Env) + "  " + e.g.sep + "  status " + string(r.Status) +
		"\nprofile " + prof + "  " + e.g.sep + "  groups " + grp + "  " + e.g.sep + "  creds " + ansiStrip(e.expiry(r.CredsExp))
	if r.Account.RolesErr != "" {
		detail += "\nroles unknown: " + r.Account.RolesErr
	}
	if len(roles) > 0 {
		detail += "\n\nChoose the preferred role (used by the configured/manual policies):"
	}
	id := r.Account.ID
	return openDialog(newListDialog(e, r.Account.Name, detail, roles, descs, cur, func(i int, ok bool) tea.Cmd {
		if !ok || i < 0 {
			return nil
		}
		if err := e.svc.SetPreferredRole(id, roles[i]); err != nil {
			return toast("error: " + err.Error())
		}
		return tea.Batch(after(), toast("preferred role "+roles[i]))
	}))
}

// refreshDiscovery runs a live discovery for the current session.
func (e *env) refreshDiscovery() (int, tea.Cmd) {
	s := e.session
	return e.run("Refreshing from AWS", func(ctx context.Context) (any, error) { return e.svc.Discover(ctx, s) })
}

// ------------------------------------------------------- browser screen ---

type browserScreen struct {
	e    *env
	b    *browser
	opID int
	err  error
}

func newBrowserScreen(e *env) *browserScreen {
	s := &browserScreen{e: e, b: newBrowser(e, modeBrowse)}
	s.reload()
	return s
}

func (s *browserScreen) reload()         { s.b.setRows(s.e.svc.Accounts(s.e.session)) }
func (s *browserScreen) Init() tea.Cmd   { return nil }
func (s *browserScreen) Title() string   { return "Accounts & Roles" }
func (s *browserScreen) badge() string   { return s.e.discoveryBadge() }
func (s *browserScreen) capturing() bool { return s.b.searching }
func (s *browserScreen) KeyHints() []hint {
	if s.b.searching {
		return []hint{{"type", "search"}, {"enter", "keep"}, {"esc", "clear"}}
	}
	return []hint{{"/", "search"}, {"e/s/g", "env/status/group"}, {"o/O", "sort"}, {"space", "select"}, {"a/n/i", "all/none/inv"},
		{"enter", "details"}, {"x", "export selected"}, {"r", "refresh"}}
}

func (s *browserScreen) Update(msg tea.Msg) tea.Cmd {
	e := s.e
	switch m := msg.(type) {
	case resumeMsg:
		s.reload()
	case opDoneMsg:
		if m.id == s.opID {
			s.opID, s.err = 0, m.err
			if m.err == nil {
				e.live[e.session] = true
			}
			s.reload()
		}
	case tea.KeyMsg:
		if handled, cmd := s.b.update(m); handled {
			return cmd
		}
		switch m.String() {
		case "enter":
			if c := s.b.current(); c != nil {
				return e.roleDialog(*c, func() tea.Cmd { s.reload(); return nil })
			}
		case "r":
			if s.opID == 0 {
				id, cmd := e.refreshDiscovery()
				s.opID = id
				return cmd
			}
		case "x":
			ids := s.b.selected()
			if len(ids) == 0 {
				if c := s.b.current(); c != nil {
					ids = []string{c.Account.ID}
				}
			}
			if len(ids) == 0 {
				return nil
			}
			x := newExportScreen(e)
			x.scope = engine.Scope{Kind: engine.ScopeSelected, AccountIDs: ids}
			for _, r := range s.b.rows {
				if s.b.sel[r.Account.ID] {
					x.scope.Profiles = append(x.scope.Profiles, r.Profiles...)
				}
			}
			x.scopeLbl, x.phase = itoa(len(ids))+" selected accounts", exOptions
			return push(x)
		}
	}
	return nil
}

func (s *browserScreen) View(w, h int) string {
	if len(s.b.rows) == 0 {
		t := s.e.th
		msg := []string{t.Muted.Render("No accounts yet for session " + s.e.orDash(s.e.session) + "."), "",
			t.Key.Render("r") + t.Muted.Render(" discover accounts from AWS now")}
		if s.err != nil {
			msg = append(msg, "", s.e.errText(s.err))
		}
		return strings.Join(msg, "\n")
	}
	return s.b.view(w, h)
}

// --------------------------------------------------------- group editor ---

type groupEditor struct {
	e         *env
	name      string
	desc      string
	b         *browser
	opID      int
	newRows   []engine.AccountRow
	dismissed bool
	err       error
}

func newGroupEditor(e *env, name string) *groupEditor {
	g := &groupEditor{e: e, name: name, b: newBrowser(e, modeGroup)}
	g.reload(true)
	return g
}

func (g *groupEditor) reload(resetPending bool) {
	e := g.e
	g.b.setRows(e.svc.Accounts(e.session))
	gi, err := e.svc.Group(g.name)
	g.err = err
	g.desc = gi.Description
	ids := make([]string, 0, len(gi.Members))
	for _, m := range gi.Members {
		ids = append(ids, m.AccountID)
	}
	if resetPending {
		g.b.setSaved(ids)
	} else {
		pending := g.b.sel
		g.b.setSaved(ids)
		g.b.sel = pending
	}
	g.newRows = e.svc.NewAccounts(e.session)
}

func (g *groupEditor) Init() tea.Cmd { return nil }
func (g *groupEditor) Title() string {
	if g.name == "main" {
		return "Manage Main Accounts"
	}
	return "Group " + g.name
}
func (g *groupEditor) badge() string   { return g.e.discoveryBadge() }
func (g *groupEditor) capturing() bool { return g.b.searching }
func (g *groupEditor) KeyHints() []hint {
	if g.b.searching {
		return []hint{{"type", "search"}, {"enter", "keep"}, {"esc", "clear"}}
	}
	h := []hint{{"space", "toggle member"}, {"ctrl+s", "save"}, {"u", "restore"}, {"/", "search"}, {"e/s/g", "filter"},
		{"a/n/i", "all/none/inv"}, {"enter", "role"}, {"r", "refresh"}}
	if len(g.newRows) > 0 && !g.dismissed {
		h = append([]hint{{"v", "review new"}}, h...)
	}
	return h
}

func (g *groupEditor) back() (bool, tea.Cmd) {
	if !g.b.dirty() {
		return false, nil
	}
	a, r := g.b.diff()
	return true, openDialog(newConfirm(g.e, "Discard unsaved changes?",
		"You have "+itoa(a)+" additions and "+itoa(r)+" removals in "+g.name+" that are not saved.", false,
		func(ok bool) tea.Cmd {
			if ok {
				return pop()
			}
			return nil
		}))
}

func (g *groupEditor) save() tea.Cmd {
	ids := g.b.selected()
	if err := g.e.svc.GroupSet(g.name, ids); err != nil {
		g.err = err
		return toast("save failed: " + err.Error())
	}
	g.reload(true)
	return toast("saved " + g.name + " (" + itoa(len(ids)) + " accounts)")
}

func (g *groupEditor) Update(msg tea.Msg) tea.Cmd {
	e := g.e
	switch m := msg.(type) {
	case resumeMsg:
		g.reload(false)
	case opDoneMsg:
		if m.id == g.opID {
			g.opID = 0
			if m.err == nil {
				e.live[e.session] = true
			} else {
				g.err = m.err
			}
			g.reload(false)
		}
	case tea.KeyMsg:
		if handled, cmd := g.b.update(m); handled {
			return cmd
		}
		switch m.String() {
		case "ctrl+s":
			return g.save()
		case "u":
			g.b.restore()
			return toast("restored saved selection")
		case "r":
			if g.opID == 0 {
				id, cmd := e.refreshDiscovery()
				g.opID = id
				return cmd
			}
		case "enter":
			if c := g.b.current(); c != nil {
				return e.roleDialog(*c, func() tea.Cmd { g.reload(false); return nil })
			}
		case "v":
			if len(g.newRows) > 0 {
				return g.newAccountsDialog()
			}
		}
	}
	return nil
}

func (g *groupEditor) newAccountsDialog() tea.Cmd {
	e := g.e
	target := "Main"
	if g.name != "main" {
		target = g.name
	}
	items := []string{"Review & select", "Add all to " + target, "Add to another group" + e.g.ellipsis, "Keep ungrouped"}
	descs := []string{"show only new accounts", "pending until ctrl+s", "saved immediately", "hide this notice"}
	var names []string
	for _, r := range g.newRows {
		names = append(names, r.Account.Name)
	}
	detail := strings.Join(names, ", ")
	return openDialog(newListDialog(e, itoa(len(g.newRows))+" newly discovered accounts", detail, items, descs, 0, func(i int, ok bool) tea.Cmd {
		if !ok {
			return nil
		}
		switch i {
		case 0:
			for ix, s := range statusOpts {
				if s == string(model.StatusNew) {
					g.b.statusF = ix
				}
			}
			g.b.cursor = 0
			g.b.refilter()
		case 1:
			for _, r := range g.newRows {
				g.b.sel[r.Account.ID] = true
			}
		case 2:
			var others []string
			for _, n := range groupNames(e) {
				if n != g.name {
					others = append(others, n)
				}
			}
			ids := make([]string, 0, len(g.newRows))
			for _, r := range g.newRows {
				ids = append(ids, r.Account.ID)
			}
			return openDialog(newListDialog(e, "Add to which group?", "", others, nil, 0, func(j int, ok bool) tea.Cmd {
				if !ok {
					return nil
				}
				if err := e.svc.GroupAdd(others[j], ids...); err != nil {
					return toast("error: " + err.Error())
				}
				g.reload(false)
				g.dismissed = true
				return toast("added " + itoa(len(ids)) + " accounts to " + others[j])
			}))
		case 3:
			g.dismissed = true
		}
		return nil
	}))
}

func (g *groupEditor) View(w, h int) string {
	e, t := g.e, g.e.th
	var top []string
	if len(g.newRows) > 0 && !g.dismissed {
		top = append(top, t.Accent.Bold(true).Render(e.g.stNew+" "+itoa(len(g.newRows))+" newly discovered accounts")+
			t.Muted.Render(" "+e.g.sep+" ")+t.Key.Render("[v]")+t.Muted.Render(" review"))
	}
	help := "Membership only: removing an account from " + g.name + " never touches AWS profiles or credentials."
	top = append(top, t.Muted.Render(e.trunc(help, w)))
	if g.err != nil {
		top = append(top, e.errText(g.err))
	}
	top = append(top, "")
	return strings.Join(top, "\n") + "\n" + g.b.view(w, h-len(top))
}

// ------------------------------------------------------- groups screen ---

type groupsScreen struct {
	e      *env
	groups []engine.GroupInfo
	cur    int
}

func newGroupsScreen(e *env) *groupsScreen {
	s := &groupsScreen{e: e}
	s.reload()
	return s
}

func (s *groupsScreen) reload() {
	s.groups = s.e.svc.Groups()
	s.cur = min(s.cur, max(0, len(s.groups)-1))
}

func (s *groupsScreen) Init() tea.Cmd { return nil }
func (s *groupsScreen) Title() string { return "Account Groups" }
func (s *groupsScreen) KeyHints() []hint {
	g := s.e.g
	return []hint{{g.up + g.down, "move"}, {"enter", "edit members"}, {"c", "create"}, {"r", "rename"}, {"d", "delete"}}
}

func (s *groupsScreen) Update(msg tea.Msg) tea.Cmd {
	e := s.e
	switch m := msg.(type) {
	case resumeMsg:
		s.reload()
	case tea.KeyMsg:
		switch m.String() {
		case "up", "k":
			s.cur = max(0, s.cur-1)
		case "down", "j":
			s.cur = min(len(s.groups)-1, s.cur+1)
		case "enter":
			if len(s.groups) > 0 {
				return push(newGroupEditor(e, s.groups[s.cur].Name))
			}
		case "c":
			f := []*field{newField("Name", "e.g. payments", ""), newField("Description", "optional", "")}
			return openDialog(newForm(e, "Create group", "Groups are saved selections of accounts; they never change AWS files.", f,
				func(v []string) (error, tea.Cmd) {
					if v[0] == "" {
						return errors.New("a name is required"), nil
					}
					if err := e.svc.CreateGroup(v[0], v[1]); err != nil {
						return err, nil
					}
					s.reload()
					return nil, toast("created group " + v[0])
				}))
		case "r":
			if len(s.groups) == 0 {
				return nil
			}
			old := s.groups[s.cur].Name
			if old == "main" {
				return toast("the main group cannot be renamed")
			}
			f := []*field{newField("New name", "", old)}
			return openDialog(newForm(e, "Rename "+old, "", f, func(v []string) (error, tea.Cmd) {
				if err := e.svc.RenameGroup(old, v[0]); err != nil {
					return err, nil
				}
				s.reload()
				return nil, toast("renamed " + old + " " + e.g.right + " " + v[0])
			}))
		case "d":
			if len(s.groups) == 0 {
				return nil
			}
			name := s.groups[s.cur].Name
			if name == "main" {
				return toast("the main group cannot be deleted (empty it instead)")
			}
			cd := newConfirm(e, "Delete group "+name+"?", "Only the group is deleted; member accounts, AWS profiles and credentials stay as they are.", false,
				func(ok bool) tea.Cmd {
					if !ok {
						return nil
					}
					if err := e.svc.DeleteGroup(name); err != nil {
						return toast("error: " + err.Error())
					}
					s.reload()
					return toast("deleted group " + name)
				})
			cd.danger = true
			return openDialog(cd)
		}
	}
	return nil
}

func (s *groupsScreen) View(w, h int) string {
	e, t, g := s.e, s.e.th, s.e.g
	out := []string{t.Primary.Bold(true).Render(e.fit("   "+padRight("GROUP", 20)+padRight("MEMBERS", 10)+"DESCRIPTION", w))}
	for i, gr := range s.groups {
		name := gr.Name
		if name == "main" {
			name += " " + g.dot
		}
		if i == s.cur {
			out = append(out, t.Sel.Render(e.fit(" "+g.cursor+" "+padRight(name, 20)+padRight(itoa(len(gr.Members)), 10)+gr.Description, w)))
		} else {
			out = append(out, "   "+t.Text.Render(padRight(name, 20))+t.Accent.Render(padRight(itoa(len(gr.Members)), 10))+t.Muted.Render(e.trunc(gr.Description, max(4, w-33))))
		}
	}
	if len(s.groups) == 0 {
		out = append(out, t.Muted.Render("   no groups yet "+g.sep+" press c to create one"))
	}
	out = append(out, "", t.Muted.Render("   "+g.dot+" main is the default export scope and cannot be deleted or renamed."))
	if len(s.groups) > 0 {
		gr := s.groups[s.cur]
		out = append(out, "", e.label("Members of "+gr.Name))
		max := h - len(out) - 1
		for i, m := range gr.Members {
			if i >= max {
				out = append(out, t.Muted.Render("   "+g.ellipsis+" "+itoa(len(gr.Members)-i)+" more"))
				break
			}
			out = append(out, "   "+t.Text.Render(padRight(e.trunc(m.AccountName, 34), 36))+t.Muted.Render(padRight(m.AccountID, 14)+e.orDash(m.Profile)))
		}
	}
	return strings.Join(out, "\n")
}
