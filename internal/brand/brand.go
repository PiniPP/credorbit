// Package brand holds CredOrbit's visual identity in a dependency-free form:
// palette constants, terminal themes, the Unicode/ASCII brand mark and the
// environment rules that decide whether colour and Unicode may be used.
//
// It deliberately imports nothing beyond the standard library so that any
// front-end (Cobra help, Bubble Tea/lipgloss, plain logs) can style it.
package brand

import (
	"fmt"
	"strings"
)

// Product identity.
const (
	Name    = "CredOrbit"
	Tagline = "YOUR CLOUD ACCESS. IN ORBIT."
	// TaglineTitle is the sentence-case tagline for prose and help text.
	TaglineTitle = "Your Cloud Access. In Orbit."
)

// Core palette (dark, the default). Hex strings, upper case, with '#'.
const (
	ColorBackground = "#0B1020" // deep space navy
	ColorSurface    = "#111C31" // panels, cards
	ColorOrbitBlue  = "#2563EB" // orbital structure
	ColorCyan       = "#22D3EE" // identity core, highlights
	ColorSuccess    = "#34D399"
	ColorWarning    = "#FBBF24"
	ColorDanger     = "#F87171"
	ColorText       = "#F8FAFC"
	ColorMuted      = "#94A3B8"
)

// Light palette: deepened hues that keep WCAG AA contrast on white.
const (
	LightBackground = "#FFFFFF"
	LightSurface    = "#F1F5F9"
	LightOrbitBlue  = "#1D4ED8"
	LightCyan       = "#0891B2"
	LightSuccess    = "#047857"
	LightWarning    = "#B45309"
	LightDanger     = "#B91C1C"
	LightText       = "#0B1020"
	LightMuted      = "#475569"
)

// Theme is a named set of semantic colours. Empty strings mean "use the
// terminal default" (see Mono).
type Theme struct {
	Name       string
	Background string
	Surface    string
	Primary    string // structure: borders, headings (Orbit Blue)
	Accent     string // focus, selection, highlights (Electric Cyan)
	Success    string
	Warning    string
	Danger     string
	Text       string
	Muted      string
}

// Theme names accepted by ByName.
const (
	ThemeDefault = "default"
	ThemeLight   = "light"
	ThemeMono    = "mono"
)

// Default returns the dark brand theme.
func Default() Theme {
	return Theme{
		Name:       ThemeDefault,
		Background: ColorBackground,
		Surface:    ColorSurface,
		Primary:    ColorOrbitBlue,
		Accent:     ColorCyan,
		Success:    ColorSuccess,
		Warning:    ColorWarning,
		Danger:     ColorDanger,
		Text:       ColorText,
		Muted:      ColorMuted,
	}
}

// Light returns the theme for light terminal backgrounds.
func Light() Theme {
	return Theme{
		Name:       ThemeLight,
		Background: LightBackground,
		Surface:    LightSurface,
		Primary:    LightOrbitBlue,
		Accent:     LightCyan,
		Success:    LightSuccess,
		Warning:    LightWarning,
		Danger:     LightDanger,
		Text:       LightText,
		Muted:      LightMuted,
	}
}

// Mono returns a colourless theme: every colour is empty so renderers fall
// back to the terminal's own foreground/background and use only attributes
// (bold, faint, reverse) for emphasis.
func Mono() Theme {
	return Theme{Name: ThemeMono}
}

// Names lists the available theme names.
func Names() []string { return []string{ThemeDefault, ThemeLight, ThemeMono} }

// ByName resolves a theme name (case-insensitive; "" and "dark" mean default).
func ByName(name string) (Theme, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", ThemeDefault, "dark":
		return Default(), nil
	case ThemeLight:
		return Light(), nil
	case ThemeMono, "none", "monochrome":
		return Mono(), nil
	}
	return Theme{}, fmt.Errorf("brand: unknown theme %q (want one of %s)", name, strings.Join(Names(), ", "))
}

// IsMono reports whether the theme carries no colours.
func (t Theme) IsMono() bool {
	return t.Primary == "" && t.Accent == "" && t.Text == ""
}
