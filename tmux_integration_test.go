package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jmvaldez/bosun/internal/runner"
	"github.com/jmvaldez/bosun/internal/tmux"
)

// These tests drive a private tmux server (-L bosun-test-*, -f /dev/null) and
// never the user's. They are opt-in: BOSUN_TEST_TMUX=1.

func needTmux(t *testing.T) {
	t.Helper()
	if os.Getenv("BOSUN_TEST_TMUX") == "" {
		t.Skip("set BOSUN_TEST_TMUX=1 to drive a private tmux server")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
}

type tserver struct {
	t     *testing.T
	label string
	env   []string
	root  string
	sock  string
	spid  int
}

var tmuxSeq int

// cleanEnv drops everything that would tie a child to the parent Claude Code
// session or to a real tmux server.
func cleanEnv(extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "CLAUDE") || strings.HasPrefix(k, "TMUX") || strings.HasPrefix(k, "BOSUN_") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, extra...)
	// No claude on PATH: the evaluator's detached refresh must not reach the
	// real binary from a test.
	return append(env, "PATH=/usr/bin:/bin")
}

func newTserver(t *testing.T) *tserver {
	needTmux(t)
	tmuxSeq++
	root := t.TempDir()
	s := &tserver{t: t, root: root, label: fmt.Sprintf("bosun-test-%d-%d", os.Getpid(), tmuxSeq)}
	s.env = cleanEnv(
		"HOME="+root+"/home",
		"BOSUN_CONFIG_DIR="+root+"/config", "BOSUN_STATE_DIR="+root+"/state",
		"BOSUN_CACHE_DIR="+root+"/cache", "BOSUN_DATA_DIR="+root+"/data",
		"TERM=xterm-256color", "LANG=C.UTF-8",
	)
	os.MkdirAll(root+"/home", 0o755)
	os.MkdirAll(root+"/config", 0o755)
	os.WriteFile(root+"/config/config.toml", []byte(testConfig), 0o600) // never a real desktop notice from a test
	s.raw("new-session", "-d", "-s", "s1", "-x", "200", "-y", "20", "sleep 600")
	t.Cleanup(func() {
		exec.Command("tmux", "-L", s.label, "-f", "/dev/null", "kill-server").Run()
		os.Remove(s.sock)
		time.Sleep(300 * time.Millisecond) // let in-flight #() and refresh children finish before TempDir removal
	})
	s.sock = strings.TrimSpace(s.tmux("display-message", "-p", "#{socket_path}"))
	s.spid, _ = strconv.Atoi(strings.TrimSpace(s.tmux("display-message", "-p", "#{pid}")))
	return s
}

func (s *tserver) raw(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "tmux", append([]string{"-L", s.label, "-f", "/dev/null"}, args...)...)
	cmd.Env = s.env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (s *tserver) tmux(args ...string) string {
	s.t.Helper()
	out, err := s.raw(args...)
	if err != nil {
		s.t.Fatalf("tmux %v: %v\n%s", args, err, out)
	}
	return out
}

// addPane makes a window running a long sleep and returns its pane id and the
// pid Claude Code would report as CLAUDE_PID.
func (s *tserver) addPane(session, name string) (pane string, pid int) {
	s.t.Helper()
	args := []string{"new-window", "-P", "-F", "#{pane_id} #{pane_pid}", "-n", name}
	if session != "" {
		args = append(args, "-t", session+":")
	}
	out := strings.Fields(s.tmux(append(args, "sleep 600")...))
	pid, _ = strconv.Atoi(out[1])
	return out[0], pid
}

func (s *tserver) newSession(name string) (pane string, pid int) {
	s.t.Helper()
	out := strings.Fields(s.tmux("new-session", "-d", "-P", "-F", "#{pane_id} #{pane_pid}", "-s", name, "-x", "200", "-y", "20", "sleep 600"))
	pid, _ = strconv.Atoi(out[1])
	return out[0], pid
}

// hook feeds the real `bosun hook` the way Claude Code would from that pane.
func (s *tserver) hook(pane string, pid int, sid, event string, fields map[string]any) {
	s.t.Helper()
	p := map[string]any{"session_id": sid, "hook_event_name": event}
	for k, v := range fields {
		p[k] = v
	}
	b, _ := json.Marshal(p)
	cmd := exec.Command(binPath, "hook", "--plugin-version", manifestVersion())
	cmd.Env = append(append([]string{}, s.env...),
		fmt.Sprintf("TMUX=%s,%d,0", s.sock, s.spid), "TMUX_PANE="+pane, "CLAUDE_PID="+strconv.Itoa(pid))
	cmd.Stdin = bytes.NewReader(b)
	if out, err := cmd.CombinedOutput(); err != nil || len(out) != 0 {
		s.t.Fatalf("hook %s: %v %q", event, err, out)
	}
}

// bosun runs a bosun subcommand the way a tmux #() job or key binding would:
// with TMUX pointing at this server.
func (s *tserver) bosun(args ...string) string {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.Env = append(append([]string{}, s.env...), fmt.Sprintf("TMUX=%s,%d,0", s.sock, s.spid))
	out, err := cmd.CombinedOutput()
	if err != nil {
		s.t.Fatalf("bosun %v: %v\n%s", args, err, out)
	}
	return string(out)
}

type counts struct{ Agents, NeedsYou, Done, Working, Idle int }

func (s *tserver) status() counts {
	s.t.Helper()
	var res struct{ Counts counts }
	if err := json.Unmarshal([]byte(s.bosun("status", "--json")), &res); err != nil {
		s.t.Fatal(err)
	}
	return res.Counts
}

func (s *tserver) windowMark(target string) string {
	out, _ := s.raw("show-options", "-wqv", "-t", target, "@bosun-wait")
	return strings.TrimSpace(out)
}

// threeSessions starts three claude-like sessions in three tmux sessions, one
// each working, needs-you and done.
func (s *tserver) threeSessions() (panes [3]string) {
	p1, pid1 := s.newSession("two")
	p2, pid2 := s.newSession("three")
	p0 := strings.TrimSpace(s.tmux("list-panes", "-t", "s1", "-F", "#{pane_id} #{pane_pid}"))
	f := strings.Fields(p0)
	pid0, _ := strconv.Atoi(f[1])
	panes = [3]string{f[0], p1, p2}
	pids := [3]int{pid0, pid1, pid2}
	sids := [3]string{"aaaaaaaa-0000-0000-0000-000000000001", "bbbbbbbb-0000-0000-0000-000000000002", "cccccccc-0000-0000-0000-000000000003"}
	for i := range panes {
		s.hook(panes[i], pids[i], sids[i], "SessionStart", map[string]any{"source": "startup", "cwd": s.root})
		s.hook(panes[i], pids[i], sids[i], "UserPromptSubmit", map[string]any{"prompt_id": "p1"})
	}
	s.hook(panes[1], pids[1], sids[1], "PermissionRequest", map[string]any{"tool_name": "Bash"})
	s.hook(panes[2], pids[2], sids[2], "Stop", nil)
	return panes
}

func TestTmuxThreeSessionsCountedWithinTwoSeconds(t *testing.T) {
	s := newTserver(t)
	start := time.Now()
	s.threeSessions()
	c := s.status()
	if c.Agents != 3 || c.Working != 1 || c.NeedsYou != 1 || c.Done != 1 {
		t.Fatalf("counts = %+v", c)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("setup plus evaluation took %v", d)
	}
	if got := s.windowMark("two:"); got != "needs-you" {
		t.Errorf("window mark for needs-you = %q", got)
	}
	if got := s.windowMark("three:"); got != "done" {
		t.Errorf("window mark for done = %q", got)
	}
	if got := s.windowMark("s1:"); got != "working" {
		t.Errorf("window mark for working = %q", got)
	}
	seg := s.bosun("status", "--tmux")
	for _, want := range []string{" ⚑ 1 waiting ", " ✓ 1 done ", "3 sessions"} {
		if !strings.Contains(seg, want) {
			t.Errorf("segment %q lacks %q", seg, want)
		}
	}
}

func TestTmuxKillingPaneRemovesCountAndMarker(t *testing.T) {
	s := newTserver(t)
	panes := s.threeSessions()
	s.status()
	s.tmux("kill-pane", "-t", panes[1])
	c := s.status()
	if c.NeedsYou != 0 || c.Agents != 2 {
		t.Errorf("counts after kill = %+v", c)
	}
	// The window is gone with its only pane; the next marks.json must not keep it.
	b, _ := os.ReadFile(s.root + "/state/marks.json")
	if strings.Contains(string(b), "needs-you") {
		t.Errorf("marks.json = %s", b)
	}
}

func TestTmuxIdlePromptNotificationIsNotNeedsYou(t *testing.T) {
	s := newTserver(t)
	pane := strings.Fields(s.tmux("list-panes", "-t", "s1", "-F", "#{pane_id} #{pane_pid}"))
	pid, _ := strconv.Atoi(pane[1])
	sid := "dddddddd-0000-0000-0000-000000000004"
	s.hook(pane[0], pid, sid, "UserPromptSubmit", nil)
	s.hook(pane[0], pid, sid, "Stop", nil)
	s.hook(pane[0], pid, sid, "Notification", map[string]any{"notification_type": "idle_prompt", "message": "waiting"})
	if c := s.status(); c.NeedsYou != 0 || c.Done != 1 {
		t.Errorf("counts = %+v", c)
	}
}

// attach starts a client for session on a second private server and returns
// that server's capture function. The inner client is a real tmux client, so
// the capture shows what a user would see (LESSONS L-07).
func (s *tserver) attach(session string, width, height int) (capture func() string, client string) {
	s.t.Helper()
	outer := s.label + "-outer"
	cmd := func(args ...string) *exec.Cmd {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		s.t.Cleanup(cancel)
		c := exec.CommandContext(ctx, "tmux", append([]string{"-L", outer, "-f", "/dev/null"}, args...)...)
		c.Env = s.env
		return c
	}
	inner := fmt.Sprintf("tmux -L %s -f /dev/null attach -t %s", s.label, session)
	if out, err := cmd("new-session", "-d", "-x", strconv.Itoa(width), "-y", strconv.Itoa(height), inner).CombinedOutput(); err != nil {
		s.t.Fatalf("outer: %v %s", err, out)
	}
	s.t.Cleanup(func() {
		exec.Command("tmux", "-L", outer, "-f", "/dev/null", "kill-server").Run()
		os.Remove(filepath.Join(filepath.Dir(s.sock), outer)) // a dead server leaves its socket file
	})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		out := s.tmux("list-clients", "-F", "#{client_name}")
		if strings.TrimSpace(out) != "" {
			client = strings.Fields(out)[0]
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if client == "" {
		s.t.Fatal("client never attached")
	}
	return func() string {
		out, err := cmd("capture-pane", "-p", "-t", "0").CombinedOutput()
		if err != nil {
			s.t.Fatalf("capture: %v %s", err, out)
		}
		return string(out)
	}, client
}

func (s *tserver) currentPane(client string) string {
	return strings.TrimSpace(s.tmux("list-clients", "-t", client, "-F", "#{pane_id}"))
}

func TestTmuxJumpWalksNeedsYouThenDoneOldestFirstAndWraps(t *testing.T) {
	s := newTserver(t)
	s.tmux("set", "-g", "status-interval", "1")
	_, client := s.attach("s1", 120, 20)
	// Order of waiting: A needs-you (oldest), B needs-you, C done, D done.
	var panes []string
	var pids []int
	for _, n := range []string{"A", "B", "C", "D"} {
		p, pid := s.addPane("s1", n)
		panes, pids = append(panes, p), append(pids, pid)
	}
	sid := func(i int) string { return fmt.Sprintf("eeeeeeee-0000-0000-0000-00000000000%d", i) }
	for i := range panes {
		s.hook(panes[i], pids[i], sid(i), "UserPromptSubmit", nil)
	}
	s.hook(panes[2], pids[2], sid(2), "Stop", nil) // C done first
	time.Sleep(20 * time.Millisecond)
	s.hook(panes[3], pids[3], sid(3), "Stop", nil) // D done second
	time.Sleep(20 * time.Millisecond)
	s.hook(panes[1], pids[1], sid(1), "PermissionRequest", nil) // B needs-you first
	time.Sleep(20 * time.Millisecond)
	s.hook(panes[0], pids[0], sid(0), "PermissionRequest", nil) // A needs-you later
	// The client sits on s1's first window, which is not in the queue.
	s.tmux("select-window", "-t", "s1:0")

	var walked []string
	for i := 0; i < 5; i++ {
		s.bosun("jump", "--queue", "--client", client)
		walked = append(walked, s.currentPane(client))
		time.Sleep(30 * time.Millisecond)
	}
	// B (oldest needs-you), A, then C, D (oldest done first); press 5 wraps to B.
	want := []string{panes[1], panes[0], panes[2], panes[3], panes[1]}
	if strings.Join(walked, ",") != strings.Join(want, ",") {
		t.Errorf("walk = %v, want %v", walked, want)
	}
}

func TestTmuxJumpNothingWaiting(t *testing.T) {
	s := newTserver(t)
	_, client := s.attach("s1", 120, 20)
	out := s.bosun("jump", "--queue", "--client", client)
	if strings.Contains(out, "panic") {
		t.Errorf("output: %s", out)
	}
	// With a client the message goes to the status line, not stdout.
	if out != "" {
		t.Errorf("stdout = %q; run-shell output would open a view-mode pane", out)
	}
}

// LESSONS L-09: a stopped tmux server must not leave a stuck bosun.
func TestTmuxStoppedServerLeavesNoStuckBosun(t *testing.T) {
	s := newTserver(t)
	s.threeSessions()
	s.bosun("status", "--json") // warm live.json
	if err := syscall.Kill(s.spid, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	defer syscall.Kill(s.spid, syscall.SIGCONT)
	cmd := exec.Command(binPath, "status", "--tmux")
	cmd.Env = append(append([]string{}, s.env...), fmt.Sprintf("TMUX=%s,%d,0", s.sock, s.spid))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		cmd.Process.Kill()
		t.Fatal("bosun status still running 2 s after the tmux server was stopped")
	}
	// And no later tick is blocked on a leftover lock.
	syscall.Kill(s.spid, syscall.SIGCONT)
	if c := s.status(); c.Agents != 3 {
		t.Errorf("counts after resume = %+v", c)
	}
}

// installConf runs the real `bosun install` (isolated dirs) and returns the
// generated conf path.
func (s *tserver) installConf() string {
	s.t.Helper()
	cmd := exec.Command(binPath, "install", "--tmux-conf", s.root+"/none", "--settings", s.root+"/none")
	cmd.Env = s.env
	if out, err := cmd.CombinedOutput(); err != nil {
		s.t.Fatalf("install: %v\n%s", err, out)
	}
	return s.root + "/data/tmux.conf"
}

func TestTmuxConfSourcedTwiceIsIdempotent(t *testing.T) {
	s := newTserver(t)
	conf := s.installConf()
	s.tmux("set", "-g", "status-right", "%H:%M")
	s.tmux("set", "-gw", "window-status-format", "#I:#W")
	s.tmux("set", "-gw", "window-status-current-format", "#I:#W*")
	for i := 0; i < 2; i++ {
		s.tmux("source-file", conf)
	}
	right := s.tmux("show", "-gv", "status-right")
	if n := strings.Count(right, "bosun status"); n != 1 {
		t.Errorf("status-right has %d bosun segments: %s", n, right)
	}
	if !strings.Contains(right, "%H:%M") {
		t.Errorf("theme's status-right was lost: %s", right)
	}
	for _, opt := range []string{"window-status-format", "window-status-current-format"} {
		v := s.tmux("show", "-gwv", opt)
		if n := strings.Count(v, "@bosun-win-mark"); n != 1 {
			t.Errorf("%s has %d markers: %s", opt, n, v)
		}
	}
	hooks := s.tmux("show-hooks", "-g", "client-attached")
	if n := strings.Count(hooks, "status --catch-up"); n != 1 {
		t.Errorf("client-attached has %d catch-up hooks:\n%s", n, hooks)
	}
	if keys := s.tmux("list-keys", "-T", "prefix", "a"); !strings.Contains(keys, "jump --queue") {
		t.Errorf("jump key not bound: %s", keys)
	}
}

// renderCheck sources confs, builds four windows (api needs-you, web done, docs
// and shell working), attaches a real client, and returns the captured outer
// pane.
func (s *tserver) renderCheck(confs ...string) string {
	s.t.Helper()
	s.tmux("set", "-g", "status-interval", "1")
	for _, c := range confs {
		s.tmux("source-file", c)
	}
	var panes []string
	var pids []int
	first := strings.Fields(s.tmux("list-panes", "-t", "s1", "-F", "#{pane_id} #{pane_pid}"))
	fp, _ := strconv.Atoi(first[1])
	panes, pids = append(panes, first[0]), append(pids, fp)
	for _, n := range []string{"api", "web", "docs"} {
		p, pid := s.addPane("s1", n)
		panes, pids = append(panes, p), append(pids, pid)
	}
	s.tmux("rename-window", "-t", "s1:0", "shell")
	sid := func(i int) string { return fmt.Sprintf("ffffffff-0000-0000-0000-00000000000%d", i) }
	for i := range panes {
		s.hook(panes[i], pids[i], sid(i), "UserPromptSubmit", nil)
	}
	s.hook(panes[1], pids[1], sid(1), "PermissionRequest", nil) // api: needs-you
	s.hook(panes[2], pids[2], sid(2), "Stop", nil)              // web: done
	// docs and shell: working
	s.tmux("select-window", "-t", "s1:shell")
	capture, _ := s.attach("s1", 200, 12)
	s.bosun("status", "--catch-up")
	time.Sleep(2500 * time.Millisecond) // two status-interval ticks
	return capture()
}

func assertRendered(t *testing.T, capture string) {
	t.Helper()
	for _, raw := range []string{"#{", "#[", "bg=", "fg=", "colour", "#(", "bosun status"} {
		if strings.Contains(capture, raw) {
			t.Errorf("raw format text %q in the rendered bar:\n%s", raw, capture)
		}
	}
	lines := strings.Split(strings.TrimRight(capture, "\n"), "\n")
	bar := lines[len(lines)-1]
	for _, want := range []string{"⚑ 1 waiting", "✓ 1 done", "4 sessions"} {
		if !strings.Contains(bar, want) {
			t.Errorf("bar lacks %q:\n%s", want, bar)
		}
	}
	// Each tab marker sits right after its window's name; working windows have none.
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`1:api\s+⚑`), regexp.MustCompile(`2:web\s+✓`),
		regexp.MustCompile(`3:docs-\s{2,}\S`), regexp.MustCompile(`0:shell\*?\s+1:`),
	} {
		if !re.MatchString(bar) {
			t.Errorf("no tab marker match for %s:\n%s", re, bar)
		}
	}
}

func saveCapture(t *testing.T, name, capture string) {
	if dir := os.Getenv("BOSUN_TEST_CAPTURE_DIR"); dir != "" {
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, name), []byte(capture), 0o644)
	}
}

func TestTmuxRealRenderPlain(t *testing.T) {
	s := newTserver(t)
	conf := s.installConf()
	capture := s.renderCheck(conf)
	t.Logf("capture:\n%s", capture)
	saveCapture(t, "render-plain.txt", capture)
	assertRendered(t, capture)
}

// Composes with the user's real theme: tmux-themepack, powerline/block/blue,
// read from the installed plugin dir without modifying it.
func TestTmuxRealRenderWithThemepack(t *testing.T) {
	home, _ := os.UserHomeDir()
	theme := filepath.Join(home, ".tmux/plugins/tmux-themepack/powerline/block/blue.tmuxtheme")
	if _, err := os.Stat(theme); err != nil {
		t.Skip("tmux-themepack not installed")
	}
	s := newTserver(t)
	conf := s.installConf()
	capture := s.renderCheck(theme, conf)
	t.Logf("capture:\n%s", capture)
	saveCapture(t, "render-themepack.txt", capture)
	assertRendered(t, capture)
	right := s.tmux("show", "-gv", "status-right")
	if n := strings.Count(right, "bosun status"); n != 1 {
		t.Errorf("segment count in themed status-right = %d", n)
	}
	// Re-sourcing on top of the theme stays a no-op.
	s.tmux("source-file", conf)
	if n := strings.Count(s.tmux("show", "-gv", "status-right"), "bosun status"); n != 1 {
		t.Errorf("segment count after second source = %d", n)
	}
}

// H-1: tmux format-expands new-window -c, so a directory named #(cmd) must not
// run cmd, and the window must still start in that directory.
func TestTmuxNewWindowDirIsNotFormatExpanded(t *testing.T) {
	for _, tc := range []struct {
		name, wantName string
		open           func(c tmux.Client, dir, command string) error
	}{
		{"NewNamedWindow", "t#1", func(c tmux.Client, dir, command string) error {
			return c.NewNamedWindow(dir, "t#1", nil, command)
		}},
		{"NewWindow", "diff", func(c tmux.Client, dir, command string) error {
			return c.NewWindow(dir, command)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTserver(t)
			tmp := t.TempDir()
			// A directory name cannot hold a slash, so #() reaches the sentinel
			// path through the server's environment.
			s.tmux("set-environment", "-g", "PWNED_PATH", tmp+"/PWNED")
			sub := filepath.Join(tmp, "#(touch $PWNED_PATH)")
			if err := os.Mkdir(sub, 0o755); err != nil {
				t.Fatal(err)
			}
			c := tmux.Client{Runner: runner.Exec{}, Socket: s.sock}
			if err := tc.open(c, sub, "pwd > "+tmp+"/where; sleep 5"); err != nil {
				t.Fatal(err)
			}
			time.Sleep(time.Second)
			if _, err := os.Stat(filepath.Join(tmp, "PWNED")); err == nil {
				t.Error("tmux ran #(...) from the window directory")
			}
			where, _ := os.ReadFile(filepath.Join(tmp, "where"))
			if got := strings.TrimSpace(string(where)); got != sub {
				t.Errorf("window started in %q, want %q", got, sub)
			}
			if got := strings.TrimSpace(s.tmux("display-message", "-p", "#{window_name}")); got != tc.wantName {
				t.Errorf("window name = %q, want %q", got, tc.wantName)
			}
		})
	}
}
