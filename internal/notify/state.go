package notify

import (
	"encoding/json"
	"os"
	"time"

	"github.com/jmvaldez/bosun/internal/atomicfile"
)

// Entry is one live notice.
type Entry struct {
	ID   uint32    `json:"id,omitempty"`
	Edge string    `json:"edge,omitempty"`
	At   time.Time `json:"at"`
	// ListenUntil is when the notify-click listener spawned for this notice
	// gives up (zero: none was spawned), so a replacement knows whether one is
	// still watching the id.
	ListenUntil time.Time `json:"listen_until,omitempty"`
}

// State is notify.json: posted groups (so another process can retract and
// replace, LESSONS L-40) and the rolling rate-limit window. It is written only
// under notify.lock, atomically (LESSONS L-41).
type State struct {
	Posted map[string]Entry `json:"posted"`
	// Window holds the post times in the last minute.
	Window []time.Time `json:"window,omitempty"`
	// Actions caches whether the server advertises "actions" ("yes" or "no").
	Actions string `json:"actions,omitempty"`
	// Markup is the same for "body-markup".
	Markup    string    `json:"markup,omitempty"`
	ActionsAt time.Time `json:"actions_at,omitempty"`
}

// LoadState reads notify.json; absent or corrupt means nothing posted.
func LoadState(path string) State {
	s := State{Posted: map[string]Entry{}}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	if s.Posted == nil {
		s.Posted = map[string]Entry{}
	}
	return s
}

func (s State) Save(path string) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return atomicfile.Write(path, b, 0o600)
}

// prune drops window entries older than a minute, and posted groups older than
// a day (their notices are long gone from any notification center).
func (s *State) prune(now time.Time) {
	keep := s.Window[:0]
	for _, t := range s.Window {
		if now.Sub(t) < time.Minute {
			keep = append(keep, t)
		}
	}
	s.Window = keep
	for g, p := range s.Posted {
		if now.Sub(p.At) > 24*time.Hour {
			delete(s.Posted, g)
		}
	}
}
