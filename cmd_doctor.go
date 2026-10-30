package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/jmvaldez/bosun/internal/claude"
	"github.com/jmvaldez/bosun/internal/config"
	"github.com/jmvaldez/bosun/internal/doctor"
	"github.com/jmvaldez/bosun/internal/install"
	"github.com/jmvaldez/bosun/internal/tmux"
	"github.com/jmvaldez/bosun/internal/uninstall"
)

// doctor is read-only: it stats, reads, and asks tmux, claude and gdbus
// questions. Nothing here may create a file or a directory.
func (a *app) doctor(args []string) int {
	asJSON := false
	for _, arg := range args {
		if arg != "--json" {
			fmt.Fprintf(a.errw, "bosun doctor: unexpected argument %q\n", arg)
			return 2
		}
		asJSON = true
	}
	return a.runDoctor(asJSON)
}

func (a *app) runDoctor(asJSON bool) int {
	checks := a.doctorChecks()
	if asJSON {
		if err := doctor.JSON(a.out, checks); err != nil {
			fmt.Fprintln(a.errw, err)
			return 1
		}
	} else {
		doctor.Text(a.out, checks)
	}
	if doctor.Failed(checks) {
		return 1
	}
	return 0
}

// doctorChecks runs every check and returns the results unrendered.
func (a *app) doctorChecks() []doctor.Check {
	cfg, res, cerr := config.Load(a.ad.Paths.ConfigFile())
	exe, _ := os.Executable()
	tmuxBin, _ := exec.LookPath("tmux")
	claudeBin, _ := claude.Resolve(cfg)
	env := doctor.Env{
		Paths: a.ad.Paths, Version: version, Kind: a.ad.Kind, Now: a.ad.Clock.Now(), Getenv: a.ad.Getenv,
		Runner: a.ad.Runner, Procs: a.ad.Procs, Executable: exe, LookPath: exec.LookPath,
		TmuxBin: tmuxBin, TmuxSocket: tmux.SocketFromEnv(a.ad.Getenv("TMUX")), ClaudeBin: claudeBin,
		ManagedSettings: []string{"/etc/claude-code/managed-settings.json", "/Library/Application Support/ClaudeCode/managed-settings.json"},
		Config:          cfg, ConfigRes: res, ConfigErr: cerr,
	}
	return doctor.Run(env)
}

func (a *app) uninstall(args []string) int {
	apply, yes, showDiff := false, false, false
	o := uninstall.Options{Home: a.ad.Getenv("HOME"), Settings: a.ad.Paths.ClaudeSettings(), Explicit: map[string]bool{}}
	for _, arg := range args {
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
			fmt.Fprintf(a.errw, "bosun uninstall: unexpected argument %q\n", arg)
			return 2
		}
	}
	for kind, env := range map[string]string{"state": "BOSUN_STATE_DIR", "cache": "BOSUN_CACHE_DIR", "data": "BOSUN_DATA_DIR", "config": "BOSUN_CONFIG_DIR"} {
		o.Explicit[kind] = a.ad.Getenv(env) != ""
	}
	if exe, err := os.Executable(); err == nil {
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
