package engine

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvOverridesNotPersisted(t *testing.T) {
	te := newTestEnv(t)
	alt := filepath.Join(te.dir, "alt-credentials")
	te.env["CREDS_FILE"] = alt
	te.env["ROLE_UPDATE"] = "0"
	te.env["SYNC"] = "0"
	te.env["SSO_SESSION"] = "alpha"
	e := te.engine(&fakeUI{review: true})
	ctx := context.Background()
	cfgBefore := te.read(te.paths.AWSConfig)
	rep, err := e.RunFull(ctx, FullOptions{Compat: true, Scope: Scope{Kind: ScopeSelected, Profiles: []string{"team-prd"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(te.read(alt), "[team-prd]") {
		t.Error("CREDS_FILE not honoured")
	}
	if te.read(te.paths.AWSConfig) != cfgBefore {
		t.Error("ROLE_UPDATE=0 / SYNC=0 not honoured: config changed")
	}
	for _, s := range rep.Steps {
		if s.Step == "sync" && !s.Skipped {
			t.Errorf("sync step ran with SYNC=0: %+v", s)
		}
	}
	// Nothing of the overrides was persisted.
	for _, s := range te.engine(nil).Settings() {
		if s.Key == "export.credentials_file" && s.Value != "" {
			t.Errorf("credentials_file persisted: %q", s.Value)
		}
	}
}
