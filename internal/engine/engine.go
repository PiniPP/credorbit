package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PiniPP/credorbit/internal/awsini"
	"github.com/PiniPP/credorbit/internal/fsutil"
	"github.com/PiniPP/credorbit/internal/model"
	"github.com/PiniPP/credorbit/internal/netx"
	"github.com/PiniPP/credorbit/internal/paths"
	"github.com/PiniPP/credorbit/internal/redact"
	"github.com/PiniPP/credorbit/internal/roles"
	"github.com/PiniPP/credorbit/internal/sso"
	"github.com/PiniPP/credorbit/internal/state"
)

var _ Service = (*Engine)(nil)

// ErrDeclined is returned when the user (or the non-interactive default)
// declined a reviewed change. It wraps model.ErrCanceled.
var ErrDeclined = fmt.Errorf("change declined: %w", model.ErrCanceled)

// ErrNeedsLogin wraps model.ErrUnauthorized with the remedy.
func needsLogin(session string, err error) error {
	return fmt.Errorf("SSO session %q is not logged in (run: credorbit login --session %s): %w", session, session, errors.Join(model.ErrUnauthorized, err))
}

// UpdateCheckFunc looks up the latest release of repo.
type UpdateCheckFunc func(ctx context.Context, hc *http.Client, repo, current string) (latest, url string, newer bool, err error)

// ProbeFunc probes url with the configured client and with system roots only.
type ProbeFunc func(ctx context.Context, hc *http.Client, url string) (configured, systemOnly netx.ProbeResult)

// Options configure New.
type Options struct {
	Paths       paths.Paths // zero → paths.Resolve()
	Version     string
	Interaction Interaction    // nil → non-interactive defaults (see defaultInteraction)
	Reporter    model.Reporter // nil → discard
	Logger      *slog.Logger   // nil → JSON log file in the app config dir
	Verbose     bool           // debug-level logging
	Now         func() time.Time
	Env         func(string) string // nil → os.Getenv
	// Compat forces script-compatible mode for this process (not persisted).
	Compat bool
	// NoExtraCA ignores network.extra_cas for this process (legacy SASE_CA=none).
	NoExtraCA bool

	// SSOClient builds the portal client (nil → sso.NewClient).
	SSOClient func(region string, hc *http.Client) sso.Client
	// OIDCEndpoint / PortalEndpoint override the SSO endpoints used by the
	// authenticator (tests, VPC endpoints).
	OIDCEndpoint   string
	PortalEndpoint string
	// NewAuthenticator fully replaces authenticator construction (advanced).
	NewAuthenticator func(hc *http.Client, cache sso.TokenCache) *sso.Authenticator
	// BedrockEndpoint overrides the Bedrock runtime endpoint for rotation verification.
	BedrockEndpoint string
	UpdateCheck     UpdateCheckFunc
	OpenBrowser     func(string) error
	// Probe replaces the network probe (tests); nil → netx.Probe + netx.ProbeWithSystemOnly.
	Probe ProbeFunc
}

// Engine implements Service over the real files and AWS endpoints.
type Engine struct {
	opts  Options
	paths paths.Paths
	log   *slog.Logger
	now   func() time.Time
	env   func(string) string

	hc      *http.Client
	netInfo netx.Info
	netErr  error

	mu    sync.Mutex // guards cfg, st, live, added, roleChoice, ui, rep
	cfg   *state.Config
	st    *state.State
	live  map[string]*model.Discovery // live discoveries of this process, by session
	added map[string][]string         // session → profiles added by ApplySync in this process
	// roleChoice remembers roles picked interactively for new accounts (accountID → role).
	roleChoice map[string]string
	cfgWarns   []string
	stateWarn  string
	ui         Interaction
	rep        model.Reporter
}

// New loads configuration and state and prepares the HTTP client.
func New(o Options) (*Engine, error) {
	e := &Engine{opts: o, live: map[string]*model.Discovery{}, added: map[string][]string{}, roleChoice: map[string]string{}}
	e.env = o.Env
	if e.env == nil {
		e.env = os.Getenv
	}
	e.now = o.Now
	if e.now == nil {
		e.now = time.Now
	}
	e.paths = o.Paths
	if e.paths.AWSConfig == "" {
		p, err := paths.Resolve()
		if err != nil {
			return nil, fmt.Errorf("resolve paths: %w", err)
		}
		e.paths = p
	}
	e.log = o.Logger
	if e.log == nil {
		e.log = newFileLogger(e.paths.AppLogFile, o.Verbose)
	}
	e.ui = o.Interaction
	e.rep = o.Reporter

	cfg, warns, err := state.LoadWithWarnings(e.paths.AppConfigFile)
	if err != nil {
		return nil, err
	}
	for _, w := range warns {
		e.log.Warn("unknown config key", "key", w)
	}
	e.cfg, e.cfgWarns = cfg, warns
	st, warn := state.LoadStateOrReset(e.paths.AppStateFile)
	if warn != "" {
		e.log.Warn("state reset", "detail", warn)
	}
	e.st, e.stateWarn = st, warn

	extras := cfg.Network.ExtraCAs
	if o.NoExtraCA {
		extras = nil
	}
	e.hc, e.netInfo, e.netErr = netx.NewHTTPClient(netx.Options{CABundle: cfg.Network.CABundle, ExtraCAs: extras})
	if e.netErr != nil {
		e.log.Warn("http client", "err", e.netErr)
		e.hc = &http.Client{Timeout: netx.DefaultTimeout}
	}
	e.log.Debug("engine ready", "aws_config", e.paths.AWSConfig, "mode", cfg.Mode)
	return e, nil
}

// newFileLogger returns a JSON slog logger writing to path (0600), rotated to
// path.1 when it exceeds 5 MB, wrapped in the redacting handler.
func newFileLogger(path string, verbose bool) *slog.Logger {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	if path == "" {
		return slog.New(redact.NewHandler(slog.NewJSONHandler(discardWriter{}, nil)))
	}
	if st, err := os.Stat(path); err == nil && st.Size() > 5<<20 {
		_ = os.Rename(path, path+".1")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return slog.New(redact.NewHandler(slog.NewJSONHandler(discardWriter{}, nil)))
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return slog.New(redact.NewHandler(slog.NewJSONHandler(discardWriter{}, nil)))
	}
	_ = fsutil.Restrict(path, 0o600)
	return slog.New(redact.NewHandler(slog.NewJSONHandler(f, &slog.HandlerOptions{Level: level})))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// ---- front-end wiring ----

func (e *Engine) SetInteraction(i Interaction) { e.mu.Lock(); e.ui = i; e.mu.Unlock() }
func (e *Engine) SetReporter(r model.Reporter) { e.mu.Lock(); e.rep = r; e.mu.Unlock() }

func (e *Engine) interaction() Interaction {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ui == nil {
		return defaultInteraction{}
	}
	return e.ui
}

func (e *Engine) hasInteraction() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.ui != nil
}

func (e *Engine) reporter() model.Reporter {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.rep == nil {
		return model.Discard
	}
	return e.rep
}

// defaultInteraction is used when no front-end is attached: confirmations get
// their default answer (destructive ones default to "no"), plan reviews are
// approved (they are non-destructive; removals need an extra confirmation),
// account selection keeps the preselection and role selection is unavailable.
type defaultInteraction struct{}

func (defaultInteraction) Confirm(_ context.Context, _, _ string, def bool) (bool, error) {
	return def, nil
}
func (defaultInteraction) ReviewPlan(context.Context, string, string) (bool, error) { return true, nil }
func (defaultInteraction) SelectAccounts(_ context.Context, _ string, _ []AccountRow, pre []string) ([]string, error) {
	return pre, nil
}
func (defaultInteraction) SelectRole(context.Context, model.DiscoveredAccount, string) (string, error) {
	return "", errors.New("non-interactive: cannot choose a role")
}
func (defaultInteraction) LoginPrompt(string, string) {}

// runOpts carry per-run decisions that must not be persisted.
type runOpts struct {
	compat      bool
	yes         bool // auto-approve non-destructive reviews
	allowRemove bool // approve stale removals
	dryRun      bool
	deferRoles  bool // leave role-change handling to the caller (RunFull)
}

func (e *Engine) review(ctx context.Context, ro runOpts, title, preview string) (bool, error) {
	if ro.yes {
		return true, nil
	}
	return e.interaction().ReviewPlan(ctx, title, preview)
}

func (e *Engine) confirmDestructive(ctx context.Context, ro runOpts, title, detail string) (bool, error) {
	if ro.allowRemove {
		return true, nil
	}
	if ro.yes {
		return false, nil // --yes never approves destructive changes
	}
	return e.interaction().Confirm(ctx, title, detail, false)
}

// ---- configuration snapshot helpers ----

// conf returns a deep copy of the configuration (safe to read without the lock).
func (e *Engine) conf() *state.Config {
	e.mu.Lock()
	defer e.mu.Unlock()
	return cloneConfig(e.cfg)
}

func cloneConfig(c *state.Config) *state.Config {
	out := *c
	out.Groups = make(map[string]*state.Group, len(c.Groups))
	for k, g := range c.Groups {
		if g == nil {
			continue
		}
		cp := *g
		cp.Members = slices.Clone(g.Members)
		out.Groups[k] = &cp
	}
	out.Accounts = make(map[string]state.AccountPref, len(c.Accounts))
	for k, v := range c.Accounts {
		out.Accounts[k] = v
	}
	out.Roles.Fallbacks = slices.Clone(c.Roles.Fallbacks)
	out.Roles.Ranking = slices.Clone(c.Roles.Ranking)
	out.Rotation.Accounts = slices.Clone(c.Rotation.Accounts)
	out.Network.ExtraCAs = slices.Clone(c.Network.ExtraCAs)
	return &out
}

// mutateConfig applies fn to a copy, validates and saves it, then installs it.
func (e *Engine) mutateConfig(fn func(c *state.Config) error) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	c := cloneConfig(e.cfg)
	if err := fn(c); err != nil {
		return err
	}
	if err := c.Validate(); err != nil {
		return err
	}
	if err := c.Save(e.paths.AppConfigFile); err != nil {
		return err
	}
	e.cfg = c
	return nil
}

// mutateState applies fn to the state under the lock and persists it.
func (e *Engine) mutateState(fn func(s *state.State)) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	fn(e.st)
	return e.st.Save(e.paths.AppStateFile)
}

func (e *Engine) ConfigPath() string { return e.paths.AppConfigFile }
func (e *Engine) Version() string    { return e.opts.Version }
func (e *Engine) Mode() string {
	if e.opts.Compat {
		return state.ModeCompat
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cfg.Mode
}
func (e *Engine) compat() bool { return e.Mode() == state.ModeCompat }

func (e *Engine) SaveConfig() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cfg.Save(e.paths.AppConfigFile)
}

// ---- environment overrides (this run only, never persisted) ----

func (e *Engine) envBool(k string) (val, set bool) {
	v := strings.TrimSpace(e.env(k))
	if v == "" {
		return false, false
	}
	return v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes"), true
}

func (e *Engine) forceLoginEnv() bool { v, _ := e.envBool("FORCE_LOGIN"); return v }

func (e *Engine) syncDisabledEnv() bool {
	v, set := e.envBool("SYNC")
	return set && !v
}

func (e *Engine) parallel(c *state.Config, override int) int {
	if override > 0 {
		return min(override, 64)
	}
	if n, err := strconv.Atoi(strings.TrimSpace(e.env("PARALLEL"))); err == nil && n > 0 {
		return min(n, 64)
	}
	return c.Export.Concurrency
}

func (e *Engine) credsFile(c *state.Config) string {
	if v := strings.TrimSpace(e.env("CREDS_FILE")); v != "" {
		return fsutil.ExpandHome(v)
	}
	if c.Export.CredentialsFile != "" {
		return fsutil.ExpandHome(c.Export.CredentialsFile)
	}
	return e.paths.AWSCredentials
}

func (e *Engine) fallbacks(c *state.Config) []string {
	if v := strings.TrimSpace(e.env("FALLBACK_ROLES")); v != "" {
		return strings.Fields(v)
	}
	return c.Roles.Fallbacks
}

// roleUpdateEnv returns the ROLE_UPDATE override, if set.
func (e *Engine) roleUpdateEnv() (bool, bool) { return e.envBool("ROLE_UPDATE") }

func (e *Engine) rotationFile(c *state.Config) string {
	if c.Rotation.File != "" {
		return fsutil.ExpandHome(c.Rotation.File)
	}
	return e.paths.ClaudeAccountFile
}

func ranker(c *state.Config) roles.Ranker {
	if len(c.Roles.Ranking) == 0 {
		return roles.DefaultRanker()
	}
	r := roles.Ranker{}
	for _, x := range c.Roles.Ranking {
		r.Rules = append(r.Rules, roles.Rule{Match: x.Match, Weight: x.Weight})
	}
	return r
}

func (e *Engine) policy(c *state.Config, override model.RolePolicy, compat bool) model.RolePolicy {
	if override != "" {
		return override
	}
	if compat {
		return model.PolicyScript
	}
	return c.Roles.Policy
}

// ---- AWS config helpers ----

func (e *Engine) readAWSConfig() (*awsini.File, error) {
	data, err := fsutil.ReadFileOptional(e.paths.AWSConfig)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", e.paths.AWSConfig, err)
	}
	f, err := awsini.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", e.paths.AWSConfig, err)
	}
	return f, nil
}

func (e *Engine) profiles() ([]model.Profile, error) {
	f, err := e.readAWSConfig()
	if err != nil {
		return nil, err
	}
	return awsini.Profiles(f), nil
}

// legacyPrefix names pseudo-sessions for profiles with inline sso_start_url.
const legacyPrefix = "legacy:"

func legacyName(startURL string) string {
	host := startURL
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	return legacyPrefix + host
}

// tokenSession maps a (pseudo-)session to the key the token cache uses.
func tokenSession(s model.SSOSession) model.SSOSession {
	if strings.HasPrefix(s.Name, legacyPrefix) {
		s.Name = ""
	}
	return s
}

// inSession reports whether p is an SSO profile of session s.
func inSession(p model.Profile, s model.SSOSession) bool {
	if !p.IsSSO() {
		return false
	}
	if p.Session != "" {
		return p.Session == s.Name
	}
	return p.LegacyStartURL != "" && strings.TrimRight(p.LegacyStartURL, "/") == strings.TrimRight(s.StartURL, "/")
}

// session resolves name ("" → default) to a session.
func (e *Engine) session(name string) (model.SSOSession, error) {
	if name == "" {
		name = e.DefaultSession()
	}
	ss, err := e.Sessions()
	if err != nil {
		return model.SSOSession{}, err
	}
	if name == "" {
		switch len(ss) {
		case 0:
			return model.SSOSession{}, fmt.Errorf("no SSO session configured: create one with `credorbit login --name NAME --start-url https://<id>.awsapps.com/start --region <region>`: %w", model.ErrNotFound)
		default:
			names := make([]string, len(ss))
			for i, s := range ss {
				names[i] = s.Name
			}
			return model.SSOSession{}, fmt.Errorf("several SSO sessions (%s): pass --session or run `credorbit config set sso.default_session NAME`", strings.Join(names, ", "))
		}
	}
	for _, s := range ss {
		if s.Name == name {
			return s, nil
		}
	}
	return model.SSOSession{}, fmt.Errorf("SSO session %q: %w (not in %s)", name, model.ErrNotFound, e.paths.AWSConfig)
}

func (e *Engine) authenticator() *sso.Authenticator {
	cache := sso.TokenCache{Dir: e.paths.SSOCacheDir}
	var a *sso.Authenticator
	if e.opts.NewAuthenticator != nil {
		a = e.opts.NewAuthenticator(e.hc, cache)
	} else {
		a = &sso.Authenticator{HTTP: e.hc, Cache: cache, OIDCEndpoint: e.opts.OIDCEndpoint, PortalEndpoint: e.opts.PortalEndpoint}
	}
	if a.Now == nil {
		a.Now = e.now
	}
	return a
}

func (e *Engine) ssoClient(region string) sso.Client {
	if e.opts.SSOClient != nil {
		return e.opts.SSOClient(region, e.hc)
	}
	return sso.NewClient(region, e.hc)
}

// token returns a valid token for s (refreshing if possible).
func (e *Engine) token(ctx context.Context, s model.SSOSession) (model.Token, error) {
	if e.netErr != nil {
		return model.Token{}, fmt.Errorf("network configuration: %w", e.netErr)
	}
	tok, err := e.authenticator().Valid(ctx, tokenSession(s))
	if err != nil {
		if errors.Is(err, model.ErrUnauthorized) {
			return model.Token{}, needsLogin(s.Name, err)
		}
		return model.Token{}, err
	}
	return tok, nil
}

func (e *Engine) report(ev model.Event) { e.reporter().Report(ev) }
