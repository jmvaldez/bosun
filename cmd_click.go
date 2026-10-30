package main

import (
	"fmt"
	"os/exec"
	"strconv"
	"time"

	"github.com/jmvaldez/bosun/internal/notify"
	"github.com/jmvaldez/bosun/internal/paths"
	"github.com/jmvaldez/bosun/internal/runner"
)

// notifyClick is the hidden short-lived child that waits for a click on one
// notice (a hard deadline, then it exits) and then jumps to its session.
func (a *app) notifyClick(args []string) int {
	var id uint32
	var session string
	timeout := notify.ClickTimeout
	for i := 0; i+1 < len(args); i += 2 {
		switch args[i] {
		case "--id":
			n, err := strconv.ParseUint(args[i+1], 10, 32)
			if err != nil {
				fmt.Fprintln(a.errw, "bosun notify-click: bad --id")
				return 2
			}
			id = uint32(n)
		case "--session":
			session = args[i+1]
		case "--timeout":
			d, err := time.ParseDuration(args[i+1])
			if err != nil {
				fmt.Fprintln(a.errw, "bosun notify-click: bad --timeout")
				return 2
			}
			timeout = d
		default:
			fmt.Fprintf(a.errw, "bosun notify-click: unknown flag %s\n", args[i])
			return 2
		}
	}
	if session != "" && !paths.ValidSessionID(session) {
		fmt.Fprintf(a.errw, "bosun notify-click: invalid --session %q\n", session)
		return 2
	}
	gdbus, err := exec.LookPath("gdbus")
	if id == 0 || session == "" || err != nil {
		fmt.Fprintln(a.errw, "bosun notify-click: needs --id, --session and gdbus")
		return 2
	}
	exe, _ := a.exe()
	e := runner.Exec{}
	clicked, err := notify.Listen(notify.ClickDeps{Stream: e, Run: a.ad.Runner, Gdbus: gdbus, Exe: exe, Timeout: timeout}, id, session)
	if err != nil {
		fmt.Fprintln(a.errw, err)
		return 1
	}
	fmt.Fprintf(a.out, "clicked=%v\n", clicked)
	return 0
}
