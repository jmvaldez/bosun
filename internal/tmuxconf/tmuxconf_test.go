package tmuxconf

import (
	"strings"
	"testing"
)

func gen(t *testing.T) string {
	t.Helper()
	s, err := Generate(Options{Version: "0.1.0", Bosun: "/home/me/.local/bin/bosun", JumpKey: "a", PopupKey: "W"})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestHeader(t *testing.T) {
	if first := strings.SplitN(gen(t), "\n", 2)[0]; first != "# bosun 0.1.0" {
		t.Errorf("header = %q", first)
	}
}

// LESSONS L-05: parse every conditional in the generated conf and assert no
// comma sits inside a #[...] block within it.
func TestNoCommaInsideStyleBlocksOfConditionals(t *testing.T) {
	for _, line := range strings.Split(gen(t), "\n") {
		if !strings.Contains(line, "#{?") {
			continue
		}
		rest := line
		for {
			i := strings.Index(rest, "#[")
			if i < 0 {
				break
			}
			j := strings.Index(rest[i:], "]")
			if j < 0 {
				t.Fatalf("unterminated style block in %q", line)
			}
			if strings.Contains(rest[i:i+j], ",") {
				t.Errorf("comma inside style block %q", rest[i:i+j+1])
			}
			rest = rest[i+j:]
		}
	}
}

func TestPrependsAreEscapedAndGuarded(t *testing.T) {
	s := gen(t)
	for _, want := range []string{
		"if -F '#{m:*bosun status*,#{status-right}}' ''",
		"##(/home/me/.local/bin/bosun status --tmux)#{status-right}",
		"#{window-status-format}##{E:@bosun-win-mark}",
		"#{window-status-current-format}##{E:@bosun-win-mark}",
		"set -g @bosun-win-mark '",
		"set-hook -g 'client-attached[42]'",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(s, "set-hook -ag") || strings.Contains(s, "set -gF @bosun") {
		t.Error("non-idempotent hook or -F on the marker format")
	}
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "run ") || strings.HasPrefix(line, "run-shell ") {
			t.Errorf("run wrapper expands formats at load: %q", line)
		}
	}
}

func TestRejectsUnquotableValues(t *testing.T) {
	for _, bad := range []string{"/tmp/it's/bosun", "/tmp/a\"b/bosun", "/tmp/$x/bosun", "/tmp/a#b/bosun"} {
		if _, err := Generate(Options{Version: "1", Bosun: bad, JumpKey: "a", PopupKey: "W"}); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if _, err := Generate(Options{Version: "1", Bosun: "/b", JumpKey: "a'b", PopupKey: "W"}); err == nil {
		t.Error("accepted a quote in the jump key")
	}
}

// D-3: the popup title flag only exists from tmux 3.3, so the binding is gated.
func TestPopupBindingIsVersionGated(t *testing.T) {
	s := gen(t)
	var line string
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, "display-popup") && !strings.HasPrefix(l, "#") {
			line = l
		}
	}
	if !strings.HasPrefix(line, "if -F '#{>=:#{version},3.3}' ") {
		t.Fatalf("the popup binding is not gated on the tmux version: %q", line)
	}
	withTitle, without, ok := strings.Cut(line, "\" \"")
	if !ok || !strings.Contains(withTitle, "-T ' bosun '") || strings.Contains(without, "-T") {
		t.Errorf("only the >= 3.3 branch may pass -T:\n%s", line)
	}
	for _, b := range []string{withTitle, without} {
		if !strings.Contains(b, "bind W display-popup -E -w 90% -h 60%") || !strings.Contains(b, "/home/me/.local/bin/bosun ui") {
			t.Errorf("branch lacks the binding: %s", b)
		}
	}
}

func TestSpendKeyIsQuotedAndOptional(t *testing.T) {
	s, err := Generate(Options{Version: "1", Bosun: "/b", JumpKey: "a", PopupKey: "W", SpendKey: "$"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s, "bind '$' display-popup") || !strings.Contains(s, "ui --spend") {
		t.Errorf("spend binding missing or unquoted:\n%s", s)
	}
	if strings.Contains(gen(t), "ui --spend") {
		t.Error("no spend key, yet a binding was written")
	}
	if _, err := Generate(Options{Version: "1", Bosun: "/b$", JumpKey: "a", PopupKey: "W"}); err == nil {
		t.Error("$ in the bosun path was accepted")
	}
}

func TestTabMarkGlyphs(t *testing.T) {
	for _, ascii := range []bool{false, true} {
		m := WinMark(ascii)
		for _, bad := range []string{"\ufe0f", "$!"} {
			if strings.Contains(m, bad) {
				t.Errorf("ascii=%v: mark holds %q", ascii, bad)
			}
		}
	}
	if m := WinMark(false); !strings.Contains(m, "⚑") || !strings.Contains(m, "▲") || !strings.Contains(m, "✓") {
		t.Errorf("unicode mark = %s", m)
	}
	if m := WinMark(true); strings.ContainsAny(m, "⚑▲✓") {
		t.Errorf("ascii mark = %s", m)
	}
}
