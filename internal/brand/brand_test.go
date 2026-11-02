package brand

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

func envMap(m map[string]string) lookupFunc {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func TestSupportsUnicode(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"empty env", map[string]string{}, false},
		{"LANG utf-8", map[string]string{"LANG": "en_US.UTF-8"}, true},
		{"LANG utf8 lower", map[string]string{"LANG": "C.utf8"}, true},
		{"LANG non-utf", map[string]string{"LANG": "en_US.ISO-8859-1"}, false},
		{"LC_ALL overrides LANG", map[string]string{"LC_ALL": "C", "LANG": "en_US.UTF-8"}, false},
		{"LC_CTYPE overrides LANG", map[string]string{"LC_CTYPE": "UTF-8", "LANG": "C"}, true},
		{"LC_ALL wins over LC_CTYPE", map[string]string{"LC_ALL": "en_GB.UTF-8", "LC_CTYPE": "C"}, true},
		{"empty LC_ALL ignored", map[string]string{"LC_ALL": "", "LANG": "en_US.UTF-8"}, true},
		{"windows terminal", map[string]string{"WT_SESSION": "abc"}, true},
		{"term dumb", map[string]string{"TERM": "dumb", "LANG": "en_US.UTF-8"}, false},
		{"term dumb beats WT", map[string]string{"TERM": "dumb", "WT_SESSION": "x"}, false},
		{"ascii forced", map[string]string{"CREDORBIT_ASCII": "1", "LANG": "en_US.UTF-8"}, false},
		{"ascii forced beats WT", map[string]string{"CREDORBIT_ASCII": "true", "WT_SESSION": "x"}, false},
		{"ascii=0 not forced", map[string]string{"CREDORBIT_ASCII": "0", "LANG": "en_US.UTF-8"}, true},
		{"xterm utf8", map[string]string{"TERM": "xterm-256color", "LANG": "de_DE.UTF-8"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := supportsUnicode(envMap(tt.env)); got != tt.want {
				t.Fatalf("supportsUnicode(%v) = %v, want %v", tt.env, got, tt.want)
			}
		})
	}
}

func TestColorEnabled(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"default", map[string]string{}, true},
		{"NO_COLOR=1", map[string]string{"NO_COLOR": "1"}, false},
		{"NO_COLOR any value", map[string]string{"NO_COLOR": "false"}, false},
		{"NO_COLOR empty ignored", map[string]string{"NO_COLOR": ""}, true},
		{"NO_COLOR beats force", map[string]string{"NO_COLOR": "x", "CLICOLOR_FORCE": "1"}, false},
		{"force beats dumb", map[string]string{"CLICOLOR_FORCE": "1", "TERM": "dumb"}, true},
		{"force=0 is not force", map[string]string{"CLICOLOR_FORCE": "0", "TERM": "dumb"}, false},
		{"term dumb", map[string]string{"TERM": "dumb"}, false},
		{"CLICOLOR=0", map[string]string{"CLICOLOR": "0"}, false},
		{"CLICOLOR=1", map[string]string{"CLICOLOR": "1"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := colorEnabled(envMap(tt.env)); got != tt.want {
				t.Fatalf("colorEnabled(%v) = %v, want %v", tt.env, got, tt.want)
			}
		})
	}
}

func TestPublicEnvWrappers(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("CREDORBIT_ASCII", "1")
	if ColorEnabled() {
		t.Error("ColorEnabled with NO_COLOR set")
	}
	if ColorForced() {
		t.Error("ColorForced with NO_COLOR set")
	}
	if SupportsUnicode() {
		t.Error("SupportsUnicode with CREDORBIT_ASCII=1")
	}
}

var hexRe = regexp.MustCompile(`^#[0-9A-F]{6}$`)

func TestThemes(t *testing.T) {
	d := Default()
	want := map[string]string{
		"Background": "#0B1020", "Surface": "#111C31", "Primary": "#2563EB",
		"Accent": "#22D3EE", "Success": "#34D399", "Warning": "#FBBF24",
		"Danger": "#F87171", "Text": "#F8FAFC", "Muted": "#94A3B8",
	}
	got := map[string]string{
		"Background": d.Background, "Surface": d.Surface, "Primary": d.Primary,
		"Accent": d.Accent, "Success": d.Success, "Warning": d.Warning,
		"Danger": d.Danger, "Text": d.Text, "Muted": d.Muted,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("Default().%s = %s, want %s", k, got[k], v)
		}
	}
	for _, th := range []Theme{Default(), Light()} {
		for _, c := range []string{th.Background, th.Surface, th.Primary, th.Accent, th.Success, th.Warning, th.Danger, th.Text, th.Muted} {
			if !hexRe.MatchString(c) {
				t.Errorf("theme %s: bad colour %q", th.Name, c)
			}
		}
	}
	if !Mono().IsMono() || Default().IsMono() {
		t.Error("IsMono mismatch")
	}
}

func TestByName(t *testing.T) {
	for in, want := range map[string]string{"": "default", "Default": "default", "dark": "default", "LIGHT": "light", " mono ": "mono"} {
		th, err := ByName(in)
		if err != nil || th.Name != want {
			t.Errorf("ByName(%q) = %q, %v; want %q", in, th.Name, err, want)
		}
	}
	if _, err := ByName("neon"); err == nil {
		t.Error("ByName(neon): expected error")
	}
}

func TestMark(t *testing.T) {
	for _, uni := range []bool{true, false} {
		m := Mark(uni)
		if len(m) != MarkHeight || len(m) > 7 {
			t.Fatalf("unicode=%v: %d lines, want %d", uni, len(m), MarkHeight)
		}
		for i, l := range m {
			if w := utf8.RuneCountInString(l); w > MarkWidth || w > 24 {
				t.Errorf("unicode=%v line %d: width %d", uni, i, w)
			}
			if strings.TrimRight(l, " ") != l {
				t.Errorf("unicode=%v line %d: trailing spaces", uni, i)
			}
			if !uni {
				for _, r := range l {
					if r > 0x7e || r < 0x20 {
						t.Errorf("ascii mark line %d has non-ASCII %q", i, r)
					}
				}
			}
		}
		// Mark returns a copy.
		m[0] = "x"
		if Mark(uni)[0] == "x" {
			t.Error("Mark must return a copy")
		}
	}
}

func TestMarkSegments(t *testing.T) {
	for _, uni := range []bool{true, false} {
		segs := MarkSegments(uni)
		mark := Mark(uni)
		var cores int
		for i, line := range segs {
			var b strings.Builder
			for _, s := range line {
				b.WriteString(s.Text)
				if s.Role == RoleCore {
					cores++
				}
			}
			if b.String() != mark[i] {
				t.Errorf("unicode=%v line %d: segments %q != mark %q", uni, i, b.String(), mark[i])
			}
		}
		if cores != 1 {
			t.Errorf("unicode=%v: %d core segments, want 1", uni, cores)
		}
	}
}

func TestWordmark(t *testing.T) {
	if got := Wordmark(); got != "C R E D O R B I T" {
		t.Fatalf("Wordmark() = %q", got)
	}
	c, o := WordmarkParts()
	if c+" "+o != Wordmark() {
		t.Fatalf("parts %q + %q", c, o)
	}
	if Tagline != "YOUR CLOUD ACCESS. IN ORBIT." {
		t.Fatalf("Tagline = %q", Tagline)
	}
}
