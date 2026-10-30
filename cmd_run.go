package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jmvaldez/bosun/internal/shell"
	"github.com/jmvaldez/bosun/internal/task"
	"github.com/jmvaldez/bosun/internal/worktree"
)

// runTask opens a tasked Claude Code session in a new tmux window. The command goes
// through `$SHELL -ic` so the user's launcher (an alias or function) applies,
// and PATH is exported again after the rc file, which may reset it: without
// that, hooks cannot find bosun and every event is lost (LESSONS L-02).
func (a *app) runTask(args []string) int {
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

	exe, err := a.exe()
	if err != nil {
		fmt.Fprintf(a.errw, "bosun run: %v\n", err)
		return 1
	}
	launcher := cfg.Claude.Launcher
	if launcher == "" {
		launcher = "claude"
	}
	inner := "export PATH=" + shell.Quote(filepath.Dir(exe)) + ":$PATH; export BOSUN_TASK=" + shell.Quote(key) + "; " + launcher
	for _, c := range claudeArgs {
		inner += " " + shell.Quote(c)
	}
	loginShell := a.ad.Getenv("SHELL")
	if loginShell == "" {
		loginShell = "/bin/sh"
	}
	command := shell.Quote(loginShell) + " -ic " + shell.Quote(inner)
	if err := tc.NewNamedWindow(dir, key, []string{"BOSUN_TASK=" + key}, command); err != nil {
		fmt.Fprintf(a.errw, "bosun run: tmux: %v\n", err)
		return 1
	}
	fmt.Fprintf(a.out, "opened window %s in %s\n", key, dir)
	return 0
}
