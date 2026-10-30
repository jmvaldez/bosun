package main

import (
	"github.com/jmvaldez/bosun/internal/install"
	"github.com/jmvaldez/bosun/internal/tmux"
)

// tmuxSync is the SessionStart hook's detached child: load the current conf
// into the tmux server the session runs under, if that server has not yet.
func (a *app) tmuxSync(_ []string) int {
	defer func() { _ = recover() }() // a hook's child must never be the thing that fails
	opt := install.Options{Version: version, Config: a.loadConfig()}
	a.exePaths(&opt)
	tenv := a.ad.Getenv("TMUX")
	tmuxBin, _ := a.tmuxBin()
	env := install.Env{Runner: a.ad.Runner, TmuxBin: tmuxBin, TmuxSocket: tmux.SocketFromEnv(tenv), InTmux: tenv != "", Home: a.ad.Getenv("HOME")}
	install.SyncTmux(a.ad, opt, env)
	return 0
}
