// Package netx builds the HTTP client CredOrbit uses to talk to AWS and offers
// TLS diagnostics.
//
// The central idea: the client trusts the operating system's roots PLUS any
// configured corporate CA certificates. On a TLS-intercepting network (SASE,
// corporate proxy) the corporate CA validates the re-signed chain; off that
// network the public Amazon chain validates against the system roots. One client
// therefore works in both places and nothing ever needs to be toggled.
package netx

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/PiniPP/credorbit/internal/fsutil"
)

// Default timeouts.
const (
	DefaultTimeout      = 30 * time.Second
	dialTimeout         = 10 * time.Second
	tlsHandshakeTimeout = 10 * time.Second
)

// Options configures NewHTTPClient.
type Options struct {
	// CABundle is an explicit PEM bundle appended to the system roots. When empty
	// and AWS_CA_BUNDLE is set, that file is used instead.
	CABundle string
	// ExtraCAs are additional PEM files APPENDED to the system pool.
	ExtraCAs []string
	// Timeout bounds a whole request (connect + headers + body). 0 → 30s.
	Timeout time.Duration
}

// Info describes how a client was configured (for `status` / `doctor`).
type Info struct {
	CASources  []string // PEM files appended to the root pool, in order
	Proxy      string   // proxy from the environment with userinfo redacted; "" = direct
	SystemPool bool     // true when the OS root pool was available as a base
}

// NewHTTPClient returns a client that trusts the system roots plus the
// configured CA files. TLS 1.2 is the minimum, the proxy comes from the
// environment (HTTPS_PROXY/HTTP_PROXY/NO_PROXY) and certificate verification is
// never disabled. A missing, unreadable or certificate-less PEM file is an error
// naming the file, because silently ignoring it would make the client fail later
// with a far less helpful "unknown authority".
func NewHTTPClient(o Options) (*http.Client, Info, error) {
	var info Info
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	} else {
		info.SystemPool = true
	}

	var files []string
	if b := strings.TrimSpace(o.CABundle); b != "" {
		files = append(files, b)
	} else if b := strings.TrimSpace(os.Getenv("AWS_CA_BUNDLE")); b != "" {
		files = append(files, b)
	}
	for _, f := range o.ExtraCAs {
		if f = strings.TrimSpace(f); f != "" {
			files = append(files, f)
		}
	}
	seen := map[string]bool{}
	for _, f := range files {
		path := fsutil.ExpandHome(f)
		if seen[path] {
			continue
		}
		seen[path] = true
		certs, err := loadPEMFile(path, true)
		if err != nil {
			return nil, info, err
		}
		for _, c := range certs {
			pool.AddCert(c.cert)
		}
		info.CASources = append(info.CASources, path)
	}

	timeout := o.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	info.Proxy = proxyFromEnv()
	return &http.Client{Transport: newTransport(pool), Timeout: timeout}, info, nil
}

// newTransport builds a transport with sane timeouts. roots == nil means "use
// the platform verifier / system roots only".
func newTransport(roots *x509.CertPool) *http.Transport {
	d := &net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           d.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   tlsHandshakeTimeout,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    roots,
		},
	}
}

// proxyFromEnv reports the proxy configured for HTTPS traffic with any
// credentials removed. It mirrors the precedence of http.ProxyFromEnvironment.
func proxyFromEnv() string {
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return RedactProxy(v)
		}
	}
	return ""
}

// RedactProxy returns a proxy URL with its userinfo replaced by "***".
func RedactProxy(raw string) string {
	s := raw
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "(unparseable proxy setting)"
	}
	if u.User != nil {
		u.User = url.User("***")
	}
	return u.String()
}

type pemCert struct {
	cert *x509.Certificate // nil when lenient parsing skipped an unparseable block
	der  []byte
}

// loadPEMFile reads every CERTIFICATE block from path. In strict mode an
// unparseable certificate (or a private key) is an error; in lenient mode (used
// for system bundles, which Go's stricter parser may partially reject)
// unparseable certificate blocks are kept verbatim. Either way a file without a
// single parseable certificate is an error.
func loadPEMFile(path string, strict bool) ([]pemCert, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("CA bundle %s: %w", path, err)
	}
	var out []pemCert
	parsed := 0
	rest := data
	for {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		if b.Type != "CERTIFICATE" {
			// A private key in a trust bundle is almost certainly a mistake that
			// exposes a secret; refuse it. Other block types (CRLs, parameters)
			// are irrelevant to trust and skipped.
			if strict && strings.Contains(b.Type, "PRIVATE KEY") {
				return nil, fmt.Errorf("CA bundle %s: contains a %s block; a CA bundle must hold certificates only", path, b.Type)
			}
			continue
		}
		c, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			if strict {
				return nil, fmt.Errorf("CA bundle %s: invalid certificate #%d: %w", path, len(out)+1, err)
			}
			out = append(out, pemCert{der: b.Bytes})
			continue
		}
		parsed++
		out = append(out, pemCert{cert: c, der: b.Bytes})
	}
	if parsed == 0 {
		return nil, fmt.Errorf("CA bundle %s: no PEM certificates found", path)
	}
	return out, nil
}

// SystemBundleCandidates are the well-known locations of the OS PEM bundle, in
// lookup order. Windows and some macOS setups keep roots only in a certificate
// store, so none may exist.
var SystemBundleCandidates = []string{
	"/etc/ssl/cert.pem",                  // macOS, Alpine, BSD
	"/etc/ssl/certs/ca-certificates.crt", // Debian/Ubuntu/Gentoo
	"/etc/pki/tls/certs/ca-bundle.crt",   // Fedora/RHEL/Amazon Linux
	"/etc/ssl/ca-bundle.pem",             // openSUSE
}

// ErrNoSystemBundle is returned by WriteCombinedBundle when no OS PEM bundle
// file exists (always the case on Windows).
var ErrNoSystemBundle = errors.New("no system CA bundle file found")

// WriteCombinedBundle writes dst = first existing system PEM bundle + extras.
// Tools that REPLACE their trust store with AWS_CA_BUNDLE (the AWS CLI, Python
// SDKs, child processes) need the public roots too, or they would break off the
// corporate network. Every extra must parse; the file is written atomically with
// 0600. It returns the number of certificates written.
func WriteCombinedBundle(dst string, extra []string) (int, error) {
	var sys string
	for _, c := range SystemBundleCandidates {
		if st, err := os.Stat(c); err == nil && st.Mode().IsRegular() {
			sys = c
			break
		}
	}
	if sys == "" {
		return 0, fmt.Errorf("%w (checked %s); this platform keeps its roots in a certificate store, so a combined bundle cannot be built — point AWS_CA_BUNDLE at your extra CA file(s) only if every AWS endpoint is reached through the intercepting proxy",
			ErrNoSystemBundle, strings.Join(SystemBundleCandidates, ", "))
	}

	var buf strings.Builder
	seen := map[string]bool{}
	n := 0
	add := func(certs []pemCert) {
		for _, c := range certs {
			k := string(c.der)
			if seen[k] {
				continue
			}
			seen[k] = true
			_ = pem.Encode(&buf, &pem.Block{Type: "CERTIFICATE", Bytes: c.der})
			n++
		}
	}
	certs, err := loadPEMFile(sys, false)
	if err != nil {
		return 0, err
	}
	add(certs)
	for _, e := range extra {
		if e = strings.TrimSpace(e); e == "" {
			continue
		}
		certs, err := loadPEMFile(fsutil.ExpandHome(e), true)
		if err != nil {
			return 0, err
		}
		add(certs)
	}
	if err := fsutil.AtomicWrite(dst, []byte(buf.String()), 0o600); err != nil {
		return 0, fmt.Errorf("write combined CA bundle: %w", err)
	}
	return n, nil
}
