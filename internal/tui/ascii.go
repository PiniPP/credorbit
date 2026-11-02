package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

func ansiStrip(s string) string { return ansi.Strip(s) }

// asciiMap keeps display width: every replacement is exactly one cell, so a
// late fold never breaks alignment.
var asciiMap = map[rune]rune{
	'·': '|', '…': '.', '—': '-', '–': '-', '−': '-', '→': '>', '←': '<', '↑': '^', '↓': 'v',
	'✓': '+', '✗': 'x', '●': '*', '○': 'o', '◐': '~', '•': '*', '▸': '>', '✦': '+', '◦': 'o',
	'’': '\'', '‘': '\'', '“': '"', '”': '"', '█': '#', '░': '.', '─': '-', '━': '-', '│': '|',
	'╭': '+', '╮': '+', '╰': '+', '╯': '+', '┌': '+', '┐': '+', '└': '+', '┘': '+', '├': '+',
	'┤': '+', '┬': '+', '┴': '+', '┼': '+', '◜': '|', '◝': '/', '◞': '-', '◟': '\\', '×': 'x',
	'☐': '_', '☑': 'x', '⎈': '*',
}

// asciiFold replaces any non-ASCII rune left in s (e.g. from engine messages)
// so ASCII-mode output is guaranteed 7-bit.
func asciiFold(s string) string {
	clean := true
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r < 0x80:
			b.WriteRune(r)
		default:
			if m, ok := asciiMap[r]; ok {
				b.WriteRune(m)
			} else {
				b.WriteByte('?')
			}
		}
	}
	return b.String()
}
