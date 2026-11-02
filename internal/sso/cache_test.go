package sso

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/PiniPP/credorbit/internal/model"
)

const testStartURL = "https://d-1234567890.awsapps.com/start"

func TestCachePathCLICompatible(t *testing.T) {
	c := TokenCache{Dir: "/x"}
	tests := []struct {
		s    model.SSOSession
		want string
	}{
		{model.SSOSession{Name: "my-sso", StartURL: testStartURL}, "0ad374308c5a4e22f723adf10145eafad7c4031c.json"},
		{model.SSOSession{StartURL: testStartURL}, "7487b5b67d5fa1132cf33fa93ae51ef38cc37752.json"},
	}
	for _, tt := range tests {
		if got := filepath.Base(c.Path(tt.s)); got != tt.want {
			t.Errorf("Path(%+v) = %s, want %s", tt.s, got, tt.want)
		}
	}
}

func TestCacheSaveLoad(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sso", "cache")
	c := TokenCache{Dir: dir}
	s := model.SSOSession{Name: "my-sso", StartURL: testStartURL, Region: "eu-west-1"}
	exp := time.Date(2026, 11, 2, 20, 0, 0, 0, time.UTC)
	in := model.Token{AccessToken: "AT-SECRET", ExpiresAt: exp, RefreshToken: "RT-SECRET", ClientID: "cid", ClientSecret: "CS-SECRET",
		RegistrationExpiresAt: exp.Add(48 * time.Hour)}
	if err := c.Save(s, in); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(c.Path(s))
		if st.Mode().Perm() != 0o600 {
			t.Errorf("file perm = %v", st.Mode().Perm())
		}
		dst, _ := os.Stat(dir)
		if dst.Mode().Perm() != 0o700 {
			t.Errorf("dir perm = %v", dst.Mode().Perm())
		}
	}
	raw, _ := os.ReadFile(c.Path(s))
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"startUrl": testStartURL, "region": "eu-west-1", "accessToken": "AT-SECRET", "expiresAt": "2026-11-02T20:00:00Z",
		"refreshToken": "RT-SECRET", "clientId": "cid", "clientSecret": "CS-SECRET", "registrationExpiresAt": "2026-11-04T20:00:00Z",
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("json %s = %q, want %q", k, m[k], v)
		}
	}

	out, err := c.Load(s)
	if err != nil {
		t.Fatal(err)
	}
	in.StartURL, in.Region = testStartURL, "eu-west-1"
	if out != in {
		t.Errorf("Load = %#v\nwant %#v", out, in)
	}
	if str := out.String(); strings.Contains(str, "SECRET") {
		t.Errorf("String leaks secrets: %s", out)
	}

	if err := c.Delete(s); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(s); err != nil {
		t.Errorf("second delete: %v", err)
	}
	if _, err := c.Load(s); !errors.Is(err, model.ErrUnauthorized) {
		t.Errorf("missing file: %v", err)
	}
}

func TestCacheLoadVariants(t *testing.T) {
	s := model.SSOSession{Name: "sess", StartURL: testStartURL, Region: "us-east-1"}
	want := time.Date(2026, 11, 2, 20, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		body    string
		wantErr bool
		exp     time.Time
	}{
		{"cli Z", `{"startUrl":"` + testStartURL + `","region":"us-east-1","accessToken":"a","expiresAt":"2026-11-02T20:00:00Z"}`, false, want},
		{"cli UTC suffix", `{"startUrl":"` + testStartURL + `","accessToken":"a","expiresAt":"2026-11-02T20:00:00UTC"}`, false, want},
		{"fractional", `{"startUrl":"` + testStartURL + `","accessToken":"a","expiresAt":"2026-11-02T20:00:00.000Z"}`, false, want},
		{"offset", `{"startUrl":"` + testStartURL + `","accessToken":"a","expiresAt":"2026-11-02T22:00:00+02:00"}`, false, want},
		{"trailing slash start URL", `{"startUrl":"` + testStartURL + `/","accessToken":"a","expiresAt":"2026-11-02T20:00:00Z"}`, false, want},
		{"other start URL", `{"startUrl":"https://d-other.awsapps.com/start","accessToken":"a","expiresAt":"2026-11-02T20:00:00Z"}`, true, time.Time{}},
		{"corrupt", `{"accessToken": "secret-value"`, true, time.Time{}},
		{"no token", `{"startUrl":"` + testStartURL + `","expiresAt":"2026-11-02T20:00:00Z"}`, true, time.Time{}},
		{"bad time", `{"startUrl":"` + testStartURL + `","accessToken":"a","expiresAt":"tomorrow"}`, true, time.Time{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := TokenCache{Dir: t.TempDir()}
			if err := os.WriteFile(c.Path(s), []byte(tt.body), 0o600); err != nil {
				t.Fatal(err)
			}
			tok, err := c.Load(s)
			if tt.wantErr {
				if !errors.Is(err, model.ErrUnauthorized) {
					t.Fatalf("err = %v, want ErrUnauthorized", err)
				}
				if strings.Contains(err.Error(), "secret-value") {
					t.Errorf("error leaks content: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !tok.ExpiresAt.Equal(tt.exp) {
				t.Errorf("ExpiresAt = %v, want %v", tok.ExpiresAt, tt.exp)
			}
		})
	}
}

func TestParseScopes(t *testing.T) {
	tests := map[string]string{
		"":                   "sso:account:access",
		"  ":                 "sso:account:access",
		"sso:account:access": "sso:account:access",
		"sso:account:access, codewhisperer:completions": "sso:account:access|codewhisperer:completions",
		"a b,a": "a|b",
	}
	for in, want := range tests {
		if got := strings.Join(ParseScopes(in), "|"); got != want {
			t.Errorf("ParseScopes(%q) = %s, want %s", in, got, want)
		}
	}
}
