package ui

import (
	"fmt"
	"strings"

	"github.com/jmvaldez/bosun/internal/eval"
	"github.com/jmvaldez/bosun/internal/platform"
	"github.com/jmvaldez/bosun/internal/tmux"
)

// NotSent is the refusal message when the pane no longer holds the session.
const NotSent = "pane changed, not sent"

// maxAncestry bounds the walk from the session's pid up to the pane's process.
const maxAncestry = 64

type InterruptDeps struct {
	Tmux  tmux.Client
	Procs platform.Procs
	Keys  string // ui.interrupt_keys, e.g. "Escape"
}

// Interrupt re-checks the pane right now and, only if every check passes, sends
// the interrupt keys. The checks: the tmux server is the one the session's
// locator names, the pane id still exists, the session's process still has the
// recorded start token, and that process sits under the pane's process. Any
// mismatch sends nothing. This is the only place bosun writes to an agent pane.
func Interrupt(d InterruptDeps, v eval.View) (string, error) {
	panes, err := d.Tmux.Panes()
	if err != nil {
		return "tmux did not answer, not sent", nil
	}
	var pane *tmux.Pane
	for i := range panes {
		if panes[i].ID == v.Pane {
			pane = &panes[i]
		}
	}
	if pane == nil || pane.ServerPID != v.ServerPID {
		return NotSent, nil
	}
	// An empty token would make Alive vouch for any process with that pid.
	if v.PID <= 0 || v.StartTok == "" || !d.Procs.Alive(v.PID, v.StartTok) || !underPane(d.Procs, v.PID, pane.PanePID) {
		return NotSent, nil
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

// underPane reports whether pid is the pane's process or one of its descendants.
func underPane(p platform.Procs, pid, panePID int) bool {
	cur := pid
	for i := 0; i < maxAncestry && cur > 1; i++ {
		if cur == panePID {
			return true
		}
		parent, err := p.Parent(cur)
		if err != nil {
			return false
		}
		cur = parent
	}
	return cur == panePID
}
