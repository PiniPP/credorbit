package roles

import (
	"reflect"
	"testing"

	"github.com/PiniPP/credorbit/internal/model"
)

func TestDefaultRank(t *testing.T) {
	r := DefaultRanker()
	cases := map[string]int{
		"AdministratorAccess":    0,
		"administratoraccess":    0,
		"Awsrole-Acme-Admin-Lim": 100,
		"AdministratorAccess-DR": 200,
		"acme-admin-dr":          200,
		"PowerUserAccess":        300,
		"Acme-DevOps":            300,
		"Developer":              400,
		"Billing":                400,
		"ReadOnlyAccess":         500,
		"ViewOnlyAccess":         500,
		"":                       400,
	}
	for role, want := range cases {
		if got := r.Rank(role); got != want {
			t.Errorf("Rank(%q) = %d want %d", role, got, want)
		}
	}
}

func TestRuleMatch(t *testing.T) {
	if !(Rule{Match: "*ADMIN*"}).Matches("myadminrole") {
		t.Fatal("case-insensitive glob")
	}
	if (Rule{Match: "[bad"}).Matches("[bad") {
		t.Fatal("malformed pattern must not match")
	}
	custom := Ranker{Rules: []Rule{{Match: "dev*", Weight: 1}}}
	if custom.Rank("Developer") != 1 || custom.Rank("x") != UnmatchedWeight {
		t.Fatal("custom ranker")
	}
}

func TestSort(t *testing.T) {
	in := []string{"ReadOnlyAccess", "Zeta", "AdministratorAccess-DR", "Alpha", "Acme-Admin", "AdministratorAccess", "PowerUserAccess"}
	want := []string{"AdministratorAccess", "Acme-Admin", "AdministratorAccess-DR", "PowerUserAccess", "Alpha", "Zeta", "ReadOnlyAccess"}
	got := DefaultRanker().Sort(in)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if in[0] != "ReadOnlyAccess" {
		t.Fatal("input modified")
	}
}

func TestCandidates(t *testing.T) {
	r := DefaultRanker()
	assigned := []string{"ReadOnlyAccess", "AdministratorAccess", "Developer"}
	cases := []struct {
		name string
		req  Request
		want []string
	}{
		{"configured assigned", Request{Policy: model.PolicyConfigured, Assigned: assigned, AssignedKnown: true, Configured: "Developer"}, []string{"Developer"}},
		{"configured unknown", Request{Policy: model.PolicyConfigured, Configured: "Gone"}, []string{"Gone"}},
		{"configured not assigned", Request{Policy: model.PolicyConfigured, Assigned: assigned, AssignedKnown: true, Configured: "Gone"}, []string{}},
		{"configured empty", Request{Policy: model.PolicyConfigured}, []string{}},
		{"unknown policy = configured", Request{Policy: "weird", Configured: "X"}, []string{"X"}},
		{"manual", Request{Policy: model.PolicyManual, Manual: "Developer", Configured: "X"}, []string{"Developer"}},
		{"manual empty", Request{Policy: model.PolicyManual}, []string{}},
		{"strongest", Request{Policy: model.PolicyStrongest, Assigned: assigned, AssignedKnown: true, Configured: "X"}, []string{"AdministratorAccess", "Developer", "ReadOnlyAccess"}},
		{"strongest unknown", Request{Policy: model.PolicyStrongest, Configured: "X"}, []string{"X"}},
		{"readonly", Request{Policy: model.PolicyReadOnly, Assigned: assigned, AssignedKnown: true}, []string{"ReadOnlyAccess", "Developer", "AdministratorAccess"}},
		{"script", Request{Policy: model.PolicyScript, Assigned: assigned, AssignedKnown: true, Configured: "Developer",
			Fallbacks: []string{"AdministratorAccess", "Acme-Admin-Limit", ""}},
			[]string{"AdministratorAccess", "Developer", "ReadOnlyAccess", "Acme-Admin-Limit"}},
		{"script unknown", Request{Policy: model.PolicyScript, Configured: "Developer", Fallbacks: []string{"AdministratorAccess"}},
			[]string{"Developer", "AdministratorAccess"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Candidates(c.req, r)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %v want %v", got, c.want)
			}
		})
	}
}
