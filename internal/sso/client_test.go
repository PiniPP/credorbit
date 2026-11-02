package sso

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PiniPP/credorbit/internal/model"
)

const testToken = "tok-SECRET-0123456789"

// fakePortal emulates the SSO portal REST API.
type fakePortal struct {
	mu       sync.Mutex
	accounts []model.Account
	roles    map[string][]string
	pageSize int
	// failures: account ID → behaviour for /assignment/roles
	attempts map[string]int
	inflight atomic.Int32
	maxSeen  atomic.Int32
	delay    time.Duration
	logouts  atomic.Int32
}

func newFakePortal(nAccounts int) *fakePortal {
	f := &fakePortal{roles: map[string][]string{}, pageSize: 2, attempts: map[string]int{}}
	for i := range nAccounts {
		id := fmt.Sprintf("1000000000%02d", i)
		// Names in reverse order to verify sorting.
		f.accounts = append(f.accounts, model.Account{ID: id, Name: fmt.Sprintf("acct-%02d", nAccounts-i), Email: id + "@example.com"})
		f.roles[id] = []string{"ReadOnly", "Admin", "PowerUser"}
	}
	return f
}

func writeAPIError(w http.ResponseWriter, status int, code, msg string, headerForm bool) {
	w.Header().Set("Content-Type", "application/json")
	if headerForm {
		// Shape used by the live service for unmodeled errors.
		w.Header().Set("x-amzn-ErrorType", code+":http://internal.amazon.com/coral/com.amazonaws.switchboard.portal/")
		w.WriteHeader(status)
		fmt.Fprintf(w, `{"message":%q}`, msg)
		return
	}
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"__type":%q,"message":%q}`, code, msg)
}

func page[T any](items []T, next string, size int) ([]T, string) {
	start, _ := strconv.Atoi(next)
	end := min(start+size, len(items))
	n := ""
	if end < len(items) {
		n = strconv.Itoa(end)
	}
	return items[start:end], n
}

func (f *fakePortal) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cur := f.inflight.Add(1)
	defer f.inflight.Add(-1)
	for {
		m := f.maxSeen.Load()
		if cur <= m || f.maxSeen.CompareAndSwap(m, cur) {
			break
		}
	}
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	if r.Header.Get("x-amz-sso_bearer_token") != testToken {
		writeAPIError(w, 401, "UnauthorizedException", "Session token not found or invalid", false)
		return
	}
	q := r.URL.Query()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/assignment/accounts":
		items, next := page(f.accounts, q.Get("next_token"), f.pageSize)
		list := []map[string]string{}
		for _, a := range items {
			list = append(list, map[string]string{"accountId": a.ID, "accountName": a.Name, "emailAddress": a.Email})
		}
		writeJSONResp(w, map[string]any{"accountList": list, "nextToken": nullable(next)})
	case r.Method == http.MethodGet && r.URL.Path == "/assignment/roles":
		id := q.Get("account_id")
		f.mu.Lock()
		f.attempts[id]++
		n := f.attempts[id]
		f.mu.Unlock()
		switch {
		case strings.HasSuffix(id, "403"):
			writeAPIError(w, 403, "ForbiddenException", "No access", true)
			return
		case strings.HasSuffix(id, "500"):
			writeAPIError(w, 500, "InternalServerException", "boom", false)
			return
		case strings.HasSuffix(id, "429") && n <= 2:
			writeAPIError(w, 429, "TooManyRequestsException", "slow down", false)
			return
		case strings.HasSuffix(id, "404"):
			writeAPIError(w, 404, "ResourceNotFoundException", "nope", false)
			return
		}
		roles, ok := f.roles[id]
		if !ok {
			roles = []string{"Admin"}
		}
		items, next := page(roles, q.Get("next_token"), f.pageSize)
		list := []map[string]string{}
		for _, rn := range items {
			list = append(list, map[string]string{"accountId": id, "roleName": rn})
		}
		writeJSONResp(w, map[string]any{"roleList": list, "nextToken": nullable(next)})
	case r.Method == http.MethodGet && r.URL.Path == "/federation/credentials":
		if q.Get("role_name") == "Denied" {
			writeAPIError(w, 403, "ForbiddenException", "No access", true)
			return
		}
		writeJSONResp(w, map[string]any{"roleCredentials": map[string]any{
			"accessKeyId": "ASIAEXAMPLEEXAMPLE", "secretAccessKey": "secret", "sessionToken": "session",
			"expiration": int64(1793664000000),
		}})
	case r.Method == http.MethodPost && r.URL.Path == "/logout":
		f.logouts.Add(1)
		w.WriteHeader(http.StatusOK)
	default:
		http.NotFound(w, r)
	}
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func writeJSONResp(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func startPortal(t *testing.T, f *fakePortal) (*httptest.Server, *portal) {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return srv, newPortal("eu-west-1", srv.Client(), srv.URL, 2*time.Millisecond)
}

func TestPortalPagination(t *testing.T) {
	f := newFakePortal(5)
	_, p := startPortal(t, f)
	ctx := context.Background()

	accts, err := p.ListAccounts(ctx, testToken)
	if err != nil {
		t.Fatal(err)
	}
	if len(accts) != 5 || accts[0].ID != f.accounts[0].ID || accts[4].Email != f.accounts[4].Email {
		t.Fatalf("accounts = %+v", accts)
	}
	roles, err := p.ListAccountRoles(ctx, testToken, accts[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(roles, ",") != "Admin,PowerUser,ReadOnly" {
		t.Errorf("roles = %v", roles)
	}
	c, err := p.GetRoleCredentials(ctx, testToken, accts[0].ID, "Admin")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Valid(time.Unix(0, 0)) || !c.Expiration.Equal(time.UnixMilli(1793664000000)) {
		t.Errorf("creds = %v", c)
	}
}

func TestPortalErrorMapping(t *testing.T) {
	f := newFakePortal(1)
	srv, p := startPortal(t, f)
	ctx := context.Background()

	canceled, cancel := context.WithCancel(ctx)
	cancel()

	tests := []struct {
		name     string
		call     func() error
		want     error
		attempts int // expected server attempts for the account (0 = don't check)
		acct     string
	}{
		{"forbidden is no access, not retried", func() error {
			_, err := p.ListAccountRoles(ctx, testToken, "000000000403")
			return err
		}, model.ErrNoAccess, 1, "000000000403"},
		{"forbidden role credentials", func() error {
			_, err := p.GetRoleCredentials(ctx, testToken, "1", "Denied")
			return err
		}, model.ErrNoAccess, 0, ""},
		{"unauthorized", func() error {
			_, err := p.ListAccounts(ctx, "wrong-"+testToken)
			return err
		}, model.ErrUnauthorized, 0, ""},
		{"5xx retried then transient", func() error {
			_, err := p.ListAccountRoles(ctx, testToken, "000000000500")
			return err
		}, model.ErrTransient, maxAttempts, "000000000500"},
		{"not found", func() error {
			_, err := p.ListAccountRoles(ctx, testToken, "000000000404")
			return err
		}, model.ErrNotFound, 0, ""},
		{"canceled passes through", func() error {
			_, err := p.ListAccounts(canceled, testToken)
			return err
		}, context.Canceled, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call()
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if tt.want == context.Canceled && errors.Is(err, model.ErrTransient) {
				t.Errorf("cancellation classified as transient: %v", err)
			}
			if strings.Contains(err.Error(), testToken) {
				t.Errorf("error leaks token: %v", err)
			}
			if tt.attempts > 0 {
				f.mu.Lock()
				got := f.attempts[tt.acct]
				f.mu.Unlock()
				if got != tt.attempts {
					t.Errorf("attempts = %d, want %d", got, tt.attempts)
				}
			}
		})
	}

	// Throttling is retried transparently.
	roles, err := p.ListAccountRoles(ctx, testToken, "000000000429")
	if err != nil || len(roles) != 1 {
		t.Errorf("throttled call: roles=%v err=%v", roles, err)
	}

	// Network failure → transient.
	srv.Close()
	_, err = p.ListAccounts(ctx, testToken)
	if !errors.Is(err, model.ErrTransient) || strings.Contains(err.Error(), testToken) {
		t.Errorf("network error = %v", err)
	}
}

func TestNewClientConstructors(t *testing.T) {
	f := newFakePortal(3)
	srv := httptest.NewServer(f)
	defer srv.Close()
	c := NewClientWithEndpoint("us-east-1", srv.Client(), srv.URL+"/")
	accts, err := c.ListAccounts(context.Background(), testToken)
	if err != nil || len(accts) != 3 {
		t.Fatalf("accts=%v err=%v", accts, err)
	}
	if NewClient("us-east-1", nil) == nil {
		t.Fatal("NewClient returned nil")
	}
}
