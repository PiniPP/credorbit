// Package version reports the CredOrbit build version.
//
// Release builds set the variables with -ldflags, e.g.
//
//	-X github.com/credorbit/credorbit/internal/version.Version=v1.2.3
//	-X github.com/credorbit/credorbit/internal/version.Commit=abc1234
//	-X github.com/credorbit/credorbit/internal/version.Date=2026-01-02T03:04:05Z
//
// Builds made with `go install module@version` carry no ldflags; for those
// the module version and VCS stamps from runtime/debug.ReadBuildInfo are used.
package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
)

// Set via -ldflags by GoReleaser / the Makefile.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// Build is the resolved build metadata.
type Build struct {
	Version   string
	Commit    string
	Date      string
	Modified  bool // VCS tree was dirty (from build info only)
	GoVersion string
	Platform  string // GOOS/GOARCH
}

var (
	once     sync.Once
	resolved Build
)

// readBuildInfo is replaceable in tests.
var readBuildInfo = debug.ReadBuildInfo

// Get returns the resolved build metadata (ldflags first, then build info).
func Get() Build {
	once.Do(func() { resolved = resolve(Version, Commit, Date) })
	return resolved
}

func resolve(v, c, d string) Build {
	b := Build{
		Version:   v,
		Commit:    c,
		Date:      d,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
	bi, ok := readBuildInfo()
	if !ok || bi == nil {
		return b
	}
	if b.Version == "" || b.Version == "dev" {
		if mv := bi.Main.Version; mv != "" && mv != "(devel)" {
			b.Version = mv
		}
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			if b.Commit == "" || b.Commit == "none" {
				b.Commit = s.Value
			}
		case "vcs.time":
			if b.Date == "" || b.Date == "unknown" {
				b.Date = s.Value
			}
		case "vcs.modified":
			b.Modified = s.Value == "true"
		}
	}
	if b.Version == "" {
		b.Version = "dev"
	}
	return b
}

// Short returns just the version, e.g. "v1.2.3" or "dev".
func Short() string { return Get().Version }

// IsDev reports whether this is an unreleased development build.
func IsDev() bool {
	v := Short()
	return v == "dev" || strings.Contains(v, "-dev") || strings.HasSuffix(v, "-dirty")
}

// Info returns a one-line human description, e.g.
// "credorbit v1.2.3 (commit abc1234, built 2026-01-02T03:04:05Z, go1.26.2 darwin/arm64)".
func Info() string {
	b := Get()
	commit := b.Commit
	if len(commit) > 12 {
		commit = commit[:12]
	}
	if b.Modified {
		commit += "-dirty"
	}
	return fmt.Sprintf("credorbit %s (commit %s, built %s, %s %s)",
		b.Version, commit, b.Date, b.GoVersion, b.Platform)
}
