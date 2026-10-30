// Package live holds bosun's per-session state: the event fold that turns
// events.jsonl lines into attention states, and the live.json file the
// evaluator keeps between runs.
package live

import (
	"encoding/json"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jmvaldez/bosun/internal/atomicfile"
	"github.com/jmvaldez/bosun/internal/subcost"
)

// Attention states (SPEC section 5), set by the event fold. Runaway and Quiet
// below are display-only.
const (
	Idle     = "idle"
	Working  = "working"
	NeedsYou = "needs-you"
	Done     = "done"
	// Runaway is a display state, not an attention state: a session with an
	// active, undismissed runaway alert shows as runaway unless it needs you.
	Runaway = "runaway"
	// Quiet is a display state too: a working session with no events for the
	// quiet rule's window, shown purple instead of counted as a runaway.
	Quiet = "quiet"
)

// Locator is hook.TmuxLoc as stored in live.json.
type Locator struct {
	Sock string `json:"sock,omitempty"`
	SPID int    `json:"spid,omitempty"`
	Pane string `json:"pane,omitempty"`
}

// Sub is one running subagent.
type Sub struct {
	Type  string    `json:"type"`
	Start time.Time `json:"start"`
	Tools int       `json:"tools"`
	// Cost reads the subagent's transcript incrementally; CostUSD is its
	// estimate, always shown as est.
	Cost    *subcost.Tracker `json:"cost,omitempty"`
	CostUSD float64          `json:"cost_usd,omitempty"`
}

// Session is everything the evaluator knows about one agent session.
type Session struct {
	ID         string    `json:"id"`
	State      string    `json:"state"`
	StateSince time.Time `json:"state_since"`
	Loc        Locator   `json:"loc"`
	PID        int       `json:"pid,omitempty"`
	StartTok   string    `json:"start_token,omitempty"`

	Cwd     string `json:"cwd,omitempty"`
	Repo    string `json:"repo,omitempty"`
	Branch  string `json:"branch,omitempty"`
	Task    string `json:"task,omitempty"`
	TaskSrc string `json:"task_src,omitempty"`
	// BaseTask and BaseSrc are the SessionStart task (env or branch). Task is
	// BaseTask unless `bosun tag` has set one, which outranks it.
	BaseTask   string `json:"base_task,omitempty"`
	BaseSrc    string `json:"base_src,omitempty"`
	Account    string `json:"account,omitempty"`
	Entrypoint string `json:"entrypoint,omitempty"`
	Transcript string `json:"transcript,omitempty"`

	LastEvent  time.Time `json:"last_event"`
	LastTool   string    `json:"last_tool,omitempty"`
	LastToolAt time.Time `json:"last_tool_at,omitempty"`
	// Edits counts the main thread's editTools calls since the turn began;
	// TurnStart is when the turn began.
	Edits     int       `json:"edits"`
	TurnStart time.Time `json:"turn_start,omitempty"`
	Failed    bool      `json:"failed,omitempty"`

	// BGPending is true while a background task the last Stop reported is still
	// running; BGIDs are those tasks. It holds the session in working.
	BGPending bool `json:"bg_pending,omitempty"`
	// TurnCarry is set when a Stop still had running background tasks: the next
	// UserPromptSubmit continues that turn even if every task has already ended.
	TurnCarry bool            `json:"turn_carry,omitempty"`
	BGIDs     []string        `json:"bg_ids,omitempty"`
	Subs      map[string]*Sub `json:"subs,omitempty"`
	SubsEnded int             `json:"subs_ended,omitempty"`

	NeedsAID string `json:"needs_aid,omitempty"`
	// WaitKind is "permission" or "question": which prompt is waiting. An enum;
	// the text of either is only ever read from the transcript at render time.
	WaitKind    string    `json:"wait_kind,omitempty"`
	LastPermReq time.Time `json:"last_perm_req,omitempty"`
	LastLeave   time.Time `json:"last_leave,omitempty"`

	// agents.json reconciliation. AgentsFetch is the last fetch applied; the
	// Reads fields count distinct fetches, not evaluations.
	AgentsFetch     time.Time `json:"agents_fetch,omitempty"`
	NotWaitingReads int       `json:"not_waiting_reads,omitempty"`
	MissingReads    int       `json:"missing_reads,omitempty"`
	WaitingFor      string    `json:"waiting_for,omitempty"`

	Ended bool `json:"ended,omitempty"`
	// Offset is how many bytes of events.jsonl are folded: complete lines only.
	Offset int64 `json:"offset"`

	// PaneGone is recomputed every evaluation; it is stored so a failed tmux
	// call can fall back to the last known answer.
	PaneGone bool `json:"pane_gone,omitempty"`
	// Foreign marks a session whose pane belongs to another tmux server than the
	// one evaluating: unknown to this pass, not gone.
	Foreign bool `json:"foreign,omitempty"`

	// ToolOpen is set between a main-thread PreToolUse and its PostToolUse: a
	// long single tool call is not "quiet".
	ToolOpen bool `json:"tool_open,omitempty"`
	// Source is the latest SessionStart source, one of the values hook.source
	// allows.
	Source string `json:"source,omitempty"`
	// Forked is sticky: a session that started as a fork stays one through
	// later SessionStarts (compact, resume), which overwrite Source.
	Forked bool `json:"forked,omitempty"`
	// Runaway is recomputed each evaluation from alerts.json: any undismissed
	// session or subagent alert except quiet. QuietAlert is the quiet rule on its own, shown as its own state.
	Runaway    bool `json:"runaway,omitempty"`
	QuietAlert bool `json:"quiet_alert,omitempty"`
}

// Display is the state shown on every surface: needs-you hides any alert;
// otherwise a runaway alert shows as runaway, and a quiet alert on a working
// session as quiet.
func (s *Session) Display() string {
	if s.State != NeedsYou {
		if s.Runaway {
			return Runaway
		}
		if s.QuietAlert && s.State == Working {
			return Quiet
		}
	}
	return s.State
}

// Interactive reports whether a person is at this session (D-10): sessions
// started by the SDK or `claude -p` never produce a needs-you notice. An
// unknown entrypoint counts as interactive only if a status line ever ran.
func (s *Session) Interactive(statuslineSeen bool) bool {
	switch {
	case strings.HasPrefix(s.Entrypoint, "sdk-"):
		return false
	case s.Entrypoint == "":
		return statuslineSeen
	}
	return true
}

// Visible reports whether the session counts anywhere: it has not ended, runs
// in a live pane, and is known to tmux at all.
func (s *Session) Visible() bool {
	return !s.Ended && !s.PaneGone && s.Loc.Pane != ""
}

// File is live.json.
type File struct {
	V         int                 `json:"v"`
	UpdatedAt time.Time           `json:"updated_at"`
	Sessions  map[string]*Session `json:"sessions"`
	// RefreshTried is when the evaluator last spawned `bosun refresh` (see
	// eval.refreshRetry).
	RefreshTried time.Time `json:"refresh_tried,omitempty"`
}

// Read loads live.json; a missing or corrupt file is an empty one.
func Read(path string) File {
	f := File{V: 1, Sessions: map[string]*Session{}}
	b, err := os.ReadFile(path)
	if err != nil {
		return f
	}
	var got File
	if json.Unmarshal(b, &got) != nil || got.Sessions == nil {
		return f
	}
	return got
}

// Write stores live.json atomically.
func Write(path string, f File) error {
	f.V = 1
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return atomicfile.Write(path, b, 0o600)
}

// Marks is marks.json: the last marker written per window, per tmux server,
// so evaluators of two servers sharing a state dir do not strand each other's
// markers. ServerPID and Windows are the pre-per-server shape, still read.
type Marks struct {
	ServerPID int                          `json:"server_pid,omitempty"`
	Windows   map[string]string            `json:"windows,omitempty"`
	Servers   map[string]map[string]string `json:"servers,omitempty"`
}

// For returns the windows marked on one server; empty when none are known.
func (m Marks) For(server int) map[string]string {
	if w, ok := m.Servers[strconv.Itoa(server)]; ok {
		return w
	}
	if m.ServerPID == server && m.Windows != nil {
		return m.Windows
	}
	return map[string]string{}
}

// ReadMarks loads marks.json; absent or corrupt means nothing written.
func ReadMarks(path string) Marks {
	m := Marks{}
	b, err := os.ReadFile(path)
	if err != nil {
		return m
	}
	var got Marks
	if json.Unmarshal(b, &got) != nil {
		return m
	}
	return got
}

// WriteMarks stores marks.json atomically.
func WriteMarks(path string, m Marks) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return atomicfile.Write(path, b, 0o600)
}

// Rank orders states for display precedence: needs-you first.
func Rank(state string) int {
	switch state {
	case NeedsYou:
		return 0
	case Runaway:
		return 1
	case Done:
		return 2
	case Working:
		return 3
	case Quiet:
		return 4
	}
	return 5
}

// Queue returns jump's waiting queue (SPEC section 7, Jump): needs-you oldest
// first (by time in state), then done oldest first. Working and idle are never
// targets, nor is a done session carrying a runaway alert (Display shows it as
// runaway). A needs-you session is always a target: Display lets needs-you
// hide any alert.
func Queue(sessions map[string]*Session) []*Session {
	var q []*Session
	for _, s := range sessions {
		if s.Visible() && (s.State == NeedsYou || (s.State == Done && !s.Runaway)) {
			q = append(q, s)
		}
	}
	sort.Slice(q, func(i, j int) bool {
		if q[i].State != q[j].State {
			return q[i].State == NeedsYou
		}
		if !q[i].StateSince.Equal(q[j].StateSince) {
			return q[i].StateSince.Before(q[j].StateSince)
		}
		return q[i].ID < q[j].ID
	})
	return q
}
