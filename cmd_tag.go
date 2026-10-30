package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/jmvaldez/bosun/internal/live"
	"github.com/jmvaldez/bosun/internal/paths"
	"github.com/jmvaldez/bosun/internal/task"
)

// tag sets or clears a session's task. The tag outranks the key the hook found
// in env or branch, on every surface (segment, popup, ledger, spend).
// task.Parser.Tag normalises the text.
func (a *app) tag(args []string) int {
	var words []string
	var session string
	clear := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--clear":
			clear = true
		case "--session":
			if i+1 >= len(args) {
				fmt.Fprintln(a.errw, "bosun tag: --session needs a value")
				return 2
			}
			i++
			session = args[i]
		default:
			if strings.HasPrefix(args[i], "-") {
				fmt.Fprintf(a.errw, "bosun tag: unexpected argument %q\n", args[i])
				return 2
			}
			words = append(words, args[i])
		}
	}
	if clear == (len(words) > 0) {
		fmt.Fprintln(a.errw, "usage: bosun tag TEXT [--session ID] | bosun tag --clear [--session ID]")
		return 2
	}
	key := ""
	if !clear {
		var err error
		if key, err = a.parser().Tag(strings.Join(words, " ")); err != nil {
			fmt.Fprintf(a.errw, "bosun tag: %v (up to %d bytes, no control characters)\n", err, task.MaxTagBytes)
			return 1
		}
	}
	if session == "" {
		pane := a.ad.Getenv("TMUX_PANE")
		if pane == "" {
			fmt.Fprintln(a.errw, "bosun tag: no --session given and $TMUX_PANE is not set")
			return 1
		}
		var best *live.Session
		for _, s := range live.Read(a.ad.Paths.Live()).Sessions {
			if !s.Ended && s.Loc.Pane == pane && (best == nil || s.LastEvent.After(best.LastEvent)) {
				best = s
			}
		}
		if best == nil {
			fmt.Fprintf(a.errw, "bosun tag: no live session in pane %s\n", pane)
			return 1
		}
		session = best.ID
	}
	if !paths.ValidSessionID(session) {
		fmt.Fprintf(a.errw, "bosun tag: %q is not a session id\n", session)
		return 2
	}
	if a.ad.Paths.State == "" {
		fmt.Fprintln(a.errw, "bosun tag: no state dir (HOME unset?)")
		return 1
	}
	if _, err := os.Stat(a.ad.Paths.Session(session)); err != nil {
		fmt.Fprintf(a.errw, "bosun tag: unknown session %s\n", session)
		return 1
	}
	if err := task.WriteTag(a.ad.Paths.Tag(session), key); err != nil {
		fmt.Fprintf(a.errw, "bosun tag: %v\n", err)
		return 1
	}
	if clear {
		fmt.Fprintf(a.out, "cleared the tag on %s\n", paths.ShortID(session))
	} else {
		fmt.Fprintf(a.out, "tagged %s %s\n", paths.ShortID(session), key)
	}
	return 0
}
