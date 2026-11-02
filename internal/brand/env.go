package brand

import (
	"os"
	"strings"
)

// lookupFunc matches os.LookupEnv; tests substitute a map-backed version.
type lookupFunc func(string) (string, bool)

// SupportsUnicode reports whether the terminal can be expected to render the
// Unicode brand mark and box-drawing characters.
//
// Rules, in order:
//   - CREDORBIT_ASCII=1 (or true/yes) forces ASCII.
//   - TERM=dumb means ASCII.
//   - WT_SESSION set (Windows Terminal) means Unicode.
//   - The effective locale (LC_ALL, else LC_CTYPE, else LANG) naming UTF-8
//     means Unicode.
//   - Otherwise ASCII.
func SupportsUnicode() bool { return supportsUnicode(os.LookupEnv) }

func supportsUnicode(env lookupFunc) bool {
	if truthy(get(env, "CREDORBIT_ASCII")) {
		return false
	}
	if strings.EqualFold(get(env, "TERM"), "dumb") {
		return false
	}
	if _, ok := env("WT_SESSION"); ok {
		return true
	}
	return isUTF8Locale(effectiveLocale(env))
}

// effectiveLocale follows POSIX precedence: LC_ALL > LC_CTYPE > LANG.
func effectiveLocale(env lookupFunc) string {
	for _, k := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if v := get(env, k); v != "" {
			return v
		}
	}
	return ""
}

func isUTF8Locale(v string) bool {
	v = strings.ToLower(v)
	return strings.Contains(v, "utf-8") || strings.Contains(v, "utf8")
}

// ColorEnabled reports whether ANSI colour output is allowed by the
// environment. It does not check whether stdout is a TTY; callers combine it
// with their own terminal detection.
//
// Rules, in order:
//   - NO_COLOR set to any non-empty value disables colour (no-color.org).
//   - CLICOLOR_FORCE set to a non-empty value other than "0" enables colour.
//   - TERM=dumb disables colour.
//   - CLICOLOR=0 disables colour.
//   - Otherwise colour is enabled.
func ColorEnabled() bool { return colorEnabled(os.LookupEnv) }

func colorEnabled(env lookupFunc) bool {
	if get(env, "NO_COLOR") != "" {
		return false
	}
	if v := get(env, "CLICOLOR_FORCE"); v != "" && v != "0" {
		return true
	}
	if strings.EqualFold(get(env, "TERM"), "dumb") {
		return false
	}
	if get(env, "CLICOLOR") == "0" {
		return false
	}
	return true
}

// ColorForced reports whether CLICOLOR_FORCE requests colour even when output
// is not a terminal (and NO_COLOR is not set).
func ColorForced() bool {
	if get(os.LookupEnv, "NO_COLOR") != "" {
		return false
	}
	v := get(os.LookupEnv, "CLICOLOR_FORCE")
	return v != "" && v != "0"
}

func get(env lookupFunc, key string) string {
	v, _ := env(key)
	return v
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
