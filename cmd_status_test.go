package main

import (
	"strings"
	"testing"

	"github.com/jmvaldez/bosun/internal/eval"
)

func TestStatusLineDropsTerminalEscapes(t *testing.T) {
	line := statusLine(eval.View{Repo: "evil\x1b]52;c;aGk=\x07", Task: "t\u202eask", Where: "w\x1b[2Jork:1"})
	if strings.ContainsAny(line, "\x1b\x07\u202e") {
		t.Errorf("escape survived: %q", line)
	}
	if !strings.Contains(line, "evil]52;c;aGk=") {
		t.Errorf("text lost: %q", line)
	}
}
