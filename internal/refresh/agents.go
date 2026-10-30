package refresh

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/jmvaldez/bosun/internal/clean"
	"github.com/jmvaldez/bosun/internal/platform"
)

// TTL is how long the agents cache stays fresh.
const TTL = 5 * time.Second

// Row is one entry of `claude agents --json` reduced to what bosun uses. The
// raw `name` field is derived from prompt text and is never decoded.
type Row struct {
	SessionID string `json:"session_id"`
	PID       int    `json:"pid,omitempty"`
	// StartTok pins PID to one process lifetime. It is taken when the row is
	// cached, which is as close to the real start as bosun can get.
	StartTok   string `json:"start_token,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Cwd        string `json:"cwd,omitempty"`
	StartedAt  int64  `json:"started_at,omitempty"` // ms since epoch
	Status     string `json:"status,omitempty"`     // busy | waiting | idle (pid rows)
	WaitingFor string `json:"waiting_for,omitempty"`
	// Background rows have no pid and no status (FACTS-CHECK F5); Row.Live
	// says how their liveness is judged (D-11).
	Background bool   `json:"background,omitempty"`
	ID         string `json:"id,omitempty"`
	State      string `json:"state,omitempty"` // working | blocked | done | failed | stopped
}

// Cache is the content of agents.json.
type Cache struct {
	V         int       `json:"v"`
	FetchedAt time.Time `json:"fetched_at"`
	Rows      []Row     `json:"rows"`
}

// rawRow lists only the keys bosun decodes.
type rawRow struct {
	ID         string  `json:"id"`
	SessionID  string  `json:"sessionId"`
	PID        int     `json:"pid"`
	Kind       string  `json:"kind"`
	Cwd        string  `json:"cwd"`
	StartedAt  float64 `json:"startedAt"`
	Status     string  `json:"status"`
	WaitingFor string  `json:"waitingFor"`
	State      string  `json:"state"`
}

// ParseAgents decodes the output of `claude agents --json`. procs, when
// non-nil, supplies start tokens for pid rows.
func ParseAgents(out []byte, procs platform.Procs) ([]Row, error) {
	var raw []rawRow
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("agents --json: %w", err)
	}
	rows := make([]Row, 0, len(raw))
	for _, r := range raw {
		row := Row{
			SessionID: clean.Ident(r.SessionID, 64),
			Kind:      clean.Ident(r.Kind, 32),
			Cwd:       r.Cwd,
			StartedAt: int64(r.StartedAt),
		}
		if row.SessionID == "" {
			continue
		}
		if r.PID > 0 {
			row.PID = r.PID
			// A missing or unknown status counts as busy. FACTS-CHECK F5 never saw a
			// pid row without one.
			row.Status = clean.OneOf(r.Status, "busy", "busy", "waiting", "idle")
			if row.Status == "waiting" {
				row.WaitingFor = clean.OneOf(r.WaitingFor, "other",
					"permission prompt", "input needed", "sandbox request", "worker request", "dialog open")
			}
			if procs != nil {
				row.StartTok, _ = procs.StartToken(r.PID)
			}
		} else {
			row.Background = true
			row.ID = clean.Ident(r.ID, 64)
			row.State = clean.OneOf(r.State, "", "working", "blocked", "done", "failed", "stopped")
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// Live reports whether the row's agent session is still running. Background
// rows have no process to probe, so only their state counts; pid rows are
// checked with kill -0 plus the start token.
func (r Row) Live(procs platform.Procs) bool {
	if r.Background {
		return r.State == "working" || r.State == "blocked"
	}
	return procs.Alive(r.PID, r.StartTok)
}

// IdleSignal reports whether the row says the agent is idle, which is what
// lets an evaluator drop a stale `working` state. Background rows never
// qualify: their `done` is a task outcome, not an idle prompt (D-11).
func (r Row) IdleSignal() bool {
	return !r.Background && r.Status == "idle"
}

// ReadCache loads the cache and reports whether it is older than ttl. A missing
// or unreadable cache is stale and empty, never an error, so callers simply
// spawn a refresh.
func ReadCache(path string, now time.Time, ttl time.Duration) (c Cache, stale bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Cache{}, true
	}
	if json.Unmarshal(b, &c) != nil {
		return Cache{}, true
	}
	return c, now.Sub(c.FetchedAt) > ttl
}
