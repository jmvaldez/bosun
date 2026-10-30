package main

import (
	"bytes"
	"os"
	"testing"
)

func TestIsTerminalIsFalseForPipesBuffersAndDevNull(t *testing.T) {
	null, _ := os.Open(os.DevNull) // a character device, but not a terminal
	defer null.Close()
	r, w, _ := os.Pipe()
	defer r.Close()
	defer w.Close()
	for name, x := range map[string]any{"buffer": &bytes.Buffer{}, "devnull": null, "pipe": w} {
		if isTerminal(x) {
			t.Errorf("%s reported as a terminal", name)
		}
	}
}
