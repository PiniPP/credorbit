// Package inventory compares an SSO discovery with the configured profiles of
// one SSO session and decides, conservatively, which profiles are stale.
package inventory

import (
	"fmt"
	"sort"

	"github.com/credorbit/credorbit/internal/model"
)

// DefaultThreshold is the minimum ratio discovered/configured accounts below
// which stale removal is refused (the legacy script's 80% safeguard).
const DefaultThreshold = 0.8

// Item is one account: discovered, configured, or both.
type Item struct {
	Account  model.DiscoveredAccount
	Profiles []model.Profile // configured profiles for this account in this session
	Status   model.AccountStatus
}

// InSession reports whether p is an SSO profile of session: either
// sso_session == session, or a legacy profile (no sso_session) whose
// sso_start_url equals the discovery's start URL.
func InSession(p model.Profile, session string, d *model.Discovery) bool {
	if p.AccountID == "" {
		return false
	}
	if p.Session != "" {
		return p.Session == session
	}
	return p.LegacyStartURL != "" && d != nil && d.StartURL != "" && p.LegacyStartURL == d.StartURL
}

// byAccount groups the session's profiles by account ID (profiles sorted by name).
func byAccount(d *model.Discovery, profiles []model.Profile, session string) map[string][]model.Profile {
	m := map[string][]model.Profile{}
	for _, p := range profiles {
		if InSession(p, session, d) {
			m[p.AccountID] = append(m[p.AccountID], p)
		}
	}
	for _, ps := range m {
		sort.SliceStable(ps, func(i, j int) bool { return ps[i].Name < ps[j].Name })
	}
	return m
}

// Build returns the union of discovered accounts and configured accounts,
// sorted by account name then ID. Configured-only accounts are "missing" only
// when d is a complete, live discovery; otherwise "unknown". Their Account.Name
// is the (first) profile name.
func Build(d *model.Discovery, profiles []model.Profile, session string) []Item {
	conf := byAccount(d, profiles, session)
	var items []Item
	seen := map[string]bool{}
	if d != nil {
		for _, a := range d.Accounts {
			if seen[a.ID] {
				continue
			}
			seen[a.ID] = true
			it := Item{Account: a, Profiles: conf[a.ID], Status: model.StatusNew}
			if len(it.Profiles) > 0 {
				it.Status = model.StatusAvailable
			}
			items = append(items, it)
		}
	}
	decisive := d != nil && d.Complete && !d.Cached
	for id, ps := range conf {
		if seen[id] {
			continue
		}
		st := model.StatusUnknown
		if decisive {
			st = model.StatusMissing
		}
		items = append(items, Item{
			Account:  model.DiscoveredAccount{Account: model.Account{ID: id, Name: ps[0].Name}},
			Profiles: ps,
			Status:   st,
		})
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i].Account, items[j].Account
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.ID < b.ID
	})
	return items
}

// StaleResult lists profiles whose account is absent from a complete discovery.
// When Blocked, Stale is empty and Reason explains why nothing may be removed.
type StaleResult struct {
	Stale   []model.Profile
	Blocked bool
	Reason  string
}

// Stale returns the session's profiles whose account has zero presence in the
// discovery. It refuses (Blocked) when the discovery is missing, incomplete or
// cached, or when — as in the legacy script — there are stale candidates and
// fewer than threshold × configured accounts were discovered (threshold <= 0
// means DefaultThreshold). Stale is sorted by profile name.
func Stale(d *model.Discovery, profiles []model.Profile, session string, threshold float64) StaleResult {
	if threshold <= 0 {
		threshold = DefaultThreshold
	}
	switch {
	case d == nil:
		return StaleResult{Blocked: true, Reason: "discovery incomplete: no discovery available"}
	case !d.Complete:
		msg := d.Err
		if msg == "" {
			msg = "account list not fully retrieved"
		}
		return StaleResult{Blocked: true, Reason: "discovery incomplete: " + msg}
	case d.Cached:
		return StaleResult{Blocked: true, Reason: "discovery is cached/offline"}
	}
	conf := byAccount(d, profiles, session)
	present := map[string]bool{}
	for _, a := range d.Accounts {
		present[a.ID] = true
	}
	var stale []model.Profile
	for id, ps := range conf {
		if !present[id] {
			stale = append(stale, ps...)
		}
	}
	if len(stale) == 0 {
		return StaleResult{}
	}
	if n, m := len(present), len(conf); float64(n) < threshold*float64(m) {
		return StaleResult{Blocked: true, Reason: fmt.Sprintf(
			"discovered %d accounts but %d are configured (< %.0f%%) — refusing to remove anything", n, m, threshold*100)}
	}
	sort.SliceStable(stale, func(i, j int) bool {
		if stale[i].Name != stale[j].Name {
			return stale[i].Name < stale[j].Name
		}
		return stale[i].AccountID < stale[j].AccountID
	})
	return StaleResult{Stale: stale}
}
