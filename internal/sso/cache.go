package sso

import (
	"crypto/sha1" //nolint:gosec // file naming scheme mandated by the AWS CLI, not a security use
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/credorbit/credorbit/internal/fsutil"
	"github.com/credorbit/credorbit/internal/model"
)

// cacheTimeLayout is the timestamp format the AWS CLI v2 writes.
const cacheTimeLayout = "2006-01-02T15:04:05Z"

// TokenCache reads and writes SSO tokens in the AWS CLI v2 cache format
// (~/.aws/sso/cache), so tokens obtained by CredOrbit are usable by the AWS CLI
// and SDKs and vice versa.
type TokenCache struct{ Dir string }

// cachedToken is the on-disk JSON shape used by the AWS CLI v2 / botocore.
type cachedToken struct {
	StartURL              string `json:"startUrl"`
	Region                string `json:"region"`
	AccessToken           string `json:"accessToken"`
	ExpiresAt             string `json:"expiresAt"`
	RefreshToken          string `json:"refreshToken,omitempty"`
	ClientID              string `json:"clientId,omitempty"`
	ClientSecret          string `json:"clientSecret,omitempty"`
	RegistrationExpiresAt string `json:"registrationExpiresAt,omitempty"`
}

// CacheKey returns the string the AWS CLI hashes to name the cache file: the
// session name for sso-session configurations, the start URL for legacy ones.
func CacheKey(s model.SSOSession) string {
	if s.Name != "" {
		return s.Name
	}
	return s.StartURL
}

// Path returns the cache file for s: hex(sha1(CacheKey(s))) + ".json".
func (c TokenCache) Path(s model.SSOSession) string {
	return filepath.Join(fsutil.ExpandHome(c.Dir), sha1Hex(CacheKey(s))+".json")
}

func sha1Hex(s string) string {
	h := sha1.Sum([]byte(s)) //nolint:gosec // see import comment
	return hex.EncodeToString(h[:])
}

// Load returns the cached token for s. A missing, unreadable or corrupt file,
// or a token issued for a different start URL, wraps model.ErrUnauthorized
// (the remedy is always the same: log in). Expiry is NOT checked here.
func (c TokenCache) Load(s model.SSOSession) (model.Token, error) {
	p := c.Path(s)
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return model.Token{}, fmt.Errorf("no cached SSO token for %q: %w", CacheKey(s), model.ErrUnauthorized)
	}
	if err != nil {
		return model.Token{}, fmt.Errorf("read SSO token cache %s: %w: %w", p, model.ErrUnauthorized, err)
	}
	var ct cachedToken
	if err := json.Unmarshal(data, &ct); err != nil {
		// Do not include err: json syntax errors can quote file content.
		return model.Token{}, fmt.Errorf("SSO token cache %s is not valid JSON: %w", p, model.ErrUnauthorized)
	}
	if ct.AccessToken == "" {
		return model.Token{}, fmt.Errorf("SSO token cache %s has no access token: %w", p, model.ErrUnauthorized)
	}
	if s.StartURL != "" && !sameStartURL(ct.StartURL, s.StartURL) {
		return model.Token{}, fmt.Errorf("cached SSO token for %q was issued for a different start URL: %w", CacheKey(s), model.ErrUnauthorized)
	}
	exp, err := parseCacheTime(ct.ExpiresAt)
	if err != nil {
		return model.Token{}, fmt.Errorf("SSO token cache %s: bad expiresAt: %w", p, model.ErrUnauthorized)
	}
	t := model.Token{
		StartURL:     ct.StartURL,
		Region:       ct.Region,
		AccessToken:  ct.AccessToken,
		ExpiresAt:    exp,
		RefreshToken: ct.RefreshToken,
		ClientID:     ct.ClientID,
		ClientSecret: ct.ClientSecret,
	}
	if ct.RegistrationExpiresAt != "" {
		// A bad registration timestamp only disables refresh; it is not fatal.
		if r, err := parseCacheTime(ct.RegistrationExpiresAt); err == nil {
			t.RegistrationExpiresAt = r
		}
	}
	return t, nil
}

// Save writes t for s atomically with 0600 permissions (directory 0700).
func (c TokenCache) Save(s model.SSOSession, t model.Token) error {
	if t.StartURL == "" {
		t.StartURL = s.StartURL
	}
	if t.Region == "" {
		t.Region = s.Region
	}
	ct := cachedToken{
		StartURL:     t.StartURL,
		Region:       t.Region,
		AccessToken:  t.AccessToken,
		ExpiresAt:    formatCacheTime(t.ExpiresAt),
		RefreshToken: t.RefreshToken,
		ClientID:     t.ClientID,
		ClientSecret: t.ClientSecret,
	}
	if !t.RegistrationExpiresAt.IsZero() {
		ct.RegistrationExpiresAt = formatCacheTime(t.RegistrationExpiresAt)
	}
	return writeJSON(c.Path(s), ct)
}

// Delete removes the cached token for s; a missing file is not an error.
func (c TokenCache) Delete(s model.SSOSession) error {
	if err := os.Remove(c.Path(s)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove SSO token cache: %w", err)
	}
	return nil
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create SSO cache dir: %w", err)
	}
	if err := fsutil.AtomicWrite(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write SSO cache: %w", err)
	}
	return nil
}

func formatCacheTime(t time.Time) string { return t.UTC().Format(cacheTimeLayout) }

// parseCacheTime accepts RFC 3339 (with or without fractional seconds/offset)
// and the botocore variant with a literal "UTC" suffix.
func parseCacheTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "UTC") {
		s = strings.TrimSpace(strings.TrimSuffix(s, "UTC")) + "Z"
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05Z07:00"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized time %q", s)
}

func normStartURL(u string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(u), "/#"))
}

func sameStartURL(a, b string) bool { return normStartURL(a) == normStartURL(b) }
