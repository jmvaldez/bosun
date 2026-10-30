package ledger

import (
	"time"

	"github.com/jmvaldez/bosun/internal/statusline"
)

type Session struct {
	ID             string
	Account        string
	Repo           string
	Branch         string
	Task           string
	TaskSource     string
	StartedAt      time.Time
	EndedAt        time.Time
	LastActivityAt time.Time
	TotalUSD       float64 // gross reported total, before fork inheritance
	InheritedUSD   float64 // part of TotalUSD that belongs to a fork's parent
	BankedUSD      float64 // spend banked across a /login reset
	ForkedFrom     string
	LaunchID       string
	Trusted        bool // the total comes from Claude Code, not a transcript estimate
	// CoveredFrom is the earliest cost-state startTime; zero means nothing was
	// added back. See finalizeSession (D-21).
	CoveredFrom time.Time
}

// OwnUSD is the one place fork inheritance is subtracted. Every view and rule
// calls it, directly or through SnapshotOwnUSD (LESSONS L-30); the ledger
// stores gross figures only.
func OwnUSD(s Session) float64 {
	return s.TotalUSD + s.BankedUSD - s.InheritedUSD
}

// SnapshotOwnUSD is the live-side inheritance decision: a fork's first status
// line total is what it inherited. Rules and notifications both go through it
// so the answer cannot differ between them.
func SnapshotOwnUSD(snap statusline.Snapshot, forked bool) float64 {
	s := Session{TotalUSD: snap.CostUSD, BankedUSD: snap.BankedUSD}
	if forked {
		s.InheritedUSD = snap.FirstUSD
	}
	return OwnUSD(s)
}

// Request is one priced-at-read API request.
type Request struct {
	MessageID string
	RequestID string
	SessionID string
	PromptID  string
	AgentID   string // empty for the main thread
	Model     string
	TS        time.Time
	Input     int64
	Output    int64
	CacheRead int64
	Cache5m   int64
	Cache1h   int64
	Web       int64
	Speed     string
	Geo       string
}

// Prompt is a numbered user prompt. Only the number and time are kept; the
// prompt text is never read.
type Prompt struct {
	ID        string
	SessionID string
	N         int
	StartedAt time.Time
}

type Subagent struct {
	SessionID string
	AgentID   string
	AgentType string
	FirstAt   time.Time
	LastAt    time.Time
}
