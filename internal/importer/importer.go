// Package importer performs a one-time, read-only import of the legacy
// populate-aws-creds.sh Bash script. The script is parsed at runtime (arrays and
// ${VAR:-default} assignments); no value from it is compiled into CredOrbit, and
// anything that cannot be found stays empty rather than being guessed.
package importer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/credorbit/credorbit/internal/model"
	"github.com/credorbit/credorbit/internal/state"
)

// ErrNotLegacyScript is returned when the input has none of the recognised
// legacy-script settings.
var ErrNotLegacyScript = errors.New("not a recognised legacy credentials script")

// Legacy holds the settings found in the legacy script. Paths are kept raw
// (e.g. "$HOME/.aws/credentials"); use ExpandPaths for usable paths.
type Legacy struct {
	MainProfiles   []string // MAIN_PROFILES (deduplicated, order kept)
	ClaudeRotation []string // CLAUDE_ROTATION: credentials SECTION names
	FallbackRoles  []string // FALLBACK_ROLES default, space-separated

	SSOSession     string // SSO_SESSION default
	DefaultProfile string // profile exported as [default] (export_section <p> default)
	SASECA         string // SASE_CA default
	ProbeURL       string // first https URL on a curl line (SASE detection)
	CredsFile      string // CREDS_FILE default
	Prefix         string // common "<x>_" prefix of MAIN_PROFILES, if all share it
	RotationFile   string // CLAUDE_ACCT_FILE
	Parallel       int    // PARALLEL default

	// Warnings lists parse oddities (duplicates, non-numeric values, ...).
	Warnings []string
}

var (
	identRE   = `[A-Za-z_][A-Za-z0-9_]*`
	arrayRE   = regexp.MustCompile(`^\s*(` + identRE + `)=\((.*)$`)
	defaultRE = regexp.MustCompile(`^\s*(?:export\s+)?(` + identRE + `)=["']?\$\{(` + identRE + `):-([^}]*)\}["']?\s*(?:#.*)?$`)
	assignRE  = regexp.MustCompile(`^\s*(?:export\s+)?(` + identRE + `)=(?:"([^"]*)"|'([^']*)'|([^\s;#]*))\s*(?:[;#].*)?$`)
	defProfRE = regexp.MustCompile(`\bexport_section\s+["']?([^\s"'$;]+)["']?\s+["']?default["']?(?:\s|;|$)`)
	urlRE     = regexp.MustCompile(`https://[^\s"'<>)\]};|]+`)
)

// ParseScript extracts the legacy settings from the script source.
func ParseScript(data []byte) (*Legacy, error) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	l := &Legacy{}
	found := false

	arrays := map[string]*[]string{"MAIN_PROFILES": &l.MainProfiles, "CLAUDE_ROTATION": &l.ClaudeRotation}
	seenArray := map[string]bool{}
	defaults := map[string]string{}
	assigns := map[string]string{}

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if m := arrayRE.FindStringSubmatch(line); m != nil {
			dst, wanted := arrays[m[1]]
			if !wanted || seenArray[m[1]] {
				continue
			}
			items, dynamic, consumed := parseArray(m[2], lines[i+1:])
			if dynamic {
				// Built from other variables at runtime; the literal definition
				// (if any) is the one that counts.
				continue
			}
			i += consumed
			seenArray[m[1]] = true
			found = true
			uniq, dups := dedup(items)
			*dst = uniq
			if dups > 0 {
				l.Warnings = append(l.Warnings, fmt.Sprintf("%s: %d duplicate entr%s ignored", m[1], dups, plural(dups, "y", "ies")))
			}
			continue
		}
		if m := defaultRE.FindStringSubmatch(line); m != nil && m[1] == m[2] {
			if _, ok := defaults[m[1]]; !ok {
				defaults[m[1]] = unquote(strings.TrimSpace(m[3]))
			}
			continue
		}
		if m := assignRE.FindStringSubmatch(line); m != nil {
			if _, ok := assigns[m[1]]; !ok {
				assigns[m[1]] = m[2] + m[3] + m[4]
			}
		}
		if l.DefaultProfile == "" && !isComment(line) {
			if m := defProfRE.FindStringSubmatch(line); m != nil {
				l.DefaultProfile = m[1]
			}
		}
	}

	l.ProbeURL = probeURL(lines)

	if v, ok := defaults["CREDS_FILE"]; ok {
		l.CredsFile, found = v, true
	}
	if v, ok := defaults["SSO_SESSION"]; ok {
		l.SSOSession, found = v, true
	}
	if v, ok := defaults["SASE_CA"]; ok {
		l.SASECA, found = v, true
	}
	if v, ok := defaults["FALLBACK_ROLES"]; ok {
		l.FallbackRoles, _ = dedup(strings.Fields(v))
		found = true
	}
	if v, ok := defaults["PARALLEL"]; ok {
		found = true
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			l.Parallel = n
		} else {
			l.Warnings = append(l.Warnings, fmt.Sprintf("PARALLEL: %q is not a positive integer, ignored", v))
		}
	}
	if v, ok := assigns["CLAUDE_ACCT_FILE"]; ok && !strings.ContainsAny(v, "`(") {
		l.RotationFile = v
	}
	l.Prefix = commonPrefix(l.MainProfiles)

	if !found && l.DefaultProfile == "" {
		return nil, ErrNotLegacyScript
	}
	return l, nil
}

// parseArray tokenizes a bash array body starting with first (text after
// "NAME=(") and continuing over rest until the closing ")". It returns the
// items, whether the definition is dynamic (uses $ / command substitution) and
// how many lines of rest were consumed.
func parseArray(first string, rest []string) (items []string, dynamic bool, consumed int) {
	src := first
	for idx := 0; ; idx++ {
		toks, closed, dyn := tokenizeArrayLine(src)
		items = append(items, toks...)
		dynamic = dynamic || dyn
		if closed || idx >= len(rest) {
			return items, dynamic, idx
		}
		src = rest[idx]
	}
}

func tokenizeArrayLine(s string) (toks []string, closed, dynamic bool) {
	var cur strings.Builder
	inTok := false
	flush := func() {
		if inTok {
			toks = append(toks, cur.String())
			cur.Reset()
			inTok = false
		}
	}
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch == '\'' || ch == '"':
			j := strings.IndexByte(s[i+1:], ch)
			if j < 0 {
				j = len(s) - i - 1
			}
			part := s[i+1 : i+1+j]
			if ch == '"' && strings.ContainsAny(part, "$`") {
				dynamic = true
			}
			cur.WriteString(part)
			inTok = true
			i += j + 1
		case ch == '#' && !inTok:
			flush()
			return toks, false, dynamic
		case ch == ' ' || ch == '\t':
			flush()
		case ch == ')':
			flush()
			return toks, true, dynamic
		case ch == '$' || ch == '`':
			dynamic = true
			cur.WriteByte(ch)
			inTok = true
		default:
			cur.WriteByte(ch)
			inTok = true
		}
	}
	flush()
	return toks, false, dynamic
}

// probeURL returns the first https URL on a curl line, preferring the line
// that validates against a custom CA (the SASE detection probe).
func probeURL(lines []string) string {
	fallback := ""
	for _, line := range lines {
		if isComment(line) || !strings.Contains(line, "curl") {
			continue
		}
		u := urlRE.FindString(line)
		if u == "" {
			continue
		}
		if strings.Contains(line, "--cacert") || strings.Contains(line, "SASE") {
			return u
		}
		if fallback == "" {
			fallback = u
		}
	}
	return fallback
}

// commonPrefix returns "<x>_" when every entry starts with the same such prefix.
func commonPrefix(names []string) string {
	if len(names) == 0 {
		return ""
	}
	i := strings.IndexByte(names[0], '_')
	if i <= 0 {
		return ""
	}
	p := names[0][:i+1]
	for _, n := range names[1:] {
		if !strings.HasPrefix(n, p) || len(n) == len(p) {
			return ""
		}
	}
	return p
}

func dedup(in []string) ([]string, int) {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	dups := 0
	for _, x := range in {
		if x == "" {
			continue
		}
		if seen[x] {
			dups++
			continue
		}
		seen[x] = true
		out = append(out, x)
	}
	return out, dups
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

func isComment(line string) bool { return strings.HasPrefix(strings.TrimSpace(line), "#") }

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// ExpandPaths returns a copy of l with "$HOME", "${HOME}" and a leading "~"
// expanded in CredsFile, SASECA and RotationFile. The receiver keeps raw values.
func (l *Legacy) ExpandPaths() Legacy {
	out := *l
	home, err := os.UserHomeDir()
	if err != nil {
		return out
	}
	out.CredsFile = expandPath(l.CredsFile, home)
	out.SASECA = expandPath(l.SASECA, home)
	out.RotationFile = expandPath(l.RotationFile, home)
	return out
}

func expandPath(p, home string) string {
	if p == "" || p == "none" {
		return p
	}
	p = strings.ReplaceAll(p, "${HOME}", home)
	p = strings.ReplaceAll(p, "$HOME", home)
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		return filepath.Join(home, p[2:])
	}
	return filepath.Clean(p)
}

// Apply merges the legacy settings into c, mapping MAIN_PROFILES names to
// account IDs using profiles (from ~/.aws/config). It never overwrites a set
// default session, never duplicates entries and returns human-readable notes.
// Applying the same Legacy twice is a no-op the second time.
func (l *Legacy) Apply(c *state.Config, profiles []model.Profile) []string {
	var notes []string
	note := func(format string, a ...any) { notes = append(notes, fmt.Sprintf(format, a...)) }
	x := l.ExpandPaths()
	home, _ := os.UserHomeDir()

	if l.SSOSession != "" && c.SSO.DefaultSession == "" {
		c.SSO.DefaultSession = l.SSOSession
		note("sso.default_session = %s", l.SSOSession)
	}

	if x.SASECA != "" && x.SASECA != "none" {
		if fi, err := os.Stat(x.SASECA); err == nil && !fi.IsDir() {
			if !slices.Contains(c.Network.ExtraCAs, x.SASECA) {
				c.Network.ExtraCAs = append(c.Network.ExtraCAs, x.SASECA)
				note("network.extra_cas += %s (appended to the system trust store, so it works on and off the intercepting network)", x.SASECA)
			}
		} else {
			note("network: CA bundle %s not found, not added", x.SASECA)
		}
	}
	if l.ProbeURL != "" && c.Network.ProbeURL == "" {
		c.Network.ProbeURL = l.ProbeURL
		note("network.probe_url = %s", l.ProbeURL)
	}

	if x.CredsFile != "" {
		awsDefault := filepath.Join(home, ".aws", "credentials")
		if home == "" || filepath.Clean(x.CredsFile) != awsDefault {
			if c.Export.CredentialsFile != x.CredsFile {
				c.Export.CredentialsFile = x.CredsFile
				note("export.credentials_file = %s", x.CredsFile)
			}
		}
	}
	if l.Parallel > 0 {
		n := min(max(l.Parallel, 1), 64)
		if n != l.Parallel {
			note("export.concurrency: PARALLEL=%d clamped to %d", l.Parallel, n)
		}
		if c.Export.Concurrency != n {
			c.Export.Concurrency = n
			note("export.concurrency = %d", n)
		}
	}
	if l.DefaultProfile != "" && c.Export.DefaultProfile != l.DefaultProfile {
		c.Export.DefaultProfile = l.DefaultProfile
		note("export.default_profile = %s", l.DefaultProfile)
	}
	if len(l.FallbackRoles) > 0 && !slices.Equal(c.Roles.Fallbacks, l.FallbackRoles) {
		c.Roles.Fallbacks = slices.Clone(l.FallbackRoles)
		note("roles.fallbacks = %s", strings.Join(l.FallbackRoles, ", "))
	}
	if l.Prefix != "" && c.Profiles.Prefix != l.Prefix {
		c.Profiles.Prefix = l.Prefix
		note("profiles.prefix = %s", l.Prefix)
	}

	if len(l.MainProfiles) > 0 {
		byName := make(map[string]model.Profile, len(profiles))
		for _, p := range profiles {
			byName[p.Name] = p
		}
		main := c.EnsureGroup(state.MainGroup, "")
		added, missing := 0, 0
		for _, name := range l.MainProfiles {
			p, ok := byName[name]
			if !ok {
				missing++
				note("main: %s not found in ~/.aws/config, skipped", name)
				continue
			}
			if main.Add(state.Member{AccountID: p.AccountID, Profile: p.Name}) {
				added++
			}
		}
		if added > 0 {
			note("main: %d member(s) added or updated", added)
		}
	}

	if len(l.ClaudeRotation) > 0 && !slices.Equal(c.Rotation.Accounts, l.ClaudeRotation) {
		c.Rotation.Accounts = slices.Clone(l.ClaudeRotation)
		note("rotation.accounts = %s", strings.Join(l.ClaudeRotation, ", "))
	}
	if x.RotationFile != "" && home != "" && filepath.Clean(x.RotationFile) != filepath.Join(home, ".claude-aws-account") {
		if c.Rotation.File != x.RotationFile {
			c.Rotation.File = x.RotationFile
			note("rotation.file = %s", x.RotationFile)
		}
	}
	return notes
}

// CompatDefaults switches c to script-compatible behaviour. Removal of stale
// profiles stays "prompt" (always reviewed, never automatic). Fallback roles are
// taken from the parsed script only (l may be nil) and only if none are set.
func CompatDefaults(c *state.Config, l *Legacy) {
	c.Mode = state.ModeCompat
	c.Roles.Policy = model.PolicyScript
	c.Roles.UpdateConfig = true
	c.Export.Enabled = true
	c.Export.StripPrefix = true
	c.Export.IncludeNewInMain = true
	c.Sync.AddNew = true
	c.Sync.RemoveStale = state.RemoveStalePrompt
	c.Rotation.Strategy = state.StrategyRandom
	if l != nil && len(l.FallbackRoles) > 0 && len(c.Roles.Fallbacks) == 0 {
		c.Roles.Fallbacks = slices.Clone(l.FallbackRoles)
	}
}
