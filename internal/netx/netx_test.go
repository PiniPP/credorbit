package netx

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// writeServerCA writes the httptest server's certificate (self-signed, acts as
// its own CA) to a PEM file.
func writeServerCA(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "server-ca.pem")
	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func genCAPEM(t *testing.T, org string) []byte {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{Organization: []string{org}, CommonName: org + " Root"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func writeFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func newTeapot() *httptest.Server {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0) // expected handshake failures
	srv.StartTLS()
	return srv
}

func clearEnv(t *testing.T) {
	for _, k := range []string{"AWS_CA_BUNDLE", "HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
}

func TestNewHTTPClientTrust(t *testing.T) {
	clearEnv(t)
	srv := newTeapot()
	defer srv.Close()
	ca := writeServerCA(t, srv)
	other := writeFile(t, "other.pem", genCAPEM(t, "Other"))

	tests := []struct {
		name    string
		opts    Options
		env     string
		wantOK  bool
		sources int
	}{
		{"no CA rejects", Options{}, "", false, 0},
		{"unrelated CA rejects", Options{CABundle: other}, "", false, 1},
		{"explicit bundle accepts", Options{CABundle: ca}, "", true, 1},
		{"AWS_CA_BUNDLE honored", Options{}, ca, true, 1},
		{"explicit bundle wins over env", Options{CABundle: other}, ca, false, 1},
		{"extra CA appended", Options{CABundle: other, ExtraCAs: []string{ca}}, "", true, 2},
		{"duplicate files deduped", Options{CABundle: ca, ExtraCAs: []string{ca, " "}}, "", true, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AWS_CA_BUNDLE", tt.env)
			c, info, err := NewHTTPClient(tt.opts)
			if err != nil {
				t.Fatalf("NewHTTPClient: %v", err)
			}
			if len(info.CASources) != tt.sources {
				t.Errorf("CASources = %v, want %d entries", info.CASources, tt.sources)
			}
			tr := c.Transport.(*http.Transport)
			if tr.TLSClientConfig.InsecureSkipVerify || tr.TLSClientConfig.MinVersion < 0x0303 {
				t.Fatalf("insecure TLS config: %+v", tr.TLSClientConfig)
			}
			if c.Timeout != DefaultTimeout {
				t.Errorf("Timeout = %v", c.Timeout)
			}
			resp, err := c.Get(srv.URL)
			if tt.wantOK {
				if err != nil {
					t.Fatalf("GET: %v", err)
				}
				resp.Body.Close()
			} else if err == nil {
				resp.Body.Close()
				t.Fatal("GET succeeded without trusting the server CA")
			}
		})
	}
}

func TestNewHTTPClientBadPEM(t *testing.T) {
	clearEnv(t)
	key := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: []byte{1, 2, 3}})
	badCert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("nope")})
	missing := filepath.Join(t.TempDir(), "missing.pem")
	tests := []struct {
		name string
		path string
		env  bool
	}{
		{"missing", missing, false},
		{"missing via env", missing, true},
		{"empty", writeFile(t, "empty.pem", nil), false},
		{"garbage", writeFile(t, "garbage.pem", []byte("hello world")), false},
		{"private key", writeFile(t, "key.pem", append(genCAPEM(t, "X"), key...)), false},
		{"invalid cert", writeFile(t, "bad.pem", badCert), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := Options{ExtraCAs: []string{tt.path}}
			if tt.env {
				o = Options{}
				t.Setenv("AWS_CA_BUNDLE", tt.path)
			}
			_, _, err := NewHTTPClient(o)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.path) {
				t.Errorf("error %q does not name file %s", err, tt.path)
			}
			if strings.Contains(err.Error(), "BEGIN") {
				t.Errorf("error leaks PEM content: %q", err)
			}
		})
	}
}

func TestInfoProxyRedacted(t *testing.T) {
	clearEnv(t)
	tests := []struct{ env, want string }{
		{"http://user:s3cret@proxy.example:8080", "http://%2A%2A%2A@proxy.example:8080"},
		{"proxy.example:3128", "http://proxy.example:3128"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Setenv("HTTPS_PROXY", tt.env)
		_, info, err := NewHTTPClient(Options{Timeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(info.Proxy, "s3cret") || strings.Contains(info.Proxy, "user") {
			t.Errorf("proxy not redacted: %q", info.Proxy)
		}
		if u, _ := url.PathUnescape(info.Proxy); u != mustUnescape(tt.want) {
			t.Errorf("Proxy = %q, want %q", info.Proxy, tt.want)
		}
	}
}

func mustUnescape(s string) string { u, _ := url.PathUnescape(s); return u }

func TestProbeOK(t *testing.T) {
	clearEnv(t)
	srv := newTeapot()
	defer srv.Close()
	c, _, err := NewHTTPClient(Options{CABundle: writeServerCA(t, srv)})
	if err != nil {
		t.Fatal(err)
	}
	r := Probe(context.Background(), c, srv.URL)
	if !r.OK || r.ErrClass != "" || r.Status != http.StatusTeapot {
		t.Fatalf("probe = %+v", r)
	}
	// httptest's certificate is issued by "Acme Co" — not a public CA.
	if !r.Intercepted || !strings.Contains(r.Issuer, "Acme Co") || len(r.ChainSubjects) == 0 {
		t.Errorf("chain info = %+v", r)
	}

	sys := ProbeWithSystemOnly(context.Background(), srv.URL)
	if sys.OK || sys.ErrClass != ClassUnknownAuthority || !strings.Contains(sys.Advice, "network.ca_bundle") {
		t.Errorf("system-only probe = %+v", sys)
	}
}

func TestIsPublicCA(t *testing.T) {
	tests := []struct {
		org, cn string
		want    bool
	}{
		{"Amazon", "Amazon Root CA 1", true},
		{"DigiCert Inc", "DigiCert Global Root G2", true},
		{"Starfield Technologies, Inc.", "", true},
		{"Internet Security Research Group", "ISRG Root X1", true},
		{"Corp Inc", "Corp SASE Inspection CA", false},
		{"", "", false},
	}
	for _, tt := range tests {
		c := &x509.Certificate{Issuer: pkix.Name{CommonName: tt.cn}}
		if tt.org != "" {
			c.Issuer.Organization = []string{tt.org}
		}
		if got := isPublicCA(c); got != tt.want {
			t.Errorf("isPublicCA(%q,%q) = %v", tt.org, tt.cn, got)
		}
	}
}

func clientWithDial(dial func(ctx context.Context, network, addr string) (net.Conn, error), timeout time.Duration) *http.Client {
	tr := newTransport(nil)
	tr.Proxy = nil
	tr.DialContext = dial
	return &http.Client{Transport: tr, Timeout: timeout}
}

func TestProbeClassification(t *testing.T) {
	clearEnv(t)
	plain := httptest.NewServer(http.NotFoundHandler())
	defer plain.Close()

	// A proxy that refuses every CONNECT.
	denyProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "denied", http.StatusForbidden)
	}))
	defer denyProxy.Close()
	denyURL, _ := url.Parse(denyProxy.URL)

	// A port with nothing listening.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadAddr := l.Addr().String()
	l.Close()

	withProxy := func(p *url.URL) *http.Client {
		tr := newTransport(nil)
		tr.Proxy = http.ProxyURL(p)
		return &http.Client{Transport: tr, Timeout: 5 * time.Second}
	}

	tests := []struct {
		name   string
		client *http.Client
		url    string
		want   string
	}{
		{"dns", clientWithDial(func(ctx context.Context, n, a string) (net.Conn, error) {
			return nil, &net.OpError{Op: "dial", Net: n, Err: &net.DNSError{Err: "no such host", Name: "x.invalid", IsNotFound: true}}
		}, 5*time.Second), "https://x.invalid/", ClassDNS},
		{"timeout", clientWithDial(func(ctx context.Context, n, a string) (net.Conn, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}, 100*time.Millisecond), "https://slow.invalid/", ClassTimeout},
		{"tls on plain http", withProxy(nil), strings.Replace(plain.URL, "http://", "https://", 1), ClassTLS},
		{"proxy unreachable", withProxy(&url.URL{Scheme: "http", Host: deadAddr}), "https://portal.example/", ClassProxy},
		{"proxy refuses CONNECT", withProxy(denyURL), "https://portal.example/", ClassProxy},
		{"refused", withProxy(nil), "https://" + deadAddr + "/", ClassHTTP},
		{"bad url", withProxy(nil), "://bad", ClassHTTP},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Probe(context.Background(), tt.client, tt.url)
			if r.OK || r.ErrClass != tt.want || r.Advice == "" || r.Err == "" {
				t.Errorf("probe = %+v, want class %s", r, tt.want)
			}
		})
	}
}

func TestWriteCombinedBundle(t *testing.T) {
	sysPEM := append(genCAPEM(t, "SysRootA"), genCAPEM(t, "SysRootB")...)
	sys := writeFile(t, "system.pem", sysPEM)
	extra := writeFile(t, "corp.pem", genCAPEM(t, "Corp"))
	old := SystemBundleCandidates
	t.Cleanup(func() { SystemBundleCandidates = old })

	SystemBundleCandidates = []string{filepath.Join(t.TempDir(), "absent.pem"), sys}
	dst := filepath.Join(t.TempDir(), "sub", "combined.pem")
	n, err := WriteCombinedBundle(dst, []string{extra, sys /* duplicates skipped */})
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("n = %d, want 3", n)
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), "BEGIN CERTIFICATE"); got != 3 {
		t.Errorf("bundle has %d certs", got)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(dst)
		if st.Mode().Perm() != 0o600 {
			t.Errorf("perm = %v", st.Mode().Perm())
		}
	}
	// The combined bundle is itself a valid CA bundle.
	if _, _, err := NewHTTPClient(Options{CABundle: dst}); err != nil {
		t.Errorf("combined bundle unusable: %v", err)
	}

	bad := writeFile(t, "bad.pem", []byte("junk"))
	if _, err := WriteCombinedBundle(filepath.Join(t.TempDir(), "x.pem"), []string{bad}); err == nil || !strings.Contains(err.Error(), bad) {
		t.Errorf("bad extra: err = %v", err)
	}

	SystemBundleCandidates = []string{filepath.Join(t.TempDir(), "absent.pem")}
	if _, err := WriteCombinedBundle(filepath.Join(t.TempDir(), "y.pem"), []string{extra}); !errors.Is(err, ErrNoSystemBundle) {
		t.Errorf("no system bundle: err = %v", err)
	}
}
