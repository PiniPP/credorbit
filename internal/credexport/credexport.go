// Package credexport fetches temporary role credentials for SSO profiles and
// writes them to ~/.aws/credentials.
//
// Credentials are kept in an unexported pointer field of Item, so printing an
// Item or Result with any fmt verb never reveals them; Item.Credentials is the
// only accessor (used by Write and by Bedrock verification during rotation).
package credexport

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/credorbit/credorbit/internal/model"
	"github.com/credorbit/credorbit/internal/roles"
)

// Target is one profile to export and the credentials section it goes to.
type Target struct {
	Profile model.Profile
	Section string // "p_x" → "x" (compat) or the profile name; see SectionName
}

// Fetcher obtains credentials for a profile, trying candidate roles in order.
// It returns the role that worked.
type Fetcher interface {
	Fetch(ctx context.Context, p model.Profile, candidates []string) (model.Credentials, string, error)
}

// RoleCredentialsAPI is the subset of sso.Client used here. Declared locally
// so this package does not depend on internal/sso (sso.Client satisfies it).
type RoleCredentialsAPI interface {
	GetRoleCredentials(ctx context.Context, accessToken, accountID, role string) (model.Credentials, error)
}

// Attempt is the outcome of trying one role.
type Attempt struct {
	Role string
	Err  error
}

// FetchError aggregates the failed attempts for one profile. Its message reads
// "RoleA: no access; RoleB: transient failure: ..."; errors.Is matches
// model.ErrNoAccess when every attempt was refused, otherwise the first
// non-no-access error (e.g. model.ErrTransient).
type FetchError struct{ Attempts []Attempt }

func (e *FetchError) Error() string {
	parts := make([]string, len(e.Attempts))
	for i, a := range e.Attempts {
		parts[i] = a.Role + ": " + a.Err.Error()
	}
	return strings.Join(parts, "; ")
}

func (e *FetchError) Unwrap() error {
	for _, a := range e.Attempts {
		if !errors.Is(a.Err, model.ErrNoAccess) {
			return a.Err
		}
	}
	if len(e.Attempts) > 0 {
		return model.ErrNoAccess
	}
	return nil
}

type ssoFetcher struct {
	api    RoleCredentialsAPI
	tokens func(model.Profile) (model.Token, error)
}

// NewSSOFetcher returns a Fetcher backed by SSO GetRoleCredentials. tokens
// resolves the access token for a profile (its sso_session, or its legacy
// sso_start_url); it receives the whole profile rather than a session name
// so legacy profiles can be served too.
func NewSSOFetcher(api RoleCredentialsAPI, tokens func(p model.Profile) (model.Token, error)) Fetcher {
	return &ssoFetcher{api: api, tokens: tokens}
}

func (s *ssoFetcher) Fetch(ctx context.Context, p model.Profile, candidates []string) (model.Credentials, string, error) {
	tok, err := s.tokens(p)
	if err != nil {
		return model.Credentials{}, "", err
	}
	if tok.AccessToken == "" {
		return model.Credentials{}, "", fmt.Errorf("no SSO access token for %s: %w", p.Name, model.ErrUnauthorized)
	}
	fe := &FetchError{}
	for _, role := range candidates {
		if err := ctx.Err(); err != nil {
			return model.Credentials{}, "", fmt.Errorf("%w: %w", model.ErrCanceled, err)
		}
		c, err := s.api.GetRoleCredentials(ctx, tok.AccessToken, p.AccountID, role)
		if err == nil {
			return c, role, nil
		}
		if errors.Is(err, model.ErrUnauthorized) {
			return model.Credentials{}, "", err
		}
		if errors.Is(err, model.ErrNoAccess) {
			err = model.ErrNoAccess // concise "RoleA: no access"
		}
		fe.Attempts = append(fe.Attempts, Attempt{Role: role, Err: err})
	}
	if len(fe.Attempts) == 0 {
		return model.Credentials{}, "", errors.New("no candidate roles")
	}
	return model.Credentials{}, "", fe
}

// Options controls Fetch.
type Options struct {
	Concurrency int // default 10
	Policy      model.RolePolicy
	Ranker      roles.Ranker // zero value → roles.DefaultRanker()
	Fallbacks   []string
	Manual      map[string]string   // accountID → role
	Assigned    map[string][]string // accountID → discovered roles (nil = unknown)
}

// Item is the outcome for one target.
type Item struct {
	Target     Target
	Role       string // role that worked
	Expiration time.Time
	Err        error
	creds      *model.Credentials
}

// String describes the item without secrets.
func (i Item) String() string {
	return fmt.Sprintf("Item{Profile:%s Section:%s Role:%s Expiration:%s Err:%v}",
		i.Target.Profile.Name, i.Target.Section, i.Role, i.Expiration.Format(time.RFC3339), i.Err)
}

// Format makes every fmt verb (%v, %+v, %#v, %d, %x, ...) use String, so
// credentials can never be printed through fmt.
func (i Item) Format(s fmt.State, verb rune) { _, _ = fmt.Fprint(s, i.String()) }

// OK reports whether credentials were fetched.
func (i Item) OK() bool { return i.Err == nil && i.creds != nil }

// Credentials returns the fetched credentials (zero value if the item failed).
// Only for writing the credentials file and verifying access; never print.
func (i Item) Credentials() model.Credentials {
	if i.creds == nil {
		return model.Credentials{}
	}
	return *i.creds
}

// Result holds one Item per target, in input order.
type Result struct{ Items []Item }

// Succeeded counts items with credentials.
func (r *Result) Succeeded() int {
	n := 0
	for _, it := range r.Items {
		if it.OK() {
			n++
		}
	}
	return n
}

// RoleChanges maps profile → role used, for successes whose role differs from
// the profile's configured sso_role_name.
func (r *Result) RoleChanges() map[string]string {
	m := map[string]string{}
	for _, it := range r.Items {
		if it.OK() && it.Role != it.Target.Profile.Role {
			m[it.Target.Profile.Name] = it.Role
		}
	}
	return m
}

const step = "export"

// Fetch exports all targets with a pool of o.Concurrency workers. Result.Items
// preserves input order. An ErrUnauthorized from any item cancels the remaining
// work (the token is shared, so continuing is pointless); unprocessed items get
// an error wrapping ErrUnauthorized. When ctx is canceled the partial result is
// returned and unprocessed items carry an error wrapping model.ErrCanceled.
func Fetch(ctx context.Context, targets []Target, f Fetcher, o Options, rep model.Reporter) *Result {
	if rep == nil {
		rep = model.Discard
	}
	if len(o.Ranker.Rules) == 0 {
		o.Ranker = roles.DefaultRanker()
	}
	if o.Policy == "" {
		o.Policy = model.PolicyConfigured
	}
	workers := o.Concurrency
	if workers <= 0 {
		workers = 10
	}
	workers = min(workers, max(len(targets), 1))

	res := &Result{Items: make([]Item, len(targets))}
	total := len(targets)
	rep.Report(model.Event{Kind: model.EventStepStart, Step: step, Message: fmt.Sprintf("exporting %d profiles", total), Total: total})

	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var unauthorized atomic.Bool
	var done atomic.Int64

	jobs := make(chan int, len(targets))
	for i := range targets {
		jobs <- i
	}
	close(jobs)

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				it := Item{Target: targets[i]}
				if it.Target.Section == "" {
					it.Target.Section = it.Target.Profile.Name
				}
				if err := wctx.Err(); err != nil {
					it.Err = stopped("not attempted", &unauthorized, ctx)
				} else {
					fetchOne(wctx, &it, f, o)
					if it.Err != nil && errors.Is(it.Err, model.ErrUnauthorized) {
						unauthorized.Store(true)
						cancel()
					} else if it.Err != nil && wctx.Err() != nil && !errors.Is(it.Err, model.ErrNoAccess) {
						// Aborted mid-flight by cancellation: report the cause.
						it.Err = fmt.Errorf("%w (%v)", stopped("aborted", &unauthorized, ctx), it.Err)
					}
				}
				res.Items[i] = it
				n := int(done.Add(1))
				name := it.Target.Profile.Name
				if it.OK() {
					msg := "role " + it.Role
					if !it.Expiration.IsZero() {
						msg += ", expires " + it.Expiration.Local().Format("15:04")
					}
					rep.Report(model.Event{Kind: model.EventItemOK, Step: step, Subject: name, Message: msg, Done: n, Total: total})
				} else {
					rep.Report(model.Event{Kind: model.EventItemFailed, Step: step, Subject: name, Message: it.Err.Error(), Err: it.Err, Done: n, Total: total})
				}
				rep.Report(model.Event{Kind: model.EventProgress, Step: step, Subject: name, Done: n, Total: total})
			}
		}()
	}
	wg.Wait()
	rep.Report(model.Event{Kind: model.EventStepDone, Step: step,
		Message: fmt.Sprintf("%d of %d profiles exported", res.Succeeded(), total), Done: total, Total: total})
	return res
}

func stopped(what string, unauthorized *atomic.Bool, parent context.Context) error {
	if unauthorized.Load() {
		return fmt.Errorf("%s: %w (sign in again)", what, model.ErrUnauthorized)
	}
	if err := parent.Err(); err != nil {
		return fmt.Errorf("%s: %w: %w", what, model.ErrCanceled, err)
	}
	return fmt.Errorf("%s: %w", what, model.ErrCanceled)
}

func fetchOne(ctx context.Context, it *Item, f Fetcher, o Options) {
	p := it.Target.Profile
	if p.AccountID == "" || (p.Session == "" && p.LegacyStartURL == "") {
		it.Err = fmt.Errorf("profile %s is not an SSO profile (needs sso_account_id and sso_session or sso_start_url)", p.Name)
		return
	}
	assigned, known := o.Assigned[p.AccountID]
	req := roles.Request{
		Policy:        o.Policy,
		Assigned:      assigned,
		AssignedKnown: o.Assigned != nil && known,
		Configured:    p.Role,
		Manual:        o.Manual[p.AccountID],
		Fallbacks:     o.Fallbacks,
	}
	cands := roles.Candidates(req, o.Ranker)
	if len(cands) == 0 {
		it.Err = noCandidates(req)
		return
	}
	c, role, err := f.Fetch(ctx, p, cands)
	if err != nil {
		it.Err = err
		return
	}
	it.Role, it.Expiration, it.creds = role, c.Expiration, &c
}

func noCandidates(req roles.Request) error {
	switch req.Policy {
	case model.PolicyManual:
		return errors.New("no role chosen for this account (manual policy); choose one")
	case model.PolicyConfigured:
		if req.Configured == "" {
			return errors.New("profile has no sso_role_name")
		}
		return fmt.Errorf("configured role %s is no longer assigned (assigned: %s); choose another: %w",
			req.Configured, listOrNone(req.Assigned), model.ErrNoAccess)
	default:
		return fmt.Errorf("no SSO role assigned to you on this account: %w", model.ErrNoAccess)
	}
}

func listOrNone(s []string) string {
	if len(s) == 0 {
		return "none"
	}
	return strings.Join(s, ", ")
}
