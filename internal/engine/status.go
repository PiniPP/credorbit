package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/PiniPP/credorbit/internal/awsini"
	"github.com/PiniPP/credorbit/internal/credexport"
	"github.com/PiniPP/credorbit/internal/fsutil"
	"github.com/PiniPP/credorbit/internal/model"
	"github.com/PiniPP/credorbit/internal/netx"
	"github.com/PiniPP/credorbit/internal/rotate"
)

func warnEvent(step, msg string) model.Event {
	return model.Event{Kind: model.EventWarning, Step: step, Message: msg}
}

// Status summarises local state without any network call.
func (e *Engine) Status(_ context.Context) Status {
	c := e.conf()
	st := Status{
		Version:     e.opts.Version,
		Mode:        e.Mode(),
		ConfigFile:  e.paths.AppConfigFile,
		AWSConfig:   e.paths.AWSConfig,
		Credentials: e.credsFile(c),
		Groups:      map[string]int{},
		Sessions:    []SessionStatus{},
		Exported:    []CredentialStatus{},
	}
	ss, _ := e.Sessions()
	for _, s := range ss {
		ls := e.loginState(s)
		x := SessionStatus{Name: s.Name, StartURL: s.StartURL, Region: s.Region, LoggedIn: ls.LoggedIn, ExpiresAt: ls.ExpiresAt, Refresh: ls.Refresh}
		if d := e.latest(s.Name); d != nil {
			x.Accounts = len(d.Accounts)
			x.DiscoveredAt = d.At
		}
		st.Sessions = append(st.Sessions, x)
	}
	if ps, err := e.profiles(); err == nil {
		for _, p := range ps {
			if p.IsSSO() {
				st.Profiles++
			}
		}
	}
	for _, n := range c.GroupNames() {
		st.Groups[n] = len(c.Groups[n].Members)
	}
	exp := credexport.ReadExpirations(st.Credentials)
	e.mu.Lock()
	managed := map[string]bool{}
	for _, m := range e.st.ManagedSections {
		managed[m] = true
	}
	st.LastRun = e.st.LastRun
	e.mu.Unlock()
	names := map[string]bool{}
	for k := range exp {
		names[k] = true
	}
	for k := range managed {
		names[k] = true
	}
	keys := make([]string, 0, len(names))
	for k := range names {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	now := e.now()
	for _, k := range keys {
		t := exp[k]
		st.Exported = append(st.Exported, CredentialStatus{Section: k, Expires: t, Expired: !t.IsZero() && !t.After(now), Managed: managed[k]})
	}
	if len(c.Rotation.Accounts) > 0 {
		ri := e.Rotation()
		st.Rotation = &ri
	}
	return st
}

func (e *Engine) probe(ctx context.Context, url string) (netx.ProbeResult, netx.ProbeResult) {
	pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if e.opts.Probe != nil {
		return e.opts.Probe(pctx, e.hc, url)
	}
	return netx.Probe(pctx, e.hc, url), netx.ProbeWithSystemOnly(pctx, url)
}

func portalURL(region string) string {
	if region == "" {
		region = "us-east-1"
	}
	suffix := "amazonaws.com"
	if strings.HasPrefix(region, "cn-") {
		suffix = "amazonaws.com.cn"
	}
	return "https://portal.sso." + region + "." + suffix + "/"
}

// Doctor runs local and network diagnostics. It never prints secrets.
func (e *Engine) Doctor(ctx context.Context) []Check {
	var out []Check
	add := func(area, name, status, detail, remedy string) {
		out = append(out, Check{Area: area, Name: name, Status: status, Detail: detail, Remedy: remedy})
	}
	c := e.conf()
	now := e.now()

	// --- app config
	if len(e.cfgWarns) > 0 {
		add("app", "config keys", "warn", "unknown keys in "+e.paths.AppConfigFile+": "+strings.Join(e.cfgWarns, ", "), "fix the typos or remove the keys (credorbit config list shows valid keys)")
	} else {
		add("app", "config", "ok", e.paths.AppConfigFile+" (mode "+e.Mode()+")", "")
	}
	if err := c.Validate(); err != nil {
		add("app", "config valid", "fail", err.Error(), "correct the values with `credorbit config set KEY VALUE`")
	}
	if e.stateWarn != "" {
		add("app", "state", "warn", e.stateWarn, "nothing to do: the discovery cache is rebuilt by `credorbit discover`")
	}

	// --- aws config
	var f *awsini.File
	data, err := fsutil.ReadFileOptional(e.paths.AWSConfig)
	switch {
	case err != nil:
		add("aws-config", "read", "fail", err.Error(), "check permissions of "+e.paths.AWSConfig)
	case data == nil:
		add("aws-config", "exists", "fail", e.paths.AWSConfig+" does not exist", "create a session: credorbit login --name NAME --start-url URL --region REGION")
	default:
		f, err = awsini.Parse(data)
		if err != nil {
			add("aws-config", "parse", "fail", err.Error(), "fix the syntax error in "+e.paths.AWSConfig+" (a backup is in *.credorbit-*.bak)")
		} else {
			add("aws-config", "parse", "ok", e.paths.AWSConfig, "")
			if d := f.Duplicates(); len(d) > 0 {
				add("aws-config", "duplicate sections", "fail", strings.Join(d, ", "), "merge or remove the duplicate [sections]; the AWS CLI rejects such files")
			}
		}
	}
	var profiles []model.Profile
	var sessions []model.SSOSession
	if f != nil {
		profiles = awsini.Profiles(f)
		sessions, _ = e.Sessions()
		sessNames := map[string]bool{}
		for _, s := range sessions {
			sessNames[s.Name] = true
		}
		if len(sessions) == 0 {
			add("sso", "sessions", "fail", "no [sso-session] blocks", "credorbit login --name NAME --start-url URL --region REGION")
		} else {
			add("sso", "sessions", "ok", fmt.Sprintf("%d session(s)", len(sessions)), "")
		}
		var orphan []string
		for _, p := range profiles {
			if p.Session != "" && !sessNames[p.Session] {
				orphan = append(orphan, p.Name+"→"+p.Session)
			}
		}
		if len(orphan) > 0 {
			add("aws-config", "profiles without session", "fail", strings.Join(orphan, ", "), "add the missing [sso-session] or fix sso_session in these profiles")
		}
	}

	// --- sessions/tokens/discovery
	for _, s := range sessions {
		ls := e.loginState(s)
		switch {
		case ls.LoggedIn && ls.ExpiresAt.Sub(now) < time.Hour:
			add("sso", "token "+s.Name, "warn", "expires "+ls.ExpiresAt.Local().Format("15:04"), "credorbit login --session "+s.Name)
		case ls.LoggedIn:
			add("sso", "token "+s.Name, "ok", "valid until "+ls.ExpiresAt.Local().Format("2006-01-02 15:04"), "")
		case ls.Refresh:
			add("sso", "token "+s.Name, "warn", "expired; silent refresh available", "run any command (refresh is automatic) or credorbit login")
		default:
			add("sso", "token "+s.Name, "fail", "not logged in", "credorbit login --session "+s.Name)
		}
		if ls.LoggedIn {
			if ls.Refresh {
				add("sso", "refresh "+s.Name, "ok", "refresh token available", "")
			} else {
				add("sso", "refresh "+s.Name, "info", "no refresh token (AWS CLI legacy login?)", "credorbit login renews with a refresh token")
			}
		}
		d := e.latest(s.Name)
		if d == nil {
			add("sso", "discovery "+s.Name, "info", "no discovery cached", "credorbit discover --session "+s.Name)
			continue
		}
		age := now.Sub(d.At)
		if age > 7*24*time.Hour {
			add("sso", "discovery "+s.Name, "warn", fmt.Sprintf("cached discovery is %d days old", int(age.Hours()/24)), "credorbit discover --session "+s.Name)
		} else {
			add("sso", "discovery "+s.Name, "ok", fmt.Sprintf("%d accounts, %s old", len(d.Accounts), age.Round(time.Minute)), "")
		}
		byID := d.ByID()
		var notAssigned, missing []string
		for _, p := range profiles {
			if !inSession(p, s) {
				continue
			}
			a, ok := byID[p.AccountID]
			if !ok {
				if d.Complete {
					missing = append(missing, p.Name)
				}
				continue
			}
			if a.RolesErr == "" && !a.HasRole(p.Role) {
				notAssigned = append(notAssigned, p.Name+" ("+p.Role+")")
			}
		}
		if len(notAssigned) > 0 {
			add("aws-config", "roles not assigned", "warn", strings.Join(notAssigned, ", "), "credorbit roles set PROFILE ROLE, or setup --full with --policy strongest")
		}
		if len(missing) > 0 {
			add("aws-config", "accounts missing in discovery", "warn", strings.Join(missing, ", "), "access may be revoked: credorbit sync --remove-stale (reviewed)")
		}
	}

	// --- permissions
	creds := e.credsFile(c)
	permCheck := func(area, name, path, remedy, failStatus string) {
		ok, detail, err := fsutil.CheckPrivate(path)
		switch {
		case err != nil && os.IsNotExist(err):
		case err != nil:
			add(area, name, "warn", err.Error(), remedy)
		case ok:
			add(area, name, "ok", path, "")
		default:
			add(area, name, failStatus, path+": "+detail, remedy)
		}
	}
	permCheck("permissions", "credentials file", creds, fixPerm(creds), "fail")
	permCheck("permissions", "aws config", e.paths.AWSConfig, fixPerm(e.paths.AWSConfig), "warn")
	if ents, err := os.ReadDir(e.paths.SSOCacheDir); err == nil {
		var bad []string
		for _, en := range ents {
			if en.IsDir() || !strings.HasSuffix(en.Name(), ".json") {
				continue
			}
			p := filepath.Join(e.paths.SSOCacheDir, en.Name())
			if ok, _, err := fsutil.CheckPrivate(p); err == nil && !ok {
				bad = append(bad, en.Name())
			}
		}
		if len(bad) > 0 {
			add("permissions", "token cache", "fail", fmt.Sprintf("%d token cache file(s) readable by others", len(bad)), fixPerm(filepath.Join(e.paths.SSOCacheDir, "*.json")))
		} else {
			add("permissions", "token cache", "ok", e.paths.SSOCacheDir, "")
		}
	}

	// --- credentials
	exp := credexport.ReadExpirations(creds)
	var expired []string
	for s, t := range exp {
		if !t.After(now) {
			expired = append(expired, s)
		}
	}
	sort.Strings(expired)
	if len(expired) > 0 {
		add("credentials", "expired", "warn", fmt.Sprintf("%d exported section(s) expired: %s", len(expired), strings.Join(expired, ", ")), "credorbit export")
	} else if len(exp) > 0 {
		add("credentials", "expiry", "ok", fmt.Sprintf("%d exported section(s) valid", len(exp)), "")
	}
	if f != nil {
		strip := c.Export.StripPrefix || e.compat()
		exists := map[string]bool{"default": true}
		for _, p := range profiles {
			exists[credexport.SectionName(p.Name, c.Profiles.Prefix, strip)] = true
			exists[p.Name] = true
		}
		e.mu.Lock()
		managed := append([]string(nil), e.st.ManagedSections...)
		e.mu.Unlock()
		var orphans []string
		for _, m := range managed {
			if !exists[m] {
				orphans = append(orphans, m)
			}
		}
		if len(orphans) > 0 {
			add("credentials", "orphaned sections", "warn", strings.Join(orphans, ", "), "the next credorbit export prunes them")
		}
	}

	// --- network
	if e.netErr != nil {
		add("network", "CA files", "fail", e.netErr.Error(), "fix network.ca_bundle / network.extra_cas / AWS_CA_BUNDLE")
	} else if len(e.netInfo.CASources) > 0 {
		add("network", "CA files", "ok", "appended to system roots: "+strings.Join(e.netInfo.CASources, ", "), "")
	}
	if e.netInfo.Proxy != "" {
		add("network", "proxy", "info", "proxy from environment: "+e.netInfo.Proxy, "")
	}
	if c.Network.CombinedBundlePath != "" {
		if _, err := os.Stat(fsutil.ExpandHome(c.Network.CombinedBundlePath)); err != nil {
			add("network", "combined bundle", "warn", c.Network.CombinedBundlePath+" missing", "credorbit ca bundle --out "+c.Network.CombinedBundlePath)
		} else {
			add("network", "combined bundle", "ok", c.Network.CombinedBundlePath, "")
		}
	}
	probeURL := c.Network.ProbeURL
	if probeURL == "" {
		region := ""
		if len(sessions) > 0 {
			region = sessions[0].Region
		}
		probeURL = portalURL(region)
	}
	if ctx.Err() == nil {
		pc, ps := e.probe(ctx, probeURL)
		switch {
		case pc.OK && pc.Intercepted:
			add("network", "TLS", "info", fmt.Sprintf("TLS-intercepting network detected (issuer %s); trusted via configured CA", pc.Issuer), "")
		case pc.OK:
			add("network", "TLS", "ok", probeURL+" reachable", "")
		case !pc.OK && pc.ErrClass == netx.ClassUnknownAuthority:
			add("network", "TLS", "fail", "certificate not trusted: "+pc.Err, "add your corporate root CA: credorbit config set network.extra_cas /path/root.pem")
		default:
			add("network", "TLS", "fail", pc.ErrClass+": "+pc.Err, pc.Advice)
		}
		if pc.OK && !ps.OK && ps.ErrClass == netx.ClassUnknownAuthority {
			add("network", "system trust", "info", "system roots alone do not trust this network; tools that replace their trust store need a combined bundle", "credorbit ca bundle --out ~/.aws/combined-ca.pem and point AWS_CA_BUNDLE at it")
		}
	}

	// --- claude rotation
	rf := e.rotationFile(c)
	if len(c.Rotation.Accounts) > 0 {
		cur, _ := rotate.ReadCurrent(rf)
		if cur == "" {
			add("claude", "rotation file", "warn", rf+" missing or empty", "credorbit rotate")
		} else {
			add("claude", "rotation file", "ok", rf+" → "+cur, "")
			ok, skipped := rotate.Eligible([]string{cur}, creds, now)
			if len(ok) == 0 {
				add("claude", "current account", "warn", cur+": "+skipped[cur], "credorbit export, then credorbit rotate")
			}
		}
		if env := e.env("AWS_PROFILE"); env != "" && cur != "" && env != cur {
			add("claude", "AWS_PROFILE", "info", "AWS_PROFILE="+env+" differs from the rotation file ("+cur+")", "open a new shell, or add the shell integration")
		}
		add("claude", "shell integration", "info", "AWS_PROFILE can follow the rotation file", "credorbit rotate shell "+shellName())
	}

	// --- legacy script
	if st, err := os.Stat(e.paths.LegacyScript); err == nil && !st.IsDir() {
		e.mu.Lock()
		imported := !e.st.LegacyImported.IsZero()
		e.mu.Unlock()
		if !imported {
			add("app", "legacy script", "info", e.paths.LegacyScript+" found, not imported", "credorbit import (read-only; the script is never modified)")
		}
	}
	return out
}

func fixPerm(path string) string {
	if runtime.GOOS == "windows" {
		return "restrict " + path + " to your user (icacls)"
	}
	return "chmod 600 " + path
}

func shellName() string {
	if runtime.GOOS == "windows" {
		return "powershell"
	}
	if s := filepath.Base(os.Getenv("SHELL")); s == "bash" || s == "fish" {
		return s
	}
	return "zsh"
}
