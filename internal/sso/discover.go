package sso

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/credorbit/credorbit/internal/model"
)

// DefaultConcurrency is the number of parallel ListAccountRoles calls.
const DefaultConcurrency = 8

const stepDiscover = "discover"

// Discover lists every account visible with tok and the roles assigned in each.
//
// If ListAccounts fails the result has Complete=false, Err set and no accounts —
// callers must not infer revocation from it. Otherwise Complete=true; a failure
// of ListAccountRoles for one account sets only that account's RolesErr (roles
// unknown). ErrNoAccess on ListAccountRoles is a definitive answer: no roles,
// RolesErr "no access". Cancelling ctx stops scheduling new calls; accounts not
// processed get RolesErr set to the context error.
func Discover(ctx context.Context, c Client, tok model.Token, s model.SSOSession, concurrency int, rep model.Reporter) *model.Discovery {
	if rep == nil {
		rep = model.Discard
	}
	if concurrency <= 0 {
		concurrency = DefaultConcurrency
	}
	d := &model.Discovery{Session: s.Name, StartURL: s.StartURL, At: time.Now().UTC(), Accounts: []model.DiscoveredAccount{}}
	rep.Report(model.Event{Kind: model.EventStepStart, Step: stepDiscover, Subject: s.Name, Message: "listing accounts"})

	accts, err := c.ListAccounts(ctx, tok.AccessToken)
	if err != nil {
		d.Err = err.Error()
		rep.Report(model.Event{Kind: model.EventItemFailed, Step: stepDiscover, Subject: s.Name, Message: "list accounts failed: " + d.Err, Err: err})
		rep.Report(model.Event{Kind: model.EventStepDone, Step: stepDiscover, Subject: s.Name, Message: "discovery incomplete"})
		return d
	}

	// Deduplicate defensively: a misbehaving page sequence must not double accounts.
	seen := make(map[string]bool, len(accts))
	out := make([]model.DiscoveredAccount, 0, len(accts))
	for _, a := range accts {
		if a.ID == "" || seen[a.ID] {
			continue
		}
		seen[a.ID] = true
		out = append(out, model.DiscoveredAccount{Account: a, Roles: []string{}})
	}

	total := len(out)
	var (
		mu   sync.Mutex // serializes progress counting and reporting
		done int
	)
	report := func(i int, failErr error, msg string) {
		mu.Lock()
		defer mu.Unlock()
		done++
		if failErr != nil {
			rep.Report(model.Event{Kind: model.EventItemFailed, Step: stepDiscover, Subject: out[i].ID, Message: msg, Err: failErr})
		}
		rep.Report(model.Event{Kind: model.EventProgress, Step: stepDiscover, Done: done, Total: total})
	}
	rep.Report(model.Event{Kind: model.EventProgress, Step: stepDiscover, Done: 0, Total: total})

	workers := min(concurrency, total)
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				// Each worker owns out[i] exclusively for the index it received.
				roles, err := c.ListAccountRoles(ctx, tok.AccessToken, out[i].ID)
				switch {
				case err == nil:
					sort.Strings(roles)
					out[i].Roles = dedupe(roles)
					report(i, nil, "")
				case errors.Is(err, model.ErrNoAccess):
					out[i].RolesErr = "no access"
					report(i, err, fmt.Sprintf("%s (%s): no access to roles", out[i].Name, out[i].ID))
				default:
					out[i].RolesErr = err.Error()
					report(i, err, fmt.Sprintf("%s (%s): list roles failed: %s", out[i].Name, out[i].ID, err))
				}
			}
		}()
	}
	next := 0
feed:
	for ; next < total; next++ {
		select {
		case jobs <- next:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
	// Accounts never handed to a worker because ctx was cancelled.
	for i := next; i < total; i++ {
		out[i].RolesErr = ctx.Err().Error()
	}

	model.SortAccounts(out)
	d.Accounts = out
	d.Complete = true
	rep.Report(model.Event{Kind: model.EventStepDone, Step: stepDiscover, Subject: s.Name,
		Message: fmt.Sprintf("discovered %d accounts", total), Done: total, Total: total})
	return d
}

// dedupe removes adjacent duplicates from a sorted slice, never returning nil.
func dedupe(sorted []string) []string {
	out := make([]string, 0, len(sorted))
	for i, r := range sorted {
		if i == 0 || r != sorted[i-1] {
			out = append(out, r)
		}
	}
	return out
}
