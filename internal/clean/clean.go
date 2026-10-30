// Package clean turns decoded strings into values safe to store. Free text fails
// every check here: only identifier-like characters or fixed enum values get
// through, which is what makes the hook decoders allowlists in practice
// (SPEC section 2, "No prompt text").
package clean

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Ident keeps identifier-like values (ids, tool names, agent types) of at most
// max bytes and drops anything else.
func Ident(s string, max int) string {
	if len(s) == 0 || len(s) > max {
		return ""
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') &&
			c != '-' && c != '_' && c != '.' && c != ':' {
			return ""
		}
	}
	return s
}

func OneOf(s, fallback string, allowed ...string) string {
	for _, a := range allowed {
		if s == a {
			return s
		}
	}
	return fallback
}

// Unsafe reports whether r can act on a terminal or reorder displayed text:
// C0, DEL and C1 controls (U+009B is the 8-bit CSI) and the bidi controls.
// U+200D (ZWJ) is allowed because emoji sequences need it.
func Unsafe(r rune) bool {
	if unicode.IsControl(r) {
		return true
	}
	switch {
	case r == 0x061C, r == 0x200E, r == 0x200F,
		r >= 0x202A && r <= 0x202E,
		r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
}

// Text drops every Unsafe rune and any invalid UTF-8 from s. Use it on free text
// (repo, branch, task) before it is styled or printed.
func Text(s string) string {
	return strings.Map(func(r rune) rune {
		if r == utf8.RuneError || Unsafe(r) {
			return -1
		}
		return r
	}, s)
}
