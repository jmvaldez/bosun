package main

import (
	"fmt"

	"github.com/jmvaldez/bosun/internal/jump"
	"github.com/jmvaldez/bosun/internal/live"
	"github.com/jmvaldez/bosun/internal/paths"
)

const nothingWaiting = "bosun: nothing waiting"

// jump switches a tmux client to the next waiting pane. The catch-up
// evaluation has already run (the command table says so).
func (a *app) jump(args []string) int {
	var client, socket, tmuxBin, session string
	for i := 0; i < len(args); i++ {
		next := func() string {
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch args[i] {
		case "--queue": // the default walk; the generated binding passes it and doctor looks for it
		case "--client":
			client = next()
		case "--socket":
			socket = next()
		case "--tmux":
			tmuxBin = next()
		case "--session":
			session = next()
		default:
			fmt.Fprintf(a.errw, "bosun jump: unknown argument %q\n", args[i])
			return 2
		}
	}
	if session != "" && !paths.ValidSessionID(session) {
		fmt.Fprintf(a.errw, "bosun jump: invalid --session %q\n", session)
		return 2
	}
	tc := a.tmuxClient(socket, tmuxBin)
	f := live.Read(a.ad.Paths.Live())

	// The client's current pane comes from list-clients, never from
	// `display -p -c`, which resolves the default target (LESSONS L-10).
	clients, _ := tc.Clients()
	current := ""
	for _, c := range clients {
		if client == "" || c.Name == client {
			current, client = c.PaneID, c.Name
			break
		}
	}

	var target string
	store := jump.Load(a.ad.Paths.Jump())
	if session != "" {
		s := f.Sessions[session]
		if s == nil || !s.Visible() {
			fmt.Fprintf(a.errw, "bosun: session %s is not in a live pane\n", session)
			return 1
		}
		target = s.Loc.Pane
	} else {
		var queue []string
		for _, s := range live.Queue(f.Sessions) {
			queue = append(queue, s.Loc.Pane)
		}
		var walk jump.Walk
		target, walk = jump.Next(store[client], queue, current, a.ad.Clock.Now())
		store[client] = walk
		if target == "" {
			delete(store, client)
		}
		_ = store.Save(a.ad.Paths.Jump())
	}
	if target == "" {
		if client != "" {
			_ = tc.DisplayMessage(client, nothingWaiting)
		} else {
			fmt.Fprintln(a.out, nothingWaiting)
		}
		return 0
	}
	if err := tc.SwitchClient(client, target); err != nil {
		fmt.Fprintf(a.errw, "bosun jump: %v\n", err)
		return 1
	}
	return 0
}
