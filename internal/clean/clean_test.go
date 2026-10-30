package clean

import "testing"

func TestIdent(t *testing.T) {
	for in, want := range map[string]string{
		"toolu_01AbC": "toolu_01AbC", "mcp__srv__tool": "mcp__srv__tool", "plug:agent-1.2": "plug:agent-1.2",
		"": "", "has space": "", "semi;colon": "", "new\nline": "", "über": "",
	} {
		if got := Ident(in, 64); got != want {
			t.Errorf("Ident(%q) = %q, want %q", in, got, want)
		}
	}
	if Ident("abcdef", 5) != "" {
		t.Error("length cap ignored")
	}
}

func TestOneOf(t *testing.T) {
	if OneOf("b", "x", "a", "b") != "b" || OneOf("c", "x", "a", "b") != "x" {
		t.Error("OneOf")
	}
}

func TestText(t *testing.T) {
	for name, c := range map[string][2]string{
		"ascii":    {"acme/api", "acme/api"},
		"esc":      {"a\x1b[31mb", "a[31mb"},
		"osc52":    {"x\x1b]52;c;aGk=\x07y", "x]52;c;aGk=y"},
		"c1 csi":   {"a\u009b31mb", "a31mb"},
		"bidi":     {"a\u202eb\u2066c", "abc"},
		"zwj":      {"\U0001F468\u200D\U0001F469", "\U0001F468\u200D\U0001F469"},
		"bad utf8": {"a\xffb", "ab"},
		"newline":  {"a\nb\tc", "abc"},
	} {
		if got := Text(c[0]); got != c[1] {
			t.Errorf("%s: Text(%q) = %q, want %q", name, c[0], got, c[1])
		}
	}
}

func TestUnsafe(t *testing.T) {
	for r, want := range map[rune]bool{
		'a': false, 0x1b: true, 0x7f: true, 0x9b: true, 0x200D: false,
		0x200E: true, 0x202E: true, 0x061C: true, 0x2069: true, 0x2070: false,
	} {
		if Unsafe(r) != want {
			t.Errorf("Unsafe(%U) = %v, want %v", r, !want, want)
		}
	}
}
