// Package shell quotes strings for a POSIX shell.
package shell

import "strings"

// Quote single-quotes s for a POSIX shell, always.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// QuoteWord is Quote, except that a plain word is returned as it is, so the
// commands bosun prints for people to read stay clean.
func QuoteWord(s string) string {
	if s != "" && IsWord(s) {
		return s
	}
	return Quote(s)
}

// IsWord reports whether s is a single shell word with nothing to split or
// expand. A space is plain for install.WrapCommand's pass-through but not here:
// an unquoted path with a space is two arguments.
func IsWord(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("_./-=:,@+", r):
		default:
			return false
		}
	}
	return true
}
