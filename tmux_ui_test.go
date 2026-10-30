package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// uiPane is `bosun ui` running in a pane of a second private tmux server (a real
// pty, so raw mode, sizing and escape handling are the real ones), pointed at
// the first server through $TMUX. runUI starts one.
type uiPane struct {
	t     *testing.T
	s     *tserver
	outer string
}

func (s *tserver) runUI(w, h int) *uiPane {
	s.t.Helper()
	outer := s.label + "-ui"
	cmdline := fmt.Sprintf("env TMUX=%s,%d,0 %s ui", s.sock, s.spid, binPath)
	c := exec.Command("tmux", "-L", outer, "-f", "/dev/null", "new-session", "-d", "-x", strconv.Itoa(w), "-y", strconv.Itoa(h), cmdline)
	c.Env = s.env
	if out, err := c.CombinedOutput(); err != nil {
		s.t.Fatalf("ui pane: %v %s", err, out)
	}
	s.t.Cleanup(func() {
		exec.Command("tmux", "-L", outer, "-f", "/dev/null", "kill-server").Run()
		os.Remove(filepath.Join(filepath.Dir(s.sock), outer))
	})
	return &uiPane{t: s.t, s: s, outer: outer}
}

func (p *uiPane) tmux(args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, "tmux", append([]string{"-L", p.outer, "-f", "/dev/null"}, args...)...)
	c.Env = p.s.env
	out, _ := c.CombinedOutput()
	return string(out)
}

func (p *uiPane) capture() string { return p.tmux("capture-pane", "-p", "-t", "0") }

func (p *uiPane) send(keys ...string) {
	p.tmux(append([]string{"send-keys", "-t", "0"}, keys...)...)
}

// waitFor polls the capture until it contains want.
func (p *uiPane) waitFor(want string) string {
	p.t.Helper()
	var got string
	for deadline := time.Now().Add(6 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if got = p.capture(); strings.Contains(got, want) {
			return got
		}
	}
	p.t.Fatalf("never saw %q in the popup:\n%s", want, got)
	return ""
}

func (p *uiPane) alive() bool {
	return exec.Command("tmux", "-L", p.outer, "-f", "/dev/null", "has-session").Run() == nil
}

// seedSpend lays down transcripts under the isolated HOME for the given session
// ids, so the Spend tab and the SPEND column have data.
func seedSpend(t *testing.T, s *tserver, ids map[string]float64) {
	t.Helper()
	for id, total := range ids {
		writeTranscript(t, s.root, id, total, time.Now().Add(-time.Minute))
	}
}

var sessionIDs = [3]string{
	"aaaaaaaa-0000-0000-0000-000000000001",
	"bbbbbbbb-0000-0000-0000-000000000002",
	"cccccccc-0000-0000-0000-000000000003",
}

func TestTmuxUIAgentsTabRowsMatchStatusJSON(t *testing.T) {
	s := newTserver(t)
	s.threeSessions()
	seedSpend(t, s, map[string]float64{sessionIDs[0]: 1.25, sessionIDs[1]: 3.5, sessionIDs[2]: 0.5})
	p := s.runUI(150, 30)
	got := p.waitFor("permission")
	var js struct {
		Counts   counts
		Sessions []struct{ ID, Display string }
	}
	if err := json.Unmarshal([]byte(s.bosun("status", "--json")), &js); err != nil {
		t.Fatal(err)
	}
	// Each state shows as its own detail text (a working session with no open
	// tool call shows its activity, "thinking"); rows are the icon rows.
	detail := map[string]string{"needs-you": "permission", "working": "thinking", "done": "done ·", "runaway": "runaway", "idle": "idle ·"}
	for _, sess := range js.Sessions {
		if !strings.Contains(got, detail[sess.Display]) {
			t.Errorf("status --json has a %s row the popup lacks:\n%s", sess.Display, got)
		}
	}
	rows := 0
	for _, l := range strings.Split(got, "\n") {
		for _, icon := range []string{"⚑", "▲", "✓", "•", "✱", "▸", "⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"} {
			if strings.Contains(l, "│ "+icon+" ") && !strings.Contains(l, "bosun status") {
				rows++
				break
			}
		}
	}
	if rows != len(js.Sessions) {
		t.Errorf("popup shows %d rows, status --json has %d:\n%s", rows, len(js.Sessions), got)
	}
	for _, want := range []string{fmt.Sprintf("%d sessions", js.Counts.Agents), "⚑ 1", "✓ 1", "$3.50", "$1.25", "$0.50"} {
		if !strings.Contains(got, want) {
			t.Errorf("popup lacks %q:\n%s", want, got)
		}
	}
	saveCapture(t, "ui-agents-wide.txt", got)
	p.send("q")
	time.Sleep(300 * time.Millisecond)
	if p.alive() {
		t.Error("q did not close the popup")
	}
}

func TestTmuxUIEnterJumpsAndSpendTab(t *testing.T) {
	s := newTserver(t)
	panes := s.threeSessions()
	seedSpend(t, s, map[string]float64{sessionIDs[0]: 1.25, sessionIDs[1]: 3.5, sessionIDs[2]: 0.5})
	_, client := s.attach("s1", 160, 40)
	s.tmux("select-window", "-t", "s1:0")
	p := s.runUI(150, 30)
	p.waitFor("permission")
	// The first row is the needs-you session ("two"); enter jumps the client there.
	p.send("Enter")
	time.Sleep(500 * time.Millisecond)
	if cur := s.currentPane(client); cur != panes[1] {
		t.Errorf("client is on %s, want the needs-you pane %s", cur, panes[1])
	}
	if p.alive() {
		t.Error("enter should close the popup")
	}

	p2 := s.runUI(160, 40)
	p2.waitFor("permission")
	p2.send("Tab")
	spendTab := p2.waitFor("TOTAL")
	for _, want := range []string{"bosun spend", "TODAY", "LAST 7 DAYS", "BY MODEL", "estimates at API rates", "NAME"} {
		if !strings.Contains(spendTab, want) {
			t.Errorf("Spend tab lacks %q:\n%s", want, spendTab)
		}
	}
	saveCapture(t, "ui-spend-wide.txt", spendTab)
	p2.send("p")
	p2.waitFor("· 7d")
	p2.send("g")
	p2.waitFor("--by day")
}

func TestTmuxUINarrowCollapsesTheSummary(t *testing.T) {
	s := newTserver(t)
	s.threeSessions()
	seedSpend(t, s, map[string]float64{sessionIDs[0]: 1.25, sessionIDs[1]: 3.5, sessionIDs[2]: 0.5})
	p := s.runUI(90, 28)
	p.waitFor("permission")
	saveCapture(t, "ui-agents-narrow.txt", p.capture())
	p.send("Tab")
	got := p.waitFor("TOTAL")
	if strings.Contains(got, "BY MODEL") || !strings.Contains(got, "LAST 7 DAYS") {
		t.Errorf("narrow Spend tab:\n%s", got)
	}
	saveCapture(t, "ui-spend-narrow.txt", got)
}

// The interrupt reaches the right pane on a private server; n sends nothing.
func TestTmuxUIInterruptSendsToTheRightPane(t *testing.T) {
	s := newTserver(t)
	// A session whose pane runs `cat -v`, which echoes an Escape as ^[ .
	out := strings.Fields(s.tmux("new-session", "-d", "-P", "-F", "#{pane_id} #{pane_pid}", "-s", "intr", "-x", "100", "-y", "10", "cat -v"))
	pane := out[0]
	pid, _ := strconv.Atoi(out[1])
	sid := "dddddddd-0000-0000-0000-000000000004"
	s.hook(pane, pid, sid, "SessionStart", map[string]any{"source": "startup", "cwd": s.root})
	s.hook(pane, pid, sid, "UserPromptSubmit", nil)

	p := s.runUI(140, 24)
	p.waitFor("thinking")
	p.send("!")
	p.waitFor("interrupt")
	p.send("n")
	p.waitFor("nothing sent")
	time.Sleep(300 * time.Millisecond)
	if strings.Contains(s.tmux("capture-pane", "-p", "-t", pane), "^[") {
		t.Fatal("n sent a key")
	}
	p.send("!", "y")
	p.waitFor("sent Escape")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(s.tmux("capture-pane", "-p", "-t", pane), "^[") {
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(s.tmux("capture-pane", "-p", "-t", pane), "^[") {
		t.Errorf("the pane never received Escape:\n%s", s.tmux("capture-pane", "-p", "-t", pane))
	}
}

// The popup binding in the generated conf opens the UI on a real client, with
// the title flag gated off on tmux 3.2 (D-3).
func TestTmuxPopupBindingOpensTheUI(t *testing.T) {
	s := newTserver(t)
	conf := s.installConf()
	s.threeSessions()
	s.tmux("source-file", conf) // fails the test if the gated binding does not parse
	capture, _ := s.attach("s1", 140, 30)
	outer := s.label + "-outer"
	send := func(keys ...string) {
		c := exec.Command("tmux", append([]string{"-L", outer, "-f", "/dev/null", "send-keys", "-t", "0"}, keys...)...)
		c.Env = s.env
		c.Run()
	}
	send("C-b", "W")
	var got string
	for deadline := time.Now().Add(6 * time.Second); time.Now().Before(deadline); time.Sleep(150 * time.Millisecond) {
		if got = capture(); strings.Contains(got, "bosun status") && strings.Contains(got, "STATE") {
			break
		}
	}
	if !strings.Contains(got, "STATE") || !strings.Contains(got, "permission") {
		t.Fatalf("prefix + W did not open the popup:\n%s", got)
	}
	saveCapture(t, "ui-popup-binding.txt", got)
	send("q")
}

// `e` writes the same CSV `bosun spend --csv` prints for the same period and group.
func TestTmuxUIExportMatchesSpendCSV(t *testing.T) {
	s := newTserver(t)
	s.threeSessions()
	seedSpend(t, s, map[string]float64{sessionIDs[0]: 1.25, sessionIDs[1]: 3.5, sessionIDs[2]: 0.5})
	p := s.runUI(150, 30)
	p.waitFor("permission")
	p.send("Tab")
	p.waitFor("TOTAL")
	p.send("e")
	got := p.waitFor("wrote ")
	files, _ := filepath.Glob(s.root + "/data/exports/spend-today-task-*.csv")
	if len(files) != 1 {
		t.Fatalf("exports = %v\n%s", files, got)
	}
	exported, _ := os.ReadFile(files[0])
	want := s.bosun("spend", "--by", "task", "--since", "today", "--csv")
	if string(exported) != want {
		t.Errorf("export differs from spend --csv:\n%s\n---\n%s", exported, want)
	}
}

// The Spend column is `spend --by session`'s figure for the same session.
func TestTmuxUISpendColumnEqualsSpendBySession(t *testing.T) {
	s := newTserver(t)
	s.threeSessions()
	seedSpend(t, s, map[string]float64{sessionIDs[0]: 1.25, sessionIDs[1]: 3.5, sessionIDs[2]: 0.5})
	p := s.runUI(150, 30)
	got := p.waitFor("permission")
	var res struct {
		Rows []struct {
			Key string
			USD float64
		}
	}
	if err := json.Unmarshal([]byte(s.bosun("spend", "--by", "session", "--json")), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 3 {
		t.Fatalf("rows = %+v", res.Rows)
	}
	for _, r := range res.Rows {
		if want := fmt.Sprintf("$%.2f", r.USD); !strings.Contains(got, want) {
			t.Errorf("session %s: spend --by session says %s, the popup lacks it:\n%s", r.Key, want, got)
		}
	}
}

// `d` opens ui.diff_cmd in a new window at the session's cwd.
func TestTmuxUIDiffOpensAWindowAtTheSessionCwd(t *testing.T) {
	s := newTserver(t)
	os.WriteFile(s.root+"/config/config.toml", []byte(testConfig+"[ui]\ndiff_cmd = \"sleep 30\"\n"), 0o600)
	cwd := s.root + "/proj"
	os.MkdirAll(cwd, 0o755)
	pane := strings.Fields(s.tmux("list-panes", "-t", "s1", "-F", "#{pane_id} #{pane_pid}"))
	pid, _ := strconv.Atoi(pane[1])
	sid := "eeeeeeee-0000-0000-0000-000000000005"
	s.hook(pane[0], pid, sid, "SessionStart", map[string]any{"source": "startup", "cwd": cwd})
	s.hook(pane[0], pid, sid, "UserPromptSubmit", nil)
	_, _ = s.attach("s1", 120, 30)
	p := s.runUI(140, 24)
	p.waitFor("thinking")
	p.send("d")
	deadline := time.Now().Add(4 * time.Second)
	var windows string
	for time.Now().Before(deadline) {
		windows = s.tmux("list-windows", "-t", "s1", "-F", "#{window_name} #{pane_current_path}")
		if strings.Contains(windows, "diff "+cwd) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Errorf("no diff window at %s:\n%s", cwd, windows)
}

// With privacy.show_text off, peek is the only way tool input shows: on screen
// after `v`, never in a file (SPEC M4, D-27).
func TestTmuxUIPeekShowsOnScreenAndNeverInAnyFile(t *testing.T) {
	s := newTserver(t)
	transcript := s.root + "/home/t.jsonl"
	line, _ := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
		map[string]any{"type": "tool_use", "id": "t1", "name": "Bash", "input": map[string]any{"command": "echo PEEK-SENTINEL-4471"}}}}})
	os.WriteFile(transcript, append(line, '\n'), 0o600)
	// privacy.show_text off: only `v` reveals the text. (On, it is in the row.)
	os.WriteFile(s.root+"/config/config.toml", []byte("[notify]\nbackend = \"off\"\n[privacy]\nshow_text = false\n"), 0o600)
	pane := strings.Fields(s.tmux("list-panes", "-t", "s1", "-F", "#{pane_id} #{pane_pid}"))
	pid, _ := strconv.Atoi(pane[1])
	sid := "ffffffff-0000-0000-0000-000000000006"
	s.hook(pane[0], pid, sid, "SessionStart", map[string]any{"source": "startup", "cwd": s.root, "transcript_path": transcript})
	s.hook(pane[0], pid, sid, "UserPromptSubmit", nil)
	p := s.runUI(140, 24)
	if got := p.waitFor("thinking"); strings.Contains(got, "PEEK-SENTINEL") {
		t.Fatal("peek text shown before v")
	}
	p.send("v")
	p.waitFor("⤷ Bash: echo PEEK-SENTINEL-4471")
	p.send("Tab", "Tab", "q")
	time.Sleep(300 * time.Millisecond)
	for _, dir := range []string{"state", "cache", "data", "config"} {
		filepath.WalkDir(s.root+"/"+dir, func(path string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				if b, _ := os.ReadFile(path); strings.Contains(string(b), "PEEK-SENTINEL") {
					t.Errorf("peek text was written to %s", path)
				}
			}
			return nil
		})
	}
}

// prefix + $ (the generated conf quotes it: $ is tmux's variable sigil) opens
// the spend view.
func TestTmuxSpendBindingOpensTheSpendView(t *testing.T) {
	s := newTserver(t)
	conf := s.installConf()
	s.threeSessions()
	s.tmux("source-file", conf)
	if keys := s.tmux("list-keys", "-T", "prefix"); !strings.Contains(keys, "ui --spend") {
		t.Fatalf("no spend binding after sourcing:\n%s", keys)
	}
	capture, _ := s.attach("s1", 160, 40)
	outer := s.label + "-outer"
	c := exec.Command("tmux", "-L", outer, "-f", "/dev/null", "send-keys", "-t", "0", "C-b", "$")
	c.Env = s.env
	c.Run()
	var got string
	for deadline := time.Now().Add(6 * time.Second); time.Now().Before(deadline); time.Sleep(150 * time.Millisecond) {
		if got = capture(); strings.Contains(got, "bosun spend") && strings.Contains(got, "BY MODEL") {
			break
		}
	}
	if !strings.Contains(got, "bosun spend") || !strings.Contains(got, "BY MODEL") {
		t.Fatalf("prefix + $ did not open the spend view:\n%s", got)
	}
	saveCapture(t, "ui-spend-binding.txt", got)
}
