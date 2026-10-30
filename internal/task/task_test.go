package task

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKey(t *testing.T) {
	d := Default()
	for in, want := range map[string]string{
		"feat/abc-123-add-thing": "ABC-123",
		"ABC-9":                  "ABC-9",
		"main":                   "",
		"fix/OPS-7_and_ABC-1":    "OPS-7",
		"":                       "",
	} {
		if got := d.Key(in); got != want {
			t.Errorf("Key(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPrefixAllowlist(t *testing.T) {
	p, err := New(`[A-Z][A-Z0-9]+-\d+`, []string{"ops"})
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Key("utf-8 fix/ops-12"); got != "OPS-12" {
		t.Errorf("got %q", got)
	}
	if got := p.Key("abc-1"); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestBadRegex(t *testing.T) {
	if _, err := New("([", nil); err == nil {
		t.Fatal("want error")
	}
}

func TestTag(t *testing.T) {
	d := Default()
	for in, want := range map[string]string{
		"Bosun Init":                 "Bosun Init",
		"  Bosun \t Init  ":          "", // a tab is a control character
		"  Bosun    Init  ":          "Bosun Init",
		"abc-123":                    "ABC-123",
		" ops-7 ":                    "OPS-7",
		"fix ABC-1":                  "fix ABC-1", // only a whole key is normalised
		"naïve café":                 "naïve café",
		strings.Repeat("x", 64):      strings.Repeat("x", 64),
		"":                           "",
		"   ":                        "",
		"a\nb":                       "",
		"a\x1b[31mb":                 "",
		"a\u202eb":                   "",                           // bidi override
		"\U0001F468\u200D\U0001F469": "\U0001F468\u200D\U0001F469", // ZWJ sequence
		"bad\xffutf8":                "",
		strings.Repeat("x", 65):      "",
	} {
		got, err := d.Tag(in)
		if (err != nil) != (want == "") || got != want {
			t.Errorf("Tag(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestTagWholeKeyHonoursPrefixes(t *testing.T) {
	p, _ := New(`[A-Z][A-Z0-9]+-\d+`, []string{"ops"})
	if got, _ := p.Tag("abc-1"); got != "abc-1" {
		t.Errorf("a key with a disallowed prefix is free text, got %q", got)
	}
	if got, _ := p.Tag("ops-1"); got != "OPS-1" {
		t.Errorf("got %q", got)
	}
}

func TestTagFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tag")
	if err := WriteTag(path, "Bosun Init"); err != nil {
		t.Fatal(err)
	}
	if got := ReadTag(path); got != "Bosun Init" {
		t.Errorf("ReadTag = %q", got)
	}
	os.WriteFile(path, []byte("x\x1b[0m\n"), 0o600)
	if got := ReadTag(path); got != "" {
		t.Errorf("a control character survived: %q", got)
	}
	os.WriteFile(path, []byte(strings.Repeat("x", 80)), 0o600)
	if got := ReadTag(path); got != "" {
		t.Errorf("an over-long tag survived: %q", got)
	}
	if err := WriteTag(path, ""); err != nil || ReadTag(path) != "" {
		t.Errorf("clear failed: %v", err)
	}
}
