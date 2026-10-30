package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/jmvaldez/bosun/internal/eval"
	"github.com/jmvaldez/bosun/internal/tmux"
)

type fakeProcs struct {
	parent map[int]int
	alive  map[int]string // pid -> token
}

func (f fakeProcs) StartToken(pid int) (string, error) { return f.alive[pid], nil }
func (f fakeProcs) StartTime(int) (time.Time, error)   { return time.Time{}, nil }
func (f fakeProcs) Alive(pid int, tok string) bool {
	t, ok := f.alive[pid]
	return ok && (tok == "" || tok == t)
}
func (f fakeProcs) Parent(pid int) (int, error) { return f.parent[pid], nil }

type tmuxLog struct {
	calls [][]string
	panes string
}

func (l *tmuxLog) Run(_ time.Duration, name string, args ...string) ([]byte, error) {
	l.calls = append(l.calls, args)
	if args[0] == "list-panes" {
		return []byte(l.panes), nil
	}
	return nil, nil
}

func (l *tmuxLog) sent() []string {
	for _, c := range l.calls {
		if c[0] == "send-keys" {
			return c
		}
	}
	return nil
}

// Pane %3 runs shell 500; claude is 501 under it; a node child 502 under claude.
func setup() (InterruptDeps, *tmuxLog, eval.View) {
	log := &tmuxLog{panes: "4242\t%3\t@1\t500\t0\t0\twork\n"}
	d := InterruptDeps{
		Tmux:  tmux.Client{Runner: log},
		Procs: fakeProcs{parent: map[int]int{502: 501, 501: 500, 500: 1}, alive: map[int]string{501: "tok", 500: "x"}},
		Keys:  "Escape",
	}
	v := eval.View{ID: "s1", Pane: "%3", ServerPID: 4242, PID: 501, StartTok: "tok"}
	return d, log, v
}

func TestInterruptSendsToTheRightPane(t *testing.T) {
	d, log, v := setup()
	msg, err := Interrupt(d, v)
	if err != nil || !strings.HasPrefix(msg, "sent Escape") {
		t.Fatalf("msg %q err %v", msg, err)
	}
	if got := strings.Join(log.sent(), " "); got != "send-keys -t %3 Escape" {
		t.Errorf("tmux got %q", got)
	}
	// Multi-key config.
	d.Keys = "C-c Escape"
	d2, log2, v2 := setup()
	d2.Keys = d.Keys
	Interrupt(d2, v2)
	if got := strings.Join(log2.sent(), " "); got != "send-keys -t %3 C-c Escape" {
		t.Errorf("tmux got %q", got)
	}
}

func TestInterruptRefusesWhenAnyCheckFails(t *testing.T) {
	tests := []struct {
		name string
		mod  func(*InterruptDeps, *tmuxLog, *eval.View)
	}{
		{"server pid changed", func(_ *InterruptDeps, _ *tmuxLog, v *eval.View) { v.ServerPID = 9999 }},
		{"pane id gone", func(_ *InterruptDeps, l *tmuxLog, _ *eval.View) { l.panes = "4242\t%9\t@1\t500\t0\t0\twork\n" }},
		{"start token mismatch (pid reused)", func(_ *InterruptDeps, _ *tmuxLog, v *eval.View) { v.StartTok = "other" }},
		{"process gone", func(d *InterruptDeps, _ *tmuxLog, _ *eval.View) {
			d.Procs = fakeProcs{parent: map[int]int{501: 500}, alive: map[int]string{}}
		}},
		{"not under the pane's process", func(_ *InterruptDeps, l *tmuxLog, _ *eval.View) { l.panes = "4242\t%3\t@1\t777\t0\t0\twork\n" }},
		// Review finding 8: the platform's Alive treats an empty token as "any".
		{"no start token recorded", func(_ *InterruptDeps, _ *tmuxLog, v *eval.View) { v.StartTok = "" }},
		{"no pid recorded", func(_ *InterruptDeps, _ *tmuxLog, v *eval.View) { v.PID = 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, log, v := setup()
			tt.mod(&d, log, &v)
			msg, err := Interrupt(d, v)
			if err != nil || msg != NotSent {
				t.Errorf("msg %q err %v, want %q", msg, err, NotSent)
			}
			if log.sent() != nil {
				t.Errorf("sent %v", log.sent())
			}
		})
	}
}

func TestInterruptNothingSentWhenTmuxFails(t *testing.T) {
	d, _, v := setup()
	d.Tmux = tmux.Client{Runner: failing{}}
	msg, _ := Interrupt(d, v)
	if !strings.Contains(msg, "not sent") {
		t.Errorf("msg = %q", msg)
	}
}

type failing struct{}

func (failing) Run(time.Duration, string, ...string) ([]byte, error) { return nil, errTimeout }

var errTimeout = errString("timeout")

type errString string

func (e errString) Error() string { return string(e) }
