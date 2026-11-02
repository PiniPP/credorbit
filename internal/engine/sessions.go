package engine

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/credorbit/credorbit/internal/awsini"
	"github.com/credorbit/credorbit/internal/fsutil"
	"github.com/credorbit/credorbit/internal/model"
	"github.com/credorbit/credorbit/internal/sso"
	"github.com/credorbit/credorbit/internal/state"
)

// Sessions returns the [sso-session] blocks of the AWS config plus one
// pseudo-session "legacy:<host>" per distinct inline (start URL, region) pair
// used by legacy profiles.
func (e *Engine) Sessions() ([]model.SSOSession, error) {
	f, err := e.readAWSConfig()
	if err != nil {
		return nil, err
	}
	out := awsini.SSOSessions(f)
	seen := map[string]bool{}
	for _, s := range out {
		seen[s.Name] = true
	}
	for _, p := range awsini.Profiles(f) {
		if p.Session != "" || p.LegacyStartURL == "" {
			continue
		}
		name := legacyName(p.LegacyStartURL)
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, model.SSOSession{Name: name, StartURL: p.LegacyStartURL, Region: p.LegacyRegion})
	}
	return out, nil
}

// DefaultSession: env SSO_SESSION > sso.default_session > the only session > "".
func (e *Engine) DefaultSession() string {
	if v := strings.TrimSpace(e.env("SSO_SESSION")); v != "" {
		return v
	}
	if d := e.conf().SSO.DefaultSession; d != "" {
		return d
	}
	if ss, err := e.Sessions(); err == nil && len(ss) == 1 {
		return ss[0].Name
	}
	return ""
}

func (e *Engine) SetDefaultSession(name string) error {
	if name != "" {
		if _, err := e.session(name); err != nil {
			return err
		}
	}
	return e.mutateConfig(func(c *state.Config) error { c.SSO.DefaultSession = name; return nil })
}

var sessionNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// AddSession appends an [sso-session] block to the AWS config after review.
func (e *Engine) AddSession(s model.SSOSession) error {
	ctx := context.Background()
	if !sessionNameRE.MatchString(s.Name) {
		return fmt.Errorf("invalid session name %q (letters, digits, '.', '_', '-')", s.Name)
	}
	u, err := url.Parse(s.StartURL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("start URL %q must be an https URL", s.StartURL)
	}
	if !regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-\d$`).MatchString(s.Region) {
		return fmt.Errorf("region %q is not an AWS region name", s.Region)
	}
	if ss, _ := e.Sessions(); ss != nil {
		for _, x := range ss {
			if x.Name == s.Name {
				return fmt.Errorf("SSO session %q already exists in %s", s.Name, e.paths.AWSConfig)
			}
		}
	}
	scopes := s.Scopes
	if scopes == "" {
		scopes = "sso:account:access"
	}
	preview := fmt.Sprintf("+ [sso-session %s]\n+ sso_start_url = %s\n+ sso_region = %s\n+ sso_registration_scopes = %s\n", s.Name, s.StartURL, s.Region, scopes)
	ok, err := e.interaction().ReviewPlan(ctx, "Add SSO session to "+e.paths.AWSConfig, preview)
	if err != nil {
		return err
	}
	if !ok {
		return ErrDeclined
	}
	lock, err := fsutil.AcquireLock(e.paths.AWSConfig, 10*time.Second)
	if err != nil {
		return err
	}
	defer lock.Release()
	data, err := fsutil.ReadFileOptional(e.paths.AWSConfig)
	if err != nil {
		return err
	}
	f, err := awsini.Parse(data)
	if err != nil {
		return fmt.Errorf("parse %s: %w", e.paths.AWSConfig, err)
	}
	if f.Section("sso-session "+s.Name) != nil {
		return fmt.Errorf("SSO session %q already exists", s.Name)
	}
	sec := f.AddSection("sso-session " + s.Name)
	sec.Set("sso_start_url", s.StartURL)
	sec.Set("sso_region", s.Region)
	sec.Set("sso_registration_scopes", scopes)
	out := f.Bytes()
	chk, err := awsini.Parse(out)
	if err != nil || chk.Section("sso-session "+s.Name) == nil {
		return fmt.Errorf("validation of the new AWS config failed; nothing written")
	}
	perm := os.FileMode(0o600)
	if st, err := os.Stat(e.paths.AWSConfig); err == nil {
		perm = st.Mode().Perm()
	}
	keep := e.conf().Export.BackupKeep
	if _, err := fsutil.Backup(e.paths.AWSConfig, keep); err != nil {
		return err
	}
	if err := fsutil.AtomicWrite(e.paths.AWSConfig, out, perm); err != nil {
		return err
	}
	e.log.Info("sso session added", "session", s.Name)
	return nil
}

func (e *Engine) loginState(s model.SSOSession) LoginState {
	ls := LoginState{Session: s}
	tok, err := sso.TokenCache{Dir: e.paths.SSOCacheDir}.Load(tokenSession(s))
	if err != nil {
		ls.Err = err.Error()
		return ls
	}
	ls.ExpiresAt = tok.ExpiresAt
	ls.LoggedIn = !tok.Expired(e.now())
	ls.Refresh = tok.RefreshToken != "" && tok.ClientID != "" && tok.ClientSecret != "" &&
		(tok.RegistrationExpiresAt.IsZero() || tok.RegistrationExpiresAt.After(e.now()))
	if !ls.LoggedIn {
		if ls.Refresh {
			ls.Err = "token expired; silent refresh available"
		} else {
			ls.Err = "token expired; sign in again"
		}
	}
	return ls
}

// LoginStatus reads the token cache only (no network, no browser).
func (e *Engine) LoginStatus(_ context.Context, session string) LoginState {
	s, err := e.session(session)
	if err != nil {
		return LoginState{Session: model.SSOSession{Name: session}, Err: err.Error()}
	}
	return e.loginState(s)
}

// Login ensures a valid token: reuse/refresh unless force, else run the OIDC flow.
func (e *Engine) Login(ctx context.Context, session, flow string, force bool) (LoginState, error) {
	s, err := e.session(session)
	if err != nil {
		return LoginState{Session: model.SSOSession{Name: session}, Err: err.Error()}, err
	}
	if e.netErr != nil {
		return e.loginState(s), fmt.Errorf("network configuration: %w", e.netErr)
	}
	a := e.authenticator()
	ts := tokenSession(s)
	if !force {
		if _, err := a.Valid(ctx, ts); err == nil {
			return e.loginState(s), nil
		} else if !errors.Is(err, model.ErrUnauthorized) {
			return e.loginState(s), err
		}
	}
	if flow == "" {
		flow = e.conf().SSO.LoginFlow
	}
	f, err := sso.ParseFlow(flow)
	if err != nil {
		return e.loginState(s), err
	}
	ui := e.interaction()
	a.OnPrompt = func(p sso.Prompt) { ui.LoginPrompt(p.VerificationURL, p.UserCode) }
	if e.opts.OpenBrowser != nil {
		a.OpenBrowser = e.opts.OpenBrowser
	}
	lctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	e.report(model.Event{Kind: model.EventInfo, Step: "login", Subject: s.Name, Message: "signing in (" + string(f) + " flow)"})
	if _, err := a.Login(lctx, ts, f); err != nil {
		e.log.Warn("login failed", "session", s.Name, "err", err)
		return e.loginState(s), fmt.Errorf("sign-in to %q failed: %w", s.Name, err)
	}
	e.log.Info("logged in", "session", s.Name)
	return e.loginState(s), nil
}

func (e *Engine) Logout(ctx context.Context, session string) error {
	s, err := e.session(session)
	if err != nil {
		return err
	}
	return e.authenticator().Logout(ctx, tokenSession(s))
}
