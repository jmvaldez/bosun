package main

import (
	"fmt"
	"os/exec"

	"github.com/jmvaldez/bosun/internal/claude"
	"github.com/jmvaldez/bosun/internal/config"
	"github.com/jmvaldez/bosun/internal/doctor"
	"github.com/jmvaldez/bosun/internal/tmux"
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
	exe, _ := a.exe()
	tmuxBin, _ := a.tmuxBin()
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
