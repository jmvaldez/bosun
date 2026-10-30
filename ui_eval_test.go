package main

import (
	"testing"

	"github.com/jmvaldez/bosun/internal/config"
)

// Review finding 4: the popup's tick used to be a catch-up (no Exe, no Notify),
// so it never retracted a notice and swallowed alert fires.
func TestPopupTickIsANormalEvaluation(t *testing.T) {
	a := &app{}
	d := (&uiSource{a: a, cfg: config.Default()}).evalDeps()
	if !d.Notify {
		t.Error("popup evaluation must be allowed to notify")
	}
	if d.Exe == "" {
		t.Error("popup evaluation needs Exe to spawn the dispatcher and the refresh")
	}
}
