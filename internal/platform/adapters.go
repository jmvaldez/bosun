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

// Capabilities describes what a notification backend can do.
type Capabilities struct {
	Click, Sound, Replace, Retract bool
}

// Notice is one desktop notification. Every value is already rendered from a
// fixed template: no prompt, tool, assistant or error text ever gets here.
type Notice struct {
	Title    string
	Subtitle string
	Body     string
	// Line2 is a second, quieter line under the body. With Markup the backend
	// may style it (italic); otherwise it is plain text.
	Line2  string
	Markup bool
	// Icon is a theme icon name (dialog-question and friends).
	Icon  string
	Sound bool
	// Group names the notice for replace and retract (bosun.<session>.attn).
	Group string
	// ReplaceID is the id of a notice this one replaces; 0 posts a new one.
	ReplaceID uint32
	// Action, when set, is the label of a default action (a click on the notice);
	// backends that cannot offer one ignore it.
	Action string
}

// Notifier posts and retracts desktop notices. Backends run their argv through
// the injected Runner, which always applies a timeout.
type Notifier interface {
	Name() string
	Capabilities() Capabilities
	// Post returns the server's id for the notice (0 when the backend has none).
	Post(Notice) (uint32, error)
	Retract(id uint32, group string) error
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
