package eval

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jmvaldez/bosun/internal/flock"
	"github.com/jmvaldez/bosun/internal/hook"
	"github.com/jmvaldez/bosun/internal/live"
	"github.com/jmvaldez/bosun/internal/paths"
	"github.com/jmvaldez/bosun/internal/platform"
	"github.com/jmvaldez/bosun/internal/refresh"
	"github.com/jmvaldez/bosun/internal/tmux"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fakeTmux answers list-panes / list-clients from a table and records every call.
type fakeTmux struct {
	panes   []string // "%1 @1 work:0.0"
	clients string
	spid    int // the server's pid in list-panes; 0 means 4242
	fail    error
	calls   [][]string
}

func (f *fakeTmux) Run(_ time.Duration, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	if f.fail != nil {
		return nil, f.fail
	}
	switch args[0] {
	case "list-panes":
		var b strings.Builder
		spid := f.spid
		if spid == 0 {
			spid = 4242
		}
		for _, p := range f.panes {
			var id, win, where string
			fmt.Sscan(p, &id, &win, &where)
			fmt.Fprintf(&b, "%d\t%s\t%s\t900\t0\t0\t%s\n", spid, id, win, where)
		}
		return []byte(b.String()), nil
	case "list-clients":
		return []byte(f.clients), nil
	}
	return nil, nil
}

func (f *fakeTmux) setCalls() (n int) {
	for _, c := range f.calls {
		if c[0] == "set-option" {
			n++
		}
	}
	return
}

type spawner struct{ calls [][]string }

func (s *spawner) Spawn(name string, args ...string) error {
	s.calls = append(s.calls, append([]string{name}, args...))
	return nil
}

type rig struct {
	t   *testing.T
	d   Deps
	clk *clock
	tm  *fakeTmux
	sp  *spawner
}

func newRig(t *testing.T) *rig {
	root := t.TempDir()
	clk := &clock{t: time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)}
	tm := &fakeTmux{panes: []string{"%1 @1 work:0.0", "%2 @2 home:0.0", "%3 @3 api:0.0", "%4 @3 api:0.1"}}
	sp := &spawner{}
	ad := platform.Adapters{
		Paths:   paths.Paths{State: root + "/state", Cache: root + "/cache"},
		Procs:   platform.NewProcs(),
		Clock:   clk,
		Spawner: sp,
	}
	return &rig{t, Deps{Ad: ad, Tmux: tmux.Client{Runner: tm}, Exe: "/bin/bosun"}, clk, tm, sp}
}

// emit appends an event for a session, stamped with the rig clock.
func (r *rig) emit(sid, pane, ev string, mod ...func(*hook.Event)) {
	r.t.Helper()
	e := hook.Event{V: 1, TS: r.clk.Now().Format(time.RFC3339Nano), Ev: ev, SID: sid,
		Tmux: &hook.TmuxLoc{Sock: "/s", SPID: 4242, Pane: pane}}
	for _, m := range mod {
		m(&e)
	}
	b, _ := json.Marshal(e)
	dir := r.d.Ad.Paths.Session(sid)
	os.MkdirAll(dir, 0o700)
	f, _ := os.OpenFile(dir+"/events.jsonl", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	f.Write(append(b, '\n'))
	f.Close()
}

func (r *rig) writeAgents(fetched time.Time, rows ...refresh.Row) {
	b, _ := json.Marshal(refresh.Cache{V: 1, FetchedAt: fetched, Rows: rows})
	os.MkdirAll(r.d.Ad.Paths.Cache, 0o700)
	os.WriteFile(r.d.Ad.Paths.AgentsCache(), b, 0o600)
}

func (r *rig) marks() map[string]string { return live.ReadMarks(r.d.Ad.Paths.Marks()).For(4242) }

func TestCountsAndMarkersAcrossSessions(t *testing.T) {
	r := newRig(t)
	r.emit("aaa", "%1", "UserPromptSubmit")
	r.emit("bbb", "%2", "PermissionRequest")
	r.emit("ccc", "%3", "UserPromptSubmit")
	r.emit("ccc", "%3", "Stop")
	r.emit("ddd", "%4", "UserPromptSubmit") // same window as ccc: done outranks working
	res := Evaluate(r.d)
	c := res.Counts
	if c.Sessions != 4 || c.NeedsYou != 1 || c.Done != 1 || c.Working != 2 {
		t.Fatalf("counts = %+v", c)
	}
	want := map[string]string{"@1": "working", "@2": "needs-you", "@3": "done"}
	if got := r.marks(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("marks = %v, want %v", got, want)
	}
	sets := r.tm.setCalls()
	if sets != 1 {
		t.Errorf("set-option execs = %d, want one batched exec", sets)
	}
	// Written only on change.
	Evaluate(r.d)
	if r.tm.setCalls() != sets {
		t.Error("an unchanged evaluation wrote markers again")
	}
}

func TestKilledPaneDropsCountAndMarker(t *testing.T) {
	r := newRig(t)
	r.emit("aaa", "%2", "PermissionRequest")
	Evaluate(r.d)
	if r.marks()["@2"] != "needs-you" {
		t.Fatal("marker not set")
	}
	r.tm.panes = []string{"%1 @1 work:0.0", "%3 @3 api:0.0"} // %2 and window @2 are gone
	res := Evaluate(r.d)
	if res.Counts.Sessions != 0 || res.Counts.NeedsYou != 0 {
		t.Errorf("counts = %+v; a killed pane still counts", res.Counts)
	}
	if _, ok := r.marks()["@2"]; ok {
		t.Error("marker entry for a dead window was kept")
	}
	if q := live.Queue(live.Read(r.d.Ad.Paths.Live()).Sessions); len(q) != 0 {
		t.Error("pane-gone session still has a queue slot")
	}
}

func TestPaneIdReuseOnNewServerIsGone(t *testing.T) {
	r := newRig(t)
	r.emit("aaa", "%1", "PermissionRequest", func(e *hook.Event) { e.Tmux.SPID = 1111 }) // old server pid
	if res := Evaluate(r.d); res.Counts.Sessions != 0 {
		t.Errorf("a locator from another server must read as pane gone: %+v", res.Counts)
	}
}

func TestWindowMarkClearedWhenSessionGoesIdle(t *testing.T) {
	r := newRig(t)
	r.emit("aaa", "%1", "PermissionRequest")
	Evaluate(r.d)
	r.clk.Advance(time.Second)
	r.emit("aaa", "%1", "PostToolUse")
	r.emit("aaa", "%1", "SessionEnd")
	Evaluate(r.d)
	if len(r.marks()) != 0 {
		t.Errorf("marks = %v", r.marks())
	}
	last := r.tm.calls[len(r.tm.calls)-1]
	if strings.Join(last, " ") != "set-option -wu -t @1 @bosun-wait" {
		t.Errorf("last tmux call = %v", last)
	}
}

func TestDoneBecomesIdleWhenViewed(t *testing.T) {
	r := newRig(t)
	r.emit("aaa", "%1", "UserPromptSubmit")
	r.emit("aaa", "%1", "Stop")
	r.tm.clients = "c1\t%2\tattached\tmouse\n"
	if res := Evaluate(r.d); res.Counts.Done != 1 {
		t.Fatalf("counts = %+v", res.Counts)
	}
	r.tm.clients = "c1\t%1\tattached,focused\tfocus\n"
	if res := Evaluate(r.d); res.Counts.Done != 0 || res.Counts.Idle != 1 {
		t.Errorf("counts = %+v; viewing a done pane should idle it", res.Counts)
	}
}

func TestStaleAgentsCacheSpawnsRefreshOnce(t *testing.T) {
	r := newRig(t)
	r.emit("aaa", "%1", "UserPromptSubmit")
	for i := 0; i < 3; i++ {
		Evaluate(r.d)
		r.clk.Advance(time.Second)
	}
	if len(r.sp.calls) != 1 || strings.Join(r.sp.calls[0], " ") != "/bin/bosun refresh" {
		t.Fatalf("spawns = %v, want exactly one", r.sp.calls)
	}
	// A fresh cache means no spawn.
	r.clk.Advance(10 * time.Second)
	r.writeAgents(r.clk.Now(), refresh.Row{SessionID: "aaa", PID: 1, Status: "busy"})
	Evaluate(r.d)
	if len(r.sp.calls) != 1 {
		t.Errorf("spawned with a fresh cache: %v", r.sp.calls)
	}
	// Nobody watching: no spawn for a stale cache either.
	r2 := newRig(t)
	Evaluate(r2.d)
	if len(r2.sp.calls) != 0 {
		t.Error("spawned a refresh with no sessions")
	}
}

// The Ctrl+C path: working in hooks, idle in agents.json, no manual refresh.
func TestCtrlCMovesWorkingToIdleViaAgents(t *testing.T) {
	r := newRig(t)
	r.emit("aaa", "%1", "UserPromptSubmit")
	Evaluate(r.d)
	r.clk.Advance(5 * time.Second)
	r.writeAgents(r.clk.Now(), refresh.Row{SessionID: "aaa", PID: 1, Status: "idle"})
	res := Evaluate(r.d)
	if res.Counts.Working != 0 || res.Counts.Idle != 1 {
		t.Errorf("counts = %+v", res.Counts)
	}
}

func TestNeedsYouLeavesAfterTwoNotWaitingFetches(t *testing.T) {
	r := newRig(t)
	r.emit("aaa", "%1", "PermissionRequest")
	r.clk.Advance(time.Second)
	r.writeAgents(r.clk.Now(), refresh.Row{SessionID: "aaa", PID: 1, Status: "idle"})
	if Evaluate(r.d).Counts.NeedsYou != 1 {
		t.Fatal("left needs-you after one read")
	}
	r.clk.Advance(5 * time.Second)
	r.writeAgents(r.clk.Now(), refresh.Row{SessionID: "aaa", PID: 1, Status: "idle"})
	if res := Evaluate(r.d); res.Counts.NeedsYou != 0 || res.Counts.Idle != 1 {
		t.Errorf("counts = %+v", res.Counts)
	}
}

func TestBackgroundRowDoesNotIdleWorkingSession(t *testing.T) {
	r := newRig(t)
	r.emit("aaa", "%1", "UserPromptSubmit")
	Evaluate(r.d)
	r.clk.Advance(5 * time.Second)
	r.writeAgents(r.clk.Now(), refresh.Row{SessionID: "aaa", Background: true, State: "done"})
	if res := Evaluate(r.d); res.Counts.Working != 1 {
		t.Errorf("counts = %+v", res.Counts)
	}
}

// D-17: sessions known only from agents.json never appear.
func TestAgentsOnlySessionsAreInvisible(t *testing.T) {
	r := newRig(t)
	r.writeAgents(r.clk.Now(), refresh.Row{SessionID: "ghost", Background: true, State: "blocked"},
		refresh.Row{SessionID: "ghost2", PID: 1, Status: "waiting"})
	res := Evaluate(r.d)
	if res.Counts.Sessions != 0 || len(res.Sessions) != 0 {
		t.Errorf("result = %+v", res)
	}
	if len(r.tm.calls) > 0 && r.tm.setCalls() > 0 {
		t.Error("markers written for agents-only sessions")
	}
}

func TestDeadPidEndsSession(t *testing.T) {
	r := newRig(t)
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	tok, _ := r.d.Ad.Procs.StartToken(cmd.Process.Pid)
	pid := cmd.Process.Pid
	r.emit("aaa", "%1", "SessionStart", func(e *hook.Event) { e.PID = pid; e.Start = &hook.StartExtra{StartTok: tok} })
	r.emit("aaa", "%1", "UserPromptSubmit", func(e *hook.Event) { e.PID = pid })
	if res := Evaluate(r.d); res.Counts.Working != 1 {
		t.Fatalf("counts = %+v", res.Counts)
	}
	cmd.Process.Kill()
	cmd.Wait()
	if res := Evaluate(r.d); res.Counts.Sessions != 0 {
		t.Errorf("a session whose process died still counts: %+v", res.Counts)
	}
}

func TestPidReuseWithWrongTokenEndsSession(t *testing.T) {
	r := newRig(t)
	r.emit("aaa", "%1", "SessionStart", func(e *hook.Event) { e.PID = os.Getpid(); e.Start = &hook.StartExtra{StartTok: "1"} })
	if res := Evaluate(r.d); res.Counts.Sessions != 0 {
		t.Errorf("start token mismatch must read as dead: %+v", res.Counts)
	}
}

func TestTmuxFailureKeepsCountsAndWritesNoMarks(t *testing.T) {
	r := newRig(t)
	r.emit("aaa", "%1", "PermissionRequest")
	Evaluate(r.d)
	n := len(r.tm.calls)
	r.tm.fail = errors.New("timeout")
	res := Evaluate(r.d)
	if res.Counts.NeedsYou != 1 {
		t.Errorf("counts = %+v; a tmux failure must not zero the bar", res.Counts)
	}
	for _, c := range r.tm.calls[n:] {
		if c[0] == "set-option" {
			t.Error("wrote markers while tmux was failing")
		}
	}
}

func TestLockedEvaluatorServesLastLiveJSON(t *testing.T) {
	r := newRig(t)
	r.emit("aaa", "%1", "PermissionRequest")
	Evaluate(r.d)
	held, err := flock.TryLock(r.d.Ad.Paths.Locks()+"/eval.lock", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Unlock()
	r.emit("bbb", "%2", "PermissionRequest") // not folded: someone else is evaluating
	res := Evaluate(r.d)
	if !res.Stale || res.Counts.NeedsYou != 1 {
		t.Errorf("res = %+v", res)
	}
}

// A stale result with Counts.Runaway set must still carry the alerts behind it.
func TestStaleResultCarriesAlerts(t *testing.T) {
	r := newRig(t).withRules()
	r.d.Cfg.Rules.SessionCost = 0.01
	r.emit("aaa", "%1", "UserPromptSubmit")
	r.snapshot("aaa", 0.05, 0)
	Evaluate(r.d)
	held, err := flock.TryLock(r.d.Ad.Paths.Locks()+"/eval.lock", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Unlock()
	res := Evaluate(r.d)
	if !res.Stale || res.Counts.Runaway != 1 || len(res.Alerts) != 1 {
		t.Errorf("stale result lost its alerts: %+v", res)
	}
}

func TestFoldIsIncrementalAcrossEvaluations(t *testing.T) {
	r := newRig(t)
	r.emit("aaa", "%1", "UserPromptSubmit")
	Evaluate(r.d)
	off := live.Read(r.d.Ad.Paths.Live()).Sessions["aaa"].Offset
	r.emit("aaa", "%1", "Stop")
	Evaluate(r.d)
	s := live.Read(r.d.Ad.Paths.Live()).Sessions["aaa"]
	if s.Offset <= off || s.State != live.Done {
		t.Errorf("offset %d -> %d state %s", off, s.Offset, s.State)
	}
}

func TestOldDirsAreNotDiscovered(t *testing.T) {
	r := newRig(t)
	r.emit("old", "%1", "PermissionRequest")
	old := r.clk.Now().Add(-48 * time.Hour)
	os.Chtimes(filepath.Join(r.d.Ad.Paths.Session("old"), "events.jsonl"), old, old)
	if res := Evaluate(r.d); res.Counts.Sessions != 0 {
		t.Errorf("counts = %+v", res.Counts)
	}
}
