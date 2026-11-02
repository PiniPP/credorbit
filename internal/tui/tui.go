// Package tui is CredOrbit's interactive terminal UI (Bubble Tea). It is a
// thin front-end over engine.Service: every long operation runs in the
// background, progress arrives as model.Events, and engine decisions
// (engine.Interaction) are answered through modal dialogs.
package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/muesli/termenv"

	"github.com/PiniPP/credorbit/internal/engine"
	"github.com/PiniPP/credorbit/internal/model"
	"github.com/PiniPP/credorbit/internal/tui/demo"
)

// Run starts the full-screen UI and blocks until the user quits or ctx is canceled.
func Run(ctx context.Context, svc engine.Service, o engine.UIOptions) (err error) {
	if o.Demo || svc == nil {
		svc = demo.New()
	}
	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	e := newEnv(ctx, svc, os.Stdout, nil, nil)
	a := newApp(e, o.Screen, o.Group)
	p := tea.NewProgram(a, tea.WithAltScreen(), tea.WithContext(ctx), tea.WithFPS(30))
	br := newBridge(p.Send, e)
	e.flush = br.flush
	svc.SetReporter(br)
	svc.SetInteraction(br)
	go br.loop(ctx)
	defer func() {
		br.close()
		cancel() // cancels every running operation and pending dialog
		svc.SetReporter(model.Discard)
		if r := recover(); r != nil {
			_ = p.ReleaseTerminal()
			err = fmt.Errorf("tui: internal error: %v", r)
		}
	}()

	_, err = p.Run()
	if errors.Is(err, tea.ErrProgramKilled) && parent.Err() != nil {
		return parent.Err()
	}
	return err
}

// staticLook is a fixed UTF-8 environment so RenderStatic is deterministic.
func staticLook(k string) (string, bool) {
	if k == "LANG" {
		return "en_US.UTF-8", true
	}
	return "", false
}

// RenderStatic renders one frame of a screen for docs, screenshots and tests.
// screen: "menu" (default), "accounts", "main", "groups", "rotate", "setup",
// "export-progress", "report", or any other UIOptions.Screen value. The result
// contains ANSI styling (true colour) unless theme is "mono".
func RenderStatic(svc engine.Service, screen string, width, height int, theme string, ascii bool) string {
	return renderStatic(svc, screen, width, height, theme, ascii, io.Discard)
}

func renderStatic(svc engine.Service, screen string, w, h int, theme string, ascii bool, out io.Writer) string {
	if svc == nil {
		svc = demo.New()
	}
	prof := termenv.TrueColor
	e := newEnv(context.Background(), svc, out, &prof, staticLook)
	e.th = newTheme(theme, newRenderer(out, theme == "mono", &prof))
	e.g = newGlyphs(ascii)
	e.spin.Spinner.Frames = e.g.spin
	e.spin.Style = e.th.Accent
	e.login, e.loginKnown = svc.LoginStatus(context.Background(), e.session), true
	e.w, e.h = w, h
	a := newApp(e, "", "")
	switch screen {
	case "", "menu":
	case "export-progress":
		a.stack = append(a.stack, sampleExport(e))
	case "report":
		wz := newWizard(e)
		wz.step, wz.report = wzReport, sampleReport()
		a.stack = append(a.stack, wz)
	default:
		if s := initialScreen(e, screen, ""); s != nil {
			a.stack = append(a.stack, s)
		}
	}
	return a.View()
}

// sampleExport builds an export screen mid-run from the service's accounts.
func sampleExport(e *env) *exportScreen {
	x := newExportScreen(e)
	x.scope, x.scopeLbl, x.phase = engine.Scope{Kind: engine.ScopeAll}, "All AWS accounts", exRun
	x.prog.reset("export")
	var profs []string
	for _, r := range e.svc.Accounts(e.session) {
		profs = append(profs, r.Profiles...)
	}
	total := len(profs)
	x.prog.apply(model.Event{Kind: model.EventStepStart, Step: "export", Message: "Fetching credentials for " + itoa(total) + " profiles", Total: total})
	done := total * 6 / 10
	for i := 0; i < done; i++ {
		ev := model.Event{Kind: model.EventItemOK, Step: "export", Subject: profs[i], Message: "AdministratorAccess " + e.g.sep + " valid 12h00m"}
		if i%7 == 5 {
			ev = model.Event{Kind: model.EventItemFailed, Step: "export", Subject: profs[i], Message: "ForbiddenException: No access", Err: model.ErrNoAccess}
		} else if i%11 == 9 {
			ev = model.Event{Kind: model.EventItemFailed, Step: "export", Subject: profs[i], Message: "rate exceeded (retried 3x)", Err: model.ErrTransient}
		}
		x.prog.apply(ev)
	}
	x.prog.apply(model.Event{Kind: model.EventProgress, Step: "export", Done: done, Total: total})
	e.labels[1<<30] = "Exporting credentials"
	e.cancels[1<<30] = func() {}
	return x
}

func sampleReport() *engine.Report {
	r := &engine.Report{Exported: 31, Failed: 3, Duration: 14*time.Second + 300*time.Millisecond,
		Backups:  []string{"~/.aws/config.credorbit-20261102-101500.bak", "~/.aws/credentials.credorbit-20261102-101514.bak"},
		Warnings: []string{"3 profiles failed; their previous credentials were kept"}}
	for _, s := range demo.FullSteps {
		st := engine.StepResult{Step: s, OK: true, Summary: "done"}
		switch s {
		case "discover":
			st.Summary = "38 accounts discovered"
		case "apply-sync":
			st.Summary = "4 changes applied"
		case "export":
			st.Summary = "31 ok · 3 failed"
		case "rotate", "verify-bedrock":
			st.Skipped, st.Summary = true, "rotation disabled"
		}
		r.Steps = append(r.Steps, st)
	}
	return r
}
