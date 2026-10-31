package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jmvaldez/bosun/internal/config"
	"github.com/jmvaldez/bosun/internal/shell"
	"github.com/jmvaldez/bosun/internal/task"
	"github.com/jmvaldez/bosun/internal/tmux"
	"github.com/jmvaldez/bosun/internal/worktree"
)

// runTask opens a tasked Claude Code session in a new tmux window.
func (a *app) runTask(args []string) int {
	var taskArg, dir, branch string
	var claudeArgs []string
	p := a.args("run", args)
	for arg, ok := p.next(); ok; arg, ok = p.next() {
		var got bool
		switch arg {
		case "--task":
			taskArg, got = p.value()
		case "--dir":
			dir, got = p.value()
		case "--worktree":
			branch, got = p.value()
		case "--":
			claudeArgs, got = p.rest(), true
		default:
			return p.unknown()
		}
		if !got {
			return 2
		}
	}
	if a.ad.Getenv("TMUX") == "" {
		fmt.Fprintln(a.errw, "bosun run: run it inside tmux (no $TMUX); it opens a new window")
		return 1
	}
	cfg := a.loadConfig()
	tc := a.tmuxClient("", "")
	dir, err := a.resolveDir(tc, dir)
	if err != nil {
		fmt.Fprintf(a.errw, "bosun run: %v\n", err)
		return 1
	}

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

	if err := a.openTaskWindow(tc, cfg, dir, key, nil, claudeArgs); err != nil {
		fmt.Fprintf(a.errw, "bosun run: %v\n", err)
		return 1
	}
	return 0
}

// openTaskWindow opens a tasked Claude Code window at dir. env holds extra
// NAME=value entries for the window beside BOSUN_TASK, and claudeArgs follow
// the launcher on its command line. `bosun run` and `bosun handoff` share it.
func (a *app) openTaskWindow(tc tmux.Client, cfg config.Config, dir, key string, env, claudeArgs []string) error {
	exe, err := a.exe()
	if err != nil {
		return err
	}
	launcher := cfg.Claude.Launcher
	if launcher == "" {
		launcher = "claude"
	}
	command := launchCommand(exe, launcher, key, a.ad.Getenv("SHELL"), claudeArgs)
	if err := tc.NewNamedWindow(dir, key, append([]string{"BOSUN_TASK=" + key}, env...), command); err != nil {
		return fmt.Errorf("tmux: %w", err)
	}
	fmt.Fprintf(a.out, "opened window %s in %s\n", key, dir)
	return nil
}

// resolveDir picks the window's directory: dir if given, else the invoking
// pane's path, else the working directory, made absolute.
func (a *app) resolveDir(tc tmux.Client, dir string) (string, error) {
	if dir == "" {
		if pane := a.ad.Getenv("TMUX_PANE"); pane != "" {
			dir, _ = tc.PanePath(pane)
		}
		if dir == "" {
			dir, _ = os.Getwd()
		}
	}
	return filepath.Abs(dir)
}

// launchCommand builds the command for the new window. It goes through
// `$SHELL -ic` so the user's launcher (an alias or function) applies, and PATH
// is exported again after the rc file, which may reset it: without that, hooks
// cannot find bosun and every event is lost (LESSONS L-02).
func launchCommand(exe, launcher, key, loginShell string, claudeArgs []string) string {
	inner := "export PATH=" + shell.Quote(filepath.Dir(exe)) + ":$PATH; export BOSUN_TASK=" + shell.Quote(key) + "; " + launcher
	for _, c := range claudeArgs {
		inner += " " + shell.Quote(c)
	}
	if loginShell == "" {
		loginShell = "/bin/sh"
	}
	return shell.Quote(loginShell) + " -ic " + shell.Quote(inner)
}
