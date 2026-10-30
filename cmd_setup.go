package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/jmvaldez/bosun/internal/doctor"
	"github.com/jmvaldez/bosun/internal/install"
	"github.com/jmvaldez/bosun/internal/tmux"
)

// setup is the first-run flow (D-31): one question, then the plugin, the tmux
// conf and, if the answer is yes, the status line wrap.
func (a *app) setup(args []string) int {
	rc, _ := a.runSetup(args)
	return rc
}

// runSetup is setup, also reporting whether the question was cancelled, in
// which case nothing ran after it.
func (a *app) runSetup(args []string) (rc int, cancelled bool) {
	opt := install.Options{Version: version}
	var so install.SetupOptions
	answer := func(v bool) bool {
		if so.Answer != nil {
			return false
		}
		so.Answer = &v
		return true
	}
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "--yes":
			if !answer(true) {
				fmt.Fprintln(a.errw, "bosun setup: --yes and --no cannot be combined")
				return 2, false
			}
		case arg == "--no":
			if !answer(false) {
				fmt.Fprintln(a.errw, "bosun setup: --yes and --no cannot be combined")
				return 2, false
			}
		case arg == "--repo" && i+1 < len(args):
			i++
			opt.Repo = args[i]
		default:
			fmt.Fprintf(a.errw, "bosun setup: unknown argument %q\n", arg)
			return 2, false
		}
	}
	opt.Config = a.loadConfig()
	a.withIdentity(opt.Config)
	a.exePaths(&opt)
	env := a.applyEnv(opt.Config, true)
	if in, ok := a.in.(*os.File); ok && isTerminal(in) && isTerminal(a.out) {
		so.Ask = func(def bool) (bool, error) { return install.AskLiveCost(in, a.out, def, env.Color) }
	}
	rc, cancelled = install.Setup(a.ad, opt, env, so, a.out)
	if cancelled || so.Answer == nil && so.Ask == nil {
		return rc, cancelled // the user backed out, or nothing was asked and nothing was done
	}
	a.afterSetup(rc)
	return rc, false
}

// exePaths gives opt the running binary's stable path and the directories to
// look for the share dir in, plus the working directory for a checkout.
func (a *app) exePaths(opt *install.Options) {
	if exe, err := os.Executable(); err == nil {
		bosun, dirs := install.StablePaths(exe, exec.LookPath)
		opt.Bosun = bosun
		opt.SearchDirs = append(opt.SearchDirs, dirs...)
	}
	if wd, err := os.Getwd(); err == nil {
		opt.SearchDirs = append(opt.SearchDirs, wd)
	}
}

// afterSetup refreshes the spend files so the bar follows the new live cost
// setting without waiting for the next refresh, then runs the doctor checks and
// shows only what failed.
func (a *app) afterSetup(rc int) {
	if exe, err := os.Executable(); err == nil && a.ad.Spawner != nil {
		_ = a.ad.Spawner.Spawn(exe, "refresh")
	}
	var bad []doctor.Check
	restart := false
	for _, c := range a.doctorChecks() {
		switch {
		case c.Status != doctor.Fail:
		case c.Name == "heartbeat":
			// No hook has run yet: expected until a Claude Code session restarts.
			restart = true
		default:
			bad = append(bad, c)
		}
	}
	home := a.ad.Getenv("HOME")
	for _, c := range bad {
		fmt.Fprintf(a.out, "! %s: %s\n", c.Name, tildePath(home, c.Detail))
	}
	switch {
	case len(bad) > 0:
		fmt.Fprintln(a.out, "run `bosun doctor` for the full check")
	case restart && rc == 0:
		fmt.Fprintln(a.out, "restart your Claude Code sessions so the plugin loads")
	}
}

func tildePath(home, s string) string {
	if home == "" {
		return s
	}
	return strings.ReplaceAll(s, home, "~")
}

// bare is `bosun` with no arguments. On a terminal, before bosun is set up, it
// runs the setup flow first; either way it then does what it always did, except
// that cancelling the question ends it there.
func (a *app) bare() int {
	if in, ok := a.in.(*os.File); ok && isTerminal(in) && isTerminal(a.out) && !install.IsSetUp(a.ad, install.Options{}) {
		if rc, cancelled := a.runSetup(nil); cancelled {
			return rc
		}
		fmt.Fprintln(a.out)
	}
	usage(a.errw)
	return 2
}

// tmuxSync is the SessionStart hook's detached child: load the current conf
// into the tmux server the session runs under, if that server has not yet.
func (a *app) tmuxSync(_ []string) int {
	defer func() { _ = recover() }() // a hook's child must never be the thing that fails
	opt := install.Options{Version: version, Config: a.loadConfig()}
	a.exePaths(&opt)
	tenv := a.ad.Getenv("TMUX")
	tmuxBin, _ := exec.LookPath("tmux")
	env := install.Env{Runner: a.ad.Runner, TmuxBin: tmuxBin, TmuxSocket: tmux.SocketFromEnv(tenv), InTmux: tenv != "", Home: a.ad.Getenv("HOME")}
	install.SyncTmux(a.ad, opt, env)
	return 0
}
