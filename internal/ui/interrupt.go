package ui

import (
	"fmt"
	"strings"

	"github.com/jmvaldez/bosun/internal/eval"
	"github.com/jmvaldez/bosun/internal/panewrite"
	"github.com/jmvaldez/bosun/internal/platform"
	"github.com/jmvaldez/bosun/internal/tmux"
)

// NotSent is the refusal message when the pane no longer holds the session.
const NotSent = panewrite.NotSent

type InterruptDeps struct {
	Tmux  tmux.Client
	Procs platform.Procs
	Keys  string // ui.interrupt_keys, e.g. "Escape"
}

// Interrupt re-checks the pane right now with panewrite.Verify and, only if it
// passes, sends the interrupt keys. Any mismatch sends nothing.
func Interrupt(d InterruptDeps, v eval.View) (string, error) {
	ok, why := panewrite.Verify(d.Tmux, d.Procs, panewrite.Target{Pane: v.Pane, ServerPID: v.ServerPID, PID: v.PID, StartTok: v.StartTok})
	if !ok {
		return why, nil
	}
	keys := strings.Fields(d.Keys)
	if len(keys) == 0 {
		return "", fmt.Errorf("ui.interrupt_keys is empty")
	}
	if err := d.Tmux.SendKeys(v.Pane, keys); err != nil {
		return "", err
	}
	return "sent " + strings.Join(keys, " ") + " to " + v.Pane, nil
}
