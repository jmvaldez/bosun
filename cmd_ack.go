package main

import (
	"fmt"

	"github.com/jmvaldez/bosun/internal/eval"
	"github.com/jmvaldez/bosun/internal/notify"
)

// ack dismisses alerts until their conditions clear, and retracts their notices.
func (a *app) ack(args []string) int {
	const usage = "bosun ack ID | --all"
	id, all := "", false
	p := a.args("ack", args)
	for arg, ok := p.next(); ok; arg, ok = p.next() {
		switch {
		case arg == "--all" && id == "" && !all:
			all = true
		case arg != "" && arg[0] != '-' && id == "" && !all:
			id = arg
		default:
			return p.usage(usage)
		}
	}
	if id == "" && !all {
		return p.usage(usage)
	}
	got, err := eval.Dismiss(a.ad.Paths, id, all)
	if err != nil {
		fmt.Fprintf(a.errw, "bosun ack: %v\n", err)
		return 1
	}
	d, _ := a.notifyDeps()
	for _, al := range got {
		if d.Notifier != nil {
			_, _, _ = notify.Dispatch(d, notify.Request{Retract: true, Group: notify.Group(notify.Kind(al.Rule), al.Session)})
		}
		fmt.Fprintf(a.out, "dismissed %s (%s %s)\n", al.ID, al.Rule, al.Detail)
	}
	if len(got) == 0 {
		fmt.Fprintln(a.out, "nothing to dismiss")
	}
	return 0
}
