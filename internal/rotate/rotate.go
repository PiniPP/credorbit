// Package rotate picks the AWS account (credentials section) Claude Code uses
// through Amazon Bedrock, persists it in a small file read by the user's shell,
// and can verify that an account actually has Bedrock access.
package rotate

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/credorbit/credorbit/internal/fsutil"
)

// Strategy selects how the next account is chosen.
type Strategy string

const (
	StrategyRandom     Strategy = "random"
	StrategyRoundRobin Strategy = "round-robin"
	StrategyManual     Strategy = "manual"
)

// ParseStrategy validates a strategy name.
func ParseStrategy(s string) (Strategy, error) {
	switch Strategy(s) {
	case StrategyRandom, StrategyRoundRobin, StrategyManual:
		return Strategy(s), nil
	}
	return "", fmt.Errorf("unknown rotation strategy %q (want random|round-robin|manual)", s)
}

// ErrNoEligible is returned by Next when no account is eligible.
var ErrNoEligible = errors.New("no eligible rotation account (none has valid credentials)")

// ErrManual is returned by Next for the manual strategy.
var ErrManual = errors.New("manual strategy: use --set")

// ExpirySkew is how long credentials must remain valid to be eligible.
const ExpirySkew = 5 * time.Minute

// ExpirationKey is the credentials-file key CredOrbit writes with each section.
const ExpirationKey = "x_credorbit_expiration"

// Next chooses the account after current. eligible is deduplicated (order
// kept). With one eligible account it is returned even if it equals current;
// random never returns current when an alternative exists. rnd may be nil.
func Next(current string, eligible []string, s Strategy, rnd *rand.Rand) (string, error) {
	switch s {
	case StrategyRandom, StrategyRoundRobin:
	case StrategyManual:
		return "", ErrManual
	default:
		return "", fmt.Errorf("unknown rotation strategy %q", s)
	}
	list := uniq(eligible)
	switch len(list) {
	case 0:
		return "", ErrNoEligible
	case 1:
		return list[0], nil
	}
	if s == StrategyRoundRobin {
		for i, a := range list {
			if a == current {
				return list[(i+1)%len(list)], nil
			}
		}
		return list[0], nil
	}
	candidates := make([]string, 0, len(list))
	for _, a := range list {
		if a != current {
			candidates = append(candidates, a)
		}
	}
	return candidates[intN(rnd, len(candidates))], nil
}

func intN(r *rand.Rand, n int) int {
	if r == nil {
		return rand.IntN(n)
	}
	return r.IntN(n)
}

func uniq(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, x := range in {
		x = strings.TrimSpace(x)
		if x == "" || seen[x] {
			continue
		}
		seen[x] = true
		out = append(out, x)
	}
	return out
}

// ReadCurrent returns the account stored in path (trimmed); missing → "".
func ReadCurrent(path string) (string, error) {
	b, err := fsutil.ReadFileOptional(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return strings.TrimSpace(string(b)), nil
}

// WriteCurrent atomically stores account (plus a trailing newline, as the
// legacy script did) with 0600 permissions.
func WriteCurrent(path, account string) error {
	account = strings.TrimSpace(account)
	if account == "" || strings.ContainsAny(account, "\r\n\x00") {
		return fmt.Errorf("invalid rotation account %q", account)
	}
	if err := fsutil.AtomicWrite(path, []byte(account+"\n"), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// Eligible returns the accounts (credentials section names, deduplicated, in
// order) that have complete, unexpired credentials in credsPath, and a reason
// for every skipped account.
func Eligible(accounts []string, credsPath string, now time.Time) (ok []string, skipped map[string]string) {
	skipped = map[string]string{}
	list := uniq(accounts)
	data, err := fsutil.ReadFileOptional(credsPath)
	if err != nil {
		for _, a := range list {
			skipped[a] = "credentials file unreadable: " + err.Error()
		}
		return nil, skipped
	}
	sections := parseINI(data)
	for _, a := range list {
		kv, found := sections[a]
		if !found {
			skipped[a] = "no credentials section"
			continue
		}
		if kv["aws_access_key_id"] == "" || kv["aws_secret_access_key"] == "" || kv["aws_session_token"] == "" {
			skipped[a] = "incomplete credentials"
			continue
		}
		if raw := kv[ExpirationKey]; raw != "" {
			exp, perr := time.Parse(time.RFC3339, raw)
			if perr != nil {
				skipped[a] = fmt.Sprintf("unreadable %s %q", ExpirationKey, raw)
				continue
			}
			if !exp.After(now.Add(ExpirySkew)) {
				skipped[a] = "expired at " + exp.Local().Format(time.RFC3339)
				continue
			}
		}
		ok = append(ok, a)
	}
	return ok, skipped
}

// parseINI is a minimal reader for the AWS credentials file: [section] headers,
// key = value lines, ';'/'#' comments. Repeated sections merge (last key wins).
func parseINI(data []byte) map[string]map[string]string {
	out := map[string]map[string]string{}
	var cur map[string]string
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(sc.Text(), "\ufeff"))
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' {
			end := strings.IndexByte(line, ']')
			if end < 0 {
				cur = nil
				continue
			}
			name := strings.TrimSpace(line[1:end])
			if out[name] == nil {
				out[name] = map[string]string{}
			}
			cur = out[name]
			continue
		}
		if cur == nil {
			continue
		}
		k, v, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		cur[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
	return out
}
