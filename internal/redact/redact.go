// Package redact scrubs secrets from strings and provides a slog handler that
// applies the scrubbing to every log record.
package redact

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
)

var patterns = []*regexp.Regexp{
	// AWS access key IDs (AKIA = long-term, ASIA = temporary)
	regexp.MustCompile(`\b(AKIA|ASIA)[A-Z0-9]{12,}\b`),
	// key = value / "key": "value" for secret-ish keys
	regexp.MustCompile(`(?i)("?(?:aws_secret_access_key|aws_session_token|secret_?access_?key|session_?token|access_?token|refresh_?token|client_?secret|accessToken|refreshToken|clientSecret|password|authorization)"?\s*[:=]\s*"?)([^"\s,}]+)`),
	// Bearer tokens
	regexp.MustCompile(`(?i)(bearer\s+)([A-Za-z0-9\-._~+/]+=*)`),
	// Long base64-ish blobs (session tokens are hundreds of chars)
	regexp.MustCompile(`[A-Za-z0-9+/]{120,}={0,2}`),
	// PEM blocks
	regexp.MustCompile(`(?s)-----BEGIN [A-Z ]+-----.*?-----END [A-Z ]+-----`),
}

// String returns s with secrets replaced by "[REDACTED]".
func String(s string) string {
	for i, re := range patterns {
		switch i {
		case 1, 2:
			s = re.ReplaceAllString(s, "${1}[REDACTED]")
		default:
			s = re.ReplaceAllString(s, "[REDACTED]")
		}
	}
	return s
}

// SecretKeys are attribute keys whose values are always dropped.
var SecretKeys = map[string]bool{
	"secret": true, "secret_access_key": true, "session_token": true, "access_token": true,
	"refresh_token": true, "client_secret": true, "token": true, "password": true, "credentials": true,
}

// Handler wraps a slog.Handler and redacts message text and string attributes.
type Handler struct{ inner slog.Handler }

// NewHandler wraps h.
func NewHandler(h slog.Handler) *Handler { return &Handler{inner: h} }

func (h *Handler) Enabled(ctx context.Context, l slog.Level) bool { return h.inner.Enabled(ctx, l) }

func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	nr := slog.NewRecord(r.Time, r.Level, String(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		nr.AddAttrs(redactAttr(a))
		return true
	})
	return h.inner.Handle(ctx, nr)
}

func (h *Handler) WithAttrs(as []slog.Attr) slog.Handler {
	out := make([]slog.Attr, len(as))
	for i, a := range as {
		out[i] = redactAttr(a)
	}
	return &Handler{inner: h.inner.WithAttrs(out)}
}

func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{inner: h.inner.WithGroup(name)}
}

func redactAttr(a slog.Attr) slog.Attr {
	if SecretKeys[strings.ToLower(a.Key)] {
		return slog.String(a.Key, "[REDACTED]")
	}
	switch a.Value.Kind() {
	case slog.KindString:
		return slog.String(a.Key, String(a.Value.String()))
	case slog.KindGroup:
		g := a.Value.Group()
		out := make([]any, 0, len(g))
		for _, x := range g {
			out = append(out, redactAttr(x))
		}
		return slog.Group(a.Key, out...)
	case slog.KindAny:
		if err, ok := a.Value.Any().(error); ok {
			return slog.String(a.Key, String(err.Error()))
		}
		return slog.String(a.Key, String(a.Value.String()))
	}
	return a
}
