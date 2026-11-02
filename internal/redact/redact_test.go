package redact

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestString(t *testing.T) {
	long := strings.Repeat("Ab3+", 60)
	cases := map[string]string{
		"key ASIAABCDEFGHIJKLMNOP used":                                "key [REDACTED] used",
		"aws_secret_access_key = wJalrXUtnFEMI/K7":                     "aws_secret_access_key = [REDACTED]",
		`{"accessToken":"eyJabc.def","x":1}`:                           `{"accessToken":"[REDACTED]","x":1}`,
		"Authorization: Bearer abc.def-ghi":                            "Authorization: [REDACTED]",
		"token " + long:                                                "token [REDACTED]",
		"-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----": "[REDACTED]",
		"nothing secret here":                                          "nothing secret here",
	}
	for in, want := range cases {
		if got := String(in); got != want {
			t.Errorf("String(%q)\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestHandler(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(NewHandler(slog.NewTextHandler(&buf, nil)))
	log.Info("fetched ASIAABCDEFGHIJKLMNOP", "session_token", "zzz", "note", "refresh_token=abc", "err", errors.New("clientSecret: s3cr3t"),
		slog.Group("g", "token", "t0k"))
	log.With("access_token", "x").Info("with")
	out := buf.String()
	for _, leak := range []string{"ASIAABCDEFGHIJKLMNOP", "zzz", "abc", "s3cr3t", "t0k", "access_token=x"} {
		if strings.Contains(out, leak) {
			t.Errorf("leaked %q in %s", leak, out)
		}
	}
}
