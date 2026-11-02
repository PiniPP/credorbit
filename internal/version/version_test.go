package version

import (
	"runtime/debug"
	"strings"
	"testing"
)

func withBuildInfo(t *testing.T, bi *debug.BuildInfo, ok bool) {
	t.Helper()
	orig := readBuildInfo
	readBuildInfo = func() (*debug.BuildInfo, bool) { return bi, ok }
	t.Cleanup(func() { readBuildInfo = orig })
}

func TestResolveLdflagsWin(t *testing.T) {
	withBuildInfo(t, &debug.BuildInfo{
		Main:     debug.Module{Version: "v9.9.9"},
		Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "deadbeef"}},
	}, true)
	b := resolve("v1.2.3", "abc1234", "2026-01-02T03:04:05Z")
	if b.Version != "v1.2.3" || b.Commit != "abc1234" || b.Date != "2026-01-02T03:04:05Z" {
		t.Fatalf("ldflags values not kept: %+v", b)
	}
}

func TestResolveBuildInfoFallback(t *testing.T) {
	withBuildInfo(t, &debug.BuildInfo{
		Main: debug.Module{Version: "v0.4.0"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "0123456789abcdef0123"},
			{Key: "vcs.time", Value: "2026-03-04T05:06:07Z"},
			{Key: "vcs.modified", Value: "true"},
		},
	}, true)
	b := resolve("dev", "none", "unknown")
	if b.Version != "v0.4.0" || b.Commit != "0123456789abcdef0123" || b.Date != "2026-03-04T05:06:07Z" || !b.Modified {
		t.Fatalf("fallback not applied: %+v", b)
	}
}

func TestResolveDevel(t *testing.T) {
	withBuildInfo(t, &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, true)
	if b := resolve("dev", "none", "unknown"); b.Version != "dev" {
		t.Fatalf("(devel) should stay dev, got %q", b.Version)
	}
	withBuildInfo(t, nil, false)
	if b := resolve("", "none", "unknown"); b.Version != "" {
		// no build info: values are returned untouched
		t.Fatalf("got %q", b.Version)
	}
}

func TestInfoAndShort(t *testing.T) {
	if Short() == "" {
		t.Fatal("Short() empty")
	}
	info := Info()
	if !strings.HasPrefix(info, "credorbit ") || !strings.Contains(info, "commit ") {
		t.Fatalf("Info() = %q", info)
	}
}
