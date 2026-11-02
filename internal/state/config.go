// Package state owns CredOrbit's persistent settings (config.yaml) and machine
// state (state.json). Neither file ever contains credentials or tokens.
package state

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/PiniPP/credorbit/internal/fsutil"
	"github.com/PiniPP/credorbit/internal/model"
)

// CurrentVersion is the config.yaml schema version written by this build.
const CurrentVersion = 1

// MainGroup is the built-in group that always exists and cannot be deleted.
const MainGroup = "main"

// Mode values.
const (
	ModeNormal = "normal"
	ModeCompat = "compat"
)

// Sync.RemoveStale values.
const (
	RemoveStaleNever  = "never"
	RemoveStalePrompt = "prompt"
)

// Rotation strategies (mirrors rotate.Strategy without importing it).
const (
	StrategyRandom     = "random"
	StrategyRoundRobin = "round-robin"
	StrategyManual     = "manual"
)

// ErrNewerConfig is returned when config.yaml was written by a newer CredOrbit.
var ErrNewerConfig = errors.New("config written by newer CredOrbit")

const configHeader = "# CredOrbit settings — no secrets are stored here\n"

// Config is the user-editable configuration stored in config.yaml.
type Config struct {
	Version  int                    `yaml:"version"`
	Mode     string                 `yaml:"mode"`
	SSO      SSOConfig              `yaml:"sso"`
	Profiles ProfilesConfig         `yaml:"profiles"`
	Groups   map[string]*Group      `yaml:"groups"`
	Accounts map[string]AccountPref `yaml:"accounts,omitempty"`
	Roles    RolesConfig            `yaml:"roles"`
	Sync     SyncConfig             `yaml:"sync"`
	Export   ExportConfig           `yaml:"export"`
	Rotation RotationConfig         `yaml:"rotation"`
	Network  NetworkConfig          `yaml:"network"`
	UI       UIConfig               `yaml:"ui"`
	Update   UpdateConfig           `yaml:"update"`
}

// SSOConfig selects the session used when none is given and the login flow.
type SSOConfig struct {
	DefaultSession string `yaml:"default_session"`
	LoginFlow      string `yaml:"login_flow"` // "browser" | "device"
}

// ProfilesConfig controls how new AWS config profiles are named and filled.
type ProfilesConfig struct {
	Prefix string `yaml:"prefix"`
	Region string `yaml:"region"`
	Output string `yaml:"output"`
}

// AccountPref holds per-account preferences keyed by account ID.
type AccountPref struct {
	Profile string `yaml:"profile,omitempty"`
	Role    string `yaml:"role,omitempty"` // preferred / manual role
}

// RankRule is one role-ranking rule (glob match, lower weight = stronger). The
// engine converts these into roles.Rule.
type RankRule struct {
	Match  string `yaml:"match"`
	Weight int    `yaml:"weight"`
}

// RolesConfig selects how roles are chosen.
type RolesConfig struct {
	Policy       model.RolePolicy `yaml:"policy"`
	Fallbacks    []string         `yaml:"fallbacks"`
	Ranking      []RankRule       `yaml:"ranking"` // empty = built-in default ranking
	UpdateConfig bool             `yaml:"update_config"`
}

// SyncConfig controls ~/.aws/config synchronisation with SSO discovery.
type SyncConfig struct {
	AddNew      bool    `yaml:"add_new"`
	RemoveStale string  `yaml:"remove_stale"` // "never" | "prompt"
	Threshold   float64 `yaml:"threshold"`
}

// ExportConfig controls writing ~/.aws/credentials.
type ExportConfig struct {
	Enabled          bool   `yaml:"enabled"`
	CredentialsFile  string `yaml:"credentials_file"` // "" = AWS default path
	StripPrefix      bool   `yaml:"strip_prefix"`
	DefaultProfile   string `yaml:"default_profile"`
	Concurrency      int    `yaml:"concurrency"`
	BackupKeep       int    `yaml:"backup_keep"`
	IncludeNewInMain bool   `yaml:"include_new_in_main"`
}

// RotationConfig controls Claude Code / Bedrock account rotation.
type RotationConfig struct {
	Target        string   `yaml:"target"`
	File          string   `yaml:"file"`     // "" = ~/.claude-aws-account
	Accounts      []string `yaml:"accounts"` // credential section names
	Strategy      string   `yaml:"strategy"`
	VerifyBedrock bool     `yaml:"verify_bedrock"`
	BedrockRegion string   `yaml:"bedrock_region"`
	BedrockModel  string   `yaml:"bedrock_model"`
}

// NetworkConfig controls TLS trust and connectivity probing.
type NetworkConfig struct {
	CABundle           string   `yaml:"ca_bundle"`
	ExtraCAs           []string `yaml:"extra_cas"`
	ProbeURL           string   `yaml:"probe_url"`
	CombinedBundlePath string   `yaml:"combined_bundle_path"`
}

// UIConfig controls presentation.
type UIConfig struct {
	Theme string `yaml:"theme"` // "default" | "light" | "mono"
	ASCII bool   `yaml:"ascii"`
}

// UpdateConfig controls self-update checks.
type UpdateConfig struct {
	Repo  string `yaml:"repo"` // "owner/name"
	Check bool   `yaml:"check"`
}

// Default returns the built-in configuration.
func Default() *Config {
	c := &Config{
		Version:  CurrentVersion,
		Mode:     ModeNormal,
		SSO:      SSOConfig{LoginFlow: "browser"},
		Profiles: ProfilesConfig{Prefix: "p_"},
		Groups:   map[string]*Group{},
		Accounts: map[string]AccountPref{},
		Roles: RolesConfig{
			Policy:    model.PolicyConfigured,
			Fallbacks: []string{},
		},
		Sync: SyncConfig{AddNew: true, RemoveStale: RemoveStalePrompt, Threshold: 0.8},
		Export: ExportConfig{
			StripPrefix: true,
			Concurrency: 10,
			BackupKeep:  5,
		},
		Rotation: RotationConfig{Target: "claude", Strategy: StrategyRandom},
		UI:       UIConfig{Theme: "default"},
		Update:   UpdateConfig{Repo: DefaultUpdateRepo, Check: true},
	}
	c.normalize()
	return c
}

// Load reads path; a missing file yields Default(). Unknown keys are ignored.
func Load(path string) (*Config, error) {
	c, _, err := LoadWithWarnings(path)
	return c, err
}

// LoadWithWarnings reads path and additionally reports unknown keys (as dotted
// paths) so typos in hand-edited files are surfaced instead of silently ignored.
func LoadWithWarnings(path string) (*Config, []string, error) {
	data, err := fsutil.ReadFileOptional(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read config %s: %w", path, err)
	}
	if data == nil {
		return Default(), nil, nil
	}
	c, warns, err := Parse(data)
	if err != nil {
		return nil, warns, fmt.Errorf("config %s: %w", path, err)
	}
	return c, warns, nil
}

// Parse decodes config YAML on top of the defaults, migrates it to
// CurrentVersion and fills zero values that have non-zero defaults.
func Parse(data []byte) (*Config, []string, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, nil, fmt.Errorf("parse yaml: %w", err)
	}
	c := Default()
	if root.Kind == 0 || len(root.Content) == 0 {
		return c, nil, nil // empty document
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return nil, nil, errors.New("parse yaml: top level must be a mapping")
	}
	warns := unknownKeys(doc, configType, "")

	version := 0
	if v := mapValue(doc, "version"); v != nil {
		if err := v.Decode(&version); err != nil {
			return nil, warns, fmt.Errorf("version: %w", err)
		}
	}
	if version > CurrentVersion {
		return nil, warns, fmt.Errorf("%w (version %d, this build supports %d); upgrade CredOrbit", ErrNewerConfig, version, CurrentVersion)
	}
	if version < 0 {
		return nil, warns, fmt.Errorf("invalid config version %d", version)
	}
	// Groups are decoded into a fresh map so defaults don't leak into user data.
	c.Groups = nil
	if err := doc.Decode(c); err != nil {
		return nil, warns, fmt.Errorf("decode: %w", err)
	}
	migrate(c, version)
	c.fillDefaults()
	c.normalize()
	return c, warns, nil
}

// migrate upgrades a config decoded from schema version from to CurrentVersion.
// Version 0 (no version key) has the same shape as version 1, so only the
// version number changes. Future schema changes add steps here.
func migrate(c *Config, from int) {
	_ = from
	c.Version = CurrentVersion
}

func (c *Config) fillDefaults() {
	d := Default()
	setStr := func(dst *string, def string) {
		if strings.TrimSpace(*dst) == "" {
			*dst = def
		}
	}
	setStr(&c.Mode, d.Mode)
	setStr(&c.SSO.LoginFlow, d.SSO.LoginFlow)
	setStr(&c.Profiles.Prefix, d.Profiles.Prefix)
	if c.Roles.Policy == "" {
		c.Roles.Policy = d.Roles.Policy
	}
	setStr(&c.Sync.RemoveStale, d.Sync.RemoveStale)
	if c.Sync.Threshold == 0 {
		c.Sync.Threshold = d.Sync.Threshold
	}
	if c.Export.Concurrency == 0 {
		c.Export.Concurrency = d.Export.Concurrency
	}
	if c.Export.BackupKeep == 0 {
		c.Export.BackupKeep = d.Export.BackupKeep
	}
	setStr(&c.Rotation.Target, d.Rotation.Target)
	setStr(&c.Rotation.Strategy, d.Rotation.Strategy)
	setStr(&c.UI.Theme, d.UI.Theme)
}

// normalize guarantees non-nil maps/slices, a "main" group and group names
// matching their map keys.
func (c *Config) normalize() {
	if c.Groups == nil {
		c.Groups = map[string]*Group{}
	}
	for name, g := range c.Groups {
		if g == nil {
			g = &Group{}
			c.Groups[name] = g
		}
		g.Name = name
	}
	if c.Groups[MainGroup] == nil {
		c.Groups[MainGroup] = &Group{Name: MainGroup, Description: "Accounts exported in main mode"}
	}
	if c.Accounts == nil {
		c.Accounts = map[string]AccountPref{}
	}
	if c.Roles.Fallbacks == nil {
		c.Roles.Fallbacks = []string{}
	}
}

var (
	groupNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	accountIDRE = regexp.MustCompile(`^\d{12}$`)
)

// ValidGroupName reports whether name is an acceptable group name.
func ValidGroupName(name string) bool { return groupNameRE.MatchString(name) }

// ValidAccountID reports whether id looks like a 12-digit AWS account ID.
func ValidAccountID(id string) bool { return accountIDRE.MatchString(id) }

// Validate checks every setting and returns all problems joined.
func (c *Config) Validate() error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	if c.Version != CurrentVersion {
		bad("version: %d is not supported (want %d)", c.Version, CurrentVersion)
	}
	switch c.Mode {
	case ModeNormal, ModeCompat:
	default:
		bad("mode: %q is invalid (want normal|compat)", c.Mode)
	}
	switch c.SSO.LoginFlow {
	case "browser", "device":
	default:
		bad("sso.login_flow: %q is invalid (want browser|device)", c.SSO.LoginFlow)
	}
	if strings.ContainsAny(c.Profiles.Prefix, " \t[]") {
		bad("profiles.prefix: %q must not contain spaces or brackets", c.Profiles.Prefix)
	}
	if _, err := model.ParseRolePolicy(string(c.Roles.Policy)); err != nil {
		bad("roles.policy: %v", err)
	}
	for i, r := range c.Roles.Ranking {
		if strings.TrimSpace(r.Match) == "" {
			bad("roles.ranking[%d]: match must not be empty", i)
		}
	}
	for id := range c.Accounts {
		if !ValidAccountID(id) {
			bad("accounts: key %q is not a 12-digit account ID", id)
		}
	}
	switch c.Sync.RemoveStale {
	case RemoveStaleNever, RemoveStalePrompt:
	default:
		bad("sync.remove_stale: %q is invalid (want never|prompt)", c.Sync.RemoveStale)
	}
	if !(c.Sync.Threshold > 0 && c.Sync.Threshold <= 1) {
		bad("sync.threshold: %v must be in (0, 1]", c.Sync.Threshold)
	}
	if c.Export.Concurrency < 1 || c.Export.Concurrency > 64 {
		bad("export.concurrency: %d must be between 1 and 64", c.Export.Concurrency)
	}
	if c.Export.BackupKeep < 0 {
		bad("export.backup_keep: %d must not be negative", c.Export.BackupKeep)
	}
	switch c.Rotation.Strategy {
	case StrategyRandom, StrategyRoundRobin, StrategyManual:
	default:
		bad("rotation.strategy: %q is invalid (want random|round-robin|manual)", c.Rotation.Strategy)
	}
	if strings.TrimSpace(c.Rotation.Target) == "" {
		bad("rotation.target must not be empty")
	}
	for i, a := range c.Rotation.Accounts {
		if strings.TrimSpace(a) == "" || strings.ContainsAny(a, "[]\r\n") {
			bad("rotation.accounts[%d]: %q is not a valid credentials section name", i, a)
		}
	}
	switch c.UI.Theme {
	case "default", "light", "mono":
	default:
		bad("ui.theme: %q is invalid (want default|light|mono)", c.UI.Theme)
	}
	if c.Groups[MainGroup] == nil {
		bad("groups: %q group is missing", MainGroup)
	}
	for _, name := range c.GroupNames() {
		if !ValidGroupName(name) {
			bad("groups: name %q is invalid (lowercase letters, digits, '.', '_', '-'; max 64)", name)
			continue
		}
		for i, m := range c.Groups[name].Members {
			switch {
			case m.AccountID != "" && !ValidAccountID(m.AccountID):
				bad("groups.%s.members[%d]: account_id %q is not 12 digits", name, i, m.AccountID)
			case m.AccountID == "" && strings.TrimSpace(m.Profile) == "":
				bad("groups.%s.members[%d]: needs an account_id or a profile", name, i)
			}
		}
	}
	return errors.Join(errs...)
}

// Marshal renders the config deterministically with the header comment.
func (c *Config) Marshal() ([]byte, error) {
	out := *c
	out.Groups = make(map[string]*Group, len(c.Groups))
	for name, g := range c.Groups {
		if g == nil {
			continue
		}
		cp := *g
		cp.Members = sortedMembers(g.Members)
		out.Groups[name] = &cp
	}
	var buf bytes.Buffer
	buf.WriteString(configHeader)
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&out); err != nil { // yaml.v3 sorts map keys
		return nil, fmt.Errorf("encode config: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Save validates and atomically writes the config with 0600 permissions.
func (c *Config) Save(path string) error {
	c.normalize()
	if err := c.Validate(); err != nil {
		return fmt.Errorf("refusing to save invalid config: %w", err)
	}
	data, err := c.Marshal()
	if err != nil {
		return err
	}
	if err := fsutil.AtomicWrite(path, data, 0o600); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	return nil
}

func sortedMembers(ms []Member) []Member {
	out := append([]Member(nil), ms...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Profile != out[j].Profile {
			return out[i].Profile < out[j].Profile
		}
		return out[i].AccountID < out[j].AccountID
	})
	return out
}

func mapValue(n *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// DefaultUpdateRepo is the GitHub repository checked by `credorbit update --check`.
// Override with `credorbit config set update.repo owner/name` (forks, mirrors).
const DefaultUpdateRepo = "PiniPP/credorbit"
