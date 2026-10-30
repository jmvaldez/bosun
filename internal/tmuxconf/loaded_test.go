package tmuxconf

import (
	"strings"
	"testing"
)

// The conf ends by recording its version, so a server that reports the current
// version has loaded all of it (D-31).
func TestConfEndsByRecordingItsVersion(t *testing.T) {
	lines := strings.Split(strings.TrimRight(gen(t), "\n"), "\n")
	if got, want := lines[len(lines)-1], "set -g @bosun-version '0.1.0'"; got != want {
		t.Errorf("last line = %q, want %q", got, want)
	}
	if _, err := Generate(Options{Version: "1'; run-shell evil", Bosun: "/b", JumpKey: "a", PopupKey: "W"}); err == nil {
		t.Error("a version that cannot be quoted safely must be refused")
	}
}
