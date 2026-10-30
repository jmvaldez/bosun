package main

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmvaldez/bosun/internal/config"
	"github.com/jmvaldez/bosun/internal/eval"
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
