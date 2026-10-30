package eval

import (
	"github.com/jmvaldez/bosun/internal/live"
	"os"
	"os/exec"
	"strconv"
	"testing"

	"github.com/jmvaldez/bosun/internal/hook"
	"github.com/jmvaldez/bosun/internal/rules"
)

// Two tmux server pids that are certainly alive; markers of a dead server are
// pruned, so the fake 4242 would not do for the second one.
var (
	otherServer = os.Getpid()
	firstServer = os.Getppid()
)

func onServer(spid int) func(*hook.Event) { return func(e *hook.Event) { e.Tmux.SPID = spid } }

// Review finding 5: two tmux servers can share a state dir. Each evaluation
// sees only its own server's panes; sessions of the other server are unknown to
// it, not gone, and must keep their markers and alerts.
func TestEvaluatingFromAnotherServerKeepsMarksAndAlerts(t *testing.T) {
	r := newRig(t).withRules()
	r.d.Cfg.Rules.SessionCost = 5
	r.emit("aaa", "%1", "PermissionRequest", onServer(firstServer))
	r.emit("bbb", "%1", "UserPromptSubmit", onServer(otherServer)) // same pane id, other server
	r.snapshot("bbb", 7, 0)
	// bbb's own server evaluates first and raises its alert.
	r.tm.spid, r.tm.panes = otherServer, []string{"%1 @9 elsewhere:0.0"}
	Evaluate(r.d)
	alerts := rules.Load(r.d.Ad.Paths.Alerts())
	if len(alerts) != 1 {
		t.Fatalf("setup: alerts = %v", alerts)
	}
	alerts.Dismiss("", true)
	alerts.Save(r.d.Ad.Paths.Alerts())
	fired := len(r.dispatches())

	// Then the first server: bbb's pane id exists here too, but it is not bbb's.
	r.tm.spid, r.tm.panes = firstServer, []string{"%1 @1 work:0.0"}
	res := Evaluate(r.d)
	if res.Counts.Sessions != 1 || res.Counts.NeedsYou != 1 {
		t.Errorf("counts = %+v; only this server's session counts", res.Counts)
	}
	r.tm.spid, r.tm.panes = otherServer, []string{"%1 @9 elsewhere:0.0"}
	Evaluate(r.d)
	r.tm.spid, r.tm.panes = firstServer, []string{"%1 @1 work:0.0"}
	n := r.tm.setCalls()
	res = Evaluate(r.d)
	if got := r.tm.setCalls() - n; got != 0 {
		t.Errorf("%d marker writes on a pass that changed nothing; marks are kept per server", got)
	}
	a := rules.Load(r.d.Ad.Paths.Alerts())
	if len(a) != 1 {
		t.Fatalf("alerts = %v; another server's session must keep its alert", a)
	}
	for _, x := range a {
		if !x.Dismissed {
			t.Error("the dismissal was lost")
		}
	}
	if len(r.dispatches()) != fired {
		t.Error("an alert was re-fired for a session on another server")
	}
}

// A tmux server that has died leaves its entry in marks.json; the next
// evaluation drops it (and the older single-server shape with it), while a live
// server's markers are kept.
func TestMarksDropDeadServersOnTheNextEvaluation(t *testing.T) {
	r := newRig(t)
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Skip("no true(1)")
	}
	dead := cmd.ProcessState.Pid()
	r.emit("aaa", "%1", "PermissionRequest", onServer(firstServer))
	r.tm.spid = firstServer
	live.WriteMarks(r.d.Ad.Paths.Marks(), live.Marks{
		ServerPID: dead, Windows: map[string]string{"@7": "done"},
		Servers: map[string]map[string]string{strconv.Itoa(dead): {"@7": "done"}, strconv.Itoa(otherServer): {"@9": "working"}},
	})
	Evaluate(r.d)
	got := live.ReadMarks(r.d.Ad.Paths.Marks())
	if _, ok := got.Servers[strconv.Itoa(dead)]; ok {
		t.Errorf("dead server %d still in marks.json: %+v", dead, got)
	}
	if got.ServerPID != 0 || got.Windows != nil {
		t.Errorf("the older single-server shape survived: %+v", got)
	}
	if got.Servers[strconv.Itoa(otherServer)]["@9"] != "working" || got.For(firstServer)["@1"] != "needs-you" {
		t.Errorf("a live server's markers were lost: %+v", got)
	}
}
