package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/credorbit/credorbit/internal/engine"
	"github.com/credorbit/credorbit/internal/model"
)

// cliUI implements engine.Interaction with line prompts on stderr.
// --yes approves plan reviews and non-destructive confirmations; destructive
// confirmations (default "no") need --allow-remove. Without a TTY and without
// --yes everything is declined with a hint naming the flag to pass.
type cliUI struct {
	mu          sync.Mutex
	in          *bufio.Reader
	out         io.Writer
	tty         bool
	yes         bool
	allowRemove bool
}

var errNonInteractive = errors.New("not an interactive terminal")

func (u *cliUI) readLine(ctx context.Context, prompt string) (string, error) {
	fmt.Fprint(u.out, prompt)
	type res struct {
		s   string
		err error
	}
	ch := make(chan res, 1)
	go func() {
		s, err := u.in.ReadString('\n')
		ch <- res{strings.TrimSpace(s), err}
	}()
	select {
	case <-ctx.Done():
		fmt.Fprintln(u.out)
		return "", ctx.Err()
	case r := <-ch:
		if r.err != nil && r.s == "" {
			return "", r.err
		}
		return r.s, nil
	}
}

func (u *cliUI) Confirm(ctx context.Context, title, detail string, def bool) (bool, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !def && u.allowRemove {
		fmt.Fprintf(u.out, "%s yes (--allow-remove)\n", title)
		return true, nil
	}
	if u.yes {
		if !def {
			fmt.Fprintf(u.out, "%s no — destructive changes need --allow-remove\n", title)
		}
		return def, nil
	}
	if !u.tty {
		hint := "--yes"
		if !def {
			hint = "--allow-remove"
		}
		fmt.Fprintf(u.out, "%s declined (non-interactive; pass %s)\n", title, hint)
		return false, nil
	}
	if detail != "" {
		fmt.Fprintln(u.out, indentLines(detail))
	}
	q := " [y/N]: "
	if def {
		q = " [Y/n]: "
	}
	ans, err := u.readLine(ctx, title+q)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(ans) {
	case "":
		return def, nil
	case "y", "yes":
		return true, nil
	}
	return false, nil
}

func (u *cliUI) ReviewPlan(ctx context.Context, title, preview string) (bool, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	fmt.Fprintf(u.out, "%s:\n%s", title, indentLines(preview))
	if !strings.HasSuffix(preview, "\n") {
		fmt.Fprintln(u.out)
	}
	if u.yes {
		return true, nil
	}
	if !u.tty {
		fmt.Fprintln(u.out, "not applied: non-interactive — re-run with --yes to apply")
		return false, nil
	}
	ans, err := u.readLine(ctx, "Apply? [y/N]: ")
	if err != nil {
		return false, err
	}
	return strings.EqualFold(ans, "y") || strings.EqualFold(ans, "yes"), nil
}

func (u *cliUI) SelectAccounts(ctx context.Context, title string, items []engine.AccountRow, pre []string) ([]string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.tty || u.yes || len(items) == 0 {
		return pre, nil
	}
	sel := map[string]bool{}
	for _, p := range pre {
		sel[p] = true
	}
	fmt.Fprintln(u.out, title)
	for i, it := range items {
		mark := " "
		if sel[it.Account.ID] {
			mark = "*"
		}
		fmt.Fprintf(u.out, "  %s %3d) %-32s %s %s\n", mark, i+1, it.Account.Name, it.Account.ID, it.Env)
	}
	ans, err := u.readLine(ctx, `Select (e.g. "1,3-5", "all", "none"; empty keeps *): `)
	if err != nil {
		return nil, err
	}
	idx, err := parseSelection(ans, len(items))
	if err != nil {
		return nil, err
	}
	if idx == nil {
		return pre, nil
	}
	out := []string{}
	for _, i := range idx {
		out = append(out, items[i].Account.ID)
	}
	return out, nil
}

// parseSelection parses "1,3-5" / "all" / "none" into zero-based indexes.
// Empty input returns nil (keep preselection).
func parseSelection(s string, n int) ([]int, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	switch s {
	case "":
		return nil, nil
	case "none":
		return []int{}, nil
	case "all":
		out := make([]int, n)
		for i := range out {
			out[i] = i
		}
		return out, nil
	}
	seen := map[int]bool{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		lo, hi := part, part
		if a, b, ok := strings.Cut(part, "-"); ok {
			lo, hi = strings.TrimSpace(a), strings.TrimSpace(b)
		}
		l, err1 := strconv.Atoi(lo)
		h, err2 := strconv.Atoi(hi)
		if err1 != nil || err2 != nil || l < 1 || h > n || l > h {
			return nil, fmt.Errorf("invalid selection %q (valid: 1-%d)", part, n)
		}
		for i := l; i <= h; i++ {
			seen[i-1] = true
		}
	}
	out := make([]int, 0, len(seen))
	for i := range seen {
		out = append(out, i)
	}
	sort.Ints(out)
	return out, nil
}

func (u *cliUI) SelectRole(ctx context.Context, a model.DiscoveredAccount, current string) (string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.tty || u.yes {
		return "", errNonInteractive
	}
	fmt.Fprintf(u.out, "Roles for %s (%s):\n", a.Name, a.ID)
	for i, r := range a.Roles {
		mark := " "
		if r == current {
			mark = "*"
		}
		fmt.Fprintf(u.out, "  %s %d) %s\n", mark, i+1, r)
	}
	ans, err := u.readLine(ctx, "Role number (empty = skip): ")
	if err != nil || ans == "" {
		return "", err
	}
	n, err := strconv.Atoi(ans)
	if err != nil || n < 1 || n > len(a.Roles) {
		return "", fmt.Errorf("invalid choice %q", ans)
	}
	return a.Roles[n-1], nil
}

func (u *cliUI) LoginPrompt(url, code string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	fmt.Fprintf(u.out, "To sign in, open:\n  %s\n", url)
	if code != "" {
		fmt.Fprintf(u.out, "and confirm the code: %s\n", code)
	}
	fmt.Fprintln(u.out, "Waiting for sign-in (Ctrl-C to cancel)...")
}

func indentLines(s string) string {
	var b strings.Builder
	for _, l := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		b.WriteString("  " + l + "\n")
	}
	return b.String()
}

// progress renders engine events on stderr in the legacy script's format.
type progress struct {
	mu      sync.Mutex
	w       io.Writer
	verbose bool
	steps   bool // print workflow step lines (setup)
}

func errLabel(class string) string {
	switch class {
	case "no_access":
		return "no access"
	case "unauthorized":
		return "token expired"
	case "transient":
		return "transient"
	case "canceled":
		return "canceled"
	}
	return "error"
}

func (p *progress) Report(ev model.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case ev.Subject == engine.WorkflowSubject:
		if ev.Kind == model.EventStepDone && p.steps {
			mark := "ok  "
			switch {
			case ev.Err != nil:
				mark = "FAIL"
			case strings.HasPrefix(ev.Message, "skipped: "):
				mark = "skip"
			}
			fmt.Fprintf(p.w, "[%s] %-12s %s\n", mark, ev.Step, strings.TrimPrefix(ev.Message, "skipped: "))
		}
	case ev.Step == "export" && ev.Kind == model.EventItemOK:
		fmt.Fprintf(p.w, "[%d/%d] %s  done (%s)\n", ev.Done, ev.Total, ev.Subject, strings.TrimPrefix(ev.Message, "role "))
	case ev.Step == "export" && ev.Kind == model.EventItemFailed:
		fmt.Fprintf(p.w, "[%d/%d] %s  skipped: %s — %s\n", ev.Done, ev.Total, ev.Subject, errLabel(engine.ClassifyErr(ev.Err)), ev.Message)
	case ev.Kind == model.EventWarning:
		fmt.Fprintf(p.w, "warning: %s\n", ev.Message)
	case ev.Step == "discover" && ev.Kind == model.EventItemFailed:
		fmt.Fprintf(p.w, "discover: %s\n", ev.Message)
	case p.verbose && ev.Kind == model.EventInfo:
		fmt.Fprintf(p.w, "%s: %s\n", ev.Step, ev.Message)
	case p.verbose && ev.Step == "discover" && ev.Kind == model.EventStepDone:
		fmt.Fprintf(p.w, "discover: %s\n", ev.Message)
	}
}
