package main

import (
	"fmt"
	"os/exec"

	"github.com/jmvaldez/bosun/internal/notify"
)

// notifyDeps resolves the configured backend. A nil Notifier means off, with a
// reason that says how to turn notifications on.
func (a *app) notifyDeps() (notify.Deps, string) {
	cfg := a.loadConfig()
	n, why := notify.Resolve(cfg.Notify.Backend, a.ad.Kind, exec.LookPath, a.ad.Runner)
	d := notify.Deps{Paths: a.ad.Paths, Cfg: cfg, Notifier: n, Clock: a.ad.Clock}
	if exe, err := a.exe(); err == nil && a.ad.Spawner != nil {
		d.Spawn = func(args ...string) error { return a.ad.Spawner.Spawn(exe, args...) }
	}
	if n != nil {
		tc := a.tmuxClient("", "")
		d.Locate = tc.PaneWhere
		if cfg.Focus.WSL != "never" {
			d.Focus = tc
		}
	}
	return d, why
}

// notifyDispatch is the hidden detached child: focus, dedupe, rate limit, post
// or retract. Its output goes to /dev/null in real use; tests read it.
func (a *app) notifyDispatch(args []string) int {
	req, err := notify.ParseArgs(args)
	if err != nil {
		fmt.Fprintln(a.errw, err)
		return 2
	}
	d, why := a.notifyDeps()
	if d.Notifier == nil {
		fmt.Fprintf(a.out, "%s: %s\n", notify.Off, why)
		return 0
	}
	out, reason, err := notify.Dispatch(d, req)
	if err != nil {
		fmt.Fprintf(a.errw, "bosun notify-dispatch: %v\n", err)
		return 1
	}
	fmt.Fprintf(a.out, "%s: %s\n", out, reason)
	return 0
}

func (a *app) notifyCmd(args []string) int {
	if len(args) != 1 || args[0] != "--test" {
		fmt.Fprintln(a.errw, "usage: bosun notify --test")
		return 2
	}
	d, why := a.notifyDeps()
	if d.Notifier == nil {
		fmt.Fprintf(a.errw, "bosun: notifications are off: %s\n", why)
		return 1
	}
	d.Focus = nil // a test notice should always show
	out, reason, err := notify.Dispatch(d, notify.Request{Test: true})
	if err != nil {
		fmt.Fprintf(a.errw, "bosun notify: %s: %v\n", d.Notifier.Name(), err)
		return 1
	}
	fmt.Fprintf(a.out, "%s via %s: %s\n", out, d.Notifier.Name(), reason)
	if out != notify.Posted {
		return 1
	}
	return 0
}
