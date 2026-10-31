package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmvaldez/bosun/internal/config"
	"github.com/jmvaldez/bosun/internal/eval"
	"github.com/jmvaldez/bosun/internal/handoff"
	"github.com/jmvaldez/bosun/internal/hook"
	"github.com/jmvaldez/bosun/internal/live"
	"github.com/jmvaldez/bosun/internal/paths"
	"github.com/jmvaldez/bosun/internal/tmux"
)

const (
	sentinelPrompt = "SENTINEL-PROMPT-7a3f the refund handler"
	sentinelCmd    = "SENTINEL-CMD-91c2 --flag"
	sentinelAsk    = "SENTINEL-ASK-4be8 keep the key?"
	sentinelName   = "SENTINEL-NAME-d05e session name"
)

// D-27: text may be shown, never stored. Run hooks, a render and a notify cycle
// with sentinels in the prompt, the pending command and a question, and look
// in every file bosun owns.
func TestShownTextIsNeverStored(t *testing.T) {
	env, root := isolated(t)
	home := root + "/home"
	shimDir := root + "/shims"
	os.MkdirAll(shimDir, 0o755)
	log := root + "/notify.log" // outside every bosun dir: this is "the screen"
	os.WriteFile(shimDir+"/notify-send", []byte("#!/bin/sh\n[ \"$1\" = --version ] && { echo 'notify-send 0.7.9'; exit 0; }\nprintf '%s\\n' \"$*\" >> '"+log+"'\n"), 0o755)
	// A claude whose agents list carries a session name, as the real one does.
	os.WriteFile(shimDir+"/claude", []byte("#!/bin/sh\n[ \"$1 $2\" = 'agents --json' ] && { printf '%s' '[{\"sessionId\":\""+sid+"\",\"pid\":1,\"kind\":\"interactive\",\"name\":\""+sentinelName+"\"}]'; exit 0; }\nexit 1\n"), 0o755)
	for i, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			env[i] = "PATH=" + shimDir + ":/usr/bin:/bin"
		}
	}
	env = append(env, "CLAUDE_CODE_ENTRYPOINT=cli", "TMUX_PANE=%1")

	setConfig := func(showText bool) {
		body := "[notify]\nbackend = \"notify-send\"\n"
		if !showText {
			body += "[privacy]\nshow_text = false\n"
		}
		os.WriteFile(root+"/config/config.toml", []byte(body), 0o600)
	}
	setConfig(true)

	tp := home + "/.claude/projects/p/" + sid + ".jsonl"
	os.MkdirAll(filepath.Dir(tp), 0o755)
	transcript := `{"type":"user","promptId":"p1","message":{"role":"user","content":"` + sentinelPrompt + `"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"` + sentinelCmd + `"}}]}}` + "\n"
	os.WriteFile(tp, []byte(transcript), 0o600)

	hook := func(event, extra string) {
		t.Helper()
		body := `{"session_id":"` + sid + `","hook_event_name":"` + event + `","transcript_path":"` + tp + `","cwd":"` + home + `"` + extra + `}`
		if r := runBosun(t, env, strings.NewReader(body), "hook", "--plugin-version", "0.1.0"); r.code != 0 {
			t.Fatalf("hook %s: %d %s", event, r.code, r.out)
		}
	}
	hook("SessionStart", `,"source":"startup"`)
	hook("UserPromptSubmit", `,"prompt":"`+sentinelPrompt+`"`)
	hook("PreToolUse", `,"tool_name":"Bash","tool_input":{"command":"`+sentinelCmd+`"}`)
	hook("PermissionRequest", `,"tool_name":"Bash","tool_input":{"command":"`+sentinelCmd+`"},"message":"`+sentinelAsk+`"`)

	// Render surfaces, then the notify cycle.
	for _, args := range [][]string{{"status", "--json"}, {"status", "--tmux"}, {"status"}, {"spend"}, {"refresh"}} {
		runBosun(t, env, nil, args...)
	}
	if b, err := os.ReadFile(root + "/cache/agents.json"); err != nil || !strings.Contains(string(b), sid) {
		t.Fatalf("refresh did not write the agents cache through the claude shim: %v %s", err, b)
	}
	dispatch := func() string {
		c := exec.Command(binPath, "notify-dispatch", "--kind", "needs-you", "--session", sid)
		c.Env = env
		out, _ := c.CombinedOutput()
		return string(out)
	}
	if out := dispatch(); !strings.Contains(out, "posted") {
		t.Fatalf("notify-dispatch: %s", out)
	}
	shown, _ := os.ReadFile(log)
	if !strings.Contains(string(shown), "wants to run "+sentinelCmd) {
		t.Fatalf("with show_text the notice should name the command; the shim saw:\n%s", shown)
	}
	assertNothingStored(t, root, sentinelPrompt, sentinelCmd, sentinelAsk, sentinelName, "SENTINEL-")

	// show_text = false: the command is not even read.
	os.Remove(root + "/state/notify.json")
	os.Remove(log)
	setConfig(false)
	if out := dispatch(); !strings.Contains(out, "posted") {
		t.Fatalf("second dispatch: %s", out)
	}
	shown, _ = os.ReadFile(log)
	if strings.Contains(string(shown), "SENTINEL") || !strings.Contains(string(shown), "is waiting on you") {
		t.Errorf("with show_text off the notice must be enums only:\n%s", shown)
	}
	assertNothingStored(t, root, "SENTINEL-")
}

// The popup's text reads (the latest tool call or question, a prompt's first
// line) stay in process memory and are never written (D-27).
func TestPopupTextReadsAreNeverStored(t *testing.T) {
	root := t.TempDir()
	tp := root + "/t.jsonl"
	os.WriteFile(tp, []byte(`{"type":"user","promptId":"p1","message":{"role":"user","content":"`+sentinelPrompt+`"}}`+"\n"+
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"AskUserQuestion","input":{"questions":[{"question":"`+sentinelAsk+`"}]}}]}}`+"\n"), 0o600)
	cfg := config.Default()
	u := &uiActions{cfg: cfg, src: &uiSource{lastSessions: []eval.View{{ID: sid, Transcript: tp}}}}
	info := u.Latest(eval.View{ID: sid, Transcript: tp})
	if info.Question != sentinelAsk {
		t.Fatalf("question = %q", info.Question)
	}
	if got := u.PromptLine(sid, "p1"); !strings.Contains(got, "SENTINEL-PROMPT") {
		t.Fatalf("prompt line = %q", got)
	}
	// Off: nothing is read at all.
	cfg.Privacy.ShowText, cfg.UI.Peek = false, false
	off := &uiActions{cfg: cfg, src: u.src}
	if off.Latest(eval.View{ID: sid, Transcript: tp}).Question != "" || off.PromptLine(sid, "p1") != "" {
		t.Error("text read with show_text and peek off")
	}
	assertNothingStored(t, root, "SENTINEL-")
}

// assertNothingStored walks every file under the test root except the planted
// transcripts, the shim scripts and the shim's log (the "screen"), and fails
// on any needle.
func assertNothingStored(t *testing.T, root string, needles ...string) {
	t.Helper()
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.HasSuffix(p, ".jsonl") && strings.Contains(p, "/.claude/projects/") || strings.HasSuffix(p, "/t.jsonl") ||
			strings.HasSuffix(p, "/notify.log") || strings.Contains(p, "/shims/") {
			return nil
		}
		b, _ := os.ReadFile(p)
		for _, n := range needles {
			if strings.Contains(string(b), n) {
				t.Errorf("%s contains %q", p, n)
			}
		}
		return nil
	})
}

const sentinelDoc = "SENTINEL-DOC-5c1e the refund plan"

// handoffClock advances only when Run sleeps.
type handoffClock struct{ now time.Time }

func (c *handoffClock) Now() time.Time        { return c.now }
func (c *handoffClock) Sleep(d time.Duration) { c.now = c.now.Add(d) }

type handoffTmux struct{}

func (handoffTmux) Panes() ([]tmux.Pane, error) {
	return []tmux.Pane{{ServerPID: 4242, ID: "%3", PanePID: 500}}, nil
}
func (handoffTmux) PasteText(string, string) error  { return nil }
func (handoffTmux) SendKeys(string, []string) error { return nil }

type handoffProcs struct{}

func (handoffProcs) StartToken(int) (string, error)   { return "tok", nil }
func (handoffProcs) StartTime(int) (time.Time, error) { return time.Time{}, nil }
func (handoffProcs) Alive(pid int, tok string) bool   { return pid == 501 && tok == "tok" }
func (handoffProcs) Parent(int) (int, error)          { return 500, nil }

// D-27 for the handoff: the doc is the session's text. It lives where the
// session wrote it, and none of it reaches handoff.json, handoff.jsonl,
// live.json or the events.
func TestHandoffDocIsNeverStored(t *testing.T) {
	root := t.TempDir()
	p := paths.Paths{State: root + "/state"}
	docDir := t.TempDir() // outside every bosun dir: this is the user's repo
	docPath := docDir + "/handoff-aaaa.md"
	os.WriteFile(docPath, []byte("## Goal\n"+sentinelDoc+"\n"), 0o600)

	clk := &handoffClock{now: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)}
	var evs []hook.Event
	sess := &live.Session{ID: "aaaa", State: live.Done, Task: "ABC-1", Cwd: docDir, PID: 501, StartTok: "tok",
		Loc: live.Locator{Pane: "%3", SPID: 4242}}
	f := live.File{Sessions: map[string]*live.Session{"aaaa": sess}}
	d := handoff.Deps{
		Clock: clk, Tmux: handoffTmux{}, Procs: handoffProcs{}, Paths: p,
		Cfg:  config.Handoff{Deadline: time.Minute},
		Live: func() live.File { return f },
		Events: func(string) []hook.Event {
			// The prompt is submitted at once; the turn ends two seconds later.
			evs = []hook.Event{{Ev: "UserPromptSubmit", SID: "aaaa", TS: clk.now.Add(-time.Second).Format(time.RFC3339Nano)}}
			if clk.now.After(time.Date(2026, 10, 31, 12, 0, 3, 0, time.UTC)) {
				evs = append(evs, hook.Event{Ev: "Stop", SID: "aaaa", TS: clk.now.Format(time.RFC3339Nano)})
			}
			return evs
		},
		Stat:   func(string) (time.Time, int64, error) { return clk.now, 64, nil },
		Doc:    func(*live.Session) (string, error) { return docPath, nil },
		Launch: func(string, string, string, string) error { return nil },
	}
	// The state dir exists as it would after real hooks ran.
	os.MkdirAll(p.Session("aaaa"), 0o700)
	if st, err := handoff.Run(d, "aaaa"); err != nil || st.Phase != handoff.PhaseDone {
		t.Fatalf("Run = %+v, %v", st, err)
	}
	if err := live.Write(p.Live(), f); err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		line, _ := json.Marshal(e)
		appendTo(t, p.Events("aaaa"), line)
	}
	for _, path := range []string{p.Handoff("aaaa"), p.HandoffLog(), p.Live(), p.Events("aaaa")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s was not written: %v", path, err)
		}
	}
	assertNothingStored(t, root, "SENTINEL-")
	if b, _ := os.ReadFile(docPath); !strings.Contains(string(b), sentinelDoc) {
		t.Fatal("the doc lost its text; the test is not testing anything")
	}
}

func appendTo(t *testing.T, path string, line []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	f.Write(append(line, '\n'))
}
