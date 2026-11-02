package importer

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/PiniPP/credorbit/internal/model"
	"github.com/PiniPP/credorbit/internal/state"
)

func loadFixture(t *testing.T) *Legacy {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "legacy.sh"))
	if err != nil {
		t.Fatal(err)
	}
	l, err := ParseScript(data)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestParseScript(t *testing.T) {
	l := loadFixture(t)
	wantMain := []string{"p_alpha-dev", "p_alpha-prd", "p_beta-stg", "p_gamma-sandbox-01", "p_gamma-sandbox-02", "p_not-configured"}
	if !slices.Equal(l.MainProfiles, wantMain) {
		t.Errorf("MainProfiles = %q", l.MainProfiles)
	}
	if want := []string{"gamma-sandbox-01", "gamma-sandbox-02", "alpha-dev"}; !slices.Equal(l.ClaudeRotation, want) {
		t.Errorf("ClaudeRotation = %q", l.ClaudeRotation)
	}
	if want := []string{"AdministratorAccess", "Example-Admin-Limited"}; !slices.Equal(l.FallbackRoles, want) {
		t.Errorf("FallbackRoles = %q", l.FallbackRoles)
	}
	checks := map[string][2]string{
		"SSOSession":     {l.SSOSession, "example"},
		"DefaultProfile": {l.DefaultProfile, "p_alpha-dev"},
		"SASECA":         {l.SASECA, "$HOME/.aws/ExampleRootCA.pem"},
		"ProbeURL":       {l.ProbeURL, "https://portal.sso.example-region.amazonaws.com/"},
		"CredsFile":      {l.CredsFile, "$HOME/.aws/credentials"},
		"Prefix":         {l.Prefix, "p_"},
		"RotationFile":   {l.RotationFile, "$HOME/.claude-aws-account"},
	}
	for name, c := range checks {
		if c[0] != c[1] {
			t.Errorf("%s = %q, want %q", name, c[0], c[1])
		}
	}
	if l.Parallel != 7 {
		t.Errorf("Parallel = %d", l.Parallel)
	}
	if len(l.Warnings) != 1 || !strings.Contains(l.Warnings[0], "MAIN_PROFILES: 1 duplicate") {
		t.Errorf("Warnings = %q", l.Warnings)
	}
}

func TestParseScriptMissingPiecesStayEmpty(t *testing.T) {
	l, err := ParseScript([]byte("#!/bin/bash\nMAIN_PROFILES=(dev-a prod_b)\nPARALLEL=\"${PARALLEL:-lots}\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(l.MainProfiles, []string{"dev-a", "prod_b"}) {
		t.Fatalf("MainProfiles = %q", l.MainProfiles)
	}
	if l.Prefix != "" || l.SSOSession != "" || l.ProbeURL != "" || l.DefaultProfile != "" || l.Parallel != 0 || l.FallbackRoles != nil || l.ClaudeRotation != nil {
		t.Fatalf("invented values: %+v", l)
	}
	if len(l.Warnings) != 1 {
		t.Fatalf("Warnings = %q", l.Warnings)
	}
}

func TestParseScriptRejectsUnrelated(t *testing.T) {
	if _, err := ParseScript([]byte("#!/bin/sh\necho hello\n")); !errors.Is(err, ErrNotLegacyScript) {
		t.Fatalf("err = %v", err)
	}
	if _, err := ParseScript(nil); !errors.Is(err, ErrNotLegacyScript) {
		t.Fatalf("err = %v", err)
	}
}

func TestParseScriptCRLFAndDynamicFirst(t *testing.T) {
	src := "CLAUDE_ROTATION=(\"${X[@]}\")\r\nCLAUDE_ROTATION=(\r\n  'a-1'\r\n  b-2 # c\r\n)\r\n"
	l, err := ParseScript([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(l.ClaudeRotation, []string{"a-1", "b-2"}) {
		t.Fatalf("ClaudeRotation = %q", l.ClaudeRotation)
	}
}

func TestExpandPaths(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	l := &Legacy{CredsFile: "$HOME/.aws/credentials", SASECA: "~/ca.pem", RotationFile: "${HOME}/x"}
	x := l.ExpandPaths()
	if x.CredsFile != filepath.Join(home, ".aws", "credentials") || x.SASECA != filepath.Join(home, "ca.pem") || x.RotationFile != filepath.Join(home, "x") {
		t.Fatalf("expanded = %+v", x)
	}
	if l.CredsFile != "$HOME/.aws/credentials" {
		t.Fatal("receiver modified")
	}
}

func fixtureProfiles() []model.Profile {
	return []model.Profile{
		{Name: "p_alpha-dev", Session: "example", AccountID: "100000000001", Role: "Admin"},
		{Name: "p_alpha-prd", Session: "example", AccountID: "100000000002", Role: "ReadOnly"},
		{Name: "p_beta-stg", Session: "example", AccountID: "100000000003", Role: "Admin"},
		{Name: "p_gamma-sandbox-01", Session: "example", AccountID: "100000000004", Role: "Admin"},
		{Name: "p_gamma-sandbox-02", Session: "example", AccountID: "100000000005", Role: "Admin"},
		{Name: "p_unrelated", Session: "example", AccountID: "100000000009", Role: "Admin"},
	}
}

func TestApply(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows
	if err := os.MkdirAll(filepath.Join(home, ".aws"), 0o700); err != nil {
		t.Fatal(err)
	}
	ca := filepath.Join(home, ".aws", "ExampleRootCA.pem")
	if err := os.WriteFile(ca, []byte("pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	l := loadFixture(t)
	c := state.Default()
	notes := l.Apply(c, fixtureProfiles())

	if c.SSO.DefaultSession != "example" || c.Export.Concurrency != 7 || c.Export.DefaultProfile != "p_alpha-dev" || c.Profiles.Prefix != "p_" {
		t.Fatalf("config = %+v", c)
	}
	if c.Export.CredentialsFile != "" {
		t.Fatalf("default credentials path must stay empty, got %q", c.Export.CredentialsFile)
	}
	if c.Rotation.File != "" {
		t.Fatalf("default rotation file must stay empty, got %q", c.Rotation.File)
	}
	if !slices.Equal(c.Network.ExtraCAs, []string{ca}) || c.Network.ProbeURL == "" {
		t.Fatalf("network = %+v", c.Network)
	}
	if !slices.Equal(c.Roles.Fallbacks, l.FallbackRoles) || !slices.Equal(c.Rotation.Accounts, l.ClaudeRotation) {
		t.Fatalf("roles/rotation = %+v %+v", c.Roles, c.Rotation)
	}
	main := c.Groups[state.MainGroup]
	if len(main.Members) != 5 || main.Members[0] != (state.Member{AccountID: "100000000001", Profile: "p_alpha-dev"}) {
		t.Fatalf("main = %+v", main.Members)
	}
	joined := strings.Join(notes, "\n")
	if !strings.Contains(joined, "main: p_not-configured not found in ~/.aws/config, skipped") {
		t.Fatalf("notes = %s", joined)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("imported config invalid: %v", err)
	}

	// Idempotent: second apply changes nothing (only the not-found note recurs).
	before, _ := c.Marshal()
	notes2 := l.Apply(c, fixtureProfiles())
	after, _ := c.Marshal()
	if string(before) != string(after) {
		t.Fatalf("re-apply changed config:\n%s\n---\n%s", before, after)
	}
	for _, n := range notes2 {
		if !strings.Contains(n, "not found") {
			t.Fatalf("unexpected note on re-apply: %q", n)
		}
	}
}

func TestApplyCustomPathsAndMissingCA(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	l := &Legacy{CredsFile: "$HOME/other/creds", SASECA: "$HOME/missing.pem", RotationFile: "~/rot", SSOSession: "s"}
	c := state.Default()
	c.SSO.DefaultSession = "keep"
	notes := l.Apply(c, nil)
	if c.SSO.DefaultSession != "keep" {
		t.Fatal("existing default session overwritten")
	}
	if c.Export.CredentialsFile != filepath.Join(home, "other", "creds") || c.Rotation.File != filepath.Join(home, "rot") {
		t.Fatalf("paths = %q %q", c.Export.CredentialsFile, c.Rotation.File)
	}
	if len(c.Network.ExtraCAs) != 0 || !strings.Contains(strings.Join(notes, "\n"), "not found, not added") {
		t.Fatalf("CA handling wrong: %v %v", c.Network.ExtraCAs, notes)
	}
}

func TestCompatDefaults(t *testing.T) {
	c := state.Default()
	CompatDefaults(c, nil)
	if c.Mode != state.ModeCompat || c.Roles.Policy != model.PolicyScript || !c.Roles.UpdateConfig || !c.Export.Enabled ||
		!c.Export.StripPrefix || !c.Export.IncludeNewInMain || !c.Sync.AddNew || c.Sync.RemoveStale != state.RemoveStalePrompt ||
		c.Rotation.Strategy != state.StrategyRandom {
		t.Fatalf("compat = %+v", c)
	}
	if len(c.Roles.Fallbacks) != 0 {
		t.Fatalf("fallbacks invented without a script: %v", c.Roles.Fallbacks)
	}
	CompatDefaults(c, &Legacy{FallbackRoles: []string{"AdministratorAccess"}})
	if !slices.Equal(c.Roles.Fallbacks, []string{"AdministratorAccess"}) {
		t.Fatalf("fallbacks = %v", c.Roles.Fallbacks)
	}
	CompatDefaults(c, &Legacy{FallbackRoles: []string{"Other"}})
	if !slices.Equal(c.Roles.Fallbacks, []string{"AdministratorAccess"}) {
		t.Fatal("existing fallbacks overwritten")
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

// TestRealScriptSanity parses a real legacy script when CREDORBIT_LEGACY_SCRIPT
// points at one (never committed, prints counts only).
func TestRealScriptSanity(t *testing.T) {
	p := os.Getenv("CREDORBIT_LEGACY_SCRIPT")
	if p == "" {
		t.Skip("CREDORBIT_LEGACY_SCRIPT not set")
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	l, err := ParseScript(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("main=%d rotation=%d fallbacks=%d parallel=%d session=%t default=%t sase=%t probe=%t creds=%t prefix=%t rotfile=%t warnings=%d",
		len(l.MainProfiles), len(l.ClaudeRotation), len(l.FallbackRoles), l.Parallel, l.SSOSession != "", l.DefaultProfile != "",
		l.SASECA != "", l.ProbeURL != "", l.CredsFile != "", l.Prefix != "", l.RotationFile != "", len(l.Warnings))
}
