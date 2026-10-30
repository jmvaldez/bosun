// Package ui is the popup: a pure model (state, keys, frames) with the terminal
// and tmux behind small interfaces, so every frame and action is testable.
package ui

import (
	"strings"
	"unicode/utf8"
)

// runeWidth is the display width of one rune: 0 for NUL, zero-width space
// and joiner, VS16 and combining marks; 2 for East Asian wide and emoji
// ranges; else 1. The ranges approximate Unicode East Asian Width
// (https://www.unicode.org/reports/tr11/). The popup glyphs
// (✓ ● └ ⤷ ▶ █ ░) are narrow.
func runeWidth(r rune) int {
	switch {
	case r == 0 || r == 0x200b || r == 0x200d || r == 0xfe0f:
		return 0
	case r >= 0x300 && r <= 0x36f, r >= 0x1ab0 && r <= 0x1aff, r >= 0x20d0 && r <= 0x20ff:
		return 0
	case r >= 0x1100 && r <= 0x115f, r >= 0x2e80 && r <= 0xa4cf, r >= 0xac00 && r <= 0xd7a3,
		r >= 0xf900 && r <= 0xfaff, r >= 0xfe30 && r <= 0xfe6f, r >= 0xff00 && r <= 0xff60,
		r >= 0xffe0 && r <= 0xffe6, r >= 0x1f300 && r <= 0x1faff, r >= 0x20000 && r <= 0x3fffd:
		return 2
	}
	return 1
}

// stripANSI removes CSI escape sequences.
func stripANSI(s string) string {
	if !strings.Contains(s, "\x1b") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
				j++
			}
			i = j + 1
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// Width is the display width of s, ignoring escape sequences.
func Width(s string) int {
	w := 0
	for _, r := range stripANSI(s) {
		w += runeWidth(r)
	}
	return w
}

// Pad pads s with spaces on the right to display width w (it never truncates).
func Pad(s string, w int) string {
	if d := w - Width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// PadLeft pads on the left, for right-aligned columns.
func PadLeft(s string, w int) string {
	if d := w - Width(s); d > 0 {
		return strings.Repeat(" ", d) + s
	}
	return s
}

// Cut shortens s to at most w display columns, ending in an ellipsis when it
// had to cut. Escape sequences are kept whole and add no width; a style left
// open by the cut is closed with a reset.
func Cut(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if Width(s) <= w {
		return s
	}
	var b strings.Builder
	cols, styled := 0, false
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
				j++
			}
			if j < len(s) {
				j++
			}
			b.WriteString(s[i:j])
			styled = true
			i = j
			continue
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		rw := runeWidth(r)
		if cols+rw > w-1 {
			break
		}
		b.WriteString(s[i : i+n])
		cols += rw
		i += n
	}
	b.WriteString("…")
	if styled {
		b.WriteString("\x1b[0m")
	}
	return b.String()
}

// Fit cuts then pads s to exactly w columns.
func Fit(s string, w int) string { return Pad(Cut(s, w), w) }

// Plain strips styling from frame lines, for tests and saved captures.
func Plain(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = stripANSI(l)
	}
	return out
}
