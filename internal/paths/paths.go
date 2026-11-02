// Package paths resolves every filesystem location CredOrbit touches, honoring
// the same environment variables as the AWS CLI/SDK.
package paths

import (
	"os"
	"path/filepath"
	"runtime"

	"github.com/PiniPP/credorbit/internal/fsutil"
)

// Paths is the resolved set of locations. All fields are absolute.
type Paths struct {
	Home              string
	AWSConfig         string // AWS_CONFIG_FILE or ~/.aws/config
	AWSCredentials    string // AWS_SHARED_CREDENTIALS_FILE or ~/.aws/credentials
	SSOCacheDir       string // ~/.aws/sso/cache (AWS CLI v2 compatible token cache)
	AppConfigDir      string // CREDORBIT_CONFIG_DIR or <os user config dir>/credorbit
	AppConfigFile     string // <AppConfigDir>/config.yaml
	AppStateFile      string // <AppConfigDir>/state.json (discovery cache, last results)
	AppLogFile        string // <AppConfigDir>/credorbit.log
	ClaudeAccountFile string // ~/.claude-aws-account
	LegacyScript      string // ~/populate-aws-creds.sh (read-only import source)
}

// Resolve computes paths from the environment.
func Resolve() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, err
	}
	p := Paths{Home: home}
	p.AWSConfig = envOr("AWS_CONFIG_FILE", filepath.Join(home, ".aws", "config"))
	p.AWSCredentials = envOr("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(home, ".aws", "credentials"))
	p.SSOCacheDir = filepath.Join(home, ".aws", "sso", "cache")
	if d := os.Getenv("CREDORBIT_CONFIG_DIR"); d != "" {
		p.AppConfigDir = fsutil.ExpandHome(d)
	} else {
		p.AppConfigDir = defaultConfigDir(home)
	}
	p.AppConfigFile = filepath.Join(p.AppConfigDir, "config.yaml")
	p.AppStateFile = filepath.Join(p.AppConfigDir, "state.json")
	p.AppLogFile = filepath.Join(p.AppConfigDir, "credorbit.log")
	p.ClaudeAccountFile = filepath.Join(home, ".claude-aws-account")
	p.LegacyScript = filepath.Join(home, "populate-aws-creds.sh")
	return p, nil
}

// defaultConfigDir: macOS and Linux use ~/.config/credorbit (XDG_CONFIG_HOME
// honored) because developers expect dotfiles there; Windows uses %AppData%.
func defaultConfigDir(home string) string {
	if runtime.GOOS == "windows" {
		if d, err := os.UserConfigDir(); err == nil {
			return filepath.Join(d, "CredOrbit")
		}
	}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "credorbit")
	}
	return filepath.Join(home, ".config", "credorbit")
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return fsutil.ExpandHome(v)
	}
	return def
}
