// Package roles ranks SSO permission-set names and turns a role policy into an
// ordered list of roles to attempt.
package roles

import (
	"path"
	"sort"
	"strings"

	"github.com/PiniPP/credorbit/internal/model"
)

// UnmatchedWeight is the weight of a role no rule matches.
const UnmatchedWeight = 400

// Rule assigns Weight to roles whose lowercased name matches the lowercased
// glob Match (path.Match semantics). Lower weight = stronger role.
type Rule struct {
	Match  string `json:"match" yaml:"match"`
	Weight int    `json:"weight" yaml:"weight"`
}

// Matches reports whether role matches the rule (case-insensitive glob).
// Malformed patterns never match.
func (r Rule) Matches(role string) bool {
	ok, err := path.Match(strings.ToLower(r.Match), strings.ToLower(role))
	return err == nil && ok
}

// Ranker ranks roles with the first matching rule.
type Ranker struct{ Rules []Rule }

// DefaultRanker reproduces the legacy script's ranking:
// AdministratorAccess (0) < *admin* (100) < *admin*-dr (200) <
// *poweruser*/*devops* (300) < other (400) < *readonly*/*viewonly* (500).
func DefaultRanker() Ranker {
	return Ranker{Rules: []Rule{
		{Match: "administratoraccess", Weight: 0},
		{Match: "*admin*-dr", Weight: 200},
		{Match: "*admin*", Weight: 100},
		{Match: "*poweruser*", Weight: 300},
		{Match: "*devops*", Weight: 300},
		{Match: "*readonly*", Weight: 500},
		{Match: "*viewonly*", Weight: 500},
	}}
}

// Rank returns the weight of the first rule matching role, or UnmatchedWeight.
func (r Ranker) Rank(role string) int {
	for _, rule := range r.Rules {
		if rule.Matches(role) {
			return rule.Weight
		}
	}
	return UnmatchedWeight
}

// Sort returns a new slice ordered strongest first; ties are alphabetical.
// The input is not modified.
func (r Ranker) Sort(roles []string) []string {
	out := append([]string(nil), roles...)
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := r.Rank(out[i]), r.Rank(out[j])
		if ri != rj {
			return ri < rj
		}
		return out[i] < out[j]
	})
	return out
}

// Request describes one account/profile for which roles must be chosen.
type Request struct {
	Policy        model.RolePolicy
	Assigned      []string // roles discovered for the account
	AssignedKnown bool     // false when role discovery did not run or failed
	Configured    string   // the profile's sso_role_name
	Manual        string   // user's stored choice for the account
	Fallbacks     []string // legacy fallback roles (script policy only)
}

// Candidates returns the roles to attempt, in order, deduplicated, without
// empty names.
//
//   - configured: [Configured] when it is assigned or assignment is unknown;
//     empty when discovery shows it is no longer assigned (the caller should
//     report "configured role no longer assigned; choose another").
//   - manual: [Manual].
//   - strongest: assigned roles strongest first. When assignment is unknown,
//     [Configured] (no substitution without knowledge).
//   - readonly: assigned roles weakest first. When unknown, [Configured].
//   - script: assigned strongest first, then Configured, then Fallbacks (tried
//     even if not listed, as the legacy script did).
//
// An unknown policy behaves like configured.
func Candidates(req Request, r Ranker) []string {
	var list []string
	switch req.Policy {
	case model.PolicyManual:
		list = []string{req.Manual}
	case model.PolicyStrongest:
		if req.AssignedKnown {
			list = r.Sort(req.Assigned)
		} else {
			list = []string{req.Configured}
		}
	case model.PolicyReadOnly:
		if req.AssignedKnown {
			list = append([]string(nil), req.Assigned...)
			sort.SliceStable(list, func(i, j int) bool {
				ri, rj := r.Rank(list[i]), r.Rank(list[j])
				if ri != rj {
					return ri > rj
				}
				return list[i] < list[j]
			})
		} else {
			list = []string{req.Configured}
		}
	case model.PolicyScript:
		list = append(list, r.Sort(req.Assigned)...)
		list = append(list, req.Configured)
		list = append(list, req.Fallbacks...)
	default: // configured
		if req.Configured != "" && (!req.AssignedKnown || contains(req.Assigned, req.Configured)) {
			list = []string{req.Configured}
		}
	}
	return dedupe(list)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func dedupe(list []string) []string {
	seen := make(map[string]bool, len(list))
	out := make([]string, 0, len(list))
	for _, s := range list {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
