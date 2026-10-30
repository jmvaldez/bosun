package hook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fixtures use placeholder ids, which are (correctly) rejected as session
// ids. Swapping in a real-looking id keeps the payload shapes untouched.
func replayWithSID(t *testing.T, e *env, name string) []Event {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(b), "<session-1>", sid)
	for _, id := range []string{"agent-1", "agent-2", "agent-3", "prompt-1", "prompt-2", "prompt-3", "prompt-4"} {
		text = strings.ReplaceAll(text, "<"+id+">", strings.ReplaceAll(id, "-", ""))
	}
	n := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if code := e.run(line, "--plugin-version", "0.1.0"); code != 0 {
			t.Fatalf("line %d: exit %d", n, code)
		}
		n++
	}
	return e.events(t, sid)
}

func TestReplaySubagentFixture(t *testing.T) {
	e := newEnv(t)
	evs := replayWithSID(t, e, "hook-subagent-session.jsonl")
	if len(evs) != 35 {
		t.Fatalf("recorded %d events, want 35", len(evs))
	}
	starts, stops := map[string]bool{}, map[string]bool{}
	var unpaired int
	for _, ev := range evs {
		switch ev.Ev {
		case "SubagentStart":
			starts[ev.AID] = true
		case "SubagentStop":
			stops[ev.AID] = true
		}
		if ev.Ev == "Stop" && len(ev.BG) > 0 && ev.BG[0].Type != "subagent" {
			t.Errorf("bg = %+v", ev.BG)
		}
	}
	for id := range stops {
		if !starts[id] {
			unpaired++
		}
	}
	if len(starts) != 2 || len(stops) != 3 || unpaired != 1 {
		t.Errorf("starts=%v stops=%v unpaired=%d; want 2 starts, 3 stops, 1 unpaired internal stop", starts, stops, unpaired)
	}
	// In this capture every subagent PreToolUse carries agent_type beside its
	// agent_id.
	for _, ev := range evs {
		if ev.Ev == "PreToolUse" && ev.AID != "" && ev.AType == "" {
			t.Errorf("subagent PreToolUse lost agent_type: %+v", ev)
		}
	}
	var withBG int
	for _, ev := range evs {
		if len(ev.BG) > 0 {
			withBG++
			if ev.BG[0].Status == "" || ev.BG[0].ID == "" {
				t.Errorf("bg = %+v", ev.BG)
			}
		}
	}
	if withBG == 0 {
		t.Error("no event carried background_tasks; fixture or decoder changed")
	}
	raw, _ := os.ReadFile(filepath.Join(e.ad.Paths.Session(sid), "events.jsonl"))
	if strings.Contains(string(raw), "<redacted>") {
		t.Error("redacted text field reached events.jsonl")
	}
}

func TestReplayNotificationFixture(t *testing.T) {
	e := newEnv(t)
	evs := replayWithSID(t, e, "hook-notification.jsonl")
	var types []string
	for _, ev := range evs {
		if ev.Ev == "Notification" {
			types = append(types, ev.NType)
		}
	}
	if got := strings.Join(types, ","); got != "permission_prompt,idle_prompt" {
		t.Errorf("notification types = %s", got)
	}
}
