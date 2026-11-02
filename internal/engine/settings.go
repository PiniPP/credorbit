package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/credorbit/credorbit/internal/importer"
	"github.com/credorbit/credorbit/internal/model"
	"github.com/credorbit/credorbit/internal/state"
)

type setting struct {
	key, desc string
	choices   []string
	get       func(c *state.Config) string
	set       func(c *state.Config, v string) error
}

var boolChoices = []string{"true", "false"}

func parseBool(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes", "on":
		return true, nil
	case "false", "0", "no", "off":
		return false, nil
	}
	return false, fmt.Errorf("%q is not a boolean (true|false)", v)
}

func splitList(v string) []string {
	out := []string{}
	for _, x := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' }) {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

func strSetting(key, desc string, choices []string, p func(c *state.Config) *string) setting {
	return setting{key: key, desc: desc, choices: choices,
		get: func(c *state.Config) string { return *p(c) },
		set: func(c *state.Config, v string) error {
			if len(choices) > 0 && !contains(choices, v) {
				return fmt.Errorf("%s: %q is invalid (want %s)", key, v, strings.Join(choices, "|"))
			}
			*p(c) = v
			return nil
		}}
}

func boolSetting(key, desc string, p func(c *state.Config) *bool) setting {
	return setting{key: key, desc: desc, choices: boolChoices,
		get: func(c *state.Config) string { return strconv.FormatBool(*p(c)) },
		set: func(c *state.Config, v string) error {
			b, err := parseBool(v)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			*p(c) = b
			return nil
		}}
}

func intSetting(key, desc string, p func(c *state.Config) *int) setting {
	return setting{key: key, desc: desc,
		get: func(c *state.Config) string { return strconv.Itoa(*p(c)) },
		set: func(c *state.Config, v string) error {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				return fmt.Errorf("%s: %q is not an integer", key, v)
			}
			*p(c) = n
			return nil
		}}
}

func listSetting(key, desc string, p func(c *state.Config) *[]string) setting {
	return setting{key: key, desc: desc,
		get: func(c *state.Config) string { return strings.Join(*p(c), ",") },
		set: func(c *state.Config, v string) error { *p(c) = splitList(v); return nil }}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

var settingsTable = []setting{
	strSetting("mode", "normal = safe defaults; compat = reproduce the legacy script", []string{state.ModeNormal, state.ModeCompat}, func(c *state.Config) *string { return &c.Mode }),
	strSetting("sso.default_session", "SSO session used when --session is not given", nil, func(c *state.Config) *string { return &c.SSO.DefaultSession }),
	strSetting("sso.login_flow", "login flow: browser (PKCE) or device code", []string{"browser", "device"}, func(c *state.Config) *string { return &c.SSO.LoginFlow }),
	strSetting("profiles.prefix", "prefix of profiles CredOrbit creates/manages (e.g. p_)", nil, func(c *state.Config) *string { return &c.Profiles.Prefix }),
	strSetting("profiles.region", "region written to new profiles (empty = session region)", nil, func(c *state.Config) *string { return &c.Profiles.Region }),
	strSetting("profiles.output", "output format written to new profiles", nil, func(c *state.Config) *string { return &c.Profiles.Output }),
	{key: "roles.policy", desc: "which role to use: configured|manual|strongest|readonly|script",
		choices: []string{"configured", "manual", "strongest", "readonly", "script"},
		get:     func(c *state.Config) string { return string(c.Roles.Policy) },
		set: func(c *state.Config, v string) error {
			p, err := model.ParseRolePolicy(v)
			if err != nil {
				return err
			}
			c.Roles.Policy = p
			return nil
		}},
	listSetting("roles.fallbacks", "fallback roles tried last (script policy only)", func(c *state.Config) *[]string { return &c.Roles.Fallbacks }),
	boolSetting("roles.update_config", "persist the role that worked to ~/.aws/config without asking", func(c *state.Config) *bool { return &c.Roles.UpdateConfig }),
	boolSetting("sync.add_new", "add profiles for newly discovered accounts", func(c *state.Config) *bool { return &c.Sync.AddNew }),
	strSetting("sync.remove_stale", "stale profile removal: prompt (reviewed) or never", []string{state.RemoveStalePrompt, state.RemoveStaleNever}, func(c *state.Config) *string { return &c.Sync.RemoveStale }),
	{key: "sync.threshold", desc: "mass-removal safeguard ratio (0-1]",
		get: func(c *state.Config) string { return strconv.FormatFloat(c.Sync.Threshold, 'f', -1, 64) },
		set: func(c *state.Config, v string) error {
			f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			if err != nil {
				return fmt.Errorf("sync.threshold: %q is not a number", v)
			}
			c.Sync.Threshold = f
			return nil
		}},
	boolSetting("export.enabled", "write temporary credentials to the credentials file", func(c *state.Config) *bool { return &c.Export.Enabled }),
	strSetting("export.credentials_file", "credentials file (empty = AWS default)", nil, func(c *state.Config) *string { return &c.Export.CredentialsFile }),
	boolSetting("export.strip_prefix", "write p_x as [x] (legacy layout)", func(c *state.Config) *bool { return &c.Export.StripPrefix }),
	strSetting("export.default_profile", "profile whose credentials are copied to [default]", nil, func(c *state.Config) *string { return &c.Export.DefaultProfile }),
	intSetting("export.concurrency", "parallel credential requests (1-64)", func(c *state.Config) *int { return &c.Export.Concurrency }),
	intSetting("export.backup_keep", "timestamped backups kept per file", func(c *state.Config) *int { return &c.Export.BackupKeep }),
	boolSetting("export.include_new_in_main", "include newly discovered accounts in main-group exports", func(c *state.Config) *bool { return &c.Export.IncludeNewInMain }),
	strSetting("rotation.strategy", "Claude account rotation strategy", []string{state.StrategyRandom, state.StrategyRoundRobin, state.StrategyManual}, func(c *state.Config) *string { return &c.Rotation.Strategy }),
	boolSetting("rotation.verify_bedrock", "verify Bedrock access before rotating", func(c *state.Config) *bool { return &c.Rotation.VerifyBedrock }),
	strSetting("rotation.bedrock_region", "region for Bedrock verification", nil, func(c *state.Config) *string { return &c.Rotation.BedrockRegion }),
	strSetting("rotation.bedrock_model", "model ID for Bedrock verification", nil, func(c *state.Config) *string { return &c.Rotation.BedrockModel }),
	strSetting("rotation.file", "rotation file read by your shell (empty = ~/.claude-aws-account)", nil, func(c *state.Config) *string { return &c.Rotation.File }),
	strSetting("network.ca_bundle", "PEM bundle appended to system roots", nil, func(c *state.Config) *string { return &c.Network.CABundle }),
	listSetting("network.extra_cas", "extra PEM files appended to system roots", func(c *state.Config) *[]string { return &c.Network.ExtraCAs }),
	strSetting("network.probe_url", "URL probed by doctor/setup for TLS interception", nil, func(c *state.Config) *string { return &c.Network.ProbeURL }),
	strSetting("ui.theme", "TUI theme", []string{"default", "light", "mono"}, func(c *state.Config) *string { return &c.UI.Theme }),
	boolSetting("ui.ascii", "ASCII-only TUI rendering", func(c *state.Config) *bool { return &c.UI.ASCII }),
	strSetting("update.repo", "release repository owner/name for update checks", nil, func(c *state.Config) *string { return &c.Update.Repo }),
	boolSetting("update.check", "allow update checks", func(c *state.Config) *bool { return &c.Update.Check }),
}

func (e *Engine) Settings() []SettingInfo {
	c := e.conf()
	out := make([]SettingInfo, 0, len(settingsTable))
	for _, s := range settingsTable {
		out = append(out, SettingInfo{Key: s.key, Value: s.get(c), Description: s.desc, Choices: append([]string(nil), s.choices...)})
	}
	return out
}

// SetSetting validates and persists one setting.
func (e *Engine) SetSetting(key, value string) error {
	for _, s := range settingsTable {
		if s.key != key {
			continue
		}
		if key == "mode" {
			return e.SetMode(value)
		}
		return e.mutateConfig(func(c *state.Config) error { return s.set(c, value) })
	}
	return fmt.Errorf("unknown setting %q (see `credorbit config list`): %w", key, model.ErrNotFound)
}

// SetMode switches between normal and compat mode and persists it.
func (e *Engine) SetMode(mode string) error {
	return e.mutateConfig(func(c *state.Config) error {
		switch mode {
		case state.ModeCompat:
			importer.CompatDefaults(c, nil)
		case state.ModeNormal:
			c.Mode = state.ModeNormal
			c.Roles.Policy = model.PolicyConfigured
			c.Roles.UpdateConfig = false
			c.Export.Enabled = false
		default:
			return fmt.Errorf("mode %q is invalid (want normal|compat)", mode)
		}
		return nil
	})
}

// ImportLegacy reads the legacy script (never writes it) and merges its settings.
func (e *Engine) ImportLegacy(ctx context.Context, path string, compat bool) ([]string, error) {
	if path == "" {
		path = e.paths.LegacyScript
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read legacy script: %w", err)
	}
	l, err := importer.ParseScript(data)
	if err != nil {
		return nil, err
	}
	profiles, err := e.profiles()
	if err != nil {
		return nil, err
	}
	c := e.conf()
	notes := l.Apply(c, profiles)
	if compat {
		importer.CompatDefaults(c, l)
		notes = append(notes, "mode = compat (role policy script, role updates saved, export enabled, new accounts included in main)")
	}
	for _, w := range l.Warnings {
		notes = append(notes, "warning: "+w)
	}
	if len(notes) == 0 {
		notes = []string{"nothing to import (settings already match)"}
	}
	preview := "Import from " + path + " (read-only) into " + e.paths.AppConfigFile + ":\n"
	for _, n := range notes {
		preview += "  " + n + "\n"
	}
	ok, err := e.interaction().ReviewPlan(ctx, "Import legacy script", preview)
	if err != nil {
		return notes, err
	}
	if !ok {
		return notes, ErrDeclined
	}
	if err := c.Validate(); err != nil {
		return notes, fmt.Errorf("imported settings are invalid: %w", err)
	}
	e.mu.Lock()
	err = c.Save(e.paths.AppConfigFile)
	if err == nil {
		e.cfg = c
	}
	e.mu.Unlock()
	if err != nil {
		return notes, err
	}
	_ = e.mutateState(func(s *state.State) { s.LegacyImported = e.now().UTC() })
	return notes, nil
}

func (e *Engine) CheckUpdate(ctx context.Context) (string, string, bool, error) {
	if e.opts.UpdateCheck == nil {
		return "", "", false, errors.New("update checking not available in this build")
	}
	c := e.conf()
	if c.Update.Repo == "" {
		return "", "", false, errors.New("update.repo is not set (credorbit config set update.repo owner/name)")
	}
	return e.opts.UpdateCheck(ctx, e.hc, c.Update.Repo, e.opts.Version)
}
