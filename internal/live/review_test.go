package live

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmvaldez/bosun/internal/hook"
	"github.com/jmvaldez/bosun/internal/paths"
	"github.com/jmvaldez/bosun/internal/platform"
)

const fixtureSID = "11111111-2222-3333-4444-555555555555"

// replayFixture runs a captured (redacted) hook file through the real hook, one
// line per invocation, and returns the recorded events in order. Placeholder ids
// are swapped for valid ones; payload shapes are untouched.
func replayFixture(t *testing.T, name string) []hook.Event {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(b), "<session-1>", fixtureSID)
	for _, id := range []string{"agent-1", "agent-2", "agent-3", "prompt-1", "prompt-2", "prompt-3", "prompt-4"} {
		text = strings.ReplaceAll(text, "<"+id+">", strings.ReplaceAll(id, "-", ""))
	}
	root := t.TempDir()
	ad := platform.Adapters{Paths: paths.Paths{State: root}, Clock: platform.SystemClock{}, Getenv: func(string) string { return "" }}
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if hook.Run(ad, nil, strings.NewReader(line)) != 0 {
			t.Fatal("hook exited nonzero")
		}
		time.Sleep(time.Millisecond) // distinct, ordered timestamps
	}
	f, err := os.Open(filepath.Join(root, "sessions", fixtureSID, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var evs []hook.Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 4<<20)
	for sc.Scan() {
		var e hook.Event
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			t.Fatal("bad event line")
		}
		evs = append(evs, e)
	}
	return evs
}

// Review finding 1 (D-8). In the real order, SubagentStop arrives between the
// Stop that still had a background task and the synthetic UserPromptSubmit; the
// turn's edit count and start must carry forward regardless.
func TestD8CarriesTheTurnWhenSubagentStopComesFirst(t *testing.T) {
	s := &Session{ID: "a", State: Idle}
	t0 := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	at := func(d int) string { return t0.Add(time.Duration(d) * time.Second).Format(time.RFC3339Nano) }
	for _, e := range []hook.Event{
		{TS: at(1), Ev: "UserPromptSubmit"},
		{TS: at(2), Ev: "PreToolUse", Tool: "Edit"},
		{TS: at(3), Ev: "SubagentStart", AID: "ag1", AType: "Explore"},
		{TS: at(4), Ev: "Stop", BG: []hook.BGTask{{ID: "ag1", Type: "subagent", Status: "running"}}},
		{TS: at(60), Ev: "SubagentStop", AID: "ag1"},
		{TS: at(61), Ev: "UserPromptSubmit"},
		{TS: at(62), Ev: "Stop"},
	} {
		s.Apply(e)
	}
	if s.Edits != 1 || !s.TurnStart.Equal(t0.Add(time.Second)) {
		t.Errorf("edits=%d turn_start=+%v; the background follow-up must continue the turn", s.Edits, s.TurnStart.Sub(t0))
	}
	// A real prompt after a clean stop, with the subagent long gone, still starts a new turn.
	s.Apply(hook.Event{TS: at(100), Ev: "UserPromptSubmit"})
	if s.Edits != 0 || !s.TurnStart.Equal(t0.Add(100*time.Second)) {
		t.Errorf("a later real prompt did not start a new turn: edits=%d", s.Edits)
	}
}

func TestD8OnTheRealFixture(t *testing.T) {
	s := &Session{ID: fixtureSID, State: Idle}
	var turnStartAtFirstPrompt time.Time
	prompts := 0
	for _, e := range replayFixture(t, "hook-subagent-session.jsonl") {
		s.Apply(e)
		if e.Ev == "UserPromptSubmit" {
			prompts++
			if prompts == 1 {
				turnStartAtFirstPrompt = s.TurnStart
			}
			// Prompt 2 (fixture line 20) is the synthetic follow-up after the Stop
			// on line 9, which still had agent-1 running: it continues turn 1.
			if prompts == 2 && !s.TurnStart.Equal(turnStartAtFirstPrompt) {
				t.Errorf("the background follow-up restarted the turn")
			}
		}
	}
	if prompts < 2 {
		t.Fatal("fixture changed")
	}
}

// Review finding 2. The fixture: a subagent's PermissionRequest (line 8), then
// the main thread's Stop (line 9), then the permission_prompt Notification with
// no agent_id (line 10). Neither the Stop nor the Notification may clear or
// dedupe the wait; only that subagent's own progress ends it.
func TestSubagentWaitSurvivesMainStopOnTheRealFixture(t *testing.T) {
	evs := replayFixture(t, "hook-subagent-session.jsonl")
	s := &Session{ID: fixtureSID, State: Idle}
	var states []string
	for _, e := range evs[:10] { // through the Notification (line 10)
		s.Apply(e)
		states = append(states, s.State)
	}
	// index 7 is the subagent PermissionRequest, 8 the main Stop, 9 the Notification.
	for i, want := range map[int]string{7: NeedsYou, 8: NeedsYou, 9: NeedsYou} {
		if states[i] != want {
			t.Errorf("after event %d (%s) state = %s, want %s", i+1, evs[i].Ev, states[i], want)
		}
	}
	if s.NeedsAID == "" {
		t.Error("the Notification (no agent_id) overwrote the subagent that is waiting")
	}
	// Line 11: the subagent's PostToolUse ends it.
	s.Apply(evs[10])
	if s.State == NeedsYou {
		t.Errorf("the waiting subagent's own PostToolUse did not end the wait")
	}
}

func TestOnlyTheAgentThatWaitsCanEndTheWait(t *testing.T) {
	t0 := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	at := func(d int) string { return t0.Add(time.Duration(d) * time.Second).Format(time.RFC3339Nano) }
	tests := []struct {
		name  string
		start hook.Event // who waits
		other hook.Event // an event from somewhere else
		stays bool
	}{
		{"main waits, a subagent's batch ends", hook.Event{Ev: "PermissionRequest"}, hook.Event{Ev: "PostToolBatch", AID: "sub1"}, true},
		{"main waits, a subagent's tool ends", hook.Event{Ev: "PermissionRequest"}, hook.Event{Ev: "PostToolUse", AID: "sub1"}, true},
		{"main waits, main batch ends", hook.Event{Ev: "PermissionRequest"}, hook.Event{Ev: "PostToolBatch"}, false},
		{"main waits, main stop ends", hook.Event{Ev: "PermissionRequest"}, hook.Event{Ev: "Stop"}, false},
		{"main waits, a real prompt ends", hook.Event{Ev: "PermissionRequest"}, hook.Event{Ev: "UserPromptSubmit"}, false},
		{"subagent waits, main stop does not end", hook.Event{Ev: "PermissionRequest", AID: "sub1"}, hook.Event{Ev: "Stop"}, true},
		{"subagent waits, main batch does not end", hook.Event{Ev: "PermissionRequest", AID: "sub1"}, hook.Event{Ev: "PostToolBatch"}, true},
		{"subagent waits, another subagent's tool does not end", hook.Event{Ev: "PermissionRequest", AID: "sub1"}, hook.Event{Ev: "PostToolUse", AID: "sub2"}, true},
		{"subagent waits, its own tool ends", hook.Event{Ev: "PermissionRequest", AID: "sub1"}, hook.Event{Ev: "PostToolUse", AID: "sub1"}, false},
		{"subagent waits, its own batch ends", hook.Event{Ev: "PermissionRequest", AID: "sub1"}, hook.Event{Ev: "PostToolBatch", AID: "sub1"}, false},
		{"subagent waits, it stops", hook.Event{Ev: "SubagentStop", AID: "sub1"}, hook.Event{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Session{ID: "a", State: Idle}
			s.Apply(hook.Event{TS: at(0), Ev: "SubagentStart", AID: "sub1", AType: "Explore"})
			s.Apply(hook.Event{TS: at(0), Ev: "SubagentStart", AID: "sub2", AType: "Explore"})
			if tt.start.Ev == "SubagentStop" {
				s.Apply(hook.Event{TS: at(1), Ev: "PermissionRequest", AID: "sub1"})
				tt.other = tt.start
			} else {
				tt.start.TS = at(1)
				s.Apply(tt.start)
			}
			if s.State != NeedsYou {
				t.Fatalf("setup: state %s", s.State)
			}
			tt.other.TS = at(2)
			s.Apply(tt.other)
			if got := s.State == NeedsYou; got != tt.stays {
				t.Errorf("still needs-you = %v, want %v", got, tt.stays)
			}
		})
	}
}

// The permission_prompt Notification (no agent_id) after a subagent's request is
// the same episode: it neither restarts the wait nor takes it away from the
// subagent.
func TestNotificationAfterASubagentRequestKeepsItsOwner(t *testing.T) {
	s := &Session{ID: "a", State: Idle}
	t0 := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	at := func(d int) string { return t0.Add(time.Duration(d) * time.Second).Format(time.RFC3339Nano) }
	s.Apply(hook.Event{TS: at(0), Ev: "SubagentStart", AID: "sub1"})
	s.Apply(hook.Event{TS: at(1), Ev: "PermissionRequest", AID: "sub1"})
	s.Apply(hook.Event{TS: at(7), Ev: "Notification", NType: "permission_prompt"})
	if s.NeedsAID != "sub1" || !s.StateSince.Equal(t0.Add(time.Second)) {
		t.Errorf("owner %q since +%v", s.NeedsAID, s.StateSince.Sub(t0))
	}
}

func TestMarksReadsTheOlderSingleServerShape(t *testing.T) {
	path := t.TempDir() + "/marks.json"
	os.WriteFile(path, []byte(`{"server_pid":7,"windows":{"@1":"done"}}`), 0o600)
	m := ReadMarks(path)
	if m.For(7)["@1"] != "done" || len(m.For(8)) != 0 {
		t.Errorf("marks = %+v", m)
	}
}
