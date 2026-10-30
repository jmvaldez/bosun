package platform

import (
	"time"

	"github.com/jmvaldez/bosun/internal/paths"
	"github.com/jmvaldez/bosun/internal/runner"
)

// Adapters is everything main builds once and passes down; there are no
// package globals. Identity is wired later, only by commands off the hot path
// (withIdentity in commands.go).
type Adapters struct {
	Paths    paths.Paths
	Kind     Kind
	Procs    Procs
	Identity Identity
	Runner   runner.Runner
	Clock    Clock
	Spawner  Spawner
	Getenv   func(string) string
}

// Procs inspects processes. Tokens are opaque: compare them, never parse them.
type Procs interface {
	// StartToken identifies one process lifetime so a reused pid is caught.
	StartToken(pid int) (string, error)
	// StartTime is the wall-clock start. Only tests call it, to check StartToken
	// against ps (LESSONS L-11).
	StartTime(pid int) (time.Time, error)
	// Parent returns the parent pid, for pane ancestry checks.
	Parent(pid int) (int, error)
	// Alive reports whether pid exists and, when token is non-empty, is still
	// the same process. EPERM counts as alive.
	Alive(pid int, token string) bool
}

// Verdict is the answer to "is the user looking at this pane".
type Verdict int

const (
	Unknown Verdict = iota
	Focused
	NotFocused
)

// Focus is implemented by tmux.Client; its Check states the verdict rules.
type Focus interface {
	Check(pane string) Verdict
}

// Identity resolves the local account key.
type Identity interface {
	Account() (string, error)
}

// Clock exists so rules and tests can run on a fake clock.
type Clock interface {
	Now() time.Time
}

type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now() }
