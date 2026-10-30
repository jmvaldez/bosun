package main

import (
	"fmt"

	"github.com/jmvaldez/bosun/internal/eval"
	"github.com/jmvaldez/bosun/internal/notify"
)

// ack dismisses alerts until their conditions clear, and retracts their notices.
func (a *app) ack(args []string) int {
	id, all := "", false
	switch {
	case len(args) == 1 && args[0] == "--all":
		all = true
	case len(args) == 1 && args[0] != "" && args[0][0] != '-':
		id = args[0]
	default:
		fmt.Fprintln(a.errw, "usage: bosun ack ID | --all")
		return 2
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
