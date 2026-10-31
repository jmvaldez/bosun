package live

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmvaldez/bosun/internal/hook"
)

var t0 = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

func at(sec int) string { return t0.Add(time.Duration(sec) * time.Second).Format(time.RFC3339Nano) }

func ev(sec int, name string, mod ...func(*hook.Event)) hook.Event {
	e := hook.Event{V: 1, TS: at(sec), Ev: name, SID: "s1", Tmux: &hook.TmuxLoc{Sock: "/s", SPID: 1, Pane: "%1"}, PID: 10}
	for _, m := range mod {
		m(&e)
	}
	return e
}

func tool(n string) func(*hook.Event)  { return func(e *hook.Event) { e.Tool = n } }
func aid(a string) func(*hook.Event)   { return func(e *hook.Event) { e.AID, e.AType = a, "Explore" } }
func ntype(n string) func(*hook.Event) { return func(e *hook.Event) { e.NType = n } }
func bg(status string) func(*hook.Event) {
	return func(e *hook.Event) { e.BG = []hook.BGTask{{ID: "a1", Type: "subagent", Status: status}} }
}

func run(t *testing.T, evs ...hook.Event) *Session {
	t.Helper()
	s := &Session{ID: "s1", State: Idle}
	for _, e := range evs {
		if !s.Apply(e) {
			t.Fatalf("event %s rejected", e.Ev)
		}
	}
	return s
}

func TestStateMachine(t *testing.T) {
	tests := []struct {
		name string
		evs  []hook.Event
		want string
	}{
		{"prompt starts work", []hook.Event{ev(0, "SessionStart"), ev(1, "UserPromptSubmit")}, Working},
		{"stop is done", []hook.Event{ev(1, "UserPromptSubmit"), ev(2, "Stop")}, Done},
		{"permission request", []hook.Event{ev(1, "UserPromptSubmit"), ev(2, "PreToolUse", tool("Bash")), ev(2, "PermissionRequest")}, NeedsYou},
		{"pretooluse never overrides needs-you", []hook.Event{ev(1, "PermissionRequest"), ev(2, "PreToolUse", tool("Bash"))}, NeedsYou},
		{"posttooluse leaves needs-you", []hook.Event{ev(1, "PermissionRequest"), ev(2, "PostToolUse")}, Working},
		{"posttoolbatch leaves needs-you", []hook.Event{ev(1, "PermissionRequest"), ev(2, "PostToolBatch")}, Working},
		{"prompt leaves needs-you", []hook.Event{ev(1, "PermissionRequest"), ev(2, "UserPromptSubmit")}, Working},
		{"notification permission_prompt enters", []hook.Event{ev(1, "Notification", ntype("permission_prompt"))}, NeedsYou},
		{"elicitation enters", []hook.Event{ev(1, "Notification", ntype("elicitation_dialog"))}, NeedsYou},
		// LESSONS L-24
		{"idle_prompt after stop stays done", []hook.Event{ev(1, "UserPromptSubmit"), ev(2, "Stop"), ev(62, "Notification", ntype("idle_prompt"))}, Done},
		{"stop with running background stays working", []hook.Event{ev(1, "UserPromptSubmit"), ev(2, "Stop", bg("running"))}, Working},
		{"second stop with none is done", []hook.Event{ev(1, "UserPromptSubmit"), ev(2, "Stop", bg("running")), ev(9, "UserPromptSubmit"), ev(10, "Stop")}, Done},
		{"stopfailure is done", []hook.Event{ev(1, "UserPromptSubmit"), ev(2, "StopFailure")}, Done},
		{"session end", []hook.Event{ev(1, "SessionStart"), ev(2, "SessionEnd")}, Idle},
		// A late permission_prompt echo after the prompt was answered is not a new wait.
		{"late permission echo ignored", []hook.Event{ev(1, "PermissionRequest"), ev(2, "PostToolUse"), ev(7, "Notification", ntype("permission_prompt"))}, Working},
		// Main events carrying agent_type but no agent_id stay on the session (L-25).
		{"agent_type on main event", []hook.Event{ev(1, "PreToolUse", func(e *hook.Event) { e.AType = "Explore" })}, Working},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := run(t, tt.evs...).State; got != tt.want {
				t.Errorf("state = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestEpisodeDedupeKeepsStateSince(t *testing.T) {
	s := run(t, ev(1, "PermissionRequest"), ev(7, "Notification", ntype("permission_prompt")))
	if !s.StateSince.Equal(t0.Add(time.Second)) {
		t.Errorf("state_since moved to %v", s.StateSince)
	}
}

func TestEditCountAndTurnCarryOver(t *testing.T) {
	s := run(t, ev(1, "UserPromptSubmit"), ev(2, "PreToolUse", tool("Edit")), ev(3, "PreToolUse", tool("Read")), ev(4, "PreToolUse", tool("Write")))
	if s.Edits != 2 {
		t.Errorf("edits = %d", s.Edits)
	}
	// D-8: stop with background work, then the synthetic prompt continues the turn.
	s = run(t, ev(1, "UserPromptSubmit"), ev(2, "PreToolUse", tool("Edit")), ev(3, "Stop", bg("running")), ev(30, "UserPromptSubmit"), ev(31, "Stop"))
	if s.Edits != 1 || !s.TurnStart.Equal(t0.Add(time.Second)) {
		t.Errorf("edits=%d turn_start=%v; the background follow-up must continue the turn", s.Edits, s.TurnStart)
	}
	// A real prompt after a clean stop starts a new turn.
	s = run(t, ev(1, "UserPromptSubmit"), ev(2, "PreToolUse", tool("Edit")), ev(3, "Stop"), ev(30, "UserPromptSubmit"))
	if s.Edits != 0 || !s.TurnStart.Equal(t0.Add(30*time.Second)) {
		t.Errorf("edits=%d turn_start=%v", s.Edits, s.TurnStart)
	}
}

func TestSubagentsPairOnAgentID(t *testing.T) {
	s := run(t,
		ev(1, "SubagentStart", aid("a1")),
		ev(2, "PreToolUse", aid("a1"), tool("Read")),
		ev(3, "PreToolUse", aid("a1"), tool("Read")),
		ev(4, "SubagentStop", aid("zzz")), // unpaired: ignored
	)
	if sub := s.Subs["a1"]; sub == nil || sub.Tools != 2 || sub.Type != "Explore" {
		t.Fatalf("subs = %+v", s.Subs)
	}
	if s.SubsEnded != 0 || s.Edits != 0 || s.LastTool != "" {
		t.Errorf("unpaired stop or subagent tool leaked into the session: %+v", s)
	}
	s.Apply(ev(5, "SubagentStop", aid("a1")))
	if len(s.Subs) != 0 || s.SubsEnded != 1 {
		t.Errorf("subs=%v ended=%d", s.Subs, s.SubsEnded)
	}
}

func TestBadTimestampLeavesStateSince(t *testing.T) {
	s := run(t, ev(1, "PermissionRequest"))
	before := s.StateSince
	bad := ev(2, "Stop")
	bad.TS = "not a time"
	if s.Apply(bad) {
		t.Error("bad ts was applied")
	}
	if s.State != NeedsYou || !s.StateSince.Equal(before) {
		t.Errorf("state=%s since=%v", s.State, s.StateSince)
	}
}

func TestReconcileNeedsYouExit(t *testing.T) {
	s := run(t, ev(0, "PermissionRequest"))
	row := AgentRow{Found: true, Status: "idle"}
	s.ReconcileAgents(row, t0.Add(-time.Second)) // older than the edge: ignored
	s.ReconcileAgents(row, t0.Add(2*time.Second))
	if s.State != NeedsYou {
		t.Fatal("one read must not leave needs-you")
	}
	s.ReconcileAgents(row, t0.Add(2*time.Second)) // same fetch again: not a second read
	if s.State != NeedsYou {
		t.Fatal("the same fetch counted twice")
	}
	s.ReconcileAgents(row, t0.Add(7*time.Second))
	if s.State != Idle {
		t.Errorf("state = %s after two consecutive not-waiting fetches", s.State)
	}
	// A waiting fetch in between resets the count.
	s = run(t, ev(0, "PermissionRequest"))
	s.ReconcileAgents(row, t0.Add(2*time.Second))
	s.ReconcileAgents(AgentRow{Found: true, Status: "waiting", WaitingFor: "permission prompt"}, t0.Add(7*time.Second))
	s.ReconcileAgents(row, t0.Add(12*time.Second))
	if s.State != NeedsYou {
		t.Errorf("state = %s; waiting fetch should reset", s.State)
	}
}

func TestReconcileIdleOverWorking(t *testing.T) {
	idle := AgentRow{Found: true, Status: "idle"}
	s := run(t, ev(0, "UserPromptSubmit"))
	s.ReconcileAgents(idle, t0.Add(500*time.Millisecond))
	if s.State != Working {
		t.Error("idle inside the grace window must not win")
	}
	s.ReconcileAgents(idle, t0.Add(6*time.Second))
	if s.State != Idle {
		t.Errorf("state = %s", s.State)
	}
	// Pending background work keeps working.
	s = run(t, ev(0, "UserPromptSubmit"), ev(1, "Stop", bg("running")))
	s.ReconcileAgents(idle, t0.Add(6*time.Second))
	if s.State != Working {
		t.Errorf("state = %s; bg pending must hold working", s.State)
	}
	// D-11: background rows never apply the idle rule.
	s = run(t, ev(0, "UserPromptSubmit"))
	s.ReconcileAgents(AgentRow{Found: true, Background: true, Status: "idle"}, t0.Add(6*time.Second))
	if s.State != Working {
		t.Errorf("state = %s; background rows must not idle a session", s.State)
	}
}

func TestQueueOrder(t *testing.T) {
	mk := func(id, state string, sec int) *Session {
		return &Session{ID: id, State: state, StateSince: t0.Add(time.Duration(sec) * time.Second), Loc: Locator{Pane: "%" + id}}
	}
	gone := mk("g", NeedsYou, 0)
	gone.PaneGone = true
	q := Queue(map[string]*Session{
		"a": mk("a", Done, 1), "b": mk("b", NeedsYou, 9), "c": mk("c", NeedsYou, 5),
		"d": mk("d", Done, 0), "e": mk("e", Working, 0), "f": mk("f", Idle, 0), "g": gone,
	})
	var ids []string
	for _, s := range q {
		ids = append(ids, s.ID)
	}
	if got := strings.Join(ids, ""); got != "cbda" {
		t.Errorf("queue = %s, want cbda (needs-you oldest first, then done oldest first)", got)
	}
}

// A needs-you session that also carries a runaway alert displays as needs-you,
// so jump must still reach it; a done runaway session displays as runaway and
// is skipped.
func TestQueueKeepsNeedsYouWithRunaway(t *testing.T) {
	ny := &Session{ID: "n", State: NeedsYou, Runaway: true, Loc: Locator{Pane: "%n"}}
	done := &Session{ID: "d", State: Done, Runaway: true, Loc: Locator{Pane: "%d"}}
	q := Queue(map[string]*Session{"n": ny, "d": done})
	if len(q) != 1 || q[0].ID != "n" {
		t.Errorf("queue = %v, want only the needs-you session", q)
	}
}

// Display precedence: needs-you > runaway > handoff > done > working > quiet >
// idle. Handoff shows only on a done or idle session, and quiet only on a
// working one.
func TestDisplayPrecedenceTable(t *testing.T) {
	states := []string{NeedsYou, Done, Idle, Working}
	for _, state := range states {
		// Needs-you is a state, so the flags run over the other three.
		for mask := 0; mask < 8; mask++ {
			run, ho, qa := mask&1 != 0, mask&2 != 0, mask&4 != 0
			s := &Session{State: state, Runaway: run, HandoffSuggested: ho, QuietAlert: qa}
			want := state
			switch {
			case state == NeedsYou:
			case run:
				want = Runaway
			case ho && (state == Done || state == Idle):
				want = Handoff
			case qa && state == Working:
				want = Quiet
			}
			if got := s.Display(); got != want {
				t.Errorf("state=%s runaway=%v handoff=%v quiet=%v: Display = %q, want %q", state, run, ho, qa, got, want)
			}
		}
	}
	both := &Session{State: Done, Runaway: true, HandoffSuggested: true}
	if both.Display() != Runaway {
		t.Error("a runaway session with a handoff suggested must show runaway")
	}
	ny := &Session{State: NeedsYou, Runaway: true, HandoffSuggested: true, QuietAlert: true}
	if ny.Display() != NeedsYou {
		t.Error("needs-you must hide both")
	}
}

func TestHandedOffDisplayAndQueue(t *testing.T) {
	for _, state := range []string{Done, Idle} {
		s := &Session{State: state, Runaway: true, HandoffSuggested: true, HandedOff: true}
		if got := s.Display(); got != HandedOff {
			t.Errorf("%s display = %q, want handed-off", state, got)
		}
	}
	if got := (&Session{State: Working, HandedOff: true}).Display(); got != Working {
		t.Errorf("a handed-off session back at work displays %q", got)
	}
	if got := (&Session{State: NeedsYou, HandedOff: true}).Display(); got != NeedsYou {
		t.Errorf("needs-you display = %q", got)
	}
	q := Queue(map[string]*Session{"a": {ID: "a", State: Done, HandedOff: true, Loc: Locator{Pane: "%1"}}})
	if len(q) != 0 {
		t.Errorf("queue = %v, a handed-off session must leave it", q)
	}
}

func TestSessionStartRecordsHandedOffFrom(t *testing.T) {
	s := run(t, ev(0, "SessionStart", func(e *hook.Event) { e.Start = &hook.StartExtra{HandoffFrom: "old-1"} }))
	if s.HandedOffFrom != "old-1" {
		t.Errorf("HandedOffFrom = %q", s.HandedOffFrom)
	}
	s.Apply(ev(5, "SessionStart", func(e *hook.Event) { e.Src = "compact"; e.Start = &hook.StartExtra{} }))
	if s.HandedOffFrom != "old-1" {
		t.Errorf("a later SessionStart cleared the link: %q", s.HandedOffFrom)
	}
}

func TestRankOrdersHandoffBetweenRunawayAndDone(t *testing.T) {
	order := []string{NeedsYou, Runaway, Handoff, Done, Working, Quiet, HandedOff, Idle}
	for i := 1; i < len(order); i++ {
		if Rank(order[i-1]) >= Rank(order[i]) {
			t.Errorf("Rank(%s) = %d is not below Rank(%s) = %d", order[i-1], Rank(order[i-1]), order[i], Rank(order[i]))
		}
	}
}

// A done session with a handoff suggested is still waiting for review, so it
// stays in the queue; a done runaway session does not.
func TestQueueKeepsDoneWithHandoffButNotRunaway(t *testing.T) {
	ho := &Session{ID: "h", State: Done, HandoffSuggested: true, Loc: Locator{Pane: "%h"}}
	run := &Session{ID: "r", State: Done, Runaway: true, Loc: Locator{Pane: "%r"}}
	q := Queue(map[string]*Session{"h": ho, "r": run})
	if len(q) != 1 || q[0].ID != "h" {
		t.Errorf("queue = %v, want only the handoff session", q)
	}
}

func writeEvents(t *testing.T, path string, evs ...hook.Event) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, e := range evs {
		b, _ := json.Marshal(e)
		f.Write(append(b, '\n'))
	}
}

func TestFoldPartialTrailingLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "events.jsonl")
	line1, _ := json.Marshal(ev(1, "UserPromptSubmit"))
	line2, _ := json.Marshal(ev(2, "Stop"))
	os.WriteFile(p, append(append(line1, '\n'), line2[:10]...), 0o600)
	s := &Session{ID: "s1", State: Idle}
	if _, err := FoldFile(p, s); err != nil {
		t.Fatal(err)
	}
	if s.State != Working || s.Offset != int64(len(line1)+1) {
		t.Fatalf("state=%s offset=%d", s.State, s.Offset)
	}
	// The writer finishes the line; it is applied exactly once.
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.Write(append(line2[10:], '\n'))
	f.Close()
	if _, err := FoldFile(p, s); err != nil {
		t.Fatal(err)
	}
	if s.State != Done {
		t.Errorf("state = %s", s.State)
	}
	applied, _ := FoldFile(p, s)
	if applied {
		t.Error("refold applied events again")
	}
}

func TestFoldHandlesOneMegabyteLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "events.jsonl")
	huge := ev(1, "UserPromptSubmit")
	huge.Tool = strings.Repeat("x", 1<<20)
	writeEvents(t, p, huge, ev(2, "Stop"))
	s := &Session{ID: "s1", State: Idle}
	if _, err := FoldFile(p, s); err != nil {
		t.Fatal(err)
	}
	if s.State != Done {
		t.Errorf("state = %s; the event after a 1 MB line was skipped", s.State)
	}
}

func TestFoldSkipsGarbageLines(t *testing.T) {
	p := filepath.Join(t.TempDir(), "events.jsonl")
	b, _ := json.Marshal(ev(2, "Stop"))
	os.WriteFile(p, bytes.Join([][]byte{[]byte("not json"), []byte(`{"ts":`), b, nil}, []byte("\n")), 0o600)
	s := &Session{ID: "s1", State: Idle}
	FoldFile(p, s)
	if s.State != Done {
		t.Errorf("state = %s", s.State)
	}
}
