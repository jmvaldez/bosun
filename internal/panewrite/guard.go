// Package panewrite is the only code that writes to an agent pane. Every write
// to an agent pane goes through Verify.
package panewrite

import (
	"github.com/jmvaldez/bosun/internal/platform"
	"github.com/jmvaldez/bosun/internal/tmux"
)

// NotSent is the refusal message when the pane no longer holds the session.
const NotSent = "pane changed, not sent"

// maxAncestry bounds the walk from the session's pid up to the pane's process.
const maxAncestry = 64

// Tmux is the part of tmux.Client that Verify needs.
type Tmux interface {
	Panes() ([]tmux.Pane, error)
}

// Target names the pane and the session process that must still sit in it.
type Target struct {
	Pane      string
	ServerPID int
	PID       int
	StartTok  string
}

// Verify re-checks the pane right now. The checks: the tmux server is the one
// the session's locator names, the pane id still exists, the session's process
// still has the recorded start token, and that process sits under the pane's
// process. When any check fails, ok is false and why says so; nothing may be
// written then.
func Verify(t Tmux, p platform.Procs, target Target) (ok bool, why string) {
	panes, err := t.Panes()
	if err != nil {
		return false, "tmux did not answer, not sent"
	}
	var pane *tmux.Pane
	for i := range panes {
		if panes[i].ID == target.Pane {
			pane = &panes[i]
		}
	}
	if pane == nil || pane.ServerPID != target.ServerPID {
		return false, NotSent
	}
	// An empty token would make Alive vouch for any process with that pid.
	if target.PID <= 0 || target.StartTok == "" || !p.Alive(target.PID, target.StartTok) || !underPane(p, target.PID, pane.PanePID) {
		return false, NotSent
	}
	return true, ""
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
