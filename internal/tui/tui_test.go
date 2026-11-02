package tui

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/PiniPP/credorbit/internal/engine"
	"github.com/PiniPP/credorbit/internal/model"
	"github.com/PiniPP/credorbit/internal/tui/demo"
)

func topIs[T any](t *testing.T, a *app) T {
	t.Helper()
	s, ok := a.top().(T)
	if !ok {
		t.Fatalf("top screen is %T", a.top())
	}
	return s
}

func TestMenuNavigation(t *testing.T) {
	h := newHarness(t, newRec(), "")
	m := topIs[*menu](t, h.a)
	h.press("down", "down")
	if m.cur != 2 {
		t.Fatalf("cur = %d, want 2", m.cur)
	}
	h.press("up")
	if m.cur != 1 {
		t.Fatalf("cur = %d, want 1", m.cur)
	}
	if !strings.Contains(h.view(), "Sign in to IAM Identity Center") {
		t.Fatal("focused item description not shown")
	}
	h.press("4")
	topIs[*browserScreen](t, h.a)
	h.press("esc")
	topIs[*menu](t, h.a)
	if m.cur != 3 {
		t.Fatalf("number jump should focus item 4, cur=%d", m.cur)
	}
	h.press("up", "up", "enter") // AWS SSO Login
	topIs[*loginScreen](t, h.a)
	h.press("esc")

	// filter
	h.press("/")
	h.typeText("rotat")
	if vis := m.visible(); len(vis) != 1 || m.items[vis[0]].title != "Claude / Bedrock Rotation" {
		t.Fatalf("filter visible = %v", vis)
	}
	h.press("enter")
	topIs[*rotateScreen](t, h.a)
	h.press("esc")
	if m.filter.Value() != "" || m.filtering {
		t.Fatal("filter should be cleared after enter")
	}
	for _, it := range []string{"Full Setup & Sync", "AWS SSO Login", "Manage Main Accounts", "Check for Updates", "Exit"} {
		if !strings.Contains(h.view(), it) {
			t.Fatalf("menu lacks %q", it)
		}
	}
	h.press("q")
	if !h.quit {
		t.Fatal("q should quit when idle")
	}
}

func TestMenuItemsOpenScreens(t *testing.T) {
	h := newHarness(t, newRec(), "")
	for i, it := range menuItems() {
		if it.open == nil {
			continue
		}
		s := it.open(h.e)
		if s.Title() == "" || len(s.KeyHints()) == 0 {
			t.Fatalf("item %d %q: empty title or hints", i, it.title)
		}
		h.update(pushMsg{s})
		h.settle()
		if out := h.view(); !strings.Contains(out, "? help") {
			t.Fatalf("%q: footer missing", it.title)
		}
		h.press("?")
		if !strings.Contains(h.view(), "EVERYWHERE") {
			t.Fatalf("%q: help overlay missing", it.title)
		}
		h.press("x") // closes help
		h.update(popMsg{})
		h.settle()
	}
}

func demoRows() []engine.AccountRow { return demo.New().Accounts("acme-sso") }

func TestBrowserSearchFilterSortSelect(t *testing.T) {
	e := newTestEnv(demo.New(), nil)
	b := newBrowser(e, modeBrowse)
	b.setRows(demoRows())
	if len(b.vis) != 40 {
		t.Fatalf("rows = %d", len(b.vis))
	}
	b.update(key("/"))
	for _, r := range "ledger" {
		b.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if len(b.vis) < 3 {
		t.Fatalf("search ledger = %d rows", len(b.vis))
	}
	for _, i := range b.vis[:3] { // fuzzy: best matches first
		if !strings.Contains(b.rows[i].Account.Name, "ledger") {
			t.Fatalf("unexpected match %s", b.rows[i].Account.Name)
		}
	}
	b.update(key("esc"))
	if b.searching || len(b.vis) != 40 {
		t.Fatal("esc should clear search")
	}
	// search by account id
	b.update(key("/"))
	for _, r := range "100000000017" {
		b.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	b.update(key("enter"))
	if c := b.current(); c == nil || c.Account.ID != "100000000017" {
		t.Fatalf("id search current = %+v", c)
	}
	b.update(key("c"))

	b.update(key("e")) // prd
	for _, i := range b.vis {
		if b.rows[i].Env != "prd" {
			t.Fatalf("env filter leaked %s", b.rows[i].Account.Name)
		}
	}
	nPrd := len(b.vis)
	b.update(key("s")) // available
	for _, i := range b.vis {
		if b.rows[i].Status != model.StatusAvailable {
			t.Fatal("status filter leaked")
		}
	}
	b.update(key("s")) // new
	b.update(key("e"))
	b.update(key("c"))
	if len(b.vis) != 40 || nPrd == 0 || nPrd == 40 {
		t.Fatalf("filters: prd=%d after clear=%d", nPrd, len(b.vis))
	}
	b.update(key("g")) // main
	if b.groupOpts[b.groupF] != "main" || len(b.vis) != 8 {
		t.Fatalf("group filter %s = %d", b.groupOpts[b.groupF], len(b.vis))
	}
	b.update(key("c"))

	// sort by id, then reverse
	b.update(key("o"))
	ids := func() []string {
		var out []string
		for _, i := range b.vis {
			out = append(out, b.rows[i].Account.ID)
		}
		return out
	}
	if !sort.StringsAreSorted(ids()) {
		t.Fatal("not sorted by id")
	}
	b.update(key("O"))
	if got := ids(); got[0] != "100000000040" {
		t.Fatalf("reverse id first = %s", got[0])
	}
	b.update(key("o")) // env
	b.update(key("o")) // status
	b.update(key("o")) // expiry
	b.update(key("o")) // name
	b.update(key("O"))

	// selection
	b.update(key(" "))
	if len(b.selected()) != 1 {
		t.Fatal("space should select one")
	}
	b.update(key("e")) // prd only
	b.update(key("a"))
	if got := len(b.selected()); got != nPrd+1 && got != nPrd {
		t.Fatalf("select all filtered = %d (prd %d)", got, nPrd)
	}
	b.update(key("n"))
	b.update(key("c"))
	if got := len(b.selected()); got > 1 {
		t.Fatalf("select none left %d", got)
	}
	b.update(key("i"))
	if got := len(b.selected()); got < 39 {
		t.Fatalf("invert = %d", got)
	}
	out := ansiStrip(b.view(96, 27))
	if !strings.Contains(out, "selected") || !strings.Contains(out, "ACCOUNT") {
		t.Fatal("view lacks header/status")
	}
}

func TestBrowserVirtualised(t *testing.T) {
	e := newTestEnv(demo.New(), nil)
	b := newBrowser(e, modeBrowse)
	var rows []engine.AccountRow
	for i := 0; i < 300; i++ {
		r := demoRows()[i%40]
		r.Account.ID = "9" + itoa(100000000000+i)
		r.Account.Name = r.Account.Name + "-" + itoa(i)
		rows = append(rows, r)
	}
	b.setRows(rows)
	out := ansiStrip(b.view(96, 20))
	if n := strings.Count(out, "\n") + 1; n != 20 {
		t.Fatalf("view has %d lines, want 20", n)
	}
	b.update(key("end"))
	out = ansiStrip(b.view(96, 20))
	if !strings.Contains(out, "of 300") {
		t.Fatalf("scroll indicator missing:\n%s", out)
	}
}

func TestGroupEditorSaveRestoreDiscard(t *testing.T) {
	svc := newRec()
	h := newHarness(t, svc, "main")
	g := topIs[*groupEditor](t, h.a)
	if len(g.b.saved) != 8 || g.b.dirty() {
		t.Fatalf("saved=%d dirty=%v", len(g.b.saved), g.b.dirty())
	}
	// first row (acme-analytics-prd) is not in main → add it
	first := g.b.current().Account.ID
	h.press(" ")
	if a, r := g.b.diff(); a != 1 || r != 0 {
		t.Fatalf("diff = +%d -%d", a, r)
	}
	if !strings.Contains(h.view(), "+1 -0 unsaved") && !strings.Contains(h.view(), "+1 −0 unsaved") {
		t.Fatalf("unsaved footer missing:\n%s", h.view())
	}
	h.press("ctrl+s")
	got := svc.groupSets["main"]
	if len(got) != 9 || !contains(got, first) {
		t.Fatalf("GroupSet(main) = %v", got)
	}
	if g.b.dirty() {
		t.Fatal("still dirty after save")
	}
	// remove it again, then restore
	h.press("home", " ")
	if !g.b.dirty() {
		t.Fatal("toggle should make dirty")
	}
	h.press("u")
	if g.b.dirty() {
		t.Fatal("u should restore saved selection")
	}
	// esc with unsaved changes asks; "n" keeps the editor, "y" discards
	h.press(" ", "esc")
	if len(h.a.dialogs) != 1 {
		t.Fatal("expected discard confirmation")
	}
	h.press("n")
	topIs[*groupEditor](t, h.a)
	h.press("esc", "y")
	topIs[*menu](t, h.a)
	if len(svc.groupSets["main"]) != 9 {
		t.Fatal("discard must not save")
	}
}

func TestGroupEditorNewAccountsBanner(t *testing.T) {
	svc := newRec()
	h := newHarness(t, svc, "main")
	g := topIs[*groupEditor](t, h.a)
	if !strings.Contains(h.view(), "4 newly discovered accounts") {
		t.Fatal("new accounts banner missing")
	}
	h.press("v", "down", "enter") // add all to Main (pending)
	if a, _ := g.b.diff(); a != 4 {
		t.Fatalf("pending additions = %d", a)
	}
	h.press("v", "enter") // review: filter status=new
	if statusOpts[g.b.statusF] != "new" || len(g.b.vis) != 4 {
		t.Fatalf("review filter %s rows=%d", statusOpts[g.b.statusF], len(g.b.vis))
	}
	// role choice via enter
	h.press("enter")
	if len(h.a.dialogs) != 1 {
		t.Fatal("role dialog expected")
	}
	h.press("enter")
	if len(svc.roles) != 1 {
		t.Fatalf("SetPreferredRole calls = %v", svc.roles)
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func TestScopePicker(t *testing.T) {
	h := newHarness(t, newRec(), "")
	var got engine.Scope
	var label string
	sp := newScopePicker(h.e, func(s engine.Scope, l string) tea.Cmd { got, label = s, l; return nil })
	run := func(cmd tea.Cmd) { h.exec(cmd); h.settle() }

	sp.cur = 0
	run(sp.update(key("enter")))
	if got.Kind != engine.ScopeAll {
		t.Fatalf("all: %+v", got)
	}
	run(sp.update(key("down")))
	run(sp.update(key("enter")))
	if got.Kind != engine.ScopeGroup || got.Group != "main" {
		t.Fatalf("main: %+v", got)
	}
	run(sp.update(key("down")))
	run(sp.update(key("enter"))) // group list dialog
	h.press("down", "enter")     // second group (dev)
	if got.Kind != engine.ScopeGroup || got.Group != "dev" {
		t.Fatalf("custom group: %+v (%s)", got, label)
	}
	run(sp.update(key("down")))
	run(sp.update(key("enter"))) // manual picker screen
	topIs[*pickerScreen](t, h.a)
	h.press(" ", " ", "enter")
	if got.Kind != engine.ScopeSelected || len(got.AccountIDs) != 2 || len(got.Profiles) != 2 {
		t.Fatalf("manual: %+v", got)
	}
	topIs[*menu](t, h.a)
	run(sp.update(key("down")))
	run(sp.update(key("enter"))) // single
	h.press("down", "enter")
	if got.Kind != engine.ScopeSingle || len(got.AccountIDs) != 1 || len(got.Profiles) != 1 {
		t.Fatalf("single: %+v", got)
	}
}

func TestBridgeDialogs(t *testing.T) {
	svc := newRec()
	h := newHarness(t, svc, "")
	ctx := context.Background()
	type res struct {
		v   any
		err error
	}
	ask := func(f func() (any, error), keys ...string) res {
		ch := make(chan res, 1)
		go func() { v, err := f(); ch <- res{v, err} }()
		if !h.until(func() bool { return len(h.a.dialogs) > 0 }, 2*time.Second) {
			t.Fatal("dialog did not open")
		}
		h.press(keys...)
		select {
		case r := <-ch:
			return r
		case <-time.After(2 * time.Second):
			t.Fatal("interaction did not return")
		}
		return res{}
	}

	r := ask(func() (any, error) { return h.br.Confirm(ctx, "Proceed?", "detail", false) }, "y")
	if r.v != true || r.err != nil {
		t.Fatalf("confirm = %+v", r)
	}
	r = ask(func() (any, error) { return h.br.Confirm(ctx, "Proceed?", "detail", true) }, "esc")
	if r.v != false {
		t.Fatalf("confirm esc = %+v", r)
	}
	r = ask(func() (any, error) {
		return h.br.ReviewPlan(ctx, "Plan", "+ [profile p_new]\n- [profile p_old]\n~ [profile p_x] sso_role_name: A -> B")
	}, "a")
	if r.v != true {
		t.Fatalf("review = %+v", r)
	}
	if !strings.Contains(ansiStrip(newPlanDialog(h.e, "t", "+ a\n- b", nil).View(90, 20)), "+1 add") {
		t.Fatal("plan dialog counts missing")
	}
	rows := svc.Accounts("acme-sso")
	r = ask(func() (any, error) {
		return h.br.SelectAccounts(ctx, "Pick", rows, []string{rows[1].Account.ID})
	}, " ", "enter")
	ids, _ := r.v.([]string)
	if len(ids) != 2 || !contains(ids, rows[0].Account.ID) || !contains(ids, rows[1].Account.ID) {
		t.Fatalf("select accounts = %v", ids)
	}
	r = ask(func() (any, error) { return h.br.SelectAccounts(ctx, "Pick", rows, nil) }, "esc")
	if !errors.Is(r.err, model.ErrCanceled) {
		t.Fatalf("select esc err = %v", r.err)
	}
	r = ask(func() (any, error) { return h.br.SelectRole(ctx, rows[0].Account, rows[0].Account.Roles[0]) }, "down", "enter")
	if r.v != rows[0].Account.Roles[1] {
		t.Fatalf("select role = %v", r.v)
	}

	// timeout: nobody answers, ctx expires, dialog is withdrawn
	tctx, cancel := context.WithTimeout(ctx, 80*time.Millisecond)
	defer cancel()
	start := time.Now()
	ok, err := h.br.Confirm(tctx, "never answered", "", true)
	if ok || !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("timeout confirm = %v %v", ok, err)
	}
	if !h.until(func() bool { return len(h.a.dialogs) == 0 }, time.Second) {
		t.Fatal("dialog not closed after ctx timeout")
	}

	// LoginPrompt is non-blocking and closes with the operation
	h.br.LoginPrompt("https://device.example/verify", "ABCD-EFGH")
	h.settle()
	if _, ok := h.a.dialogs[0].d.(*loginDialog); !ok || !strings.Contains(h.view(), "ABCD-EFGH") {
		t.Fatal("login prompt not shown")
	}
	h.update(opDoneMsg{id: 12345})
	if len(h.a.dialogs) != 0 {
		t.Fatal("login prompt should close when an operation finishes")
	}

	// after close every interaction fails fast
	h.br.close()
	if _, err := h.br.Confirm(ctx, "x", "", true); !errors.Is(err, model.ErrCanceled) {
		t.Fatalf("closed bridge err = %v", err)
	}
}

func TestReporterBatchesEvents(t *testing.T) {
	var got []eventsMsg
	b := newBridge(func(m tea.Msg) { got = append(got, m.(eventsMsg)) }, nil)
	for i := 0; i < 500; i++ {
		b.Report(model.Event{Kind: model.EventProgress, Done: i, Total: 500})
	}
	b.flush()
	b.flush()
	if len(got) != 1 || len(got[0]) != 500 {
		t.Fatalf("batches = %d", len(got))
	}
}

func TestDisplayDetection(t *testing.T) {
	env := func(kv ...string) lookupFunc {
		m := map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
	}
	cases := []struct {
		look      lookupFunc
		setTheme  string
		setASCII  string
		wantTheme string
		wantASCII bool
	}{
		{env("LANG", "en_US.UTF-8"), "", "", "default", false},
		{env("LANG", "en_US.UTF-8", "NO_COLOR", ""), "light", "", "mono", false},
		{env("LANG", "en_US.UTF-8", "NO_COLOR", "1"), "", "", "mono", false},
		{env("LANG", "C"), "", "", "default", true},
		{env("LC_ALL", "POSIX", "LANG", "en_US.UTF-8"), "", "", "default", true},
		{env("LANG", "C", "WT_SESSION", "abc"), "", "", "default", false},
		{env("LANG", "en_US.UTF-8", "CREDORBIT_ASCII", "1"), "", "", "default", true},
		{env("LANG", "en_US.UTF-8"), "light", "true", "light", true},
	}
	for i, c := range cases {
		th, a := detectDisplay(c.setTheme, c.setASCII, c.look)
		if th != c.wantTheme || a != c.wantASCII {
			t.Errorf("case %d: got %s/%v want %s/%v", i, th, a, c.wantTheme, c.wantASCII)
		}
	}
	// NO_COLOR through the env → mono theme without colour sequences
	e := newTestEnv(demo.New(), env("LANG", "en_US.UTF-8", "NO_COLOR", ""))
	if !e.th.mono || e.th.bgSeq != "" {
		t.Fatal("NO_COLOR must select the mono theme")
	}
	out := RenderStatic(demo.New(), "accounts", 100, 32, "mono", false)
	if strings.Contains(out, "38;2;") || strings.Contains(out, "48;2;") {
		t.Fatal("mono output contains colour sequences")
	}
	if !strings.Contains(out, "\x1b[7") && !strings.Contains(out, ";7m") {
		t.Fatal("mono selection should use reverse video")
	}
	if !strings.Contains(RenderStatic(demo.New(), "menu", 100, 32, "default", false), "48;2;11;16;32") {
		t.Fatal("default theme should paint the #0B1020 background")
	}
}

var allScreens = []string{"menu", "accounts", "main", "groups", "rotate", "setup", "export-progress", "report"}

func TestASCIIModeIsSevenBit(t *testing.T) {
	for _, s := range allScreens {
		for _, th := range []string{"default", "light", "mono"} {
			out := ansiStrip(RenderStatic(demo.New(), s, 100, 32, th, true))
			for i, r := range out {
				if r > 127 {
					t.Fatalf("%s/%s: non-ASCII rune %q at %d:\n%s", s, th, r, i, out)
				}
			}
		}
	}
	// a live screen with dialogs and engine text stays 7-bit too
	h := newHarness(t, newRec(), "accounts")
	h.e.g = newGlyphs(true)
	h.press("enter")
	for _, r := range h.view() {
		if r > 127 {
			t.Fatalf("dialog frame non-ASCII rune %q", r)
		}
	}
}

func TestFramesAlignedAtAllSizes(t *testing.T) {
	sizes := [][2]int{{30, 10}, {49, 20}, {60, 14}, {50, 15}, {60, 20}, {69, 24}, {70, 24}, {80, 24}, {99, 30}, {100, 32}, {140, 45}, {220, 60}}
	for _, s := range allScreens {
		for _, sz := range sizes {
			for _, ascii := range []bool{false, true} {
				out := ansiStrip(RenderStatic(demo.New(), s, sz[0], sz[1], "default", ascii))
				ls := strings.Split(out, "\n")
				if len(ls) != sz[1] {
					t.Fatalf("%s %dx%d ascii=%v: %d lines", s, sz[0], sz[1], ascii, len(ls))
				}
				for i, l := range ls {
					if width(l) != sz[0] {
						t.Fatalf("%s %dx%d ascii=%v: line %d width %d:\n%s", s, sz[0], sz[1], ascii, i, width(l), out)
					}
				}
				if sz[0] < 50 || sz[1] < 15 {
					if !strings.Contains(out, "Terminal too small") {
						t.Fatalf("%s %dx%d: missing too-small notice", s, sz[0], sz[1])
					}
				}
			}
		}
	}
	if !strings.Contains(RenderStatic(demo.New(), "menu", 100, 32, "default", false), "C R E D O R B I T") {
		t.Fatal("wide header lacks wordmark")
	}
	narrow := ansiStrip(RenderStatic(demo.New(), "menu", 60, 20, "default", false))
	if strings.Contains(narrow, "C R E D O R B I T") || !strings.Contains(strings.Split(narrow, "\n")[0], "CREDORBIT") {
		t.Fatal("narrow header should compact to one line")
	}
	if !strings.Contains(RenderStatic(demo.New(), "accounts", 100, 32, "default", false), "CACHED") {
		t.Fatal("cached badge missing")
	}
}

func TestBrandMark(t *testing.T) {
	for _, u := range []bool{true, false} {
		m := brandMark(u)
		if len(m) > 7 {
			t.Fatalf("mark has %d lines", len(m))
		}
		for _, l := range m {
			if width(l) > 22 || width(l) != width(m[0]) {
				t.Fatalf("ragged/too wide mark line %q", l)
			}
			if !u {
				for _, r := range l {
					if r > 127 {
						t.Fatalf("ASCII mark has %q", r)
					}
				}
			}
		}
	}
}

func TestSettingsValidationAndLiveTheme(t *testing.T) {
	svc := newRec()
	h := newHarness(t, svc, "")
	h.update(pushMsg{newSettingsScreen(h.e)})
	h.settle()
	s := topIs[*settingsScreen](t, h.a)
	idx := func(k string) int {
		for i, st := range s.list {
			if st.Key == k {
				return i
			}
		}
		t.Fatalf("no setting %s", k)
		return -1
	}
	s.cur = idx("export.concurrency")
	h.press("enter", "ctrl+u")
	h.typeText("999")
	h.press("enter")
	f, ok := h.a.dialogs[0].d.(*formDialog)
	if !ok || !strings.Contains(f.err, "between 1 and 64") || !strings.Contains(h.view(), "between 1 and 64") {
		t.Fatalf("inline validation error missing (%v)", ok)
	}
	h.press("ctrl+u")
	h.typeText("4")
	h.press("enter")
	if len(h.a.dialogs) != 0 || svc.settings["export.concurrency"] != "4" {
		t.Fatalf("setting not saved: %v", svc.settings)
	}
	s.cur = idx("ui.theme")
	h.press("enter", "down", "enter") // default → light
	if h.e.th.name != "light" {
		t.Fatalf("theme = %s, want light (live)", h.e.th.name)
	}
}

func TestDemoRunFullEndToEnd(t *testing.T) {
	svc := newRec()
	h := newHarness(t, svc, "setup")
	h.auto = true
	w := topIs[*wizard](t, h.a)
	h.press("enter") // session acme-sso
	h.press("enter") // normal mode
	h.press("enter") // scope: Main Accounts (default)
	if w.step != wzOptions || w.scope.Group != "main" {
		t.Fatalf("step=%d scope=%+v", w.step, w.scope)
	}
	h.press("s")
	if !h.until(func() bool { return w.step == wzReport }, 10*time.Second) {
		t.Fatalf("run did not finish; step=%d", w.step)
	}
	if w.err != nil || w.report == nil || len(w.report.Steps) != len(demo.FullSteps) || w.report.Exported == 0 {
		t.Fatalf("report = %+v err=%v", w.report, w.err)
	}
	if len(w.steps) < 15 {
		t.Fatalf("live steps = %d", len(w.steps))
	}
	out := h.view()
	for _, want := range []string{"Setup complete", "exported", "BACKUPS"} {
		if !strings.Contains(out, want) {
			t.Fatalf("report view lacks %q:\n%s", want, out)
		}
	}
	if h.e.busy() {
		t.Fatal("operation still registered")
	}
}

func TestRunCancelShowsPartialReport(t *testing.T) {
	svc := newRec()
	svc.SetSpeed(0.5)
	h := newHarness(t, svc, "setup")
	h.auto = true
	w := topIs[*wizard](t, h.a)
	h.press("enter", "enter", "enter", "s")
	if !h.until(func() bool { return len(w.steps) >= 3 }, 5*time.Second) {
		t.Fatal("run did not start")
	}
	h.press("ctrl+c")
	if h.quit {
		t.Fatal("ctrl+c during a run must cancel, not quit")
	}
	if !h.until(func() bool { return w.step == wzReport }, 5*time.Second) {
		t.Fatal("no report after cancel")
	}
	if !errors.Is(w.err, model.ErrCanceled) || w.report == nil || len(w.report.Steps) >= len(demo.FullSteps) {
		t.Fatalf("partial report err=%v", w.err)
	}
	if !strings.Contains(h.view(), "Canceled") {
		t.Fatal("canceled banner missing")
	}
}

func TestLoginShowsPromptAndExportProgress(t *testing.T) {
	svc := newRec()
	svc.SetSpeed(0.2)
	h := newHarness(t, svc, "")
	h.update(pushMsg{newLoginScreen(h.e)})
	h.settle()
	l := topIs[*loginScreen](t, h.a)
	h.press("down", "d") // acme-sandbox is signed out; device flow
	sawPrompt := h.until(func() bool {
		for _, d := range h.a.dialogs {
			if _, ok := d.d.(*loginDialog); ok {
				return strings.Contains(h.view(), "DEMO-7QX4")
			}
		}
		return false
	}, 3*time.Second)
	if !sawPrompt {
		t.Fatal("login prompt with user code not shown")
	}
	if !h.until(func() bool { return l.opID == 0 && strings.Contains(l.result, "Signed in") }, 3*time.Second) {
		t.Fatalf("login result = %q", l.result)
	}
	if len(h.a.dialogs) != 0 {
		t.Fatal("prompt must close after login")
	}

	// export with live progress, then summary
	h.update(popMsg{})
	x := newExportScreen(h.e)
	h.update(pushMsg{x})
	h.settle()
	h.press("enter") // Main Accounts
	h.press("s")
	if !h.until(func() bool { return x.prog.ok > 0 && x.phase == exRun }, 3*time.Second) && x.phase != exDone {
		t.Fatal("no live progress")
	}
	if !h.until(func() bool { return x.phase == exDone }, 5*time.Second) {
		t.Fatal("export did not finish")
	}
	if x.res == nil || x.res.Succeeded != 8 || !strings.Contains(h.view(), "Export finished") {
		t.Fatalf("export result %+v", x.res)
	}
}

func TestQuitConfirmWhileBusy(t *testing.T) {
	svc := newRec()
	svc.SetSpeed(1)
	h := newHarness(t, svc, "")
	h.update(pushMsg{newDoctorScreen(h.e)})
	if !h.e.busy() {
		t.Fatal("doctor should be loading")
	}
	h.update(key("q"))
	h.settle()
	if h.quit || len(h.a.dialogs) != 1 {
		t.Fatal("q while busy must ask first")
	}
	h.press("y")
	if !h.quit {
		t.Fatal("confirmed quit should quit")
	}
}

func TestDemoServiceIsSafe(t *testing.T) {
	d := demo.New()
	d.SetSpeed(0)
	ctx := context.Background()
	if _, _, err := d.Rotate(ctx, engine.RotateOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, sh := range []string{"zsh", "bash", "fish", "powershell"} {
		s, err := d.ShellIntegration(sh)
		if err != nil || s == "" {
			t.Fatalf("%s: %v", sh, err)
		}
	}
	if err := d.DeleteGroup("main"); err == nil {
		t.Fatal("main must not be deletable")
	}
	if len(d.Doctor(ctx)) == 0 || d.Status(ctx).Version == "" {
		t.Fatal("doctor/status empty")
	}
	if p, empty, _ := d.PlanSync(ctx, "acme-sso", engine.SyncOptions{RemoveStale: true}); empty || !strings.Contains(p, "+ [profile") || !strings.Contains(p, "- [profile") {
		t.Fatalf("plan = %q", p)
	}
}
