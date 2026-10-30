package ui

import "testing"

func TestWidth(t *testing.T) {
	for in, want := range map[string]int{
		"": 0, "abc": 3, "✓●└⤷▶█": 6, "日本": 4, "é": 1, "\x1b[31mred\x1b[0m": 3, "a😀": 3,
	} {
		if got := Width(in); got != want {
			t.Errorf("Width(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestPadAndCutByDisplayWidth(t *testing.T) {
	if got := Pad("日本", 6); got != "日本  " {
		t.Errorf("Pad = %q", got)
	}
	if got := Cut("abcdefgh", 5); got != "abcd…" {
		t.Errorf("Cut = %q", got)
	}
	if got := Cut("日本語です", 5); Width(got) > 5 || got != "日本…" {
		t.Errorf("Cut wide = %q (%d)", got, Width(got))
	}
	if got := Cut("short", 10); got != "short" {
		t.Errorf("Cut = %q", got)
	}
	if got := Cut("\x1b[31mabcdefgh\x1b[0m", 5); Width(got) != 5 {
		t.Errorf("styled cut width = %d (%q)", Width(got), got)
	}
	if got := Fit("日本語", 5); Width(got) != 5 {
		t.Errorf("Fit = %q", got)
	}
	if got := PadLeft("$1", 5); got != "   $1" {
		t.Errorf("PadLeft = %q", got)
	}
}
