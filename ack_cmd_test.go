package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// aliveSession writes a session that the catch-up evaluation inside `ack` will
// keep alerting on: a pane locator (tmux is unreachable in tests, so the last
// known answer, "pane exists", stands) and a status line total over a low step.
func aliveSession(t *testing.T, root, sid string, cost float64) {
	t.Helper()
	dir := root + "/state/sessions/" + sid
	os.MkdirAll(dir, 0o700)
	ev, _ := json.Marshal(map[string]any{"v": 1, "ts": time.Now().UTC().Format(time.RFC3339Nano), "ev": "UserPromptSubmit",
		"sid": sid, "tmux": map[string]any{"sock": "/s", "spid": 1, "pane": "%1"}})
	os.WriteFile(dir+"/events.jsonl", append(ev, '\n'), 0o600)
	snap, _ := json.Marshal(map[string]any{"session_id": sid, "cost_usd": cost})
	os.WriteFile(dir+"/statusline.json", snap, 0o600)
}

func TestAckSubprocess(t *testing.T) {
	env, root := isolated(t)
	os.WriteFile(root+"/config/config.toml", []byte(testConfig+"[rules]\nsession_cost = 0.01\n"), 0o600)
	aliveSession(t, root, "aaaaaaaa-0000-0000-0000-00000000000a", 0.05)
	aliveSession(t, root, "bbbbbbbb-0000-0000-0000-00000000000b", 0.05)
	runBosun(t, env, nil, "status", "--catch-up") // finds both alerts (quietly)

	type alert struct {
		ID        string
		Rule      string
		Dismissed bool
	}
	load := func() map[string]alert {
		var got map[string]alert
		b, _ := os.ReadFile(root + "/state/alerts.json")
		json.Unmarshal(b, &got)
		return got
	}
	got := load()
	if len(got) != 2 {
		t.Fatalf("alerts = %v", got)
	}
	var id string
	for _, a := range got {
		id = a.ID
		break
	}

	if r := runBosun(t, env, nil, "ack"); r.code != 2 {
		t.Errorf("ack with no argument: exit %d", r.code)
	}
	if r := runBosun(t, env, nil, "ack", "zzzzz"); r.code != 1 {
		t.Errorf("ack of an unknown id: exit %d", r.code)
	}
	r := runBosun(t, env, nil, "ack", id)
	if r.code != 0 || !strings.Contains(string(r.out), "dismissed "+id) {
		t.Fatalf("exit %d\n%s", r.code, r.out)
	}
	n := 0
	for _, a := range load() {
		if a.Dismissed {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d dismissed after ack ID, want 1", n)
	}
	// The dismissed alert is gone from the segment's data.
	var st struct{ Counts struct{ Runaway int } }
	json.Unmarshal(runBosun(t, env, nil, "status", "--json").out, &st)
	if st.Counts.Runaway != 1 {
		t.Errorf("runaway count = %d after dismissing one of two", st.Counts.Runaway)
	}
	if r := runBosun(t, env, nil, "ack", "--all"); r.code != 0 || !strings.Contains(string(r.out), "dismissed") {
		t.Errorf("ack --all: %d %s", r.code, r.out)
	}
	for _, a := range load() {
		if !a.Dismissed {
			t.Errorf("alert %s survived ack --all", a.ID)
		}
	}
	if r := runBosun(t, env, nil, "ack", "--all"); !strings.Contains(string(r.out), "nothing to dismiss") {
		t.Errorf("a second ack --all: %s", r.out)
	}
}

func TestNotifyTestReportsOff(t *testing.T) {
	env, _ := isolated(t) // config has notify.backend = "off"
	r := runBosun(t, env, nil, "notify", "--test")
	if r.code != 1 {
		t.Errorf("exit %d, want 1 when notifications are off", r.code)
	}
	if r := runBosun(t, env, nil, "notify"); r.code != 2 {
		t.Errorf("exit %d for missing --test", r.code)
	}
}

func TestNotifyDispatchOffAndBadFlags(t *testing.T) {
	env, _ := isolated(t)
	r := runBosun(t, env, nil, "notify-dispatch", "--kind", "needs-you", "--session", "s1")
	if r.code != 0 || !strings.HasPrefix(string(r.out), "off:") {
		t.Errorf("exit %d out %q", r.code, r.out)
	}
	if r := runBosun(t, env, nil, "notify-dispatch", "--bogus", "1"); r.code != 2 {
		t.Errorf("exit %d for an unknown flag", r.code)
	}
}
