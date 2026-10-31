// Package handoff runs the context handoff: ask a session to write a handoff
// doc, wait for it, open a fresh session that starts from it. The state file at
// sessions/<id>/handoff.json is the only thing it leaves in bosun's dirs, and
// holds enums, times, ids and the doc's path, never the doc's text (D-27).
package handoff

import (
	"encoding/json"
	"os"
	"time"

	"github.com/jmvaldez/bosun/internal/atomicfile"
)

// Phases of a handoff.
const (
	PhaseWriting   = "writing"
	PhaseLaunching = "launching"
	PhaseDone      = "done"
	PhaseFailed    = "failed"
)

// Why a handoff failed; the value is an enum, never free text.
const (
	WhyNotSubmitted = "not-submitted"
	WhyDeadline     = "deadline"
	WhyPaneGone     = "pane-gone"
	WhyCancelled    = "cancelled"
	WhyDocStale     = "doc-stale"
	WhyLaunchFailed = "launch-failed"
)

// State is handoff.json. Deadline is the unpaused deadline; the time the
// session spent in needs-you (PausedFor) moves it out.
type State struct {
	V          int           `json:"v"`
	Phase      string        `json:"phase"`
	Started    time.Time     `json:"started"`
	Pasted     time.Time     `json:"pasted,omitempty"`
	Deadline   time.Time     `json:"deadline"`
	PausedFor  time.Duration `json:"paused_ns,omitempty"`
	Doc        string        `json:"doc,omitempty"`
	NewWindow  string        `json:"new_window,omitempty"`
	NewSession string        `json:"new_session,omitempty"`
	Why        string        `json:"why,omitempty"`
}

// Read loads a state file; a missing or corrupt file is the zero State, whose
// phase is empty.
func Read(path string) State {
	var st State
	b, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(b, &st) != nil {
		return State{}
	}
	return st
}

// Write stores a state file atomically.
func Write(path string, st State) error {
	st.V = 1
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return atomicfile.Write(path, b, 0o600)
}

// Active reports whether a handoff is under way: the phase is writing or
// launching and its deadline has not passed. A state left behind by a process
// that died is not active once its deadline is over.
func (s State) Active(now time.Time) bool {
	return (s.Phase == PhaseWriting || s.Phase == PhaseLaunching) && !now.After(s.Deadline.Add(s.PausedFor))
}

// Begun reports whether a handoff is under way or finished, which is when the
// suggestion rule stands down. A failed handoff leaves the suggestion standing.
func (s State) Begun() bool {
	return s.Phase == PhaseWriting || s.Phase == PhaseLaunching || s.Phase == PhaseDone
}
