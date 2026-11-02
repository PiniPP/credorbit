package inventory

import (
	"fmt"
	"strings"
	"testing"

	"github.com/PiniPP/credorbit/internal/model"
)

func acct(id, name string, roles ...string) model.DiscoveredAccount {
	return model.DiscoveredAccount{Account: model.Account{ID: id, Name: name}, Roles: roles}
}

func prof(name, session, id string) model.Profile {
	return model.Profile{Name: name, Session: session, AccountID: id, Role: "R"}
}

func TestBuild(t *testing.T) {
	profiles := []model.Profile{
		prof("p_acme-dev", "s", "111111111111"),
		prof("p_acme-dev-2", "s", "111111111111"),
		prof("p_gone", "s", "333333333333"),
		prof("p_other-session", "t", "444444444444"),
		{Name: "legacy", AccountID: "555555555555", Role: "R", LegacyStartURL: "https://fake.awsapps.com/start"},
		{Name: "legacy-other", AccountID: "666666666666", Role: "R", LegacyStartURL: "https://other.awsapps.com/start"},
		{Name: "static"},
	}
	d := &model.Discovery{Session: "s", StartURL: "https://fake.awsapps.com/start", Complete: true, Accounts: []model.DiscoveredAccount{
		acct("111111111111", "acme-dev"), acct("222222222222", "acme-new"), acct("555555555555", "acme-legacy"),
	}}
	cases := []struct {
		name string
		d    *model.Discovery
		want map[string]model.AccountStatus
	}{
		{"complete", d, map[string]model.AccountStatus{
			"111111111111": model.StatusAvailable, "222222222222": model.StatusNew,
			"333333333333": model.StatusMissing, "555555555555": model.StatusAvailable}},
		{"cached", withCached(d), map[string]model.AccountStatus{
			"111111111111": model.StatusAvailable, "222222222222": model.StatusNew,
			"333333333333": model.StatusUnknown, "555555555555": model.StatusAvailable}},
		{"incomplete", &model.Discovery{Session: "s", Err: "boom"}, map[string]model.AccountStatus{
			"111111111111": model.StatusUnknown, "333333333333": model.StatusUnknown}},
		{"nil", nil, map[string]model.AccountStatus{
			"111111111111": model.StatusUnknown, "333333333333": model.StatusUnknown}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			items := Build(c.d, profiles, "s")
			got := map[string]model.AccountStatus{}
			for _, it := range items {
				got[it.Account.ID] = it.Status
			}
			if fmt.Sprint(got) != fmt.Sprint(c.want) {
				t.Fatalf("got %v want %v", got, c.want)
			}
			for i := 1; i < len(items); i++ {
				if items[i-1].Account.Name > items[i].Account.Name {
					t.Fatal("not sorted by name")
				}
			}
		})
	}
	items := Build(d, profiles, "s")
	for _, it := range items {
		switch it.Account.ID {
		case "111111111111":
			if len(it.Profiles) != 2 || it.Profiles[0].Name != "p_acme-dev" {
				t.Fatalf("profiles = %+v", it.Profiles)
			}
		case "333333333333":
			if it.Account.Name != "p_gone" {
				t.Fatalf("missing account name = %q", it.Account.Name)
			}
		}
	}
}

func withCached(d *model.Discovery) *model.Discovery {
	c := *d
	c.Cached = true
	return &c
}

func TestStale(t *testing.T) {
	var profiles []model.Profile
	var accounts []model.DiscoveredAccount
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("%012d", 100000000000+i)
		profiles = append(profiles, prof(fmt.Sprintf("p_acme-%02d", i), "s", id))
		accounts = append(accounts, acct(id, fmt.Sprintf("acme-%02d", i)))
	}
	profiles = append(profiles, prof("p_other", "t", "999999999999")) // other session: never stale
	complete := func(n int) *model.Discovery {
		return &model.Discovery{Session: "s", Complete: true, Accounts: accounts[:n]}
	}
	cases := []struct {
		name      string
		d         *model.Discovery
		threshold float64
		stale     int
		blocked   string
	}{
		{"nothing stale", complete(10), 0, 0, ""},
		{"two stale (80%)", complete(8), 0, 2, ""},
		{"three stale (70%) blocked", complete(7), 0, 0, "discovered 7 accounts but 10 are configured (< 80%) — refusing to remove anything"},
		{"custom threshold allows", complete(7), 0.5, 3, ""},
		{"zero accounts blocked", complete(0), 0, 0, "refusing"},
		{"incomplete", &model.Discovery{Session: "s", Err: "ListAccounts: throttled"}, 0, 0, "discovery incomplete: ListAccounts: throttled"},
		{"incomplete partial accounts", &model.Discovery{Session: "s", Accounts: accounts[:1], Err: "page 2 failed"}, 0, 0, "discovery incomplete"},
		{"cached", withCached(complete(8)), 0, 0, "discovery is cached/offline"},
		{"nil", nil, 0, 0, "discovery incomplete"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Stale(c.d, profiles, "s", c.threshold)
			if len(r.Stale) != c.stale {
				t.Fatalf("stale = %d want %d (%+v)", len(r.Stale), c.stale, r)
			}
			if (c.blocked != "") != r.Blocked || !strings.Contains(r.Reason, c.blocked) {
				t.Fatalf("blocked=%v reason=%q want %q", r.Blocked, r.Reason, c.blocked)
			}
			for i := 1; i < len(r.Stale); i++ {
				if r.Stale[i-1].Name > r.Stale[i].Name {
					t.Fatal("stale not sorted")
				}
			}
		})
	}
}
