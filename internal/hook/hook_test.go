package hook

import (
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jmvaldez/bosun/internal/heartbeat"
	"github.com/jmvaldez/bosun/internal/paths"
	"github.com/jmvaldez/bosun/internal/platform"
)

type env struct {
	root string
	vars map[string]string
	ad   platform.Adapters
}

// newEnv builds Adapters whose every dir lives under one temp root, so tests
// can scan that root for leaks and never touch the real home (LESSONS L-03).
func newEnv(t *testing.T) *env {
	t.Helper()
	root := t.TempDir()
	e := &env{root: root, vars: map[string]string{"HOME": root + "/home"}}
	for _, d := range []string{"CONFIG", "STATE", "CACHE", "DATA"} {
		e.vars["BOSUN_"+d+"_DIR"] = filepath.Join(root, strings.ToLower(d))
	}
	e.rebuild()
	return e
}

func (e *env) rebuild() {
	getenv := func(k string) string { return e.vars[k] }
	e.ad = platform.Adapters{
		Paths:  paths.Resolve(getenv),
		Procs:  platform.NewProcs(),
		Clock:  platform.SystemClock{},
		Getenv: getenv,
	}
}

func (e *env) run(payload string, args ...string) int {
	return Run(e.ad, args, strings.NewReader(payload))
}

func (e *env) events(t *testing.T, sid string) []Event {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(e.ad.Paths.Session(sid), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var out []Event
	for _, line := range bytes.Split(bytes.TrimRight(b, "\n"), []byte("\n")) {
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			t.Fatalf("invalid line %q: %v", line, err)
		}
		out = append(out, ev)
	}
	return out
}

const sid = "11111111-2222-3333-4444-555555555555"

func TestRecordsAllowlistedFields(t *testing.T) {
	e := newEnv(t)
	e.vars["TMUX"] = "/tmp/tmux-1000/default,4242,0"
	e.vars["TMUX_PANE"] = "%7"
	e.vars["CLAUDE_PID"] = "999"
	e.rebuild()
	code := e.run(`{"session_id":"`+sid+`","hook_event_name":"PreToolUse","tool_name":"Bash","tool_use_id":"toolu_01ab","prompt_id":"p-1","agent_id":"a1","agent_type":"Explore","tool_input":{"command":"rm -rf"}}`, "--plugin-version", "0.1.0")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	evs := e.events(t, sid)
	if len(evs) != 1 {
		t.Fatalf("events = %d", len(evs))
	}
	got := evs[0]
	if got.Ev != "PreToolUse" || got.Tool != "Bash" || got.TUID != "toolu_01ab" || got.PromptID != "p-1" ||
		got.AID != "a1" || got.AType != "Explore" || got.PID != 999 {
		t.Errorf("event = %+v", got)
	}
	if got.Tmux == nil || got.Tmux.Sock != "/tmp/tmux-1000/default" || got.Tmux.SPID != 4242 || got.Tmux.Pane != "%7" {
		t.Errorf("tmux = %+v", got.Tmux)
	}
	if got.Start != nil {
		t.Error("only SessionStart carries extras")
	}
	hb, _ := heartbeat.Read(e.ad.Paths.Heartbeat())
	if hb.PluginVersion != "0.1.0" || len(hb.LatencyUS["PreToolUse"]) != 1 {
		t.Errorf("heartbeat = %+v", hb)
	}
}

func TestSessionStartExtras(t *testing.T) {
	e := newEnv(t)
	repo := filepath.Join(e.root, "work", "api")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	os.WriteFile(filepath.Join(repo, ".git/HEAD"), []byte("ref: refs/heads/feat/abc-42-thing\n"), 0o644)
	os.WriteFile(filepath.Join(repo, ".git/config"), []byte("[remote \"origin\"]\n\turl = git@github.com:acme/api.git\n"), 0o644)
	os.MkdirAll(e.ad.Paths.State, 0o755)
	os.WriteFile(e.ad.Paths.Identity(), []byte(`{"account_key":"me@example.com"}`), 0o600)
	e.vars["CLAUDE_PID"] = itoa(os.Getpid())
	e.vars["CLAUDE_CODE_ENTRYPOINT"] = "sdk-cli"
	e.rebuild()
	payload, _ := json.Marshal(map[string]string{
		"session_id": sid, "hook_event_name": "SessionStart", "source": "startup",
		"cwd": repo, "transcript_path": "/home/x/.claude/projects/p/" + sid + ".jsonl",
	})
	e.run(string(payload))
	x := e.events(t, sid)[0].Start
	if x == nil {
		t.Fatal("no start extras")
	}
	if x.Repo != "acme/api" || x.Branch != "feat/abc-42-thing" || x.Task != "ABC-42" || x.TaskSrc != "branch" ||
		x.Account != "me@example.com" || x.Entrypoint != "sdk-cli" || x.Cwd != repo {
		t.Errorf("extras = %+v", x)
	}
	if x.StartTok == "" || x.StartTok == "0" {
		t.Errorf("start token = %q", x.StartTok)
	}
	if got := e.events(t, sid)[0].Src; got != "startup" {
		t.Errorf("src = %q", got)
	}
}

func TestEnvTaskBeatsBranch(t *testing.T) {
	e := newEnv(t)
	repo := filepath.Join(e.root, "r")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	os.WriteFile(filepath.Join(repo, ".git/HEAD"), []byte("ref: refs/heads/ABC-1\n"), 0o644)
	e.vars["BOSUN_TASK"] = "ops-7"
	e.rebuild()
	e.run(`{"session_id":"` + sid + `","hook_event_name":"SessionStart","cwd":"` + repo + `"}`)
	x := e.events(t, sid)[0].Start
	if x.Task != "OPS-7" || x.TaskSrc != "env" {
		t.Errorf("extras = %+v", x)
	}
}

func TestEnvReadsStayOnAllowlist(t *testing.T) {
	e := newEnv(t)
	var asked []string
	base := e.ad.Getenv
	e.ad.Getenv = func(k string) string { asked = append(asked, k); return base(k) }
	e.run(`{"session_id":"` + sid + `","hook_event_name":"SessionStart","cwd":"/nonexistent"}`)
	allowed := map[string]bool{
		"TMUX": true, "TMUX_PANE": true, "CLAUDE_PID": true, "CLAUDE_CODE_SESSION_ID": true,
		"BOSUN_TASK": true, "CLAUDE_CODE_ENTRYPOINT": true,
	}
	for _, k := range asked {
		if !allowed[k] {
			t.Errorf("hook read %s", k)
		}
	}
	if len(asked) == 0 {
		t.Error("spy saw no reads; the test is not testing anything")
	}
}

func TestUnknownEventAndFlagsIgnored(t *testing.T) {
	e := newEnv(t)
	if e.run(`{"session_id":"`+sid+`","hook_event_name":"MessageDisplay"}`, "--future-flag", "x") != 0 {
		t.Fatal("nonzero")
	}
	if _, err := os.Stat(e.ad.Paths.Sessions()); !os.IsNotExist(err) {
		t.Errorf("unknown event created state: %v", err)
	}
	e.run(`{"session_id":"`+sid+`","hook_event_name":"Stop"}`, "--future-flag", "--plugin-version=0.2.0")
	if len(e.events(t, sid)) != 1 {
		t.Error("known event with unknown flag should record")
	}
}

func TestReasonAndSourceAllowlisted(t *testing.T) {
	e := newEnv(t)
	e.run(`{"session_id":"` + sid + `","hook_event_name":"SessionEnd","reason":"free text from the user"}`)
	e.run(`{"session_id":"` + sid + `","hook_event_name":"SessionEnd","reason":"clear"}`)
	e.run(`{"session_id":"` + sid + `","hook_event_name":"SessionStart","source":"mystery"}`)
	evs := e.events(t, sid)
	if evs[0].Reason != "other" || evs[1].Reason != "clear" || evs[2].Src != "other" {
		t.Errorf("got %q %q %q", evs[0].Reason, evs[1].Reason, evs[2].Src)
	}
}

func TestBackgroundTasksReducedToEnums(t *testing.T) {
	e := newEnv(t)
	e.run(`{"session_id":"` + sid + `","hook_event_name":"Stop","background_tasks":[{"id":"a1ec1b51aa9c6ef01","type":"subagent","status":"running","description":"List files","command":"rm -rf /","agent_type":"Explore"}]}`)
	got := e.events(t, sid)[0].BG
	if len(got) != 1 || got[0] != (BGTask{ID: "a1ec1b51aa9c6ef01", Type: "subagent", Status: "running"}) {
		t.Errorf("bg = %+v", got)
	}
}

func TestLongFieldsKeepValidJSONAndLocator(t *testing.T) {
	e := newEnv(t)
	e.vars["TMUX"] = "/tmp/tmux-1000/default,4242,0"
	e.vars["TMUX_PANE"] = "%3"
	e.rebuild()
	big := strings.Repeat("x", 5000)
	repo := filepath.Join(e.root, "r")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	os.WriteFile(filepath.Join(repo, ".git/HEAD"), []byte("ref: refs/heads/"+big+"\n"), 0o644)
	p, _ := json.Marshal(map[string]string{"session_id": sid, "hook_event_name": "SessionStart", "cwd": repo + "/" + big, "transcript_path": big})
	e.run(string(p))
	ev := e.events(t, sid)[0]
	if ev.Tmux == nil || ev.Tmux.SPID != 4242 || ev.Tmux.Pane != "%3" {
		t.Fatalf("locator lost: %+v", ev.Tmux)
	}
}

func TestEncodeDropsExtrasThenBGWhenOverBudget(t *testing.T) {
	quotes := strings.Repeat(`"`, 256) // each quote escapes to two bytes
	e := Event{V: 1, TS: "t", Ev: "SessionStart", SID: sid, Tmux: &TmuxLoc{Sock: "/s", SPID: 5, Pane: "%1"},
		Start: &StartExtra{Cwd: quotes, Transcript: quotes, Repo: quotes, Branch: quotes}}
	b, err := e.encode()
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > MaxLine {
		t.Fatalf("line is %d bytes", len(b))
	}
	var got Event
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if !got.Truncated || got.Start != nil || got.Tmux == nil || got.Tmux.SPID != 5 {
		t.Errorf("got %+v", got)
	}
}

func TestClipStringKeepsRunes(t *testing.T) {
	s := strings.Repeat("é", 200) // 400 bytes
	c := clipString(s)
	if len(c) > maxField {
		t.Fatalf("len %d", len(c))
	}
	for _, r := range c {
		if r == '\uFFFD' {
			t.Fatal("cut inside a rune")
		}
	}
}

func TestTruncatedStdinStillRecordsEarlyKeys(t *testing.T) {
	e := newEnv(t)
	huge := `{"session_id":"` + sid + `","hook_event_name":"Stop","last_assistant_message":"` + strings.Repeat("y", 10<<20)
	if e.run(huge) != 0 {
		t.Fatal("nonzero")
	}
	// hook_event_name arrived before the cut, so the event is kept.
	if len(e.events(t, sid)) != 1 {
		t.Error("early keys of an oversized payload were dropped")
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// ---- hook contract (SPEC section 11): exit 0 and quick in every failure mode.

func TestContract(t *testing.T) {
	traversal := []string{"../../evil", "a/b", "", strings.Repeat("a", 65), "id with space", "..", "a\x00b"}
	t.Run("malformed and empty", func(t *testing.T) {
		e := newEnv(t)
		for _, in := range []string{"", "{", "not json", "[]", "null", `{"session_id":5}`, `{"hook_event_name":"Stop"}`, "\x00\xff\xfe"} {
			if e.run(in) != 0 {
				t.Errorf("%q: nonzero", in)
			}
		}
		if _, err := os.Stat(e.ad.Paths.Sessions()); !os.IsNotExist(err) {
			t.Error("garbage created session state")
		}
	})

	t.Run("traversal session ids write nothing", func(t *testing.T) {
		e := newEnv(t)
		for _, id := range traversal {
			p, _ := json.Marshal(map[string]string{"session_id": id, "hook_event_name": "Stop"})
			if e.run(string(p)) != 0 {
				t.Errorf("%q: nonzero", id)
			}
		}
		var files []string
		filepath.WalkDir(e.root, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				files = append(files, p)
			}
			return nil
		})
		sort.Strings(files)
		if len(files) != 0 {
			t.Errorf("files written: %v", files)
		}
	})

	t.Run("unwritable state dir", func(t *testing.T) {
		if os.Getuid() == 0 {
			t.Skip("root ignores modes")
		}
		e := newEnv(t)
		os.MkdirAll(e.ad.Paths.State, 0o500)
		defer os.Chmod(e.ad.Paths.State, 0o700)
		if e.run(`{"session_id":"`+sid+`","hook_event_name":"Stop"}`) != 0 {
			t.Error("nonzero")
		}
	})

	t.Run("state dir is a file", func(t *testing.T) {
		e := newEnv(t)
		os.WriteFile(e.ad.Paths.State, []byte("x"), 0o600)
		if e.run(`{"session_id":"`+sid+`","hook_event_name":"Stop"}`) != 0 {
			t.Error("nonzero")
		}
	})

	t.Run("HOME unset and no overrides", func(t *testing.T) {
		e := newEnv(t)
		e.vars = map[string]string{}
		e.rebuild()
		if e.run(`{"session_id":"`+sid+`","hook_event_name":"Stop"}`) != 0 {
			t.Error("nonzero")
		}
	})

	t.Run("panic still exits zero", func(t *testing.T) {
		e := newEnv(t)
		e.ad.Clock = panicClock{}
		if e.run(`{"session_id":"`+sid+`","hook_event_name":"Stop"}`) != 0 {
			t.Error("nonzero after panic")
		}
	})

	t.Run("never-closing stdin returns promptly", func(t *testing.T) {
		e := newEnv(t)
		pr, pw := io.Pipe()
		defer pw.Close()
		start := time.Now()
		code := Run(e.ad, nil, pr)
		if code != 0 {
			t.Error("nonzero")
		}
		if d := time.Since(start); d > 300*time.Millisecond {
			t.Errorf("took %v", d)
		}
	})

	t.Run("10 MB stdin returns promptly", func(t *testing.T) {
		e := newEnv(t)
		start := time.Now()
		code := Run(e.ad, nil, bytes.NewReader(bytes.Repeat([]byte("z"), 10<<20)))
		if code != 0 || time.Since(start) > 300*time.Millisecond {
			t.Errorf("code=%d after %v", code, time.Since(start))
		}
	})
}

type panicClock struct{}

func (panicClock) Now() time.Time { panic("forced") }

// ---- TestPromptFilter: SPEC section 11, LESSONS L-21, FACTS-CHECK F3.

const sentinel = "SENTINEL-7c1f-PROMPT-TEXT"

// textPayloads builds, for each event, a payload carrying the sentinel in every
// text-bearing path FACTS-CHECK F3 lists, plus the older paths from FACTS F3.
func textPayloads() map[string]map[string]any {
	s := sentinel
	nested := map[string]any{"command": s, "file_path": s, "nested": map[string]any{"deep": []any{s}}}
	bg := []any{map[string]any{"id": "a1", "type": "subagent", "status": "running",
		"description": s, "command": s, "agent_type": s, "name": s, "prompt": s}}
	common := func(ev string) map[string]any {
		return map[string]any{
			"session_id": sid, "hook_event_name": ev, "prompt_id": "pid-1",
			// cwd and transcript_path carry the sentinel here: only SessionStart
			// stores them, and it gets clean values below.
			"cwd": "/work/" + s, "transcript_path": "/t/" + s + ".jsonl",
			"scratchpad_dir": "/tmp/" + s, "model": s, "session_name": s, "name": s,
			"effort": map[string]any{"level": s},
		}
	}
	out := map[string]map[string]any{}
	for _, ev := range Events {
		out[ev] = common(ev)
	}
	set := func(ev string, kv map[string]any) {
		for k, v := range kv {
			out[ev][k] = v
		}
	}
	set("UserPromptSubmit", map[string]any{"prompt": s, "permission_mode": s})
	set("PreToolUse", map[string]any{"tool_name": "Bash", "tool_use_id": "toolu_1", "tool_input": nested, "permission_mode": s})
	set("PermissionRequest", map[string]any{"tool_name": "Bash", "tool_input": nested,
		"permission_suggestions": []any{map[string]any{"type": "addDirectories", "directories": []any{s}, "rules": []any{map[string]any{"toolName": s, "ruleContent": s}}}}})
	set("PostToolUse", map[string]any{"tool_name": "Read", "tool_use_id": "toolu_2", "tool_input": nested, "tool_response": nested, "duration_ms": 5})
	set("PostToolUseFailure", map[string]any{"tool_name": "Bash", "tool_input": nested, "error": s, "is_interrupt": false})
	set("PostToolBatch", map[string]any{"tool_calls": []any{map[string]any{"tool_input": nested, "tool_name": "Bash", "tool_response": nested, "tool_use_id": "toolu_3"}}})
	set("Notification", map[string]any{"message": s, "title": s, "notification_type": "permission_prompt"})
	set("SubagentStart", map[string]any{"agent_id": "a1", "agent_type": "Explore"})
	set("SubagentStop", map[string]any{"agent_id": "a1", "agent_type": "Explore", "agent_transcript_path": "/t/" + s,
		"last_assistant_message": s, "background_tasks": bg, "session_crons": []any{map[string]any{"prompt": s, "schedule": s}}})
	set("Stop", map[string]any{"last_assistant_message": s, "background_tasks": bg, "session_crons": []any{map[string]any{"prompt": s}}, "stop_hook_active": false})
	set("StopFailure", map[string]any{"error": s, "last_assistant_message": s})
	set("SessionEnd", map[string]any{"reason": s})
	set("SessionStart", map[string]any{"source": s})
	// SessionStart stores cwd and transcript_path on purpose.
	out["SessionStart"]["cwd"] = "/work/project"
	out["SessionStart"]["transcript_path"] = "/t/project.jsonl"
	return out
}

func TestPromptFilter(t *testing.T) {
	e := newEnv(t)
	for ev, p := range textPayloads() {
		b, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		if e.run(string(b)) != 0 {
			t.Fatalf("%s: nonzero", ev)
		}
	}
	if got := len(e.events(t, sid)); got != len(Events) {
		t.Fatalf("recorded %d events, want %d", got, len(Events))
	}
	n := 0
	filepath.WalkDir(e.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		n++
		b, _ := os.ReadFile(p)
		if bytes.Contains(b, []byte(sentinel)) || bytes.Contains(b, []byte("SENTINEL")) {
			t.Errorf("sentinel leaked into %s:\n%s", p, b)
		}
		return nil
	})
	if n == 0 {
		t.Fatal("no files scanned")
	}
}

// ---- latency

func TestHookLatency(t *testing.T) {
	if testing.Short() {
		t.Skip("latency is load sensitive; release CI runs -short")
	}
	e := newEnv(t)
	payload := `{"session_id":"` + sid + `","hook_event_name":"PreToolUse","tool_name":"Bash","tool_use_id":"toolu_1","prompt_id":"p","tool_input":{"command":"ls"}}`
	const n = 1000
	ds := make([]time.Duration, n)
	for i := range ds {
		start := time.Now()
		e.run(payload, "--plugin-version", "0.1.0")
		ds[i] = time.Since(start)
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	p50, p95 := ds[n/2], ds[n*95/100]
	t.Logf("hook latency over %d runs: p50=%v p95=%v max=%v", n, p50, p95, ds[n-1])
	if p95 > 40*time.Millisecond {
		t.Fatalf("p95 = %v, want <= 40ms", p95)
	}
}

// 100 hook runs yield 100 samples (LESSONS L-15).
func TestHeartbeatSamplesAccumulate(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 100; i++ {
		e.run(`{"session_id":"`+sid+`","hook_event_name":"PostToolUse"}`, "--plugin-version", "0.1.0")
	}
	hb, _ := heartbeat.Read(e.ad.Paths.Heartbeat())
	if got := len(hb.LatencyUS["PostToolUse"]); got != 100 {
		t.Fatalf("samples = %d, want 100", got)
	}
}

type spawnSpy struct{ calls [][]string }

func (s *spawnSpy) Spawn(name string, args ...string) error {
	s.calls = append(s.calls, args)
	return nil
}

func TestNotifyingEdgesForkTheDispatcher(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		env     map[string]string
		want    string // joined args, "" for none
	}{
		{"permission request", `"hook_event_name":"PermissionRequest"`, nil, "notify-dispatch --kind needs-you --session " + sid + " --detail permission prompt"},
		{"permission prompt notification", `"hook_event_name":"Notification","notification_type":"permission_prompt","message":"SENTINEL"`, nil, "notify-dispatch --kind needs-you --session " + sid + " --detail permission prompt"},
		{"elicitation", `"hook_event_name":"Notification","notification_type":"elicitation_dialog"`, nil, "notify-dispatch --kind needs-you --session " + sid + " --detail dialog"},
		{"agent needs input", `"hook_event_name":"Notification","notification_type":"agent_needs_input"`, nil, "notify-dispatch --kind needs-you --session " + sid + " --detail input needed"},
		{"idle_prompt is not an edge", `"hook_event_name":"Notification","notification_type":"idle_prompt"`, nil, ""},
		{"stop", `"hook_event_name":"Stop"`, nil, "notify-dispatch --kind done --session " + sid},
		{"stop with background work running", `"hook_event_name":"Stop","background_tasks":[{"id":"a1","type":"subagent","status":"running"}]`, nil, ""},
		{"stop failure", `"hook_event_name":"StopFailure","error":"SENTINEL"`, nil, "notify-dispatch --kind failed --session " + sid},
		{"pre tool use", `"hook_event_name":"PreToolUse"`, nil, ""},
		{"sdk-cli never notifies (D-10)", `"hook_event_name":"PermissionRequest"`, map[string]string{"CLAUDE_CODE_ENTRYPOINT": "sdk-cli"}, ""},
		{"interactive entrypoint notifies", `"hook_event_name":"PermissionRequest"`, map[string]string{"CLAUDE_CODE_ENTRYPOINT": "cli"}, "notify-dispatch --kind needs-you --session " + sid + " --detail permission prompt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			spy := &spawnSpy{}
			e.ad.Spawner = spy
			for k, v := range tt.env {
				e.vars[k] = v
			}
			e.rebuild()
			e.ad.Spawner = spy
			e.run(`{"session_id":"` + sid + `",` + tt.payload + `}`)
			got := ""
			if len(spy.calls) > 0 {
				got = strings.Join(spy.calls[0], " ")
			}
			if got != tt.want {
				t.Errorf("spawned %q, want %q", got, tt.want)
			}
			if len(spy.calls) > 1 {
				t.Errorf("spawned %d children", len(spy.calls))
			}
			// Nothing but enums and the session id is passed to the child.
			if strings.Contains(got, "SENTINEL") {
				t.Errorf("text leaked into argv: %s", got)
			}
		})
	}
}

func TestSelfTestPasses(t *testing.T) {
	if err := SelfTest(); err != nil {
		t.Fatal(err)
	}
}
