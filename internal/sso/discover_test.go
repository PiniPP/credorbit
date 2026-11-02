package sso

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PiniPP/credorbit/internal/model"
)

// memClient is an in-memory Client.
type memClient struct {
	accounts    []model.Account
	accountsErr error
	roles       map[string][]string
	roleErrs    map[string]error
	block       chan struct{} // if set, ListAccountRoles waits on it or ctx
	inflight    atomic.Int32
	maxInflight atomic.Int32
	calls       atomic.Int32
}

func (m *memClient) ListAccounts(ctx context.Context, tok string) ([]model.Account, error) {
	if tok != testToken {
		return nil, fmt.Errorf("bad token: %w", model.ErrUnauthorized)
	}
	return m.accounts, m.accountsErr
}

func (m *memClient) ListAccountRoles(ctx context.Context, tok, id string) ([]string, error) {
	m.calls.Add(1)
	n := m.inflight.Add(1)
	defer m.inflight.Add(-1)
	for {
		cur := m.maxInflight.Load()
		if n <= cur || m.maxInflight.CompareAndSwap(cur, n) {
			break
		}
	}
	if m.block != nil {
		select {
		case <-m.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	} else {
		time.Sleep(time.Millisecond)
	}
	if err := m.roleErrs[id]; err != nil {
		return nil, err
	}
	return append([]string(nil), m.roles[id]...), nil
}

func (m *memClient) GetRoleCredentials(context.Context, string, string, string) (model.Credentials, error) {
	return model.Credentials{}, errors.New("unused")
}

type recorder struct {
	mu     sync.Mutex
	events []model.Event
}

func (r *recorder) Report(e model.Event) {
	r.mu.Lock()
	r.events = append(r.events, e)
	r.mu.Unlock()
}

func (r *recorder) kinds(k model.EventKind) []model.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []model.Event
	for _, e := range r.events {
		if e.Kind == k {
			out = append(out, e)
		}
	}
	return out
}

func manyAccounts(n int) *memClient {
	m := &memClient{roles: map[string][]string{}, roleErrs: map[string]error{}}
	for i := range n {
		id := fmt.Sprintf("2000000000%02d", i)
		m.accounts = append(m.accounts, model.Account{ID: id, Name: fmt.Sprintf("acct-%02d", (i*7)%n)})
		m.roles[id] = []string{"ViewOnly", "Admin", "Admin"}
	}
	return m
}

func TestDiscover(t *testing.T) {
	m := manyAccounts(20)
	m.roleErrs[m.accounts[3].ID] = fmt.Errorf("sso ListAccountRoles: %w", model.ErrNoAccess)
	m.roleErrs[m.accounts[5].ID] = fmt.Errorf("sso ListAccountRoles: %w: 503", model.ErrTransient)
	rec := &recorder{}

	d := Discover(context.Background(), m, model.Token{AccessToken: testToken}, testSession, 4, rec)
	if !d.Complete || d.Err != "" || d.Session != "corp" || d.StartURL != testStartURL || d.At.IsZero() {
		t.Fatalf("discovery = %+v", d)
	}
	if len(d.Accounts) != 20 {
		t.Fatalf("accounts = %d", len(d.Accounts))
	}
	if got := m.maxInflight.Load(); got > 4 {
		t.Errorf("max in-flight = %d, want <= 4", got)
	}
	for i := 1; i < len(d.Accounts); i++ {
		if d.Accounts[i-1].Name > d.Accounts[i].Name {
			t.Fatal("accounts not sorted")
		}
	}
	by := d.ByID()
	if a := by[m.accounts[0].ID]; strings.Join(a.Roles, ",") != "Admin,ViewOnly" || a.RolesErr != "" {
		t.Errorf("roles = %+v", a)
	}
	if a := by[m.accounts[3].ID]; a.Roles == nil || len(a.Roles) != 0 || a.RolesErr != "no access" {
		t.Errorf("no-access account = %+v", a)
	}
	if a := by[m.accounts[5].ID]; !strings.Contains(a.RolesErr, "transient") {
		t.Errorf("transient account = %+v", a)
	}
	if n := len(rec.kinds(model.EventItemFailed)); n != 2 {
		t.Errorf("failed events = %d", n)
	}
	prog := rec.kinds(model.EventProgress)
	last := prog[len(prog)-1]
	if last.Done != 20 || last.Total != 20 || last.Step != "discover" {
		t.Errorf("last progress = %+v", last)
	}
	for i := 1; i < len(prog); i++ {
		if prog[i].Done < prog[i-1].Done {
			t.Fatal("progress not monotonic")
		}
	}
	for _, e := range rec.events {
		if strings.Contains(e.Message, testToken) {
			t.Errorf("event leaks token: %+v", e)
		}
	}
}

func TestDiscoverListFailure(t *testing.T) {
	m := manyAccounts(3)
	m.accountsErr = fmt.Errorf("sso ListAccounts: %w", model.ErrTransient)
	rec := &recorder{}
	d := Discover(context.Background(), m, model.Token{AccessToken: testToken}, testSession, 0, rec)
	if d.Complete || d.Err == "" || len(d.Accounts) != 0 || m.calls.Load() != 0 {
		t.Errorf("discovery = %+v", d)
	}
	if len(rec.kinds(model.EventItemFailed)) != 1 {
		t.Error("no failure event")
	}
	// nil reporter is allowed.
	_ = Discover(context.Background(), m, model.Token{AccessToken: testToken}, testSession, 0, nil)
}

func TestDiscoverCancel(t *testing.T) {
	before := runtime.NumGoroutine()
	m := manyAccounts(50)
	m.block = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for m.calls.Load() < 2 {
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()
	done := make(chan *model.Discovery)
	go func() { done <- Discover(ctx, m, model.Token{AccessToken: testToken}, testSession, 2, nil) }()
	var d *model.Discovery
	select {
	case d = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Discover did not return after cancel")
	}
	if len(d.Accounts) != 50 {
		t.Fatalf("accounts = %d", len(d.Accounts))
	}
	unknown := 0
	for _, a := range d.Accounts {
		if a.RolesErr != "" {
			unknown++
		}
	}
	if unknown != 50 {
		t.Errorf("accounts with unknown roles = %d, want 50", unknown)
	}
	if c := m.calls.Load(); c > 4 {
		t.Errorf("ListAccountRoles calls after cancel = %d", c)
	}
	// Allow scheduler a moment, then check no worker goroutines leaked.
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before+1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if g := runtime.NumGoroutine(); g > before+1 {
		t.Errorf("goroutines: before %d after %d", before, g)
	}
}

// End to end against the fake portal over HTTP with the real SDK client.
func TestDiscoverWithPortal(t *testing.T) {
	f := newFakePortal(7)
	f.accounts = append(f.accounts, model.Account{ID: "999999999403", Name: "zz-revoked"})
	f.delay = 2 * time.Millisecond
	_, p := startPortal(t, f)
	d := Discover(context.Background(), p, model.Token{AccessToken: testToken}, testSession, 3, nil)
	if !d.Complete || len(d.Accounts) != 8 {
		t.Fatalf("discovery = %+v", d)
	}
	if d.Accounts[0].Name != "acct-01" || strings.Join(d.Accounts[0].Roles, ",") != "Admin,PowerUser,ReadOnly" {
		t.Errorf("first = %+v", d.Accounts[0])
	}
	if last := d.Accounts[7]; last.RolesErr != "no access" || len(last.Roles) != 0 {
		t.Errorf("revoked = %+v", last)
	}
	if got := f.maxSeen.Load(); got > 3 {
		t.Errorf("server saw %d concurrent requests, want <= 3", got)
	}

	bad := Discover(context.Background(), p, model.Token{AccessToken: "wrong"}, testSession, 3, nil)
	if bad.Complete || !strings.Contains(bad.Err, "expired or invalid") {
		t.Errorf("unauthorized discovery = %+v", bad)
	}
}
