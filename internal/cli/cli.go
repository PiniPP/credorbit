// Package cli is the Cobra command-line front-end. Every command is a thin
// wrapper over engine.Service; no business logic lives here.
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/credorbit/credorbit/internal/engine"
	"github.com/credorbit/credorbit/internal/fsutil"
	"github.com/credorbit/credorbit/internal/model"
	"github.com/credorbit/credorbit/internal/paths"
)

// Version is set at build time: -ldflags "-X github.com/credorbit/credorbit/internal/cli.Version=v1.0.0".
var Version = "dev"

// RunUI starts the TUI. It is set by main when the TUI is linked in.
var RunUI func(ctx context.Context, svc engine.Service, o engine.UIOptions) error

// Exit codes.
const (
	ExitOK         = 0
	ExitError      = 1
	ExitUsage      = 2
	ExitPartial    = 3
	ExitNeedsLogin = 4
	ExitCanceled   = 130
)

// Test hooks.
var (
	newService = func(o engine.Options) (engine.Service, error) { return engine.New(o) }
	engineHook func(*engine.Options)
	stdin      io.Reader = os.Stdin
	isTTY                = func(out io.Writer) bool {
		f, ok := out.(*os.File)
		return ok && term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(f.Fd()))
	}
)

type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string {
	if e.err == nil {
		return ""
	}
	return e.err.Error()
}
func (e *exitError) Unwrap() error { return e.err }

func usageErr(err error) error { return &exitError{code: ExitUsage, err: err} }
func usagef(format string, a ...any) error {
	return usageErr(fmt.Errorf(format, a...))
}

// exitCode maps an error to the process exit code.
func exitCode(ctx context.Context, err error) int {
	var ee *exitError
	switch {
	case err == nil:
		return ExitOK
	case errors.As(err, &ee):
		return ee.code
	case errors.Is(err, model.ErrUnauthorized):
		return ExitNeedsLogin
	case errors.Is(err, engine.ErrDeclined):
		return ExitError
	case ctx.Err() != nil, errors.Is(err, context.Canceled), errors.Is(err, model.ErrCanceled):
		return ExitCanceled
	}
	msg := err.Error()
	for _, p := range []string{"unknown command", "unknown flag", "unknown shorthand flag", "flag needs an argument", "invalid argument", "accepts ", "requires at least", "requires at most"} {
		if strings.HasPrefix(msg, p) {
			return ExitUsage
		}
	}
	return ExitError
}

type app struct {
	ctx            context.Context
	stdout, stderr io.Writer
	in             *bufio.Reader
	tty            bool

	session, configDir                            string
	verbose, noColor, ascii, yes, jsonOut, compat bool
	noExtraCA                                     bool

	ui   *cliUI
	prog *progress
	svc  engine.Service
}

// Execute runs the CLI and returns the exit code.
func Execute(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	a := &app{ctx: ctx, stdout: stdout, stderr: stderr, in: bufio.NewReader(stdin), tty: isTTY(stdout)}
	root := a.rootCmd()
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetIn(stdin)
	err := root.ExecuteContext(ctx)
	code := exitCode(ctx, err)
	if err != nil {
		var ee *exitError
		if !errors.As(err, &ee) || ee.err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
		}
		if code == ExitUsage {
			fmt.Fprintln(stderr, "run 'credorbit --help' for usage")
		}
		if code == ExitNeedsLogin {
			fmt.Fprintln(stderr, "sign in with: credorbit login")
		}
	}
	return code
}

// service lazily builds the engine with the global flags applied.
func (a *app) service() (engine.Service, error) {
	if a.svc != nil {
		return a.svc, nil
	}
	p, err := paths.Resolve()
	if err != nil {
		return nil, err
	}
	if a.configDir != "" {
		d := fsutil.ExpandHome(a.configDir)
		if abs, err := filepath.Abs(d); err == nil {
			d = abs
		}
		p.AppConfigDir = d
		p.AppConfigFile = filepath.Join(d, "config.yaml")
		p.AppStateFile = filepath.Join(d, "state.json")
		p.AppLogFile = filepath.Join(d, "credorbit.log")
	}
	a.ui = &cliUI{in: a.in, out: a.stderr, tty: a.tty, yes: a.yes}
	a.prog = &progress{w: a.stderr, verbose: a.verbose}
	o := engine.Options{
		Paths:       p,
		Version:     Version,
		Interaction: a.ui,
		Reporter:    a.prog,
		Verbose:     a.verbose,
		Compat:      a.compat,
		NoExtraCA:   a.noExtraCA,
		OpenBrowser: openBrowser,
	}
	if engineHook != nil {
		engineHook(&o)
	}
	svc, err := newService(o)
	if err != nil {
		return nil, err
	}
	a.svc = svc
	return svc, nil
}

func (a *app) printJSON(v any) error {
	enc := json.NewEncoder(a.stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func (a *app) out(format string, args ...any) { fmt.Fprintf(a.stdout, format, args...) }
func (a *app) note(format string, args ...any) {
	fmt.Fprintf(a.stderr, format, args...)
}

func (a *app) rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "credorbit",
		Short:         "CredOrbit — Your Cloud Access. In Orbit.",
		Long:          "CredOrbit manages AWS IAM Identity Center (SSO) sessions, profiles and temporary credentials.\nRun without arguments on a terminal to open the interactive UI.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if a.tty && RunUI != nil {
				svc, err := a.service()
				if err != nil {
					return err
				}
				return RunUI(a.ctx, svc, engine.UIOptions{})
			}
			return cmd.Help()
		},
		PersistentPreRun: func(*cobra.Command, []string) {
			if a.noColor {
				_ = os.Setenv("NO_COLOR", "1")
			}
			if a.ascii {
				_ = os.Setenv("CREDORBIT_ASCII", "1")
			}
		},
	}
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageErr(err) })
	pf := root.PersistentFlags()
	pf.StringVar(&a.session, "session", "", "SSO session name (default: sso.default_session / SSO_SESSION)")
	pf.StringVar(&a.configDir, "config", "", "CredOrbit config directory (default: CREDORBIT_CONFIG_DIR or ~/.config/credorbit)")
	pf.BoolVarP(&a.verbose, "verbose", "v", false, "verbose output and debug logging")
	pf.BoolVar(&a.noColor, "no-color", false, "disable colors (NO_COLOR is honoured too)")
	pf.BoolVar(&a.ascii, "ascii", false, "ASCII-only rendering")
	pf.BoolVarP(&a.yes, "yes", "y", false, "approve non-destructive changes without asking")
	pf.BoolVar(&a.jsonOut, "json", false, "machine-readable JSON output (where supported)")
	pf.BoolVar(&a.compat, "compat", false, "script-compatible mode for this run")
	pf.BoolVar(&a.noExtraCA, "no-extra-ca", false, "ignore network.extra_cas for this run (legacy SASE_CA=none)")

	root.AddCommand(
		a.uiCmd(), a.setupCmd(), a.loginCmd(), a.logoutCmd(), a.discoverCmd(), a.accountsCmd(),
		a.syncCmd(), a.exportCmd(), a.statusCmd(), a.doctorCmd(), a.groupsCmd(), a.rolesCmd(),
		a.rotateCmd(), a.importCmd(), a.configCmd(), a.caCmd(), a.versionCmd(), a.updateCmd(),
	)
	return root
}

// exactArgs / rangeArgs wrap cobra validators so violations exit with code 2.
func exactArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := cobra.ExactArgs(n)(cmd, args); err != nil {
			return usageErr(err)
		}
		return nil
	}
}

func rangeArgs(lo, hi int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := cobra.RangeArgs(lo, hi)(cmd, args); err != nil {
			return usageErr(err)
		}
		return nil
	}
}

func minArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := cobra.MinimumNArgs(n)(cmd, args); err != nil {
			return usageErr(err)
		}
		return nil
	}
}

// parent returns a command that only groups subcommands.
func parent(use, short string, subs ...*cobra.Command) *cobra.Command {
	c := &cobra.Command{Use: use, Short: short, RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 {
			return usagef("unknown command %q for %q", args[0], cmd.CommandPath())
		}
		return cmd.Help()
	}}
	c.AddCommand(subs...)
	return c
}
