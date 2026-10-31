package main

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jmvaldez/bosun/internal/handoff"
	"github.com/jmvaldez/bosun/internal/hook"
	"github.com/jmvaldez/bosun/internal/live"
	"github.com/jmvaldez/bosun/internal/platform"
)

const handoffUsage = "bosun handoff <sid|task> [--force] [--cancel] [--wait]"

// handoff hands a session off: it asks the session to write a handoff doc and
// opens a new window on the same task that starts from it. By default it starts
// a detached child to do the waiting and returns; --wait does the whole job in
// the foreground (tests and the L2 contract test use it). --cancel stops a
// handoff under way.
func (a *app) handoff(args []string) int {
	var target string
	var force, cancel, wait bool
	p := a.args("handoff", args)
	for arg, ok := p.next(); ok; arg, ok = p.next() {
		switch {
		case arg == "--force":
			force = true
		case arg == "--cancel":
			cancel = true
		case arg == "--wait":
			wait = true
		case strings.HasPrefix(arg, "-"):
			return p.unknown()
		case target == "":
			target = arg
		default:
			return p.usage(handoffUsage)
		}
	}
	if target == "" {
		return p.usage(handoffUsage)
	}
	s, err := findSession(live.Read(a.ad.Paths.Live()), target)
	if err != nil {
		fmt.Fprintf(a.errw, "bosun handoff: %v\n", err)
		return 1
	}
	if cancel {
		if err := handoff.Cancel(a.ad.Paths, s.ID, a.ad.Clock.Now()); err != nil {
			fmt.Fprintf(a.errw, "bosun handoff: %v\n", err)
			return 1
		}
		fmt.Fprintf(a.out, "handoff cancelled: %s\n", s.Task)
		return 0
	}
	if a.ad.Getenv("TMUX") == "" {
		fmt.Fprintln(a.errw, "bosun handoff: run it inside tmux (no $TMUX); it opens a new window")
		return 1
	}
	if wait {
		return a.handoffNow(s.ID, force)
	}
	task, err := a.startHandoff(s.ID, force)
	if err != nil {
		fmt.Fprintf(a.errw, "bosun handoff: %v\n", err)
		return 1
	}
	fmt.Fprintf(a.out, "handoff started: %s\n", task)
	return 0
}

// startHandoff checks what can be checked now (the session may be handed off,
// none is running, the doc has a place to go) and then starts the detached
// child that waits for the doc. The child's own output goes nowhere, so a
// refusal it would only find later has to be caught here. It returns the task.
func (a *app) startHandoff(sid string, force bool) (string, error) {
	f := live.Read(a.ad.Paths.Live())
	s := f.Sessions[sid]
	if err := handoff.Eligible(s, force); err != nil {
		return "", err
	}
	if handoff.Read(a.ad.Paths.Handoff(sid)).Active(a.ad.Clock.Now()) {
		return "", handoff.ErrRunning
	}
	if _, err := handoff.Locate(a.ad.Runner, s.Cwd, sid, a.loadConfig().Handoff.Dir); err != nil {
		return "", err
	}
	exe, err := a.exe()
	if err != nil || a.ad.Spawner == nil {
		return "", errors.New("cannot start a background process")
	}
	args := []string{"handoff", sid, "--wait"}
	if force {
		args = append(args, "--force")
	}
	if err := a.ad.Spawner.Spawn(exe, args...); err != nil {
		return "", err
	}
	return s.Task, nil
}

// handoffNow runs the handoff in the foreground.
func (a *app) handoffNow(sid string, force bool) int {
	cfg := a.loadConfig()
	tc := a.tmuxClient("", "")
	d := handoff.Deps{
		Clock: sysClock{a.ad.Clock},
		Tmux:  tc,
		Procs: a.ad.Procs,
		Paths: a.ad.Paths,
		Cfg:   cfg.Handoff,
		// The evaluator folds events into live.json; the wait loop needs to see
		// needs-you and a vanished pane as they happen.
		Live: func() live.File {
			a.evaluate(false)
			return live.Read(a.ad.Paths.Live())
		},
		Events: func(sid string) []hook.Event { return handoff.ReadEvents(a.ad.Paths.Events(sid)) },
		Stat:   handoff.Stat,
		Doc: func(s *live.Session) (string, error) {
			return handoff.Locate(a.ad.Runner, s.Cwd, s.ID, cfg.Handoff.Dir)
		},
		Launch: func(dir, task, from, doc string) error {
			return a.openTaskWindow(tc, cfg, dir, task, []string{"BOSUN_HANDOFF_FROM=" + from},
				[]string{"Read " + doc + " and continue the work it describes."})
		},
		Force: force,
	}
	st, err := handoff.Run(d, sid)
	if err != nil {
		fmt.Fprintf(a.errw, "bosun handoff: %v\n", err)
		return 1
	}
	fmt.Fprintf(a.out, "handoff done: %s\n", st.NewWindow)
	return 0
}

// findSession resolves a session id, an unambiguous id prefix, or a task, among
// the sessions in live panes.
func findSession(f live.File, target string) (*live.Session, error) {
	if s := f.Sessions[target]; s != nil {
		return s, nil
	}
	var byPrefix, byTask []*live.Session
	for id, s := range f.Sessions {
		switch {
		case strings.HasPrefix(id, target):
			byPrefix = append(byPrefix, s)
		case s.Visible() && strings.EqualFold(s.Task, target):
			byTask = append(byTask, s)
		}
	}
	for _, m := range [][]*live.Session{byPrefix, byTask} {
		switch len(m) {
		case 0:
			continue
		case 1:
			return m[0], nil
		}
		return nil, fmt.Errorf("%q matches %d sessions; use a session id", target, len(m))
	}
	return nil, fmt.Errorf("no session matches %q", target)
}

// sysClock is the real clock Run waits on: the app's clock for Now.
type sysClock struct{ platform.Clock }

func (sysClock) Sleep(d time.Duration) { time.Sleep(d) }
