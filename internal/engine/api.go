package engine

import (
	"context"
	"time"

	"github.com/credorbit/credorbit/internal/model"
)

// ScopeKind selects which accounts/profiles an operation covers.
type ScopeKind string

const (
	ScopeAll      ScopeKind = "all"      // every SSO profile of the session (with the prefix unless AnyPrefix)
	ScopeGroup    ScopeKind = "group"    // members of a saved group (e.g. "main")
	ScopeSelected ScopeKind = "selected" // explicit account IDs / profiles chosen for this run only
	ScopeSingle   ScopeKind = "single"   // exactly one profile
)

// Scope is a temporary selection; using it never modifies saved groups.
type Scope struct {
	Kind       ScopeKind
	Group      string
	AccountIDs []string
	Profiles   []string
	AnyPrefix  bool
	// IncludeNew adds accounts newly discovered in this run (script-compatible main mode).
	IncludeNew bool
}

// Interaction is how the engine asks the user for decisions. The CLI implements it
// with prompts (or auto-answers under --yes / non-TTY), the TUI with dialogs.
type Interaction interface {
	// Confirm asks a yes/no question. def is the answer used under --yes.
	Confirm(ctx context.Context, title, detail string, def bool) (bool, error)
	// ReviewPlan shows a change preview and returns whether to apply it.
	ReviewPlan(ctx context.Context, title, preview string) (bool, error)
	// SelectAccounts lets the user pick accounts (preselected marked). Returns account IDs.
	SelectAccounts(ctx context.Context, title string, items []AccountRow, preselected []string) ([]string, error)
	// SelectRole lets the user pick one of the assigned roles.
	SelectRole(ctx context.Context, account model.DiscoveredAccount, current string) (string, error)
	// LoginPrompt shows the verification URL / user code during SSO login.
	LoginPrompt(url, code string)
}

// AccountRow is a display row combining discovery, config and group membership.
type AccountRow struct {
	Account  model.DiscoveredAccount
	Profiles []string // configured profile names for this account
	Role     string   // configured role of the primary profile
	Env      string   // inferred environment: prd|stg|dev|test|sandbox|"" (from name)
	Groups   []string
	Status   model.AccountStatus
	CredsExp time.Time // expiration of exported credentials, zero if none/unknown
}

// LoginState describes the SSO token state of a session (never the token itself).
type LoginState struct {
	Session   model.SSOSession
	LoggedIn  bool
	ExpiresAt time.Time
	Refresh   bool // a refresh token is available (silent renewal possible)
	Err       string
}

// FullOptions configure the complete "setup --full" workflow.
type FullOptions struct {
	Session     string
	Compat      bool // script-compatible mode
	Scope       Scope
	ForceLogin  bool
	LoginFlow   string // browser|device
	Policy      model.RolePolicy
	NoSync      bool
	Export      *bool // nil = from config/mode
	UpdateRoles *bool // nil = from config/mode
	Rotate      *bool // nil = from config/mode
	DryRun      bool
	Yes         bool // auto-approve non-destructive changes; destructive still need AllowRemove
	AllowRemove bool // approve stale-profile removal non-interactively
	EditMain    bool // offer the Main-group editor during setup
	Parallel    int
	Fallbacks   []string // nil = roles.fallbacks (env FALLBACK_ROLES)
}

// StepResult is one line of the final execution report.
type StepResult struct {
	Step    string
	OK      bool
	Skipped bool
	Summary string
	Details []string
}

// Report is the result of a workflow.
type Report struct {
	Steps    []StepResult
	Exported int
	Failed   int
	Backups  []string
	Warnings []string
	Duration time.Duration
}

// SyncOptions control a profile-synchronisation plan.
type SyncOptions struct {
	AddNew      *bool                                // nil = config sync.add_new
	RemoveStale bool                                 // include stale-profile removals in the plan (still reviewed)
	AccountIDs  []string                             // restrict to these accounts (nil = all)
	RoleUpdates map[string]string                    // profile → role to persist (approved role changes)
	ChooseRole  func(model.DiscoveredAccount) string // role for new profiles; nil = policy-based
}

// ExportOptions control credential retrieval/export.
type ExportOptions struct {
	Policy      model.RolePolicy // "" = config roles.policy
	Parallel    int              // 0 = config export.concurrency
	WriteFile   *bool            // nil = config export.enabled; false = fetch/verify only
	UpdateRoles *bool            // nil = config roles.update_config
	DryRun      bool
	Manual      map[string]string // accountID → role (manual policy / TUI role picker)
	Fallbacks   []string          // nil = roles.fallbacks (env FALLBACK_ROLES); script policy only
}

// ExportLine is one profile's outcome (no secrets).
type ExportLine struct {
	Profile    string
	Section    string
	AccountID  string
	Role       string // role actually used
	Expiration time.Time
	Err        string
	ErrClass   string // "no_access" | "unauthorized" | "transient" | "canceled" | "other" | ""
}

// ExportResult summarises an export run.
type ExportResult struct {
	Lines       []ExportLine
	Succeeded   int
	Failed      int
	Written     []string // credential sections written
	Kept        []string // failed targets whose previous section was kept
	Backup      string
	RoleChanges map[string]string // profile → role used, where different from configured
	RolesSaved  bool              // RoleChanges were persisted to ~/.aws/config
	Warnings    []string
}

// SyncResult summarises an applied sync plan.
type SyncResult struct {
	Applied  int
	Backup   string
	Warnings []string
}

// GroupInfo describes a saved account group.
type GroupInfo struct {
	Name        string
	Description string
	Members     []GroupMember
}

// GroupMember is a group member resolved against configuration and discovery.
type GroupMember struct {
	AccountID   string
	AccountName string
	Profile     string
	Role        string
	Status      model.AccountStatus
}

// RotationInfo is the Claude/Bedrock rotation state.
type RotationInfo struct {
	Target   string
	File     string
	Current  string
	Strategy string
	Accounts []RotationAccount
}

// RotationAccount is one rotation candidate with its eligibility.
type RotationAccount struct {
	Section  string
	Eligible bool
	Reason   string    // why not eligible
	Expires  time.Time // credential expiry if known
}

// RotateOptions control one rotation.
type RotateOptions struct {
	Strategy string // "" = config
	Set      string // explicit account (section) → manual set
	Verify   *bool  // nil = config rotation.verify_bedrock
	DryRun   bool
}

// SettingInfo is one user-editable setting, addressed by a dotted key such as
// "export.enabled" or "roles.policy". Values are strings; lists are comma-separated.
type SettingInfo struct {
	Key         string
	Value       string
	Description string
	Choices     []string // non-empty for enumerations (incl. "true","false" for booleans)
}

// UIOptions are passed to the TUI entry point.
type UIOptions struct {
	Demo   bool   // use a fake in-memory service (no AWS, no file writes)
	Screen string // initial screen: "" (menu) | "groups" | "main" | "accounts" | "rotate" | "setup"
	Group  string // group to edit when Screen == "groups"
}

// Check is one doctor diagnostic.
type Check struct {
	Area   string // "aws-config", "sso", "network", "credentials", "permissions", "claude", "app"
	Name   string
	Status string // "ok" | "warn" | "fail" | "info"
	Detail string
	Remedy string
}

// Status is the machine-readable state summary (`credorbit status --json`). No secrets.
type Status struct {
	Version     string             `json:"version"`
	Mode        string             `json:"mode"`
	ConfigFile  string             `json:"config_file"`
	AWSConfig   string             `json:"aws_config"`
	Credentials string             `json:"credentials_file"`
	Sessions    []SessionStatus    `json:"sessions"`
	Profiles    int                `json:"profiles"`
	Groups      map[string]int     `json:"groups"`
	Exported    []CredentialStatus `json:"exported"`
	Rotation    *RotationInfo      `json:"rotation,omitzero"`
	LastRun     time.Time          `json:"last_run,omitzero"`
}

type SessionStatus struct {
	Name         string    `json:"name"`
	StartURL     string    `json:"start_url"`
	Region       string    `json:"region"`
	LoggedIn     bool      `json:"logged_in"`
	ExpiresAt    time.Time `json:"expires_at,omitzero"`
	Refresh      bool      `json:"refreshable"`
	Accounts     int       `json:"accounts_discovered"`
	DiscoveredAt time.Time `json:"discovered_at,omitzero"`
}

type CredentialStatus struct {
	Section string    `json:"section"`
	Expires time.Time `json:"expires,omitzero"`
	Expired bool      `json:"expired"`
	Managed bool      `json:"managed"`
}

// Service is everything the CLI and TUI need. *Engine implements it; the TUI also
// has a fake implementation for tests and `credorbit ui --demo`.
type Service interface {
	// front-end wiring: decisions and live progress. Safe to call before any operation.
	SetInteraction(Interaction)
	SetReporter(model.Reporter)

	// configuration & paths
	ConfigPath() string
	Mode() string
	SetMode(mode string) error // "normal" | "compat"; persists
	SaveConfig() error
	Version() string
	Settings() []SettingInfo            // user-editable settings (Settings screen, `config list`)
	SetSetting(key, value string) error // validates, persists

	// sessions & login
	Sessions() ([]model.SSOSession, error)
	DefaultSession() string
	SetDefaultSession(name string) error
	AddSession(s model.SSOSession) error // writes [sso-session] to ~/.aws/config after Interaction approval
	LoginStatus(ctx context.Context, session string) LoginState
	Login(ctx context.Context, session string, flow string, force bool) (LoginState, error)
	Logout(ctx context.Context, session string) error

	// discovery & accounts
	Discover(ctx context.Context, session string) (*model.Discovery, error) // live; result cached
	CachedDiscovery(session string) *model.Discovery                        // nil if none; Cached=true
	Accounts(session string) []AccountRow                                   // from latest (live or cached) discovery + config
	SetPreferredRole(accountID, role string) error

	// sync & export
	PlanSync(ctx context.Context, session string, o SyncOptions) (preview string, empty bool, err error)
	ApplySync(ctx context.Context, session string, o SyncOptions) (*SyncResult, error) // rebuilds plan, applies after ReviewPlan
	Export(ctx context.Context, session string, scope Scope, o ExportOptions) (*ExportResult, error)
	RunFull(ctx context.Context, o FullOptions) (*Report, error)

	// groups (never touch AWS profiles or credentials)
	Groups() []GroupInfo
	Group(name string) (GroupInfo, error)
	CreateGroup(name, description string) error
	DeleteGroup(name string) error
	RenameGroup(oldName, newName string) error
	GroupAdd(name string, keys ...string) error // key = profile name or account ID
	GroupRemove(name string, keys ...string) error
	GroupSet(name string, accountIDs []string) error // replace membership (TUI editor save)
	NewAccounts(session string) []AccountRow         // discovered but in no group / unconfigured

	// rotation
	Rotation() RotationInfo
	Rotate(ctx context.Context, o RotateOptions) (from, to string, err error)
	SetRotationAccounts(sections []string) error
	ShellIntegration(shell string) (string, error)

	// diagnostics
	Status(ctx context.Context) Status
	Doctor(ctx context.Context) []Check

	// migration & updates
	ImportLegacy(ctx context.Context, path string, compat bool) ([]string, error)
	CheckUpdate(ctx context.Context) (latest string, url string, newer bool, err error)
}
