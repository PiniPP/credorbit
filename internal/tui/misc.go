package tui

import (
	"context"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/PiniPP/credorbit/internal/engine"
)

func (e *env) setting(key string) engine.SettingInfo {
	for _, s := range e.svc.Settings() {
		if s.Key == key {
			return s
		}
	}
	return engine.SettingInfo{Key: key}
}

// ---------------------------------------------------------------- roles ---

type rolesScreen struct {
	e *env
	b *browser
}

func newRolesScreen(e *env) *rolesScreen {
	r := &rolesScreen{e: e, b: newBrowser(e, modeSingle)}
	r.b.setRows(e.svc.Accounts(e.session))
	return r
}

func (r *rolesScreen) Init() tea.Cmd   { return nil }
func (r *rolesScreen) Title() string   { return "Manage IAM Roles" }
func (r *rolesScreen) capturing() bool { return r.b.searching }
func (r *rolesScreen) KeyHints() []hint {
	return []hint{{"enter", "choose role"}, {"p", "role policy"}, {"/", "search"}, {"e/s/g", "filter"}}
}

func (r *rolesScreen) Update(msg tea.Msg) tea.Cmd {
	e := r.e
	switch m := msg.(type) {
	case resumeMsg:
		r.b.setRows(e.svc.Accounts(e.session))
	case tea.KeyMsg:
		if handled, cmd := r.b.update(m); handled {
			return cmd
		}
		switch m.String() {
		case "enter":
			if c := r.b.current(); c != nil {
				return e.roleDialog(*c, func() tea.Cmd { r.b.setRows(e.svc.Accounts(e.session)); return nil })
			}
		case "p":
			cur := string(e.setting("roles.policy").Value)
			items := make([]string, len(policies))
			descs := make([]string, len(policies))
			ix := 0
			for i, p := range policies {
				items[i], descs[i] = string(p), policyHelp[p]
				if string(p) == cur {
					ix = i
				}
			}
			return openDialog(newListDialog(e, "Role selection policy", "Normal mode uses the configured role. Ranking policies are opt-in; persisting a different role is always reviewed.",
				items, descs, ix, func(i int, ok bool) tea.Cmd {
					if !ok {
						return nil
					}
					if err := e.svc.SetSetting("roles.policy", items[i]); err != nil {
						return toast("error: " + err.Error())
					}
					return toast("roles.policy = " + items[i])
				}))
		}
	}
	return nil
}

func (r *rolesScreen) View(w, h int) string {
	t := r.e.th
	head := t.Muted.Render("Policy ") + t.Accent.Render(r.e.orDash(r.e.setting("roles.policy").Value)) +
		t.Muted.Render("   The highlighted role in ROLES is the one configured for the profile.")
	return head + "\n\n" + r.b.view(w, h-2)
}

// ------------------------------------------------------------- rotation ---

type rotateScreen struct {
	e      *env
	info   engine.RotationInfo
	all    []string
	member map[string]bool
	cur    int
	opID   int
	result string
}

func newRotateScreen(e *env) *rotateScreen {
	r := &rotateScreen{e: e}
	r.reload()
	return r
}

func (r *rotateScreen) reload() {
	e := r.e
	r.info = e.svc.Rotation()
	r.member = map[string]bool{}
	seen := map[string]bool{}
	r.all = nil
	for _, a := range r.info.Accounts {
		r.member[a.Section] = true
		seen[a.Section] = true
		r.all = append(r.all, a.Section)
	}
	var extra []string
	for _, row := range e.svc.Accounts(e.session) {
		for _, p := range row.Profiles {
			sec := strings.TrimPrefix(p, e.setting("profiles.prefix").Value)
			if !seen[sec] {
				seen[sec] = true
				extra = append(extra, sec)
			}
		}
	}
	sort.Strings(extra)
	r.all = append(r.all, extra...)
	r.cur = min(r.cur, max(0, len(r.all)-1))
}

func (r *rotateScreen) Init() tea.Cmd { return nil }
func (r *rotateScreen) Title() string { return "Claude / Bedrock Rotation" }
func (r *rotateScreen) KeyHints() []hint {
	g := r.e.g
	return []hint{{"R", "rotate now"}, {"enter", "set active"}, {"space", "candidate"}, {"t", "strategy"}, {"v", "verify bedrock"}, {"z", "shell setup"}, {g.up + g.down, "move"}}
}

func (r *rotateScreen) rotate(o engine.RotateOptions) tea.Cmd {
	e := r.e
	id, cmd := e.run("Rotating Claude account", func(ctx context.Context) (any, error) {
		from, to, err := e.svc.Rotate(ctx, o)
		return [2]string{from, to}, err
	})
	r.opID = id
	return cmd
}

func (r *rotateScreen) Update(msg tea.Msg) tea.Cmd {
	e, t, g := r.e, r.e.th, r.e.g
	switch m := msg.(type) {
	case resumeMsg:
		r.reload()
	case opDoneMsg:
		if m.id == r.opID {
			r.opID = 0
			if m.err != nil {
				r.result = e.errText(m.err)
			} else if v, ok := m.val.([2]string); ok {
				r.result = t.Success.Render(g.ok+" Rotated ") + t.Text.Render(e.orDash(v[0])+" "+g.right+" ") + t.Accent.Bold(true).Render(v[1])
			}
			r.reload()
		}
	case tea.KeyMsg:
		if r.opID != 0 {
			return nil
		}
		switch m.String() {
		case "up", "k":
			r.cur = max(0, r.cur-1)
		case "down", "j":
			r.cur = min(len(r.all)-1, r.cur+1)
		case "R":
			return r.rotate(engine.RotateOptions{})
		case "enter":
			if len(r.all) > 0 {
				return r.rotate(engine.RotateOptions{Set: r.all[r.cur]})
			}
		case " ":
			if len(r.all) == 0 {
				return nil
			}
			sec := r.all[r.cur]
			r.member[sec] = !r.member[sec]
			var list []string
			for _, s := range r.all {
				if r.member[s] {
					list = append(list, s)
				}
			}
			if err := e.svc.SetRotationAccounts(list); err != nil {
				return toast("error: " + err.Error())
			}
			cur := r.cur
			r.reload()
			r.cur = cur
		case "v":
			st := e.setting("rotation.verify_bedrock")
			nv := "true"
			if st.Value == "true" {
				nv = "false"
			}
			if err := e.svc.SetSetting("rotation.verify_bedrock", nv); err != nil {
				return toast("error: " + err.Error())
			}
			return toast("verify bedrock: " + nv)
		case "t":
			st := e.setting("rotation.strategy")
			choices := st.Choices
			if len(choices) == 0 {
				choices = []string{"random", "round-robin", "manual"}
			}
			ix := 0
			for i, c := range choices {
				if c == st.Value {
					ix = i
				}
			}
			return openDialog(newListDialog(e, "Rotation strategy", "", choices, nil, ix, func(i int, ok bool) tea.Cmd {
				if !ok {
					return nil
				}
				if err := e.svc.SetSetting("rotation.strategy", choices[i]); err != nil {
					return toast("error: " + err.Error())
				}
				r.reload()
				return nil
			}))
		case "z":
			return push(newShellScreen(e))
		}
	}
	return nil
}

func (r *rotateScreen) View(w, h int) string {
	e, t, g := r.e, r.e.th, r.e.g
	elig := map[string]engine.RotationAccount{}
	for _, a := range r.info.Accounts {
		elig[a.Section] = a
	}
	cur := e.orDash(r.info.Current)
	verify := e.setting("rotation.verify_bedrock").Value
	out := []string{
		t.Muted.Render("ACTIVE CLAUDE CODE ACCOUNT"),
		"  " + t.Badge.Render(g.pillOn+"  "+cur) + "  " + e.expiry(elig[r.info.Current].Expires),
		t.Muted.Render("  file "+e.orDash(r.info.File)+"   strategy ") + t.Text.Render(e.orDash(r.info.Strategy)) +
			t.Muted.Render("   verify bedrock ") + t.Text.Render(e.orDash(verify)),
		"",
		t.Primary.Bold(true).Render(e.fit("   "+padRight("CANDIDATE", 32)+padRight("ELIGIBLE", 12)+padRight("EXPIRES", 14)+"REASON", w)),
	}
	room := h - len(out) - 3
	start := max(0, min(r.cur-room+1, len(r.all)-room))
	for i := start; i < len(r.all) && i < start+room; i++ {
		sec := r.all[i]
		a, isCand := elig[sec]
		mark := onOff(g, r.member[sec])
		el, reason, exp := t.Muted.Render(g.expNone), "", t.Muted.Render(g.expNone)
		if isCand {
			if a.Eligible {
				el = t.Success.Render(g.ok + " yes")
			} else {
				el = t.Danger.Render(g.fail + " no")
			}
			reason = a.Reason
			exp = e.expiry(a.Expires)
		} else {
			reason = "not a candidate"
		}
		name := sec
		if sec == r.info.Current {
			name += " " + g.dot + " active"
		}
		if i == r.cur {
			out = append(out, t.Sel.Render(e.fit(g.cursor+" "+mark+" "+padRight(name, 30-width(mark)+2)+padRight(ansiStrip(el), 12)+padRight(ansiStrip(exp), 14)+reason, w)))
			continue
		}
		ms := t.Muted.Render(mark)
		if r.member[sec] {
			ms = t.Accent.Render(mark)
		}
		out = append(out, "  "+ms+" "+e.fit(t.Text.Render(name), 30-width(mark)+2)+e.fit(el, 12)+e.fit(exp, 14)+t.Muted.Render(e.trunc(reason, max(4, w-60))))
	}
	out = append(out, "")
	if r.opID != 0 {
		out = append(out, e.spin.View()+" "+t.Muted.Render("rotating"+g.ellipsis))
	} else if r.result != "" {
		out = append(out, r.result)
	}
	return strings.Join(out, "\n")
}

// shellScreen shows the shell integration snippet for each shell.
type shellScreen struct {
	e      *env
	shells []string
	cur    int
	vp     viewport.Model
	text   string
	err    error
}

func newShellScreen(e *env) *shellScreen {
	s := &shellScreen{e: e, shells: []string{"zsh", "bash", "fish", "powershell"}, vp: viewport.New(80, 10)}
	s.load()
	return s
}

func (s *shellScreen) load() {
	s.text, s.err = s.e.svc.ShellIntegration(s.shells[s.cur])
	s.vp.SetContent(s.text)
	s.vp.GotoTop()
}

func (s *shellScreen) Init() tea.Cmd { return nil }
func (s *shellScreen) Title() string { return "Shell Integration" }
func (s *shellScreen) KeyHints() []hint {
	g := s.e.g
	return []hint{{g.left + g.right, "shell"}, {g.up + g.down, "scroll"}, {"esc", "back"}}
}

func (s *shellScreen) Update(msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	switch k.String() {
	case "left", "h", "shift+tab":
		s.cur = (s.cur + len(s.shells) - 1) % len(s.shells)
		s.load()
	case "right", "l", "tab":
		s.cur = (s.cur + 1) % len(s.shells)
		s.load()
	default:
		s.vp, _ = s.vp.Update(k)
	}
	return nil
}

func (s *shellScreen) View(w, h int) string {
	e, t := s.e, s.e.th
	var tabs []string
	for i, sh := range s.shells {
		if i == s.cur {
			tabs = append(tabs, t.Sel.Render(" "+sh+" "))
		} else {
			tabs = append(tabs, t.Muted.Render(" "+sh+" "))
		}
	}
	out := []string{strings.Join(tabs, " "), t.Muted.Render("Select the text with your terminal to copy it. It reads the active account from the rotation file; no secrets."), e.hrule(w)}
	if s.err != nil {
		out = append(out, e.errText(s.err))
		return strings.Join(out, "\n")
	}
	s.vp.Width, s.vp.Height = w, max(1, h-len(out))
	s.vp.SetContent(t.Text.Render(s.text))
	return strings.Join(out, "\n") + "\n" + s.vp.View()
}

// ------------------------------------------------------------- settings ---

type settingsScreen struct {
	e    *env
	list []engine.SettingInfo
	cur  int
}

func newSettingsScreen(e *env) *settingsScreen {
	return &settingsScreen{e: e, list: e.svc.Settings()}
}

func (s *settingsScreen) Init() tea.Cmd { return nil }
func (s *settingsScreen) Title() string { return "Settings" }
func (s *settingsScreen) KeyHints() []hint {
	g := s.e.g
	return []hint{{g.up + g.down, "move"}, {"enter", "edit"}}
}

func (s *settingsScreen) set(key, val string) error {
	if err := s.e.svc.SetSetting(key, val); err != nil {
		return err
	}
	s.list = s.e.svc.Settings()
	return nil
}

func (s *settingsScreen) Update(msg tea.Msg) tea.Cmd {
	e := s.e
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	switch k.String() {
	case "up", "k":
		s.cur = max(0, s.cur-1)
	case "down", "j":
		s.cur = min(len(s.list)-1, s.cur+1)
	case "enter", " ":
		if len(s.list) == 0 {
			return nil
		}
		st := s.list[s.cur]
		after := func() tea.Cmd {
			cmd := toast(st.Key + " updated")
			if strings.HasPrefix(st.Key, "ui.") {
				return tea.Batch(cmd, func() tea.Msg { return themeMsg{} })
			}
			return cmd
		}
		if len(st.Choices) > 0 {
			ix := 0
			for i, c := range st.Choices {
				if c == st.Value {
					ix = i
				}
			}
			return openDialog(newListDialog(e, st.Key, st.Description, st.Choices, nil, ix, func(i int, ok bool) tea.Cmd {
				if !ok {
					return nil
				}
				if err := s.set(st.Key, st.Choices[i]); err != nil {
					return toast("error: " + err.Error())
				}
				return after()
			}))
		}
		f := newField("Value", "", st.Value)
		return openDialog(newForm(e, st.Key, st.Description, []*field{f}, func(v []string) (error, tea.Cmd) {
			if err := s.set(st.Key, v[0]); err != nil {
				return err, nil
			}
			return nil, after()
		}))
	}
	return nil
}

func (s *settingsScreen) View(w, h int) string {
	e, t, g := s.e, s.e.th, s.e.g
	out := []string{t.Muted.Render("Stored in ") + t.Text.Render(e.trunc(e.svc.ConfigPath(), w-12)), ""}
	room := h - len(out) - 3
	start := max(0, min(s.cur-room+1, len(s.list)-room))
	for i := start; i < len(s.list) && i < start+room; i++ {
		st := s.list[i]
		val := e.orDash(st.Value)
		if i == s.cur {
			out = append(out, t.Sel.Render(e.fit(g.cursor+" "+padRight(st.Key, 26)+padRight(e.trunc(val, 22), 24)+st.Description, w)))
		} else {
			out = append(out, "  "+t.Text.Render(padRight(st.Key, 26))+t.Accent.Render(padRight(e.trunc(val, 22), 24))+t.Muted.Render(e.trunc(st.Description, max(4, w-52))))
		}
	}
	if len(s.list) > 0 {
		st := s.list[s.cur]
		out = append(out, "", t.Muted.Render(e.trunc(st.Description, w)))
		if len(st.Choices) > 0 {
			out = append(out, t.Muted.Render("choices: ")+t.Text.Render(strings.Join(st.Choices, " "+g.sep+" ")))
		}
	}
	return strings.Join(out, "\n")
}
