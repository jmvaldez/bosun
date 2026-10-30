package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/jmvaldez/bosun/internal/install"
	"github.com/jmvaldez/bosun/internal/uninstall"
)

func (a *app) uninstall(args []string) int {
	apply, yes, showDiff := false, false, false
	o := uninstall.Options{Home: a.ad.Getenv("HOME"), Settings: a.ad.Paths.ClaudeSettings(), Explicit: map[string]bool{}}
	p := a.args("uninstall", args)
	for arg, ok := p.next(); ok; arg, ok = p.next() {
		switch arg {
		case "--purge":
			o.Purge = true
		case "--purge-config":
			o.PurgeConfig, o.Purge = true, true
		case "--apply":
			apply = true
		case "--yes":
			yes = true
		case "--show-diff", "-v":
			showDiff = true
		default:
			return p.unknown()
		}
	}
	for kind, env := range map[string]string{"state": "BOSUN_STATE_DIR", "cache": "BOSUN_CACHE_DIR", "data": "BOSUN_DATA_DIR", "config": "BOSUN_CONFIG_DIR"} {
		o.Explicit[kind] = a.ad.Getenv(env) != ""
	}
	if exe, err := a.exe(); err == nil {
		o.Executable = filepath.Clean(exe)
	}
	if home := o.Home; home != "" {
		for _, c := range []string{filepath.Join(home, ".tmux.conf"), filepath.Join(home, ".config", "tmux", "tmux.conf")} {
			if _, err := os.Stat(c); err == nil {
				o.TmuxConf = c
				break
			}
		}
	}
	if yes && !apply {
		fmt.Fprintln(a.errw, "bosun uninstall: --yes only goes with --apply")
		return 2
	}
	if apply {
		cfg := a.loadConfig()
		env := a.applyEnv(cfg, yes)
		env.ShowDiff = showDiff
		o.Tilde = env.Tilde
		opt := install.Options{Settings: o.Settings, TmuxConf: o.TmuxConf, Config: cfg}
		o.Apply = func(out io.Writer, purge []install.Step) int {
			return install.UninstallApply(a.ad, opt, env, out, purge...)
		}
	}
	return uninstall.Run(a.ad.Paths, o, a.out, a.errw)
}
