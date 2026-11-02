package tui

import (
	"context"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PiniPP/credorbit/internal/engine"
)

// ---------------------------------------------------------- async views ---

// asyncScreen loads data in the background and renders it with a function.
type asyncScreen struct {
	e      *env
	title  string
	load   func(ctx context.Context) (any, error)
	render func(w, h int, v any) []string
	hints  []hint
	opID   int
	val    any
	err    error
	scroll int
}

func (a *asyncScreen) Init() tea.Cmd {
	id, cmd := a.e.run("Loading "+strings.ToLower(a.title), a.load)
	a.opID = id
	return cmd
}
func (a *asyncScreen) Title() string { return a.title }
func (a *asyncScreen) KeyHints() []hint {
	return append([]hint{{"r", "refresh"}, {a.e.g.up + a.e.g.down, "scroll"}}, a.hints...)
}

func (a *asyncScreen) Update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case opDoneMsg:
		if m.id == a.opID {
			a.opID, a.val, a.err = 0, m.val, m.err
		}
	case tea.KeyMsg:
		switch m.String() {
		case "r":
			if a.opID == 0 {
				return a.Init()
			}
		case "up", "k":
			a.scroll = max(0, a.scroll-1)
		case "down", "j":
			a.scroll++
		}
	}
	return nil
}

func (a *asyncScreen) View(w, h int) string {
	e := a.e
	if a.opID != 0 && a.val == nil {
		return e.spin.View() + " " + e.th.Muted.Render("Loading"+e.g.ellipsis)
	}
	if a.err != nil {
		return e.errText(a.err)
	}
	ls := a.render(w, h, a.val)
	a.scroll = min(a.scroll, max(0, len(ls)-h))
	return strings.Join(ls[a.scroll:], "\n")
}

func newStatusScreen(e *env) screen {
	return &asyncScreen{e: e, title: "Credentials Status",
		load: func(ctx context.Context) (any, error) { return e.svc.Status(ctx), nil },
		render: func(w, h int, v any) []string {
			st, _ := v.(engine.Status)
			return statusLines(e, st, w)
		}}
}

func statusLines(e *env, st engine.Status, w int) []string {
	t, g := e.th, e.g
	kv := func(k, v string) string {
		return "  " + t.Muted.Render(padRight(k, 16)) + t.Text.Render(e.trunc(v, w-20))
	}
	out := []string{e.label("CredOrbit"),
		kv("version", st.Version+"  "+g.sep+"  mode "+st.Mode),
		kv("config", st.ConfigFile), kv("aws config", st.AWSConfig), kv("credentials", st.Credentials),
		kv("profiles", itoa(st.Profiles)), kv("last run", ago(e.now(), st.LastRun)), "", e.label("Sessions")}
	for _, s := range st.Sessions {
		state := t.PillOff.Render(g.pillOff + " signed out")
		if s.LoggedIn {
			state = e.expiry(s.ExpiresAt)
		}
		out = append(out, "  "+e.fit(t.Text.Render(s.Name), 20)+e.fit(state, 16)+t.Muted.Render(itoa(s.Accounts)+" accounts "+g.sep+" discovered "+ago(e.now(), s.DiscoveredAt)))
	}
	ex := append([]engine.CredentialStatus(nil), st.Exported...)
	sort.SliceStable(ex, func(i, j int) bool {
		a, b := ex[i].Expires, ex[j].Expires
		if a.IsZero() != b.IsZero() {
			return b.IsZero()
		}
		return a.Before(b)
	})
	out = append(out, "", e.label("Exported credentials")+t.Muted.Render("  sorted by expiry "+g.sep+" "+g.dot+" = managed by CredOrbit"))
	for _, c := range ex {
		m := " "
		if c.Managed {
			m = t.Accent.Render(g.dot)
		}
		out = append(out, "  "+m+" "+e.fit(t.Text.Render(c.Section), 34)+e.expiry(c.Expires))
	}
	if len(st.Groups) > 0 {
		var gs []string
		for _, k := range sortedIntKeys(st.Groups) {
			gs = append(gs, k+" "+itoa(st.Groups[k]))
		}
		out = append(out, "", e.label("Groups"), "  "+t.Text.Render(strings.Join(gs, "   ")))
	}
	return out
}

func sortedIntKeys(m map[string]int) []string {
	k := make([]string, 0, len(m))
	for x := range m {
		k = append(k, x)
	}
	sort.Strings(k)
	return k
}

func newDoctorScreen(e *env) screen {
	return &asyncScreen{e: e, title: "Diagnostics",
		load: func(ctx context.Context) (any, error) { return e.svc.Doctor(ctx), nil },
		render: func(w, h int, v any) []string {
			cs, _ := v.([]engine.Check)
			return doctorLines(e, cs, w)
		}}
}

func doctorLines(e *env, cs []engine.Check, w int) []string {
	t, g := e.th, e.g
	var areas []string
	by := map[string][]engine.Check{}
	n := map[string]int{}
	for _, c := range cs {
		if _, ok := by[c.Area]; !ok {
			areas = append(areas, c.Area)
		}
		by[c.Area] = append(by[c.Area], c)
		n[c.Status]++
	}
	out := []string{t.Success.Render(g.ok+" "+itoa(n["ok"])+" ok") + "   " + t.Warning.Render(g.warn+" "+itoa(n["warn"])+" warnings") + "   " +
		t.Danger.Render(g.fail+" "+itoa(n["fail"])+" failures"), ""}
	for _, a := range areas {
		out = append(out, e.label(a))
		for _, c := range by[a] {
			out = append(out, "  "+e.statusMark(c.Status)+" "+e.fit(t.Text.Render(c.Name), 40)+" "+t.Muted.Render(e.trunc(c.Detail, max(4, w-46))))
			if c.Remedy != "" && c.Status != "ok" {
				out = append(out, "      "+t.Accent.Render(g.right+" "+e.trunc(c.Remedy, w-10)))
			}
		}
		out = append(out, "")
	}
	return out
}

func newUpdateScreen(e *env) screen {
	type upd struct {
		latest, url string
		newer       bool
	}
	return &asyncScreen{e: e, title: "Check for Updates",
		load: func(ctx context.Context) (any, error) {
			l, u, n, err := e.svc.CheckUpdate(ctx)
			return upd{l, u, n}, err
		},
		render: func(w, h int, v any) []string {
			t, g := e.th, e.g
			u, _ := v.(upd)
			out := []string{
				"  " + t.Muted.Render(padRight("installed", 12)) + t.Text.Render(e.svc.Version()),
				"  " + t.Muted.Render(padRight("latest", 12)) + t.Text.Render(e.orDash(u.latest)), ""}
			if u.newer {
				out = append(out, t.Accent.Bold(true).Render(g.stNew+" A newer version is available"), "  "+t.Accent.Render(u.url),
					"", t.Muted.Render("CredOrbit never installs updates itself; download the release or use your package manager."))
			} else {
				out = append(out, t.Success.Render(g.ok+" You are up to date"))
			}
			return out
		}}
}
