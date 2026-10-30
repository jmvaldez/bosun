package main

import (
	"bytes"
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

// bosunWith runs bosun the way a pane would: TMUX pointing at this server and
// extra environment entries. It returns stdout+stderr and the exit code.
func (s *tserver) bosunWith(extra []string, args ...string) (string, int) {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.Env = append(append([]string{}, s.env...), fmt.Sprintf("TMUX=%s,%d,0", s.sock, s.spid))
	cmd.Env = append(cmd.Env, extra...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		s.t.Fatal(err)
	}
	return out.String(), code
}

// git runs git in dir with a clean identity.
func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", dir}, args...)...)
	c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func makeRepo(t *testing.T, root, name, branch string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	os.MkdirAll(dir, 0o755)
	git(t, dir, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644)
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "init")
	if branch != "" {
		git(t, dir, "checkout", "-q", "-b", branch)
	}
	return dir
}

func (s *tserver) windows() string {
	return s.tmux("list-windows", "-a", "-F", "#{session_name}:#{window_name} #{pane_current_path}")
}

func (s *tserver) waitWindow(want string, present bool) string {
	s.t.Helper()
	var w string
	for deadline := time.Now().Add(8 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if w = s.windows(); strings.Contains(w, want) == present {
			return w
		}
	}
	s.t.Fatalf("window %q present=%v never happened:\n%s", want, present, w)
	return ""
}

// A launcher that proves what a real one would see: the task, the directory, the
// arguments, and that bosun is on PATH even though the user's rc file reset it.
func (s *tserver) fakeLauncher() (script, probe string) {
	probe = s.root + "/probe.txt"
	script = s.root + "/fake-claude"
	body := `#!/bin/sh
{
  echo "task=$BOSUN_TASK"
  echo "pwd=$PWD"
  echo "args=$*"
  echo "bosun=$(command -v bosun)"
} > ` + probe + `
printf '{"session_id":"99999999-0000-0000-0000-000000000009","hook_event_name":"SessionStart","source":"startup","cwd":"%s"}' "$PWD" | bosun hook
sleep 2
`
	os.WriteFile(script, []byte(body), 0o755)
	os.WriteFile(s.root+"/config/config.toml", []byte(testConfig+"[claude]\nlauncher = \""+script+"\"\n"), 0o600)
	// An rc file that clobbers PATH, as the first build's did (LESSONS L-02).
	os.WriteFile(s.root+"/home/.bashrc", []byte("PATH=/usr/bin:/bin\n"), 0o644)
	return script, probe
}

func TestTmuxRunOpensANamedTaskedWindowAndHooksFindBosun(t *testing.T) {
	s := newTserver(t)
	_, probe := s.fakeLauncher()
	proj := s.root + "/proj"
	os.MkdirAll(proj, 0o755)
	out, code := s.bosunWith([]string{"SHELL=/bin/bash", "TMUX_PANE=%0"}, "run", "--task", "abc-123", "--dir", proj, "--", "--foo", "bar baz")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	s.waitWindow("ABC-123 "+proj, true)
	var got string
	for deadline := time.Now().Add(6 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		b, _ := os.ReadFile(probe)
		if got = string(b); strings.Contains(got, "bosun=") {
			break
		}
	}
	for _, want := range []string{"task=ABC-123", "pwd=" + proj, "args=--foo bar baz", "bosun=" + binPath} {
		if !strings.Contains(got, want) {
			t.Errorf("launcher saw:\n%s\nlacking %q", got, want)
		}
	}
	// The hook it ran found bosun and the session shows its task.
	var st struct{ Sessions []struct{ ID, Task string } }
	for deadline := time.Now().Add(4 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		json.Unmarshal([]byte(s.bosun("status", "--json")), &st)
		if len(st.Sessions) > 0 {
			break
		}
	}
	if len(st.Sessions) != 1 || st.Sessions[0].Task != "ABC-123" {
		t.Errorf("sessions = %+v", st.Sessions)
	}
	// The window closes when the launcher exits.
	s.waitWindow("ABC-123", false)
}

func TestRunOutsideTmuxAndWithoutATaskExitNonzero(t *testing.T) {
	env, _ := isolated(t)
	var noTmux []string
	for _, e := range env {
		if !strings.HasPrefix(e, "TMUX=") {
			noTmux = append(noTmux, e)
		}
	}
	if r := runBosun(t, noTmux, nil, "run", "--task", "ABC-1"); r.code != 1 {
		t.Errorf("outside tmux: exit %d, want 1", r.code)
	}
	if r := runBosun(t, env, nil, "run"); r.code != 2 {
		t.Errorf("no task: exit %d, want 2", r.code)
	}
	if r := runBosun(t, env, nil, "run", "--task", "bad\x1btask"); r.code != 2 {
		t.Errorf("bad task: exit %d, want 2", r.code)
	}
}

func TestTmuxRunWorktreeBesideTheRepoTagsTheBranchKeyAndRefusesRerun(t *testing.T) {
	s := newTserver(t)
	_, probe := s.fakeLauncher()
	repo := makeRepo(t, s.root+"/code", "api", "")
	out, code := s.bosunWith([]string{"SHELL=/bin/bash", "TMUX_PANE=%0"}, "run", "--worktree", "feat/ABC-123", "--dir", repo)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	wt := s.root + "/code/api-feat-ABC-123"
	if !strings.Contains(out, "worktree "+wt) {
		t.Errorf("output lacks the path:\n%s", out)
	}
	s.waitWindow("ABC-123 "+wt, true)
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if b, _ := os.ReadFile(probe); strings.Contains(string(b), "bosun=") {
			break
		}
	}
	if b, _ := os.ReadFile(probe); !strings.Contains(string(b), "task=ABC-123") || !strings.Contains(string(b), "pwd="+wt) {
		t.Errorf("launcher saw:\n%s", b)
	}
	// A second request for the same worktree is refused and opens nothing.
	before := s.windows()
	out, code = s.bosunWith([]string{"SHELL=/bin/bash", "TMUX_PANE=%0"}, "run", "--worktree", "feat/ABC-123", "--dir", repo)
	if code == 0 || !strings.Contains(out, "already exists") {
		t.Errorf("rerun: exit %d\n%s", code, out)
	}
	if after := s.windows(); strings.Count(after, "ABC-123") > strings.Count(before, "ABC-123") {
		t.Errorf("the refused run opened a window:\n%s", after)
	}
}

func (s *tserver) sessionInPane(pane string, pid int, sid string, startFields map[string]any, extraEnv []string) {
	s.t.Helper()
	p := map[string]any{"session_id": sid, "hook_event_name": "SessionStart", "source": "startup"}
	for k, v := range startFields {
		p[k] = v
	}
	b, _ := json.Marshal(p)
	cmd := exec.Command(binPath, "hook", "--plugin-version", manifestVersion())
	cmd.Env = append(append([]string{}, s.env...), fmt.Sprintf("TMUX=%s,%d,0", s.sock, s.spid), "TMUX_PANE="+pane, "CLAUDE_PID="+strconv.Itoa(pid))
	cmd.Env = append(cmd.Env, extraEnv...)
	cmd.Stdin = bytes.NewReader(b)
	if out, err := cmd.CombinedOutput(); err != nil || len(out) != 0 {
		s.t.Fatalf("hook: %v %q", err, out)
	}
}

func (s *tserver) tasks() map[string]string {
	var st struct{ Sessions []struct{ ID, Task string } }
	if err := json.Unmarshal([]byte(s.bosun("status", "--json")), &st); err != nil {
		s.t.Fatal(err)
	}
	out := map[string]string{}
	for _, x := range st.Sessions {
		out[x.ID] = x.Task
	}
	return out
}

func TestTmuxTagOverridesTheBranchKeyAndClears(t *testing.T) {
	s := newTserver(t)
	repo := makeRepo(t, s.root+"/code", "api", "feat/ops-7-thing")
	pane := strings.Fields(s.tmux("list-panes", "-t", "s1", "-F", "#{pane_id} #{pane_pid}"))
	pid, _ := strconv.Atoi(pane[1])
	sid := "aaaaaaaa-0000-0000-0000-00000000000a"
	s.sessionInPane(pane[0], pid, sid, map[string]any{"cwd": repo}, nil)
	if got := s.tasks()[sid]; got != "OPS-7" {
		t.Fatalf("before tagging, task = %q (from the branch)", got)
	}
	// Targets the newest live session in $TMUX_PANE.
	out, code := s.bosunWith([]string{"TMUX_PANE=" + pane[0]}, "tag", "abc-9")
	if code != 0 || !strings.Contains(out, "tagged aaaaaaaa ABC-9") {
		t.Fatalf("tag: %d %s", code, out)
	}
	if got := s.tasks()[sid]; got != "ABC-9" {
		t.Errorf("after tag, task = %q", got)
	}
	if out, code := s.bosunWith([]string{"TMUX_PANE=" + pane[0]}, "tag", "--clear"); code != 0 {
		t.Fatalf("clear: %d %s", code, out)
	}
	if got := s.tasks()[sid]; got != "OPS-7" {
		t.Errorf("after --clear, task = %q, want the branch key back", got)
	}
	// Free text keeps its case; bad text is refused.
	if out, code := s.bosunWith([]string{"TMUX_PANE=" + pane[0]}, "tag", "Bosun Init"); code != 0 || !strings.Contains(out, "tagged aaaaaaaa Bosun Init") {
		t.Fatalf("free-form tag: %d %s", code, out)
	}
	if got := s.tasks()[sid]; got != "Bosun Init" {
		t.Errorf("after free-form tag, task = %q", got)
	}
	for _, bad := range []string{"", "  ", "a\x1b[31mb", strings.Repeat("x", 65)} {
		if _, code := s.bosunWith([]string{"TMUX_PANE=" + pane[0]}, "tag", bad); code == 0 {
			t.Errorf("tag %q was accepted", bad)
		}
	}
	if got := s.tasks()[sid]; got != "Bosun Init" {
		t.Errorf("a refused tag changed the task to %q", got)
	}
	// Errors: no pane, unknown session.
	if _, code := s.bosunWith(nil, "tag", "ABC-1"); code == 0 {
		t.Error("tag with no pane and no --session succeeded")
	}
	if _, code := s.bosunWith(nil, "tag", "ABC-1", "--session", "ffffffff-0000-0000-0000-00000000000f"); code == 0 {
		t.Error("an unknown session was tagged")
	}
	if out, code := s.bosunWith(nil, "tag", "ABC-2", "--session", sid); code != 0 {
		t.Errorf("--session: %d %s", code, out)
	}
}

// The segment/popup data, the ledger and spend must agree on every task, across
// every source: env, branch, tag, and none (D-16: tag and env are never re-derived).
func TestTmuxHookLedgerSpendAndPopupAgreeOnEveryTask(t *testing.T) {
	s := newTserver(t)
	repoBranch := makeRepo(t, s.root+"/code", "web", "feat/ops-7-thing")
	repoPlain := makeRepo(t, s.root+"/code", "plain", "")
	ids := map[string]string{
		"env":    "aaaaaaaa-0000-0000-0000-00000000000a",
		"branch": "bbbbbbbb-0000-0000-0000-00000000000b",
		"tag":    "cccccccc-0000-0000-0000-00000000000c",
		"none":   "dddddddd-0000-0000-0000-00000000000d",
	}
	panes := map[string]string{}
	pids := map[string]int{}
	first := strings.Fields(s.tmux("list-panes", "-t", "s1", "-F", "#{pane_id} #{pane_pid}"))
	panes["env"], pids["env"] = first[0], atoi(first[1])
	for _, k := range []string{"branch", "tag", "none"} {
		p, pid := s.addPane("s1", k)
		panes[k], pids[k] = p, pid
	}
	s.sessionInPane(panes["env"], pids["env"], ids["env"], map[string]any{"cwd": repoPlain}, []string{"BOSUN_TASK=env-4"})
	s.sessionInPane(panes["branch"], pids["branch"], ids["branch"], map[string]any{"cwd": repoBranch}, nil)
	s.sessionInPane(panes["tag"], pids["tag"], ids["tag"], map[string]any{"cwd": repoBranch}, nil)
	s.sessionInPane(panes["none"], pids["none"], ids["none"], map[string]any{"cwd": repoPlain}, nil)
	if _, code := s.bosunWith([]string{"TMUX_PANE=" + panes["tag"]}, "tag", "Bosun", "  Init "); code != 0 {
		t.Fatal("tag failed")
	}
	// Something to spend in each, so spend has rows.
	for _, id := range ids {
		writeTranscript(t, s.root, id, 1.0, time.Now().Add(-time.Minute))
	}

	want := map[string]string{"env": "ENV-4", "branch": "OPS-7", "tag": "Bosun Init", "none": ""}
	live := s.tasks()
	for k, id := range ids {
		if live[id] != want[k] {
			t.Errorf("status --json: %s task = %q, want %q", k, live[id], want[k])
		}
	}
	var res struct {
		Rows []struct {
			Key string
			USD float64
		}
	}
	if err := json.Unmarshal([]byte(s.bosun("spend", "--by", "task", "--json")), &res); err != nil {
		t.Fatal(err)
	}
	spendTasks := map[string]float64{}
	for _, r := range res.Rows {
		spendTasks[r.Key] = r.USD
	}
	var one struct {
		Rows []struct{ Key string }
	}
	if err := json.Unmarshal([]byte(s.bosun("spend", "--by", "session", "--task", "bosun init", "--json")), &one); err != nil || len(one.Rows) != 1 {
		t.Errorf("spend --task \"bosun init\": %v, %+v", err, one)
	}
	for k, w := range want {
		key := w
		if key == "" {
			key = "untasked"
		}
		if spendTasks[key] != 1.0 {
			t.Errorf("spend --by task: %s (%s) = %v; rows %v", k, key, spendTasks[key], spendTasks)
		}
	}
	// The popup's TASK column shows the same keys.
	p := s.runUI(150, 30)
	got := p.waitFor("(no task)")
	for _, key := range []string{"ENV-4", "OPS-7", "Bosun Init"} {
		if !strings.Contains(got, key) {
			t.Errorf("the popup lacks %s:\n%s", key, got)
		}
	}
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }
