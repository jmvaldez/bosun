package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jmvaldez/bosun/internal/live"
	"github.com/jmvaldez/bosun/internal/paths"
	"github.com/jmvaldez/bosun/internal/task"
	"github.com/jmvaldez/bosun/internal/worktree"
)

func (a *app) parser() *task.Parser {
	cfg := a.loadConfig()
	p, err := task.New(cfg.Task.KeyRegex, cfg.Task.Prefixes)
	if err != nil {
		return task.Default()
	}
	return p
}

// tag sets or clears a session's task. The tag outranks the key the hook found
// in env or branch, on every surface (segment, popup, ledger, spend).
// task.Parser.Tag normalises the text.
func (a *app) tag(args []string) int {
	var words []string
	var session string
	clear := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--clear":
			clear = true
		case "--session":
			if i+1 >= len(args) {
				fmt.Fprintln(a.errw, "bosun tag: --session needs a value")
				return 2
			}
			i++
			session = args[i]
		default:
			if strings.HasPrefix(args[i], "-") {
				fmt.Fprintf(a.errw, "bosun tag: unexpected argument %q\n", args[i])
				return 2
			}
			words = append(words, args[i])
		}
	}
	if clear == (len(words) > 0) {
		fmt.Fprintln(a.errw, "usage: bosun tag TEXT [--session ID] | bosun tag --clear [--session ID]")
		return 2
	}
	key := ""
	if !clear {
		var err error
		if key, err = a.parser().Tag(strings.Join(words, " ")); err != nil {
			fmt.Fprintf(a.errw, "bosun tag: %v (up to %d bytes, no control characters)\n", err, task.MaxTagBytes)
			return 1
		}
	}
	if session == "" {
		pane := a.ad.Getenv("TMUX_PANE")
		if pane == "" {
			fmt.Fprintln(a.errw, "bosun tag: no --session given and $TMUX_PANE is not set")
			return 1
		}
		var best *live.Session
		for _, s := range live.Read(a.ad.Paths.Live()).Sessions {
			if !s.Ended && s.Loc.Pane == pane && (best == nil || s.LastEvent.After(best.LastEvent)) {
				best = s
			}
		}
		if best == nil {
			fmt.Fprintf(a.errw, "bosun tag: no live session in pane %s\n", pane)
			return 1
		}
		session = best.ID
	}
	if !paths.ValidSessionID(session) {
		fmt.Fprintf(a.errw, "bosun tag: %q is not a session id\n", session)
		return 2
	}
	if a.ad.Paths.State == "" {
		fmt.Fprintln(a.errw, "bosun tag: no state dir (HOME unset?)")
		return 1
	}
	if _, err := os.Stat(a.ad.Paths.Session(session)); err != nil {
		fmt.Fprintf(a.errw, "bosun tag: unknown session %s\n", session)
		return 1
	}
	if err := task.WriteTag(a.ad.Paths.Tag(session), key); err != nil {
		fmt.Fprintf(a.errw, "bosun tag: %v\n", err)
		return 1
	}
	if clear {
		fmt.Fprintf(a.out, "cleared the tag on %s\n", short(session))
	} else {
		fmt.Fprintf(a.out, "tagged %s %s\n", short(session), key)
	}
	return 0
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// shellQuote single-quotes s for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// run opens a tasked Claude Code session in a new tmux window. The command goes
// through `$SHELL -ic` so the user's launcher (an alias or function) applies,
// and PATH is exported again after the rc file, which may reset it: without
// that, hooks cannot find bosun and every event is lost (LESSONS L-02).
func (a *app) run(args []string) int {
	var taskArg, dir, branch string
	var claudeArgs []string
	for i := 0; i < len(args); i++ {
		next := func() (string, bool) {
			if i+1 < len(args) {
				i++
				return args[i], true
			}
			return "", false
		}
		var ok bool
		switch args[i] {
		case "--task":
			taskArg, ok = next()
		case "--dir":
			dir, ok = next()
		case "--worktree":
			branch, ok = next()
		case "--":
			claudeArgs = args[i+1:]
			i = len(args)
			ok = true
		default:
			fmt.Fprintf(a.errw, "bosun run: unexpected argument %q\n", args[i])
			return 2
		}
		if !ok {
			fmt.Fprintf(a.errw, "bosun run: %s needs a value\n", args[i])
			return 2
		}
	}
	if a.ad.Getenv("TMUX") == "" {
		fmt.Fprintln(a.errw, "bosun run: run it inside tmux (no $TMUX); it opens a new window")
		return 1
	}
	cfg := a.loadConfig()
	tc := a.tmuxClient("", "")
	if dir == "" {
		if pane := a.ad.Getenv("TMUX_PANE"); pane != "" {
			dir, _ = tc.PanePath(pane)
		}
		if dir == "" {
			dir, _ = os.Getwd()
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		fmt.Fprintf(a.errw, "bosun run: %v\n", err)
		return 1
	}
	dir = abs

	key := ""
	if taskArg != "" {
		var err error
		if key, err = a.parser().Tag(taskArg); err != nil {
			fmt.Fprintf(a.errw, "bosun run: --task: %v (up to %d bytes, no control characters)\n", err, task.MaxTagBytes)
			return 2
		}
	}
	if branch != "" {
		res, err := worktree.Add(a.ad.Runner, dir, branch, cfg.Run.WorktreeDir)
		if err != nil {
			fmt.Fprintf(a.errw, "bosun run: %v\n", err)
			return 1
		}
		fmt.Fprintf(a.out, "worktree %s\n", res.Path)
		dir = res.Path
		if key == "" { // the branch feeds task capture
			key = a.parser().Key(branch)
		}
	}
	if key == "" {
		fmt.Fprintln(a.errw, "bosun run: needs --task TEXT, or a --worktree branch that contains an issue key")
		return 2
	}

	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(a.errw, "bosun run: %v\n", err)
		return 1
	}
	launcher := cfg.Claude.Launcher
	if launcher == "" {
		launcher = "claude"
	}
	inner := "export PATH=" + shellQuote(filepath.Dir(exe)) + ":$PATH; export BOSUN_TASK=" + shellQuote(key) + "; " + launcher
	for _, c := range claudeArgs {
		inner += " " + shellQuote(c)
	}
	shell := a.ad.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	command := shellQuote(shell) + " -ic " + shellQuote(inner)
	if err := tc.NewNamedWindow(dir, key, []string{"BOSUN_TASK=" + key}, command); err != nil {
		fmt.Fprintf(a.errw, "bosun run: tmux: %v\n", err)
		return 1
	}
	fmt.Fprintf(a.out, "opened window %s in %s\n", key, dir)
	return 0
}
