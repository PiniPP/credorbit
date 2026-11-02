package netx

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
)

// Error classes reported in ProbeResult.ErrClass.
const (
	ClassUnknownAuthority = "unknown_authority"
	ClassProxy            = "proxy"
	ClassDNS              = "dns"
	ClassTimeout          = "timeout"
	ClassTLS              = "tls"
	ClassHTTP             = "http" // any other transport failure (refused, reset, bad URL, ...)
)

// ProbeResult is the outcome of one diagnostic HTTPS request.
type ProbeResult struct {
	URL         string
	OK          bool // a TLS session was established and an HTTP response received (any status)
	Status      int  // HTTP status code when OK
	Intercepted bool // the chain's root is not a well-known public CA (see isPublicCA)
	Issuer      string
	Subject     string
	// ChainSubjects lists the subjects of the certificates the server sent, leaf first.
	ChainSubjects []string
	ErrClass      string // "", "unknown_authority", "proxy", "dns", "timeout", "tls", "http"
	Err           string
	Advice        string
}

// publicCAMarkers are case-insensitive substrings of the root's organization or
// common name that identify the public CAs AWS endpoints chain to (Amazon Trust
// Services plus its cross-signers) and common public CAs. Anything else in the
// root position almost certainly means a TLS-inspecting proxy re-signed the
// chain. The heuristic is deliberately simple: it only informs diagnostics and
// never affects trust decisions, which are made by normal chain verification.
var publicCAMarkers = []string{
	"amazon", "digicert", "starfield", "globalsign",
	"let's encrypt", "isrg", "internet security research group", "sectigo",
}

// Probe GETs url with c and classifies the result. Any HTTP status counts as a
// TLS success. A nil client probes with system roots only.
func Probe(ctx context.Context, c *http.Client, url string) ProbeResult {
	if c == nil {
		return ProbeWithSystemOnly(ctx, url)
	}
	res := ProbeResult{URL: url}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		res.ErrClass, res.Err, res.Advice = ClassHTTP, err.Error(), "check the probe URL (network.probe_url)"
		return res
	}
	req.Header.Set("User-Agent", "credorbit-probe")

	resp, err := c.Do(req)
	if err != nil {
		res.ErrClass, res.Advice = classify(ctx, err, usesProxy(c, req))
		res.Err = err.Error()
		return res
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))

	res.OK = true
	res.Status = resp.StatusCode
	if resp.TLS != nil {
		fillChain(&res, resp.TLS)
	}
	return res
}

// ProbeWithSystemOnly probes with a client that trusts only the OS roots (still
// honoring the proxy environment). Comparing it with Probe on the configured
// client detects interception: system-only fails with unknown_authority while
// the configured client succeeds.
func ProbeWithSystemOnly(ctx context.Context, url string) ProbeResult {
	tr := newTransport(nil)
	defer tr.CloseIdleConnections()
	return Probe(ctx, &http.Client{Transport: tr, Timeout: DefaultTimeout}, url)
}

func fillChain(res *ProbeResult, cs *tls.ConnectionState) {
	certs := cs.PeerCertificates
	if len(certs) == 0 {
		return
	}
	leaf := certs[0]
	res.Subject = leaf.Subject.String()
	res.Issuer = leaf.Issuer.String()
	for _, c := range certs {
		res.ChainSubjects = append(res.ChainSubjects, c.Subject.String())
	}
	// The root is identified by the issuer of the top-most certificate in the
	// verified chain (falls back to what the server sent). For a self-signed
	// root issuer == subject, for an omitted root it names the root.
	top := certs[len(certs)-1]
	if len(cs.VerifiedChains) > 0 && len(cs.VerifiedChains[0]) > 0 {
		vc := cs.VerifiedChains[0]
		top = vc[len(vc)-1]
	}
	res.Intercepted = !isPublicCA(top)
}

func isPublicCA(c *x509.Certificate) bool {
	names := append(append([]string{}, c.Issuer.Organization...), c.Issuer.CommonName)
	for _, n := range names {
		n = strings.ToLower(n)
		for _, m := range publicCAMarkers {
			if strings.Contains(n, m) {
				return true
			}
		}
	}
	return false
}

func usesProxy(c *http.Client, req *http.Request) bool {
	tr, ok := c.Transport.(*http.Transport)
	if !ok || tr.Proxy == nil {
		return false
	}
	u, err := tr.Proxy(req)
	return err == nil && u != nil
}

// classify maps a transport error to an error class and user advice.
func classify(ctx context.Context, err error, viaProxy bool) (string, string) {
	var ua x509.UnknownAuthorityError
	if errors.As(err, &ua) {
		return ClassUnknownAuthority, "the server certificate is signed by an unknown CA — on a TLS-intercepting network configure network.ca_bundle (or network.extra_cas) with your corporate root CA PEM"
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "proxyconnect" {
		return ClassProxy, "cannot reach the proxy from HTTPS_PROXY/HTTP_PROXY — check the proxy address or set NO_PROXY"
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return ClassDNS, "the host name could not be resolved — check DNS/VPN, or set HTTPS_PROXY if this network requires a proxy"
	}
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || ctx.Err() == context.DeadlineExceeded || (errors.As(err, &ne) && ne.Timeout()) {
		return ClassTimeout, "the connection timed out — a firewall may be dropping traffic; check VPN/proxy settings"
	}
	if isTLSError(err) {
		return ClassTLS, "the TLS handshake failed — check that the URL is HTTPS and that no middlebox is downgrading TLS (minimum TLS 1.2)"
	}
	if viaProxy {
		return ClassProxy, "the proxy refused the CONNECT request — check proxy credentials/allow-lists or set NO_PROXY for this host"
	}
	return ClassHTTP, "the request failed before an HTTP response was received — check connectivity"
}

func isTLSError(err error) bool {
	var (
		cve *tls.CertificateVerificationError
		rhe tls.RecordHeaderError
		ae  tls.AlertError
		he  x509.HostnameError
		cie x509.CertificateInvalidError
	)
	switch {
	case errors.As(err, &cve), errors.As(err, &rhe), errors.As(err, &ae),
		errors.As(err, &he), errors.As(err, &cie):
		return true
	}
	s := err.Error()
	return strings.Contains(s, "tls:") || strings.Contains(s, "x509:") ||
		strings.Contains(s, "server gave HTTP response to HTTPS client")
}
