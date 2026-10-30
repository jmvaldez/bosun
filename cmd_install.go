package main

import (
	"fmt"

	"github.com/jmvaldez/bosun/internal/claude"
	"github.com/jmvaldez/bosun/internal/config"
	"github.com/jmvaldez/bosun/internal/install"
	"github.com/jmvaldez/bosun/internal/tmux"
)

func (a *app) install(args []string) int {
	opt := install.Options{Version: version}
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "--repo" && i+1 < len(args):
			i++
			opt.Repo = args[i]
		case arg == "--settings" && i+1 < len(args):
			i++
			opt.Settings = args[i]
		case arg == "--tmux-conf" && i+1 < len(args):
			i++
			opt.TmuxConf = args[i]
		case arg == "--apply" || arg == "--yes" || arg == "--show-diff" || arg == "-v":
			fmt.Fprintf(a.errw, "bosun install: %s was removed; run `bosun setup`\n", arg)
			return 2
		default:
			fmt.Fprintf(a.errw, "bosun install: unknown argument %q\n", arg)
			return 2
		}
	}
	opt.Config = a.loadConfig()
	a.withIdentity(opt.Config)
	a.exePaths(&opt)
	return install.Run(a.ad, opt, a.out)
}

func (a *app) applyEnv(cfg config.Config, yes bool) install.Env {
	claudeBin, _ := claude.Resolve(cfg)
	tmuxBin, _ := a.tmuxBin()
	tty := isTerminal(a.in)
	// Bold depends on where the output goes, not on where input comes from.
	color := isTerminal(a.out) && a.ad.Getenv("NO_COLOR") == ""
	tenv := a.ad.Getenv("TMUX")
	return install.Env{
		Home: a.ad.Getenv("HOME"), Color: color,
		Runner: a.ad.Runner, ClaudeBin: claudeBin, TmuxBin: tmuxBin, TmuxSocket: tmux.SocketFromEnv(tenv), InTmux: tenv != "",
		Stdin: a.in, TTY: tty, Yes: yes, Now: a.ad.Clock.Now,
	}
}
