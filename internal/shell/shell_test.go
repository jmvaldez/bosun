package shell

import (
	"os/exec"
	"testing"
)

func TestQuote(t *testing.T) {
	for in, want := range map[string]string{
		"":         `''`,
		"word":     `'word'`,
		"it's":     `'it'\''s'`,
		"a b":      `'a b'`,
		"$HOME":    `'$HOME'`,
		"/a/b.txt": `'/a/b.txt'`,
	} {
		if got := Quote(in); got != want {
			t.Errorf("Quote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestQuoteWord(t *testing.T) {
	for in, want := range map[string]string{
		"":         `''`,
		"word":     "word",
		"/a/b.txt": "/a/b.txt",
		"it's":     `'it'\''s'`,
		"a b":      `'a b'`,
		"$HOME":    `'$HOME'`,
		"~/x":      `'~/x'`,
	} {
		if got := QuoteWord(in); got != want {
			t.Errorf("QuoteWord(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestQuotersRoundTrip(t *testing.T) {
	for _, s := range []string{`it's`, `a"b`, `$HOME && rm`, "line\nbreak", "", "/home/me/my repo", "a;b", "a*b", "~/x", "a\tb", "a|b", "a`b`", "#c", "a\\b"} {
		for name, q := range map[string]func(string) string{"Quote": Quote, "QuoteWord": QuoteWord} {
			out, err := exec.Command("/bin/sh", "-c", "printf %s "+q(s)).Output()
			if err != nil || string(out) != s {
				t.Errorf("%s: %q round-tripped to %q (%v)", name, s, out, err)
			}
		}
	}
}
