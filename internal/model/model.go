// Package model holds the domain types shared by every CredOrbit package.
// It has no dependencies on other internal packages.
package model

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

// SSOSession mirrors an `[sso-session NAME]` block in the AWS shared config.
type SSOSession struct {
	Name     string `json:"name" yaml:"name"`
	StartURL string `json:"start_url" yaml:"start_url"`
	Region   string `json:"region" yaml:"region"`
	Scopes   string `json:"scopes,omitempty" yaml:"scopes,omitempty"`
}

// Account is an AWS account visible to the authenticated SSO user.
type Account struct {
	ID    string `json:"id" yaml:"id"`
	Name  string `json:"name" yaml:"name"`
	Email string `json:"email,omitempty" yaml:"email,omitempty"`
}

// DiscoveredAccount is an account plus the roles SSO says are assigned to the user.
// RolesErr is set when role discovery for this one account failed (roles unknown,
// which is NOT the same as "no roles").
type DiscoveredAccount struct {
	Account  `yaml:",inline"`
	Roles    []string `json:"roles" yaml:"roles"`
	RolesErr string   `json:"roles_error,omitempty" yaml:"roles_error,omitempty"`
}

// HasRole reports whether role is among the discovered roles.
func (d DiscoveredAccount) HasRole(role string) bool {
	for _, r := range d.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// Discovery is the result of one account-discovery run against one SSO session.
// Complete is true only when the full paginated account list was retrieved; any
// API failure leaves Complete=false and Err set. A non-complete discovery must
// never be used to decide that an account was revoked.
type Discovery struct {
	Session  string              `json:"session" yaml:"session"`
	StartURL string              `json:"start_url" yaml:"start_url"`
	At       time.Time           `json:"at" yaml:"at"`
	Accounts []DiscoveredAccount `json:"accounts" yaml:"accounts"`
	Complete bool                `json:"complete" yaml:"complete"`
	Err      string              `json:"error,omitempty" yaml:"error,omitempty"`
	Cached   bool                `json:"-" yaml:"-"` // loaded from the offline cache, not live
}

// ByID returns a lookup map of the discovered accounts.
func (d *Discovery) ByID() map[string]DiscoveredAccount {
	m := make(map[string]DiscoveredAccount, len(d.Accounts))
	for _, a := range d.Accounts {
		m[a.ID] = a
	}
	return m
}

// SortAccounts orders accounts by name, then ID.
func SortAccounts(a []DiscoveredAccount) {
	sort.SliceStable(a, func(i, j int) bool {
		if a[i].Name != a[j].Name {
			return a[i].Name < a[j].Name
		}
		return a[i].ID < a[j].ID
	})
}

// Profile is a `[profile NAME]` block in the AWS shared config that uses SSO.
type Profile struct {
	Name      string `json:"name"`       // without the "profile " prefix
	Session   string `json:"session"`    // sso_session
	AccountID string `json:"account_id"` // sso_account_id
	Role      string `json:"role"`       // sso_role_name
	Region    string `json:"region,omitempty"`
	Output    string `json:"output,omitempty"`
	// Legacy profiles configure sso_start_url/sso_region inline instead of sso_session.
	LegacyStartURL string `json:"legacy_start_url,omitempty"`
	LegacyRegion   string `json:"legacy_region,omitempty"`
}

// IsSSO reports whether the profile has enough fields to fetch SSO role credentials.
func (p Profile) IsSSO() bool {
	return p.AccountID != "" && p.Role != "" && (p.Session != "" || p.LegacyStartURL != "")
}

// AccountStatus classifies an account when comparing discovery with configuration.
type AccountStatus string

const (
	StatusAvailable AccountStatus = "available" // configured and visible in SSO
	StatusNew       AccountStatus = "new"       // visible in SSO, not configured
	StatusMissing   AccountStatus = "missing"   // configured, not visible in a COMPLETE discovery
	StatusUnknown   AccountStatus = "unknown"   // discovery failed or incomplete; cannot decide
)

// RolePolicy selects which assigned role is used for an account.
type RolePolicy string

const (
	PolicyConfigured RolePolicy = "configured" // profile's sso_role_name; no substitution
	PolicyManual     RolePolicy = "manual"     // role chosen by the user, stored per account
	PolicyStrongest  RolePolicy = "strongest"  // highest-ranked assigned role
	PolicyReadOnly   RolePolicy = "readonly"   // lowest-privilege assigned role preferred
	PolicyScript     RolePolicy = "script"     // legacy: assigned ranked, then configured, then fallbacks
)

// ParseRolePolicy validates a policy name.
func ParseRolePolicy(s string) (RolePolicy, error) {
	switch RolePolicy(s) {
	case PolicyConfigured, PolicyManual, PolicyStrongest, PolicyReadOnly, PolicyScript:
		return RolePolicy(s), nil
	}
	return "", fmt.Errorf("unknown role policy %q (want configured|manual|strongest|readonly|script)", s)
}

// Credentials are temporary AWS role credentials. String/GoString/Format never
// reveal the secret parts so accidental logging cannot leak them.
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	Expiration      time.Time
}

func (c Credentials) String() string {
	return fmt.Sprintf("Credentials{AccessKeyID:%s, Expiration:%s}", MaskKeyID(c.AccessKeyID), c.Expiration.Format(time.RFC3339))
}
func (c Credentials) GoString() string { return c.String() }

// Valid reports whether all parts are present and not expired.
func (c Credentials) Valid(now time.Time) bool {
	return c.AccessKeyID != "" && c.SecretAccessKey != "" && c.SessionToken != "" &&
		(c.Expiration.IsZero() || c.Expiration.After(now))
}

// MaskKeyID shows only the first 4 characters of an access key ID.
func MaskKeyID(id string) string {
	if len(id) <= 4 {
		return "****"
	}
	return id[:4] + "****"
}

// Token is an SSO OIDC access token (AWS CLI cache compatible). Never log it.
type Token struct {
	StartURL              string
	Region                string
	AccessToken           string
	ExpiresAt             time.Time
	RefreshToken          string
	ClientID              string
	ClientSecret          string
	RegistrationExpiresAt time.Time
}

func (t Token) String() string {
	return fmt.Sprintf("Token{StartURL:%s, ExpiresAt:%s}", t.StartURL, t.ExpiresAt.Format(time.RFC3339))
}
func (t Token) GoString() string { return t.String() }

// Expired reports whether the access token is expired (with a safety skew).
func (t Token) Expired(now time.Time) bool {
	return t.AccessToken == "" || !t.ExpiresAt.After(now.Add(60*time.Second))
}

// Error classes. Wrap with fmt.Errorf("...: %w", ErrX) and test with errors.Is.
var (
	// ErrNoAccess: SSO explicitly refused (ForbiddenException) — the role/account
	// is not assigned. This is the ONLY error that signals revoked access.
	ErrNoAccess = errors.New("no access")
	// ErrUnauthorized: token missing/expired/invalid (UnauthorizedException) — re-login.
	ErrUnauthorized = errors.New("sso token expired or invalid")
	// ErrTransient: throttling, 5xx, network/TLS failures — retry, never treat as revoked.
	ErrTransient = errors.New("transient failure")
	// ErrNotFound: a requested profile/account/group does not exist.
	ErrNotFound = errors.New("not found")
	// ErrCanceled: user canceled.
	ErrCanceled = errors.New("canceled")
)

// Event is a progress notification emitted by long-running operations so CLI and
// TUI can render live progress. Never put secrets in Message.
type Event struct {
	Kind    EventKind
	Step    string // workflow step identifier, e.g. "discover", "export"
	Subject string // profile or account the event refers to
	Message string
	Done    int
	Total   int
	Err     error
}

type EventKind int

const (
	EventInfo EventKind = iota
	EventStepStart
	EventStepDone
	EventProgress // Done/Total updated
	EventItemOK
	EventItemFailed
	EventWarning
)

// Reporter receives events. Implementations must be safe for concurrent use.
type Reporter interface{ Report(Event) }

// ReporterFunc adapts a function to Reporter.
type ReporterFunc func(Event)

func (f ReporterFunc) Report(e Event) { f(e) }

// Discard is a Reporter that drops everything.
var Discard Reporter = ReporterFunc(func(Event) {})
