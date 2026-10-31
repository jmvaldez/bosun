package handoff

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jmvaldez/bosun/internal/config"
	"github.com/jmvaldez/bosun/internal/hook"
	"github.com/jmvaldez/bosun/internal/live"
	"github.com/jmvaldez/bosun/internal/panewrite"
	"github.com/jmvaldez/bosun/internal/paths"
	"github.com/jmvaldez/bosun/internal/platform"
)

//go:embed prompt.md
var defaultPrompt string

const (
	pollEvery       = 500 * time.Millisecond
	defaultDeadline = 5 * time.Minute
)

// Clock is the time source Run waits on; tests replace it with a fake that
// advances only when Run sleeps.
type Clock interface {
	Now() time.Time
	Sleep(time.Duration)
}

// Deps are Run's collaborators, all injected.
type Deps struct {
	Clock Clock
	Tmux  panewrite.Writer
	Procs platform.Procs
	Paths paths.Paths
	Cfg   config.Handoff
	// Live reads the evaluator's current view of the sessions.
	Live func() live.File
	// Events reads a session's recent events, oldest first.
	Events func(sid string) []hook.Event
	// Stat reports the doc's modification time and size.
	Stat func(path string) (mtime time.Time, size int64, err error)
	// Doc picks the doc's path for the session and creates its directory.
	Doc func(s *live.Session) (string, error)
	// Launch opens the new window: the session's dir and task, the old session
	// id and the doc the new session reads first.
	Launch func(dir, task, sid, doc string) error
	// Force lets a session that is mid-turn be handed off; the caller has asked
	// the person first.
	Force bool
}

// Prompt is the text typed into the session: the template with {{doc}} filled
// in. A configured handoff.prompt replaces the embedded template.
func Prompt(cfg config.Handoff, doc string) string {
	tmpl := defaultPrompt
	if cfg.Prompt != "" {
		tmpl = cfg.Prompt
	}
	return strings.ReplaceAll(tmpl, "{{doc}}", doc)
}

// Eligible says why a session cannot be handed off now, or returns nil. Only a
// done or idle session qualifies; force also allows one that is working.
func Eligible(s *live.Session, force bool) error {
	switch {
	case s == nil || !s.Visible():
		return errors.New("no such session in a live pane")
	case s.State == live.NeedsYou:
		return errors.New("session is waiting on you; answer it first")
	case s.State == live.Working && !force:
		return errors.New("session is mid-turn; use --force to hand off anyway")
	case s.Task == "":
		return errors.New("session has no task; set one with `bosun tag`")
	}
	return nil
}

// ErrRunning is returned when a handoff for the session is already under way.
var ErrRunning = errors.New("handoff already running")

// Run hands one session off and returns the final state. A session that cannot
// be handed off, or one already being handed off, returns an error and writes
// nothing. Once started, every outcome is written to handoff.json and
// appended to the log; a failure returns the failed state with an error too.
func Run(d Deps, sid string) (State, error) {
	now := d.Clock.Now()
	path := d.Paths.Handoff(sid)
	if prev := Read(path); prev.Active(now) {
		return prev, ErrRunning
	}
	s := d.Live().Sessions[sid]
	if err := Eligible(s, d.Force); err != nil {
		return State{}, err
	}
	doc, err := d.Doc(s)
	if err != nil {
		return State{}, err
	}
	deadline := d.Cfg.Deadline
	if deadline <= 0 {
		deadline = defaultDeadline
	}
	st := State{Phase: PhaseWriting, Started: now, Pasted: now, Deadline: now.Add(deadline), Doc: doc}
	reached := PhaseWriting
	save := func() { _ = Write(path, st) }
	fail := func(why string) (State, error) {
		st.Phase, st.Why = PhaseFailed, why
		save()
		d.logOutcome(st, sid, reached)
		return st, fmt.Errorf("handoff failed: %s", why)
	}
	save()

	r, err := panewrite.Submit(panewrite.Deps{
		Tmux: d.Tmux, Procs: d.Procs, Sleep: d.Clock.Sleep,
		Submitted: func(since time.Time) bool { return seen(d.Events(sid), "UserPromptSubmit", since) },
	}, panewrite.Target{Pane: s.Loc.Pane, ServerPID: s.Loc.SPID, PID: s.PID, StartTok: s.StartTok},
		Prompt(d.Cfg, doc), st.Pasted)
	switch {
	case err != nil:
		return fail(WhyNotSubmitted)
	case r.Why == panewrite.NotSent:
		return fail(WhyPaneGone)
	case !r.Submitted:
		return fail(WhyNotSubmitted)
	}

	prev := d.Clock.Now()
	for {
		d.Clock.Sleep(pollEvery)
		now = d.Clock.Now()
		dt := now.Sub(prev)
		prev = now
		if cur := Read(path); cur.Phase == PhaseFailed {
			st = cur
			d.logOutcome(st, sid, reached)
			return st, fmt.Errorf("handoff failed: %s", st.Why)
		}
		cur := d.Live().Sessions[sid]
		switch {
		case cur == nil || !cur.Visible():
			return fail(WhyPaneGone)
		case cur.State == live.NeedsYou:
			st.PausedFor += dt
			save()
			continue
		}
		if seen(d.Events(sid), "Stop", st.Pasted) {
			if mtime, size, err := d.Stat(doc); err != nil || size <= 0 || !mtime.After(st.Pasted) {
				return fail(WhyDocStale)
			}
			break
		}
		if now.After(st.Deadline.Add(st.PausedFor)) {
			return fail(WhyDeadline)
		}
	}

	if cur := Read(path); cur.Phase == PhaseFailed {
		st = cur
		d.logOutcome(st, sid, reached)
		return st, fmt.Errorf("handoff failed: %s", st.Why)
	}
	st.Phase, reached = PhaseLaunching, PhaseLaunching
	save()
	if err := d.Launch(s.Cwd, s.Task, sid, doc); err != nil {
		return fail(WhyLaunchFailed)
	}
	st.Phase, st.NewWindow = PhaseDone, s.Task
	save()
	d.logOutcome(st, sid, reached)
	return st, nil
}

// Cancel marks an active handoff failed with the reason cancelled; the running
// process notices on its next poll. It errors when none is running.
func Cancel(p paths.Paths, sid string, now time.Time) error {
	path := p.Handoff(sid)
	st := Read(path)
	if !st.Active(now) {
		return errors.New("no handoff running for this session")
	}
	st.Phase, st.Why = PhaseFailed, WhyCancelled
	return Write(path, st)
}

// seen reports whether the main thread has an event named ev after since.
func seen(evs []hook.Event, ev string, since time.Time) bool {
	for _, e := range evs {
		if e.Ev != ev || e.AID != "" {
			continue
		}
		if ts, err := time.Parse(time.RFC3339Nano, e.TS); err == nil && ts.After(since) {
			return true
		}
	}
	return false
}

// logEntry is one line of handoff.jsonl: ids, enums and numbers only.
type logEntry struct {
	TS       string `json:"ts"`
	Session  string `json:"session"`
	Outcome  string `json:"outcome"`
	Why      string `json:"why,omitempty"`
	Reached  string `json:"phase_reached"`
	DurMS    int64  `json:"dur_ms"`
	PausedMS int64  `json:"paused_ms"`
}

// logOutcome appends the outcome to handoff.jsonl. A log that cannot be
// written never fails a handoff.
func (d Deps) logOutcome(st State, sid, reached string) {
	path := d.Paths.HandoffLog()
	if path == "" {
		return
	}
	now := d.Clock.Now()
	e := logEntry{TS: now.UTC().Format(time.RFC3339Nano), Session: sid, Outcome: "ok", Why: st.Why, Reached: reached,
		DurMS: now.Sub(st.Started).Milliseconds(), PausedMS: st.PausedFor.Milliseconds()}
	if st.Phase == PhaseFailed {
		e.Outcome = "failed"
	}
	line, err := json.Marshal(e)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}
