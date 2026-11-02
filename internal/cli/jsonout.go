package cli

import (
	"time"

	"github.com/credorbit/credorbit/internal/engine"
)

// Stable JSON shapes of CLI output. They never contain secrets.

type accountJSON struct {
	ID                string     `json:"id"`
	Name              string     `json:"name"`
	Status            string     `json:"status"`
	Env               string     `json:"env"`
	Profiles          []string   `json:"profiles"`
	Role              string     `json:"role"`
	Roles             []string   `json:"roles"`
	RolesError        string     `json:"roles_error,omitempty"`
	Groups            []string   `json:"groups"`
	CredentialsExpire *time.Time `json:"credentials_expire,omitempty"`
}

type lineJSON struct {
	Profile    string     `json:"profile"`
	Section    string     `json:"section"`
	AccountID  string     `json:"account_id"`
	Role       string     `json:"role"`
	Expiration *time.Time `json:"expiration,omitempty"`
	Error      string     `json:"error,omitempty"`
	ErrorClass string     `json:"error_class,omitempty"`
}

type exportJSON struct {
	Succeeded   int               `json:"succeeded"`
	Failed      int               `json:"failed"`
	Written     []string          `json:"written"`
	Kept        []string          `json:"kept"`
	Backup      string            `json:"backup,omitempty"`
	RoleChanges map[string]string `json:"role_changes"`
	RolesSaved  bool              `json:"roles_saved"`
	Warnings    []string          `json:"warnings"`
	Lines       []lineJSON        `json:"lines"`
}

type stepJSON struct {
	Step    string   `json:"step"`
	OK      bool     `json:"ok"`
	Skipped bool     `json:"skipped"`
	Summary string   `json:"summary"`
	Details []string `json:"details"`
}

type reportJSON struct {
	Steps      []stepJSON `json:"steps"`
	Exported   int        `json:"exported"`
	Failed     int        `json:"failed"`
	Backups    []string   `json:"backups"`
	Warnings   []string   `json:"warnings"`
	DurationMS int64      `json:"duration_ms"`
}

type checkJSON struct {
	Area   string `json:"area"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Remedy string `json:"remedy,omitempty"`
}

type memberJSON struct {
	AccountID   string `json:"account_id"`
	AccountName string `json:"account_name"`
	Profile     string `json:"profile"`
	Role        string `json:"role"`
	Status      string `json:"status"`
}

type groupJSON struct {
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Members     []memberJSON `json:"members"`
}

type settingJSON struct {
	Key         string   `json:"key"`
	Value       string   `json:"value"`
	Description string   `json:"description"`
	Choices     []string `json:"choices"`
}

type rotationAccountJSON struct {
	Section  string     `json:"section"`
	Eligible bool       `json:"eligible"`
	Reason   string     `json:"reason,omitempty"`
	Expires  *time.Time `json:"expires,omitempty"`
}

type rotationJSON struct {
	Target   string                `json:"target"`
	File     string                `json:"file"`
	Current  string                `json:"current"`
	Strategy string                `json:"strategy"`
	Accounts []rotationAccountJSON `json:"accounts"`
}

type roleJSON struct {
	AccountID   string   `json:"account_id"`
	AccountName string   `json:"account_name"`
	Profiles    []string `json:"profiles"`
	Assigned    []string `json:"assigned"`
	Configured  string   `json:"configured"`
	Preferred   string   `json:"preferred,omitempty"`
}

type versionJSON struct {
	Version string `json:"version"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	Go      string `json:"go"`
}

func nz[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func tp(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func toAccountJSON(r engine.AccountRow) accountJSON {
	return accountJSON{ID: r.Account.ID, Name: r.Account.Name, Status: string(r.Status), Env: r.Env, Profiles: nz(r.Profiles),
		Role: r.Role, Roles: nz(r.Account.Roles), RolesError: r.Account.RolesErr, Groups: nz(r.Groups), CredentialsExpire: tp(r.CredsExp)}
}

func toExportJSON(r *engine.ExportResult) exportJSON {
	out := exportJSON{Succeeded: r.Succeeded, Failed: r.Failed, Written: nz(r.Written), Kept: nz(r.Kept), Backup: r.Backup,
		RoleChanges: r.RoleChanges, RolesSaved: r.RolesSaved, Warnings: nz(r.Warnings), Lines: []lineJSON{}}
	if out.RoleChanges == nil {
		out.RoleChanges = map[string]string{}
	}
	for _, l := range r.Lines {
		out.Lines = append(out.Lines, lineJSON{Profile: l.Profile, Section: l.Section, AccountID: l.AccountID, Role: l.Role, Expiration: tp(l.Expiration), Error: l.Err, ErrorClass: l.ErrClass})
	}
	return out
}

func toReportJSON(r *engine.Report) reportJSON {
	out := reportJSON{Steps: []stepJSON{}, Exported: r.Exported, Failed: r.Failed, Backups: nz(r.Backups), Warnings: nz(r.Warnings), DurationMS: r.Duration.Milliseconds()}
	for _, s := range r.Steps {
		out.Steps = append(out.Steps, stepJSON{Step: s.Step, OK: s.OK, Skipped: s.Skipped, Summary: s.Summary, Details: nz(s.Details)})
	}
	return out
}

func toGroupJSON(g engine.GroupInfo) groupJSON {
	out := groupJSON{Name: g.Name, Description: g.Description, Members: []memberJSON{}}
	for _, m := range g.Members {
		out.Members = append(out.Members, memberJSON{AccountID: m.AccountID, AccountName: m.AccountName, Profile: m.Profile, Role: m.Role, Status: string(m.Status)})
	}
	return out
}

func toRotationJSON(r engine.RotationInfo) rotationJSON {
	out := rotationJSON{Target: r.Target, File: r.File, Current: r.Current, Strategy: r.Strategy, Accounts: []rotationAccountJSON{}}
	for _, a := range r.Accounts {
		out.Accounts = append(out.Accounts, rotationAccountJSON{Section: a.Section, Eligible: a.Eligible, Reason: a.Reason, Expires: tp(a.Expires)})
	}
	return out
}
