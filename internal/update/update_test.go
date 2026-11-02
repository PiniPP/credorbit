package update

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCompare(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"v1.2.3", "1.2.3", 0},
		{"v1.2.4", "v1.2.3", 1},
		{"v1.10.0", "v1.9.9", 1},
		{"v2", "v1.99.99", 1},
		{"v1.2", "v1.2.0", 0},
		{"v1.0.0-rc.1", "v1.0.0", -1},
		{"v1.0.0", "v1.0.0-rc.1", 1},
		{"v1.0.0-alpha", "v1.0.0-alpha.1", -1},
		{"v1.0.0-alpha.1", "v1.0.0-alpha.beta", -1},
		{"v1.0.0-beta.2", "v1.0.0-beta.11", -1},
		{"v1.0.0-rc.1", "v1.0.0-beta.11", 1},
		{"v1.0.0+build.5", "v1.0.0+build.9", 0},
		{"v0.0.0-20260101000000-abcdef123456", "v0.1.0", -1},
	}
	for _, tt := range tests {
		got, ok := Compare(tt.a, tt.b)
		if !ok || got != tt.want {
			t.Errorf("Compare(%q,%q) = %d,%v want %d", tt.a, tt.b, got, ok, tt.want)
		}
	}
	for _, bad := range []string{"dev", "", "v1.x", "1.2.3.4", "v1.0.0-", "v-1.0.0", "v1..2"} {
		if _, ok := Compare(bad, "v1.0.0"); ok {
			t.Errorf("Compare(%q) should be invalid", bad)
		}
	}
}

func TestIsNewer(t *testing.T) {
	tests := []struct {
		latest, current string
		want            bool
	}{
		{"v1.2.0", "v1.1.9", true},
		{"v1.2.0", "v1.2.0", false},
		{"v1.2.0", "v1.3.0", false},
		{"v1.2.0", "v1.2.0-rc.1", true},
		{"v1.2.0", "dev", true},
		{"v1.2.0", "(devel)", true},
		{"nightly", "v1.0.0", false},
	}
	for _, tt := range tests {
		if got := IsNewer(tt.latest, tt.current); got != tt.want {
			t.Errorf("IsNewer(%q,%q) = %v want %v", tt.latest, tt.current, got, tt.want)
		}
	}
}

const releaseJSON = `{"tag_name":"v1.4.0","name":"v1.4.0","html_url":"https://example.invalid/releases/v1.4.0",
"body":"notes here","draft":false,"prerelease":false,"published_at":"2026-10-01T12:00:00Z"}`

func server(t *testing.T, status int, body string, hits *int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			atomic.AddInt32(hits, 1)
		}
		if r.URL.Path != "/repos/acme/tool/releases/latest" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Accept") != "application/vnd.github+json" || r.Header.Get("User-Agent") == "" {
			http.Error(w, "bad headers", http.StatusBadRequest)
			return
		}
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCheck(t *testing.T) {
	srv := server(t, http.StatusOK, releaseJSON, nil)
	c := &Checker{BaseURL: srv.URL, HTTP: srv.Client()}
	rel, newer, err := c.Check(context.Background(), "acme/tool", "v1.3.2")
	if err != nil {
		t.Fatal(err)
	}
	if !newer || rel.Version != "v1.4.0" || rel.Notes != "notes here" || rel.URL == "" ||
		!rel.Published.Equal(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("unexpected: %+v newer=%v", rel, newer)
	}
	if _, newer, _ = c.Check(context.Background(), "acme/tool", "v1.4.0"); newer {
		t.Fatal("same version reported newer")
	}
	_, newer, _ = c.Check(context.Background(), "acme/tool", "dev")
	if !newer {
		t.Fatal("dev build should be older than any release")
	}
	if msg := Message(rel, true, "dev"); !strings.Contains(msg, "development build") {
		t.Fatalf("Message(dev) = %q", msg)
	}
	if msg := Message(rel, true, "v1.3.2"); !strings.Contains(msg, "v1.3.2 → v1.4.0") {
		t.Fatalf("Message = %q", msg)
	}
	if msg := Message(rel, false, "v1.4.0"); !strings.Contains(msg, "up to date") {
		t.Fatalf("Message = %q", msg)
	}
}

func TestCheckErrors(t *testing.T) {
	ctx := context.Background()
	if _, _, err := Check(ctx, nil, "", "v1.0.0"); err == nil || err.Error() != "update.repo not configured" {
		t.Fatalf("empty repo: %v", err)
	}
	if _, _, err := Check(ctx, nil, "not a repo", "v1.0.0"); err == nil {
		t.Fatal("invalid repo accepted")
	}
	cases := map[int]string{
		http.StatusNotFound:            "no published release",
		http.StatusForbidden:           "rate limited",
		http.StatusTooManyRequests:     "rate limited",
		http.StatusInternalServerError: "HTTP 500",
	}
	for status, want := range cases {
		srv := server(t, status, `{}`, nil)
		c := &Checker{BaseURL: srv.URL, HTTP: srv.Client()}
		_, _, err := c.Check(ctx, "acme/tool", "v1.0.0")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("status %d: err = %v, want %q", status, err, want)
		}
		if status == http.StatusNotFound && !errors.Is(err, ErrNoRelease) {
			t.Errorf("404 should wrap ErrNoRelease")
		}
	}
	srv := server(t, http.StatusOK, `not json`, nil)
	if _, _, err := (&Checker{BaseURL: srv.URL}).Check(ctx, "acme/tool", "v1"); err == nil {
		t.Error("bad JSON accepted")
	}
	srv = server(t, http.StatusOK, `{"tag_name":"v2.0.0","draft":true}`, nil)
	if _, _, err := (&Checker{BaseURL: srv.URL}).Check(ctx, "acme/tool", "v1"); !errors.Is(err, ErrNoRelease) {
		t.Errorf("draft: %v", err)
	}
}

func TestCheckTimeout(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(block)
	c := &Checker{BaseURL: srv.URL, Timeout: 50 * time.Millisecond}
	start := time.Now()
	if _, _, err := c.Check(context.Background(), "acme/tool", "v1.0.0"); err == nil {
		t.Fatal("expected timeout")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("timeout not honoured")
	}
}

func TestCheckCached(t *testing.T) {
	var hits int32
	srv := server(t, http.StatusOK, releaseJSON, &hits)
	dir := filepath.Join(t.TempDir(), "cache")
	now := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	c := &Checker{BaseURL: srv.URL, HTTP: srv.Client(), now: func() time.Time { return now }}
	ctx := context.Background()

	rel, newer, err := c.CheckCached(ctx, dir, "acme/tool", "v1.0.0")
	if err != nil || !newer || rel.Version != "v1.4.0" {
		t.Fatalf("first: %+v %v %v", rel, newer, err)
	}
	if fi, err := os.Stat(filepath.Join(dir, CacheFileName)); err != nil {
		t.Fatalf("cache not written: %v", err)
	} else if fi.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Fatalf("cache perms %v", fi.Mode().Perm())
	}

	// Within TTL: served from cache, newer recomputed for the new current.
	now = now.Add(19 * time.Hour)
	_, newer, err = c.CheckCached(ctx, dir, "acme/tool", "v1.4.0")
	if err != nil || newer || atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("cached: newer=%v err=%v hits=%d", newer, err, hits)
	}

	// Different repo bypasses the cache.
	if _, _, err := c.CheckCached(ctx, dir, "other/tool", "v1.0.0"); err == nil {
		t.Fatal("other repo should hit the server (404)")
	}
	if atomic.LoadInt32(&hits) != 2 {
		t.Fatalf("hits=%d", hits)
	}
	// The failed check must not overwrite the good cache entry.
	if _, _, err := c.CheckCached(ctx, dir, "acme/tool", "v1.0.0"); err != nil || atomic.LoadInt32(&hits) != 2 {
		t.Fatalf("cache lost after failure: err=%v hits=%d", err, hits)
	}

	// After TTL: refreshed.
	now = now.Add(2 * time.Hour)
	if _, _, err := c.CheckCached(ctx, dir, "acme/tool", "v1.0.0"); err != nil || atomic.LoadInt32(&hits) != 3 {
		t.Fatalf("expired: err=%v hits=%d", err, hits)
	}

	// Corrupt cache is ignored.
	if err := os.WriteFile(filepath.Join(dir, CacheFileName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.CheckCached(ctx, dir, "acme/tool", "v1.0.0"); err != nil || atomic.LoadInt32(&hits) != 4 {
		t.Fatalf("corrupt: err=%v hits=%d", err, hits)
	}

	if _, _, err := CheckCached(ctx, dir, nil, "", "v1"); !errors.Is(err, ErrRepoNotConfigured) {
		t.Fatalf("empty repo: %v", err)
	}
}
