// Package jump picks the next pane in the waiting queue.
package jump

import (
	"encoding/json"
	"os"
	"time"

	"github.com/jmvaldez/bosun/internal/atomicfile"
)

// walkTTL is how long a walk survives between presses.
const walkTTL = 2 * time.Minute

// Walk is where a client is in its current pass over the queue. It exists
// because visiting a `done` pane turns it idle, which removes it from the queue;
// without a snapshot the next press would lose its place and restart.
type Walk struct {
	Panes []string  `json:"panes"` // queue snapshot at the start of the pass
	Idx   int       `json:"idx"`   // index of the pane last jumped to
	At    time.Time `json:"at"`
}

// Next returns the pane to jump to, or "" when nothing is waiting, plus the
// walk state to persist. queue is the current waiting queue in order; current is
// the pane the client shows now.
func Next(prev Walk, queue []string, current string, now time.Time) (string, Walk) {
	if len(queue) == 0 {
		return "", Walk{}
	}
	in := map[string]bool{}
	for _, p := range queue {
		in[p] = true
	}
	// Continue the pass only if the user is still where the last jump put them.
	if len(prev.Panes) > 0 && now.Sub(prev.At) < walkTTL && prev.Idx < len(prev.Panes) && prev.Panes[prev.Idx] == current {
		n := len(prev.Panes)
		for step := 1; step <= n; step++ {
			cand := prev.Panes[(prev.Idx+step)%n]
			if in[cand] && cand != current {
				return cand, Walk{Panes: prev.Panes, Idx: indexOf(prev.Panes, cand), At: now}
			}
		}
	}
	// A new pass: the entry after the current pane if it is queued, else the first.
	start := 0
	if i := indexOf(queue, current); i >= 0 {
		start = (i + 1) % len(queue)
	}
	target := queue[start]
	return target, Walk{Panes: queue, Idx: start, At: now}
}

func indexOf(list []string, s string) int {
	for i, x := range list {
		if x == s {
			return i
		}
	}
	return -1
}

// Store keeps one walk per client in jump.json.
type Store map[string]Walk

// Load reads the file; absent or corrupt means no walks.
func Load(path string) Store {
	s := Store{}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

func (s Store) Save(path string) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return atomicfile.Write(path, b, 0o600)
}
