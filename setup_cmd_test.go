package main

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// D-31: `bosun setup` with --yes or --no answers for the user; with neither and
// no terminal it asks nothing and changes nothing.
func TestSetupCommandYesNoAndNoTerminal(t *testing.T) {
	env, root := isolated(t)
	log := shims(t, env, root)
	home := root + "/home"
	os.MkdirAll(home+"/.claude", 0o755)
	settings := home + "/.claude/settings.json"
	const orig = "{\n  \"statusLine\": {\n    \"type\": \"command\",\n    \"command\": \"echo hi\"\n  }\n}\n"
	os.WriteFile(settings, []byte(orig), 0o644)
	const userTmux = "run '~/.tmux/plugins/tpm/tpm'\n"
	os.WriteFile(home+"/.tmux.conf", []byte(userTmux), 0o644)
	repo, _ := filepath.Abs(".")

	r := runBosun(t, env, strings.NewReader(""), "setup")
	if r.code != 2 || strings.TrimSpace(string(r.out)) != "run `bosun setup` in a terminal" {
		t.Fatalf("no terminal, no flag: exit %d\n%s", r.code, r.out)
	}
	if b, _ := os.ReadFile(settings); string(b) != orig {
		t.Fatal("settings.json changed without an answer")
	}
	if r := runBosun(t, env, nil, "setup", "--yes", "--no"); r.code != 2 {
		t.Errorf("--yes --no exited %d", r.code)
	}

	r = runBosun(t, env, strings.NewReader(""), "setup", "--yes", "--repo", repo)
	out := string(r.out)
	for _, want := range []string{"\u2713 Claude Code plugin installed", "\u2713 tmux configured", "\u2713 live cost on"} {
		if !strings.Contains(out, want) {
			t.Errorf("--yes output lacks %q (exit %d):\n%s", want, r.code, out)
		}
	}
	if b, _ := os.ReadFile(settings); !strings.Contains(string(b), "exec bosun statusline -- echo hi") {
		t.Errorf("settings = %s", b)
	}
	if b, _ := os.ReadFile(home + "/.tmux.conf"); string(b) != userTmux {
		t.Errorf("the user's tmux.conf was edited: %q", b)
	}
	l, _ := os.ReadFile(log)
	for _, want := range []string{"claude plugin install bosun --marketplace " + repo, "tmux -S " + root + "/no-such-tmux-socket source-file " + root + "/data/tmux.conf"} {
		if !strings.Contains(string(l), want) {
			t.Errorf("shim log lacks %q:\n%s", want, l)
		}
	}
	if strings.Contains(out, "Show live session cost?") {
		t.Errorf("--yes left a record of a question nobody asked:\n%s", out)
	}
	if strings.ContainsRune(out, '\ufe0f') {
		t.Error("output carries a variation selector")
	}

	// Set up now, so the same invocation with no terminal is quiet about it and exits 0.
	if r := runBosun(t, env, strings.NewReader(""), "setup"); r.code != 0 {
		t.Errorf("set up, no terminal: exit %d\n%s", r.code, r.out)
	}

	r = runBosun(t, env, strings.NewReader(""), "setup", "--no", "--repo", repo)
	if !strings.Contains(string(r.out), "\u2713 live cost off") {
		t.Errorf("--no output (exit %d):\n%s", r.code, r.out)
	}
	if b, _ := os.ReadFile(settings); !strings.Contains(string(b), `"command": "echo hi"`) {
		t.Errorf("settings after --no = %s", b)
	}
}

// A transcript with no cost-state: only a transcript estimate stands behind it.
func writeUntrusted(t *testing.T, root, sid string, mtime time.Time) {
	t.Helper()
	writeTranscript(t, root, sid, 0, mtime)
	p := filepath.Join(root, "home", ".claude", "projects", "-work-api", sid+".jsonl")
	b, _ := os.ReadFile(p)
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	os.WriteFile(p, []byte(strings.Join(lines[:len(lines)-1], "\n")+"\n"), 0o644)
	os.Chtimes(p, mtime, mtime)
}

// D-31: with live cost off, an amount with no trusted figure shows as a dash
// and no total counts it; with the status line wrapped it is the est. figure it
// always was.
func TestSpendRedactsEstimatesWhenLiveCostIsOff(t *testing.T) {
	env, root := isolated(t)
	now := time.Now().UTC()
	writeTranscript(t, root, "aaaaaaaa-0000-0000-0000-00000000000a", 4.50, now.Add(-2*time.Hour))
	writeUntrusted(t, root, "bbbbbbbb-0000-0000-0000-00000000000b", now.Add(-time.Hour))
	spendJSON := func() (total float64, hidden int) {
		r := runBosun(t, env, nil, "spend", "--by", "session", "--json")
		var res struct {
			Total struct {
				USD      float64
				Redacted int `json:"redacted_sessions"`
			}
		}
		if err := json.Unmarshal(r.out, &res); err != nil {
			t.Fatalf("%v\n%s", err, r.out)
		}
		return res.Total.USD, res.Total.Redacted
	}

	if usd, hidden := spendJSON(); math.Abs(usd-4.50) > 1e-6 || hidden != 1 {
		t.Errorf("live cost off: total %v, %d hidden; want 4.50 and 1", usd, hidden)
	}
	r := runBosun(t, env, nil, "spend", "--by", "session")
	if !strings.Contains(string(r.out), "\u2014") || !strings.Contains(string(r.out), "1 session without an exact cost") {
		t.Errorf("text output:\n%s", r.out)
	}

	os.MkdirAll(root+"/home/.claude", 0o755)
	os.WriteFile(root+"/home/.claude/settings.json", []byte(`{"statusLine":{"type":"command","command":"if command -v bosun >/dev/null; then exec bosun statusline; fi"}}`), 0o644)
	if usd, hidden := spendJSON(); math.Abs(usd-7.50) > 1e-6 || hidden != 0 { // 4.50 + the session's $3 of rows
		t.Errorf("live cost on: total %v, %d hidden; want 7.50 and 0", usd, hidden)
	}
}

func TestStatusSegmentShowsWhatLiveCostHides(t *testing.T) {
	env, root := isolated(t)
	day := time.Now().Format("2006-01-02")
	os.MkdirAll(root+"/state", 0o755)
	os.WriteFile(root+"/state/today.json", []byte(`{"day":"`+day+`","usd":14.2,"est_usd":0,"est_share":0,"redacted":2}`), 0o600)
	if r := runBosun(t, env, nil, "status", "--tmux"); !strings.Contains(string(r.out), "$14.20 + 2 \u2014") {
		t.Errorf("segment = %q", r.out)
	}
	os.WriteFile(root+"/state/today.json", []byte(`{"day":"`+day+`","usd":0,"redacted":1}`), 0o600)
	if r := runBosun(t, env, nil, "status", "--tmux"); !strings.Contains(string(r.out), "\u2014") || strings.Contains(string(r.out), "$") {
		t.Errorf("segment = %q", r.out)
	}
}
