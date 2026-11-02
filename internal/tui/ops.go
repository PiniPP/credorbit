package tui

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PiniPP/credorbit/internal/engine"
	"github.com/PiniPP/credorbit/internal/model"
)

// ---------------------------------------------------------------- login ---

type loginScreen struct {
	e        *env
	sessions []model.SSOSession
	states   map[string]engine.LoginState
	cur      int
	force    bool
	opID     int
	statusID int
	result   string
}

func newLoginScreen(e *env) *loginScreen {
	l := &loginScreen{e: e, states: map[string]engine.LoginState{}}
	l.sessions, _ = e.svc.Sessions()
	for i, s := range l.sessions {
		if s.Name == e.session {
			l.cur = i
		}
	}
	return l
}

func (l *loginScreen) Title() string { return "AWS SSO Login" }
func (l *loginScreen) KeyHints() []hint {
	g := l.e.g
	return []hint{{g.up + g.down, "session"}, {"enter", "login (browser)"}, {"d", "device code"}, {"f", "force"}, {"x", "logout"}}
}

func (l *loginScreen) Init() tea.Cmd { return l.loadStates() }

func (l *loginScreen) loadStates() tea.Cmd {
	e := l.e
	names := make([]string, len(l.sessions))
	for i, s := range l.sessions {
		names[i] = s.Name
	}
	id, cmd := e.run("Checking sessions", func(ctx context.Context) (any, error) {
		m := map[string]engine.LoginState{}
		for _, n := range names {
			m[n] = e.svc.LoginStatus(ctx, n)
		}
		return m, nil
	})
	l.statusID = id
	return cmd
}

func (l *loginScreen) login(flow string) tea.Cmd {
	e := l.e
	if len(l.sessions) == 0 || l.opID != 0 {
		return nil
	}
	name, force := l.sessions[l.cur].Name, l.force
	e.session = name
	l.result = ""
	id, cmd := e.run("Signing in to "+name, func(ctx context.Context) (any, error) {
		return e.svc.Login(ctx, name, flow, force)
	})
	l.opID = id
	return cmd
}

func (l *loginScreen) Update(msg tea.Msg) tea.Cmd {
	e, t, g := l.e, l.e.th, l.e.g
	switch m := msg.(type) {
	case opDoneMsg:
		switch m.id {
		case l.statusID:
			l.statusID = 0
			if st, ok := m.val.(map[string]engine.LoginState); ok {
				l.states = st
			}
		case l.opID:
			l.opID = 0
			if m.err != nil {
				l.result = t.Danger.Render(g.fail+" Login failed: ") + t.Text.Render(m.err.Error())
			} else if st, ok := m.val.(engine.LoginState); ok {
				l.states[st.Session.Name] = st
				l.result = t.Success.Render(g.ok+" Signed in to "+st.Session.Name) +
					t.Muted.Render("  token valid until "+st.ExpiresAt.Format("15:04")+" ("+humanDur(st.ExpiresAt.Sub(e.now()))+")")
			}
			return l.loadStates()
		}
	case tea.KeyMsg:
		switch m.String() {
		case "up", "k":
			l.cur = max(0, l.cur-1)
		case "down", "j":
			l.cur = min(len(l.sessions)-1, l.cur+1)
		case "enter":
			return l.login("browser")
		case "d":
			return l.login("device")
		case "f":
			l.force = !l.force
		case "x":
			if len(l.sessions) == 0 {
				return nil
			}
			name := l.sessions[l.cur].Name
			return openDialog(newConfirm(e, "Sign out of "+name+"?", "Removes the cached SSO token for this session. Exported credentials stay until they expire.", false,
				func(ok bool) tea.Cmd {
					if !ok {
						return nil
					}
					_, cmd := e.run("Signing out", func(ctx context.Context) (any, error) { return nil, e.svc.Logout(ctx, name) })
					return tea.Batch(cmd, toast("signed out of "+name))
				}))
		}
	}
	return nil
}

func (l *loginScreen) View(w, h int) string {
	e, t, g := l.e, l.e.th, l.e.g
	out := []string{t.Bold.Render("IAM Identity Center sessions"), t.Muted.Render("Tokens are shared with the AWS CLI cache; CredOrbit never displays them."), ""}
	for i, s := range l.sessions {
		st, known := l.states[s.Name]
		state := t.Muted.Render(g.pillOff + " checking" + g.ellipsis)
		if known {
			switch {
			case !st.LoggedIn:
				state = t.PillOff.Render(g.pillOff + " signed out")
			case st.ExpiresAt.Sub(e.now()) < 30*time.Minute:
				state = t.PillWarn.Render(g.pillSoon + " expires in " + humanDur(st.ExpiresAt.Sub(e.now())))
			default:
				state = t.PillOK.Render(g.pillOn + " valid " + humanDur(st.ExpiresAt.Sub(e.now())))
				if st.Refresh {
					state += t.Muted.Render(" " + g.sep + " refreshable")
				}
			}
		}
		line := padRight(s.Name, 20) + padRight(s.Region, 12)
		if i == l.cur {
			out = append(out, t.Sel.Render(e.fit(" "+g.cursor+" "+line+ansiStrip(state), w)))
		} else {
			out = append(out, "   "+t.Text.Render(padRight(s.Name, 20))+t.Muted.Render(padRight(s.Region, 12))+state)
		}
		out = append(out, "     "+t.Muted.Render(e.trunc(s.StartURL, w-6)))
	}
	out = append(out, "", t.Muted.Render("force re-login ")+t.Text.Render(onOff(g, l.force)), "")
	if l.opID != 0 {
		out = append(out, e.spin.View()+" "+t.Accent.Render("Waiting for SSO authorization"+g.ellipsis))
	}
	if l.result != "" {
		out = append(out, l.result)
	}
	return strings.Join(out, "\n")
}

// ------------------------------------------------------------- discover ---

type discoverScreen struct {
	e    *env
	opID int
	prog progressPanel
	disc *model.Discovery
	err  error
}

func newDiscoverScreen(e *env) *discoverScreen { return &discoverScreen{e: e} }

func (d *discoverScreen) Title() string { return "Discover AWS Accounts" }
func (d *discoverScreen) KeyHints() []hint {
	if d.opID != 0 {
		return []hint{{"ctrl+c", "cancel"}}
	}
	return []hint{{"enter", "browse accounts"}, {"m", "manage main"}, {"r", "run again"}}
}
func (d *discoverScreen) Init() tea.Cmd { return d.start() }

func (d *discoverScreen) start() tea.Cmd {
	e := d.e
	s := e.session
	d.prog.reset("discover")
	d.disc, d.err = nil, nil
	id, cmd := e.run("Discovering accounts", func(ctx context.Context) (any, error) { return e.svc.Discover(ctx, s) })
	d.opID = id
	return cmd
}

func (d *discoverScreen) Update(msg tea.Msg) tea.Cmd {
	e := d.e
	switch m := msg.(type) {
	case eventsMsg:
		for _, ev := range m {
			d.prog.apply(ev)
		}
	case opDoneMsg:
		if m.id == d.opID {
			d.opID = 0
			d.prog.running = false
			d.disc, _ = m.val.(*model.Discovery)
			d.err = m.err
			if m.err == nil {
				e.live[e.session] = true
			}
		}
	case tea.KeyMsg:
		if d.opID != 0 {
			return nil
		}
		switch m.String() {
		case "enter", "b":
			return replace(newBrowserScreen(e))
		case "m":
			return replace(newGroupEditor(e, "main"))
		case "r":
			return d.start()
		}
	}
	return nil
}

func (d *discoverScreen) View(w, h int) string {
	e, t, g := d.e, d.e.th, d.e.g
	out := []string{t.Muted.Render("Session ") + t.Text.Render(e.session), ""}
	if d.opID != 0 || d.disc == nil && d.err == nil {
		out = append(out, strings.Split(d.prog.view(e, w, h-2), "\n")...)
		return strings.Join(out, "\n")
	}
	if d.err != nil {
		out = append(out, t.Danger.Render(g.fail+" Discovery failed: ")+t.Text.Render(d.err.Error()),
			t.Muted.Render("A failed discovery never marks accounts as missing. Try "), t.Key.Render("r")+t.Muted.Render(" to retry or log in again."))
		return strings.Join(out, "\n")
	}
	counts := map[model.AccountStatus]int{}
	rows := e.svc.Accounts(e.session)
	for _, r := range rows {
		counts[r.Status]++
	}
	tile := func(label string, n int, st func(...string) string) string {
		return st(padRight(itoa(n), 5)) + t.Muted.Render(label)
	}
	out = append(out,
		t.Success.Render(g.ok+" Discovery complete")+t.Muted.Render("  "+g.sep+"  "+itoa(len(d.disc.Accounts))+" accounts visible in SSO"), "",
		"  "+tile("total", len(rows), t.Bold.Render),
		"  "+tile("available (configured)", counts[model.StatusAvailable], t.Success.Render),
		"  "+tile("new (not configured yet)", counts[model.StatusNew], t.Accent.Render),
		"  "+tile("missing (configured, not visible)", counts[model.StatusMissing], t.Danger.Render),
		"  "+tile("unknown", counts[model.StatusUnknown], t.Warning.Render), "")
	if !d.disc.Complete {
		out = append(out, t.Warning.Render(g.warn+" Discovery incomplete: nothing will be treated as removed."))
	}
	if d.prog.fail > 0 {
		out = append(out, t.Warning.Render(g.warn+" "+itoa(d.prog.fail)+" accounts had role-listing errors (roles unknown, not revoked)."))
	}
	out = append(out, "", t.Key.Render("enter")+t.Muted.Render(" browse accounts   ")+t.Key.Render("m")+t.Muted.Render(" manage Main group"))
	return strings.Join(out, "\n")
}

// ----------------------------------------------------------------- sync ---

type syncScreen struct {
	e       *env
	phase   int // 0 options, 1 preview, 2 result
	cur     int
	scopeIx int
	scopes  []string
	opts    []toggle
	opID    int
	preview string
	empty   bool
	scroll  int
	res     *engine.SyncResult
	err     error
}

func newSyncScreen(e *env) *syncScreen {
	s := &syncScreen{e: e, scopes: append([]string{"all accounts"}, groupNames(e)...)}
	addNew := true
	for _, st := range e.svc.Settings() {
		if st.Key == "sync.add_new" {
			addNew = st.Value != "false"
		}
	}
	s.opts = []toggle{
		{label: "Add profiles for new accounts", help: "p_<name>, role chosen by policy", on: addNew},
		{label: "Remove stale profiles (reviewed)", help: "blocked when discovery is incomplete", on: false},
	}
	return s
}

func (s *syncScreen) Init() tea.Cmd { return nil }
func (s *syncScreen) Title() string { return "Sync AWS Profiles" }
func (s *syncScreen) KeyHints() []hint {
	g := s.e.g
	switch s.phase {
	case 1:
		return []hint{{"a", "apply"}, {g.up + g.down, "scroll"}, {"esc", "back"}}
	case 2:
		return []hint{{"esc", "done"}}
	}
	return []hint{{g.up + g.down, "move"}, {g.left + g.right, "scope"}, {"space", "toggle"}, {"p", "preview plan"}}
}

func (s *syncScreen) back() (bool, tea.Cmd) {
	if s.phase == 1 && s.opID == 0 {
		s.phase = 0
		return true, nil
	}
	return false, nil
}

func (s *syncScreen) options() engine.SyncOptions {
	o := engine.SyncOptions{AddNew: boolp(s.opts[0].on), RemoveStale: s.opts[1].on}
	if s.scopeIx > 0 {
		if g, err := s.e.svc.Group(s.scopes[s.scopeIx]); err == nil {
			o.AccountIDs = []string{}
			for _, m := range g.Members {
				o.AccountIDs = append(o.AccountIDs, m.AccountID)
			}
		}
	}
	return o
}

func (s *syncScreen) Update(msg tea.Msg) tea.Cmd {
	e := s.e
	switch m := msg.(type) {
	case opDoneMsg:
		if m.id != s.opID {
			return nil
		}
		s.opID = 0
		s.err = m.err
		switch v := m.val.(type) {
		case [2]any:
			s.preview, _ = v[0].(string)
			s.empty, _ = v[1].(bool)
			if m.err == nil {
				s.phase, s.scroll = 1, 0
			}
		case *engine.SyncResult:
			s.res = v
			s.phase = 2
		}
		if m.err != nil && s.phase == 1 {
			s.phase = 2
		}
	case tea.KeyMsg:
		if s.opID != 0 {
			return nil
		}
		key := m.String()
		switch s.phase {
		case 0:
			n := len(s.opts) + 2
			switch key {
			case "up", "k":
				s.cur = (s.cur + n - 1) % n
			case "down", "j", "tab":
				s.cur = (s.cur + 1) % n
			case "left", "h":
				if s.cur == 0 {
					s.scopeIx = (s.scopeIx + len(s.scopes) - 1) % len(s.scopes)
				}
			case "right", "l":
				if s.cur == 0 {
					s.scopeIx = (s.scopeIx + 1) % len(s.scopes)
				}
			case " ", "enter":
				switch {
				case s.cur == 0:
					s.scopeIx = (s.scopeIx + 1) % len(s.scopes)
				case s.cur <= len(s.opts):
					s.opts[s.cur-1].on = !s.opts[s.cur-1].on
				case key == "enter":
					return s.plan()
				}
			case "p":
				return s.plan()
			}
		case 1:
			switch key {
			case "up", "k":
				s.scroll = max(0, s.scroll-1)
			case "down", "j":
				s.scroll++
			case "a", "enter":
				if s.empty {
					return toast("nothing to apply")
				}
				sess, o := e.session, s.options()
				id, cmd := e.run("Applying profile changes", func(ctx context.Context) (any, error) { return e.svc.ApplySync(ctx, sess, o) })
				s.opID = id
				return cmd
			}
		case 2:
			if key == "enter" {
				return pop()
			}
		}
	}
	return nil
}

func (s *syncScreen) plan() tea.Cmd {
	e := s.e
	sess, o := e.session, s.options()
	id, cmd := e.run("Building sync plan", func(ctx context.Context) (any, error) {
		p, empty, err := e.svc.PlanSync(ctx, sess, o)
		return [2]any{p, empty}, err
	})
	s.opID = id
	return cmd
}

func (s *syncScreen) View(w, h int) string {
	e, t, g := s.e, s.e.th, s.e.g
	var out []string
	switch s.phase {
	case 0:
		out = append(out, t.Bold.Render("Synchronise ~/.aws/config with SSO"), t.Muted.Render("Nothing is written before you review the plan. Unrelated profiles and comments are preserved."), "")
		sc := g.left + " " + s.scopes[s.scopeIx] + " " + g.right
		if s.cur == 0 {
			out = append(out, t.Sel.Render(e.fit(g.cursor+" Scope      "+sc, w)))
		} else {
			out = append(out, "  "+t.Text.Render("Scope      ")+t.Accent.Render(sc))
		}
		tcur := s.cur - 1
		if s.cur == 0 || s.cur > len(s.opts) {
			tcur = -1
		}
		out = append(out, e.toggles(s.opts, tcur, w)...)
		out = append(out, "")
		btn := " " + g.right + " Preview plan "
		if s.cur == len(s.opts)+1 {
			out = append(out, "  "+t.Sel.Render(btn))
		} else {
			out = append(out, "  "+t.Badge.Render(strings.TrimSpace(btn)))
		}
		if s.opID != 0 {
			out = append(out, "", e.spin.View()+" "+t.Muted.Render("building plan"+g.ellipsis))
		}
		if s.err != nil {
			out = append(out, "", e.errText(s.err))
		}
	case 1:
		if s.empty {
			out = append(out, t.Success.Render(g.ok+" Profiles are already in sync")+t.Muted.Render(" "+g.sep+" nothing to change"))
			break
		}
		a, r, c := diffCounts(s.preview)
		out = append(out, t.Bold.Render("Plan preview")+"   "+t.Added.Render(g.plus+itoa(a)+" add")+"  "+t.Removed.Render(g.minus+itoa(r)+" remove")+"  "+t.Changed.Render("~"+itoa(c)+" role change"), e.hrule(w))
		pl := lines(e.colorDiff(s.preview))
		vh := max(1, h-4)
		s.scroll = min(s.scroll, max(0, len(pl)-vh))
		out = append(out, pl[s.scroll:min(len(pl), s.scroll+vh)]...)
		for len(out) < h-1 {
			out = append(out, "")
		}
		if s.opID != 0 {
			out = append(out[:h-1], e.spin.View()+" "+t.Muted.Render("applying"+g.ellipsis))
		} else {
			out = append(out[:h-1], t.Key.Render("a")+t.Muted.Render(" apply (you will confirm once more)   ")+t.Key.Render("esc")+t.Muted.Render(" back"))
		}
	case 2:
		if s.err != nil {
			out = append(out, e.errText(s.err))
			break
		}
		out = append(out, t.Success.Render(g.ok+" "+itoa(s.res.Applied)+" changes applied to ~/.aws/config"))
		if s.res.Backup != "" {
			out = append(out, t.Muted.Render("backup  ")+t.Text.Render(s.res.Backup))
		}
		for _, wn := range s.res.Warnings {
			out = append(out, t.Warning.Render(g.warn+" "+wn))
		}
	}
	return strings.Join(out, "\n")
}
