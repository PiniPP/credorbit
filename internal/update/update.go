// Package update checks whether a newer CredOrbit release is published.
//
// It only CHECKS: it never downloads or installs anything. The release source
// is the GitHub REST API (or a compatible GitHub Enterprise endpoint via
// Checker.BaseURL); the repository comes from configuration (update.repo).
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/PiniPP/credorbit/internal/fsutil"
)

// Defaults.
const (
	DefaultBaseURL  = "https://api.github.com"
	DefaultTimeout  = 5 * time.Second
	DefaultCacheTTL = 20 * time.Hour
	CacheFileName   = "update-check.json"
	maxBody         = 1 << 20
	maxNotes        = 16 << 10
)

// ErrRepoNotConfigured is returned when no repository is configured.
var ErrRepoNotConfigured = errors.New("update.repo not configured")

// ErrNoRelease is returned when the repository has no published release.
var ErrNoRelease = errors.New("update: no published release")

var repoRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9._-]+$`)

// Release describes a published release.
type Release struct {
	Version   string    `json:"version"`
	URL       string    `json:"url"`
	Notes     string    `json:"notes,omitempty"`
	Published time.Time `json:"published"`
}

// Checker queries a GitHub-compatible releases API.
type Checker struct {
	BaseURL   string        // default DefaultBaseURL; for GHE use https://host/api/v3
	HTTP      *http.Client  // default: a client with DefaultTimeout
	Timeout   time.Duration // per check; default DefaultTimeout
	CacheTTL  time.Duration // for CheckCached; default DefaultCacheTTL
	UserAgent string        // default "credorbit-update-check"
	now       func() time.Time
}

// Check queries the latest release of repo ("owner/name") using hc (nil for a
// default client) against api.github.com.
func Check(ctx context.Context, hc *http.Client, repo, current string) (*Release, bool, error) {
	return (&Checker{HTTP: hc}).Check(ctx, repo, current)
}

// CheckCached is Check with a result cache in dir (see Checker.CheckCached).
func CheckCached(ctx context.Context, dir string, hc *http.Client, repo, current string) (*Release, bool, error) {
	return (&Checker{HTTP: hc}).CheckCached(ctx, dir, repo, current)
}

// Check fetches the latest release and reports whether it is newer than
// current. A development build (current = "dev", "(devel)", non-semver) is
// treated as older than any release, so newer is true; use IsDevelopment /
// Message to present it as a development build rather than an outdated one.
func (c *Checker) Check(ctx context.Context, repo, current string) (*Release, bool, error) {
	rel, err := c.Latest(ctx, repo)
	if err != nil {
		return nil, false, err
	}
	return rel, IsNewer(rel.Version, current), nil
}

// IsNewer reports whether latest is a newer release than current.
func IsNewer(latest, current string) bool {
	if IsDevelopment(latest) {
		return false
	}
	if IsDevelopment(current) {
		return true
	}
	c, ok := Compare(latest, current)
	return ok && c > 0
}

// Latest fetches GET {BaseURL}/repos/{repo}/releases/latest.
func (c *Checker) Latest(ctx context.Context, repo string) (*Release, error) {
	repo = strings.Trim(strings.TrimSpace(repo), "/")
	if repo == "" {
		return nil, ErrRepoNotConfigured
	}
	if !repoRe.MatchString(repo) {
		return nil, fmt.Errorf("update: invalid repo %q (want owner/name)", repo)
	}
	base := c.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	u, err := url.Parse(strings.TrimRight(base, "/") + "/repos/" + repo + "/releases/latest")
	if err != nil {
		return nil, fmt.Errorf("update: bad base URL: %w", err)
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	ua := c.UserAgent
	if ua == "" {
		ua = "credorbit-update-check"
	}
	req.Header.Set("User-Agent", ua)

	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: timeout}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("update: request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("update: reading response: %w", err)
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("%w for %s", ErrNoRelease, repo)
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("update: GitHub API rate limited or forbidden (HTTP %d)", resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("update: unexpected HTTP %d from releases API", resp.StatusCode)
	}

	var gh struct {
		TagName     string    `json:"tag_name"`
		Name        string    `json:"name"`
		HTMLURL     string    `json:"html_url"`
		Body        string    `json:"body"`
		Draft       bool      `json:"draft"`
		Prerelease  bool      `json:"prerelease"`
		PublishedAt time.Time `json:"published_at"`
	}
	if err := json.Unmarshal(body, &gh); err != nil {
		return nil, fmt.Errorf("update: decoding release: %w", err)
	}
	if gh.TagName == "" || gh.Draft {
		return nil, fmt.Errorf("%w for %s", ErrNoRelease, repo)
	}
	notes := gh.Body
	if len(notes) > maxNotes {
		notes = strings.ToValidUTF8(notes[:maxNotes], "") + "\n…"
	}
	return &Release{Version: gh.TagName, URL: gh.HTMLURL, Notes: notes, Published: gh.PublishedAt}, nil
}

type cacheEntry struct {
	Repo      string    `json:"repo"`
	CheckedAt time.Time `json:"checked_at"`
	Release   *Release  `json:"release"`
}

// CheckCached returns a cached result when the last successful check for the
// same repo is younger than CacheTTL; otherwise it queries the API and
// stores the result in dir/update-check.json. newer is always recomputed
// against current. Errors are not cached, so a failed passive check is
// retried on the next launch.
func (c *Checker) CheckCached(ctx context.Context, dir, repo, current string) (*Release, bool, error) {
	repo = strings.Trim(strings.TrimSpace(repo), "/")
	if repo == "" {
		return nil, false, ErrRepoNotConfigured
	}
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	ttl := c.CacheTTL
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	path := filepath.Join(dir, CacheFileName)
	if e, ok := readCache(path); ok && e.Repo == repo && e.Release != nil {
		age := now().Sub(e.CheckedAt)
		if age >= 0 && age < ttl {
			return e.Release, IsNewer(e.Release.Version, current), nil
		}
	}
	rel, newer, err := c.Check(ctx, repo, current)
	if err != nil {
		return nil, false, err
	}
	if dir != "" {
		_ = writeCache(dir, path, cacheEntry{Repo: repo, CheckedAt: now().UTC(), Release: rel})
	}
	return rel, newer, nil
}

func readCache(path string) (cacheEntry, bool) {
	var e cacheEntry
	data, err := os.ReadFile(path)
	if err != nil {
		return e, false
	}
	if json.Unmarshal(data, &e) != nil {
		return e, false
	}
	return e, true
}

func writeCache(dir, path string, e cacheEntry) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.AtomicWrite(path, append(data, '\n'), 0o600)
}

// Message renders a short, user-facing status line for a check result.
func Message(latest *Release, newer bool, current string) string {
	if latest == nil {
		return ""
	}
	switch {
	case IsDevelopment(current):
		return fmt.Sprintf("development build; latest release is %s", latest.Version)
	case newer:
		return fmt.Sprintf("update available: %s → %s (%s)", current, latest.Version, latest.URL)
	default:
		return fmt.Sprintf("up to date (%s)", current)
	}
}
