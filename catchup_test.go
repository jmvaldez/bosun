package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jmvaldez/bosun/internal/paths"
	"github.com/jmvaldez/bosun/internal/platform"
)

type failRunner struct{}

func (failRunner) Run(time.Duration, string, ...string) ([]byte, error) {
	return nil, errors.New("no tmux in this test")
}

// LESSONS L-26: catch-up was defined but not called. The command table is the
// one place that decides; this test makes every new command say which side it
// is on, and checks the dispatcher honors it.
func TestCatchUpTable(t *testing.T) {
	wantCatchUp := map[string]bool{
		"hook":            false, // hot path: never evaluates
		"statusline":      false, // hot path
		"refresh":         false, // detached job
		"install":         false,
		"setup":           false, // first-run flow; reads no session state
		"tmux-sync":       false, // detached child of the SessionStart hook
		"config":          false,
		"version":         false,
		"status":          false, // is itself the evaluation
		"jump":            true,
		"spend":           true,
		"ack":             true,
		"ui":              true,
		"tag":             true,
		"backfill":        false, // is an ingest itself
		"doctor":          false, // read-only: a catch-up would write live.json
		"uninstall":       false, // prints steps; reads no session state
		"run":             false, // opens a window; reads no session state
		"notify":          false, // posts a test notice; reads no session state
		"notify-dispatch": false, // detached child
		"notify-click":    false, // detached child
	}
	for _, c := range commands() {
		want, known := wantCatchUp[c.name]
		if !known {
			t.Errorf("command %q is not classified in this test; decide whether it runs catch-up", c.name)
			continue
		}
		if c.catchUp != want {
			t.Errorf("command %q: catchUp = %v, want %v", c.name, c.catchUp, want)
		}
		delete(wantCatchUp, c.name)
	}
	for name := range wantCatchUp {
		t.Errorf("classified command %q is not in the table", name)
	}
}

func TestDispatchRunsCatchUpBeforeUserFacingCommands(t *testing.T) {
	root := t.TempDir()
	for _, tt := range []struct {
		args []string
		want bool
	}{
		{[]string{"jump"}, true},
		{[]string{"config"}, false},
		{[]string{"version"}, false},
	} {
		ran := false
		var out bytes.Buffer
		a := &app{
			ad: platform.Adapters{
				Paths:  paths.Paths{State: root + "/state", Config: root + "/config"},
				Runner: failRunner{}, Clock: platform.SystemClock{},
				Getenv: func(string) string { return "" },
			},
			out: &out, errw: &out,
			catchUp: func() { ran = true },
		}
		a.dispatch(tt.args)
		if ran != tt.want {
			t.Errorf("%v: catch-up ran = %v, want %v", tt.args, ran, tt.want)
		}
	}
}

func TestUnknownCommandExitsNonzero(t *testing.T) {
	var out bytes.Buffer
	a := &app{out: &out, errw: &out, catchUp: func() {}}
	if a.dispatch([]string{"frobnicate"}) == 0 {
		t.Error("want nonzero")
	}
}

// Review finding 9: commands that write mark bosun's dirs for `uninstall
// --purge`; doctor is read-only and must not.
func TestNormalCommandsMarkExistingDirsAndDoctorDoesNot(t *testing.T) {
	for _, tt := range []struct {
		cmd  string
		want bool
	}{{"jump", true}, {"doctor", false}} {
		root := t.TempDir()
		os.MkdirAll(root+"/state", 0o700)
		os.WriteFile(root+"/state/live.json", []byte("{}"), 0o600) // from an older build
		var out bytes.Buffer
		a := &app{
			ad: platform.Adapters{
				Paths:  paths.Paths{State: root + "/state", Config: root + "/config"},
				Runner: failRunner{}, Clock: platform.SystemClock{},
				Getenv: func(string) string { return "" },
			},
			out: &out, errw: &out, catchUp: func() {},
		}
		a.dispatch([]string{tt.cmd})
		if got := paths.HasMarker(root + "/state"); got != tt.want {
			t.Errorf("%s: state marker = %v, want %v", tt.cmd, got, tt.want)
		}
	}
}

func TestJumpAndNotifyClickValidateSession(t *testing.T) {
	const good = "3f2c9a1e-4b5d-4c6e-8f70-123456789abc"
	for _, tt := range []struct {
		name string
		run  func(a *app, session string) int
	}{
		{"jump", func(a *app, s string) int { return a.jump([]string{"--session", s}) }},
		// --id 0 stops the command before it would listen on the real bus.
		{"notify-click", func(a *app, s string) int { return a.notifyClick([]string{"--id", "0", "--session", s}) }},
	} {
		newApp := func(out *bytes.Buffer) *app {
			root := t.TempDir()
			return &app{
				ad: platform.Adapters{
					Paths:  paths.Paths{State: root + "/state", Config: root + "/config"},
					Runner: failRunner{}, Clock: platform.SystemClock{},
					Getenv: func(string) string { return "" },
				},
				out: out, errw: out, catchUp: func() {},
			}
		}
		var out bytes.Buffer
		if code := tt.run(newApp(&out), "../../etc"); code != 2 || !strings.Contains(out.String(), "invalid --session") {
			t.Errorf("%s: bad id: code=%d out=%q", tt.name, code, out.String())
		}
		out.Reset()
		tt.run(newApp(&out), good)
		if strings.Contains(out.String(), "invalid --session") {
			t.Errorf("%s: valid id rejected: %q", tt.name, out.String())
		}
	}
}
