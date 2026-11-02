package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/credorbit/credorbit/internal/awsini"
	"github.com/credorbit/credorbit/internal/credexport"
	"github.com/credorbit/credorbit/internal/fsutil"
	"github.com/credorbit/credorbit/internal/rotate"
	"github.com/credorbit/credorbit/internal/state"
)

func (e *Engine) Rotation() RotationInfo {
	c := e.conf()
	file := e.rotationFile(c)
	cur, _ := rotate.ReadCurrent(file)
	if cur == "" {
		e.mu.Lock()
		cur = e.st.Rotation.Current
		e.mu.Unlock()
	}
	creds := e.credsFile(c)
	ok, skipped := rotate.Eligible(c.Rotation.Accounts, creds, e.now())
	exp := credexport.ReadExpirations(creds)
	info := RotationInfo{Target: c.Rotation.Target, File: file, Current: cur, Strategy: c.Rotation.Strategy}
	for _, a := range c.Rotation.Accounts {
		info.Accounts = append(info.Accounts, RotationAccount{Section: a, Eligible: slices.Contains(ok, a), Reason: skipped[a], Expires: exp[a]})
	}
	return info
}

func (e *Engine) credSections(path string) map[string]bool {
	out := map[string]bool{}
	data, err := fsutil.ReadFileOptional(path)
	if err != nil || data == nil {
		return out
	}
	f, err := awsini.Parse(data)
	if err != nil {
		return out
	}
	for _, s := range f.Sections() {
		out[s.Name()] = true
	}
	return out
}

// Rotate selects the next Claude/Bedrock account and writes the rotation file.
func (e *Engine) Rotate(ctx context.Context, o RotateOptions) (string, string, error) {
	c := e.conf()
	file := e.rotationFile(c)
	from, err := rotate.ReadCurrent(file)
	if err != nil {
		return "", "", err
	}
	creds := e.credsFile(c)
	verify := c.Rotation.VerifyBedrock
	if o.Verify != nil {
		verify = *o.Verify
	}
	if verify && (c.Rotation.BedrockRegion == "" || c.Rotation.BedrockModel == "") {
		return from, "", errors.New("bedrock verification needs rotation.bedrock_region and rotation.bedrock_model (credorbit config set ...)")
	}
	data, _ := fsutil.ReadFileOptional(creds)
	check := func(section string) error {
		if !verify {
			return nil
		}
		cr, ok := sectionCreds(data, section)
		if !ok {
			return fmt.Errorf("%s: no credentials in %s", section, creds)
		}
		v := rotate.BedrockVerifier{HTTP: e.hc, BaseEndpoint: e.opts.BedrockEndpoint}
		if err := v.Verify(ctx, cr, c.Rotation.BedrockRegion, c.Rotation.BedrockModel); err != nil {
			return fmt.Errorf("%s: %w", section, err)
		}
		return nil
	}

	var to string
	if o.Set != "" {
		to = strings.TrimSpace(o.Set)
		if !slices.Contains(c.Rotation.Accounts, to) {
			if !e.credSections(creds)[to] {
				return from, "", fmt.Errorf("%q is neither a rotation account nor a section of %s", to, creds)
			}
			e.report(warnEvent("rotate", fmt.Sprintf("%s is not in rotation.accounts; setting it anyway", to)))
		}
		if err := check(to); err != nil {
			return from, "", fmt.Errorf("bedrock verification failed: %w", err)
		}
	} else {
		if len(c.Rotation.Accounts) == 0 {
			return from, "", errors.New("no rotation accounts configured: run `credorbit rotate accounts set a,b,c`")
		}
		strategy := c.Rotation.Strategy
		if o.Strategy != "" {
			strategy = o.Strategy
		}
		st, err := rotate.ParseStrategy(strategy)
		if err != nil {
			return from, "", err
		}
		eligible, skipped := rotate.Eligible(c.Rotation.Accounts, creds, e.now())
		var failures []string
		for {
			next, err := rotate.Next(from, eligible, st, nil)
			if errors.Is(err, rotate.ErrNoEligible) {
				var reasons []string
				for _, a := range c.Rotation.Accounts {
					if r, ok := skipped[a]; ok {
						reasons = append(reasons, a+": "+r)
					}
				}
				reasons = append(reasons, failures...)
				sort.Strings(reasons)
				return from, "", fmt.Errorf("%w — %s (run `credorbit export` to refresh credentials)", rotate.ErrNoEligible, strings.Join(reasons, "; "))
			}
			if err != nil {
				return from, "", err
			}
			if verr := check(next); verr != nil {
				failures = append(failures, verr.Error())
				eligible = slices.DeleteFunc(eligible, func(s string) bool { return s == next })
				continue
			}
			to = next
			break
		}
	}
	if o.DryRun {
		return from, to, nil
	}
	if err := rotate.WriteCurrent(file, to); err != nil {
		return from, "", err
	}
	if err := e.mutateState(func(s *state.State) { s.RecordRotation(from, to, e.now()) }); err != nil {
		return from, to, err
	}
	e.log.Info("rotated", "from", from, "to", to)
	return from, to, nil
}

func (e *Engine) SetRotationAccounts(sections []string) error {
	var clean []string
	for _, s := range sections {
		if s = strings.TrimSpace(s); s != "" && !slices.Contains(clean, s) {
			clean = append(clean, s)
		}
	}
	return e.mutateConfig(func(c *state.Config) error { c.Rotation.Accounts = clean; return nil })
}

func (e *Engine) ShellIntegration(shell string) (string, error) {
	return rotate.ShellIntegration(shell, e.rotationFile(e.conf()))
}
