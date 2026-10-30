package main

import (
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jmvaldez/bosun/internal/hook"
	"github.com/jmvaldez/bosun/internal/testguard"
)

var binPath string

// TestMain builds the real binary once. The contract tests below run it as a
// subprocess, which is the only way to observe exit codes and process-level
// stdin behavior.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "bosun-test-bin")
	if err != nil {
		panic(err)
	}
	binPath = filepath.Join(dir, "bosun")
	cmd := exec.Command("go", "build", "-o", binPath, ".")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		os.Stderr.Write(out)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	os.Exit(func() int {
		defer os.RemoveAll(dir)
		return testguard.Run(m)
	}())
}

// isolated returns an env whose HOME and every bosun dir sit under one temp
// root. Nothing from the real environment is inherited, so a forgotten var can
// never reach the real home (LESSONS L-03).
func isolated(t *testing.T) (env []string, root string) {
	t.Helper()
	root = t.TempDir()
	os.MkdirAll(root+"/config", 0o755)
	os.WriteFile(root+"/config/config.toml", []byte(testConfig), 0o600)
	return []string{
		"PATH=/usr/bin:/bin",
		"HOME=" + root + "/home",
		// Any catch-up evaluation in a subprocess test must fail to find a tmux
		// server rather than reach the real one.
		"TMUX=" + root + "/no-such-tmux-socket,1,0",
		"BOSUN_CONFIG_DIR=" + root + "/config",
		"BOSUN_STATE_DIR=" + root + "/state",
		"BOSUN_CACHE_DIR=" + root + "/cache",
		"BOSUN_DATA_DIR=" + root + "/data",
	}, root
}

// testConfig turns notifications off: no test may post a real desktop notice,
// and hooks fork notify-dispatch, which would resolve the real backend.
const testConfig = "[notify]\nbackend = \"off\"\n"

const sid = "11111111-2222-3333-4444-555555555555"

type result struct {
	code    int
	elapsed time.Duration
	out     []byte
}

func runBosun(t *testing.T, env []string, stdin io.Reader, args ...string) result {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	cmd.Env = env
	cmd.Stdin = stdin
	var out bytes.Buffer
	cmd.Stdout = &out
	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start)
	waitForDetachedChildren(t, env)
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return result{code, elapsed, out.Bytes()}
}

// waitForDetachedChildren waits (bounded) for the detached children a bosun run
// spawned, such as notify-dispatch after a Stop. They take locks under the
// test's state dir, and a TempDir cleanup racing them fails with "directory not
// empty". Children are found by their environment, which carries the test's
// BOSUN_STATE_DIR; the wait is Linux only (no /proc elsewhere), and the time
// excluded from result.elapsed is the child's, not the hook's.
func waitForDetachedChildren(t *testing.T, env []string) {
	t.Helper()
	var needle string
	for _, e := range env {
		if strings.HasPrefix(e, "BOSUN_STATE_DIR=") {
			needle = e + "\x00"
		}
	}
	if needle == "" {
		return
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !childrenRunning(needle) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Errorf("a detached bosun child was still running after 5 s")
}

func childrenRunning(needle string) bool {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return false
	}
	for _, e := range ents {
		if e.Name()[0] < '0' || e.Name()[0] > '9' {
			continue
		}
		if exe, err := os.Readlink("/proc/" + e.Name() + "/exe"); err != nil || exe != binPath {
			continue
		}
		if b, err := os.ReadFile("/proc/" + e.Name() + "/environ"); err == nil && strings.Contains(string(b), needle) {
			return true
		}
	}
	return false
}

func TestHookContractSubprocess(t *testing.T) {
	good := `{"session_id":"` + sid + `","hook_event_name":"Stop"}`
	// It must be an *os.File: exec waits for a copier goroutine to drain any
	// other reader, which would hang the test rather than the hook.
	neverClosing := func(t *testing.T) io.Reader {
		pr, pw, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { pw.Close(); pr.Close() })
		return pr
	}
	tests := []struct {
		name  string
		stdin func(t *testing.T) io.Reader
		tweak func(env []string, root string) []string
	}{
		{"empty", func(*testing.T) io.Reader { return strings.NewReader("") }, nil},
		{"malformed", func(*testing.T) io.Reader { return strings.NewReader("{{{ nope") }, nil},
		{"10 MB", func(*testing.T) io.Reader { return bytes.NewReader(bytes.Repeat([]byte("q"), 10<<20)) }, nil},
		{"10 MB valid-looking", func(*testing.T) io.Reader {
			return io.MultiReader(strings.NewReader(`{"session_id":"`+sid+`","hook_event_name":"Stop","last_assistant_message":"`), bytes.NewReader(bytes.Repeat([]byte("q"), 10<<20)))
		}, nil},
		{"never-closing stdin", neverClosing, nil},
		{"traversal session id", func(*testing.T) io.Reader {
			return strings.NewReader(`{"session_id":"../../evil","hook_event_name":"Stop"}`)
		}, nil},
		{"unwritable state dir", func(*testing.T) io.Reader { return strings.NewReader(good) },
			func(env []string, root string) []string {
				os.MkdirAll(root+"/state", 0o500)
				return env
			}},
		{"HOME unset and no overrides", func(*testing.T) io.Reader { return strings.NewReader(good) },
			func(env []string, root string) []string { return []string{"PATH=/usr/bin:/bin"} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, root := isolated(t)
			if tt.tweak != nil {
				env = tt.tweak(env, root)
				defer os.Chmod(root+"/state", 0o700)
			}
			r := runBosun(t, env, tt.stdin(t), "hook", "--plugin-version", "0.1.0")
			if r.code != 0 {
				t.Errorf("exit %d", r.code)
			}
			if r.elapsed > 300*time.Millisecond {
				t.Errorf("took %v, want < 300ms", r.elapsed)
			}
			if len(r.out) != 0 {
				t.Errorf("hook printed %q; stdout is parsed by Claude Code", r.out)
			}
			if tt.name == "traversal session id" {
				assertNoFiles(t, root)
			}
		})
	}
}

func assertNoFiles(t *testing.T, root string) {
	t.Helper()
	var files []string
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && p != root+"/config/config.toml" && d.Name() != ".bosun" { // the test's own config; the purge marker holds nothing
			files = append(files, p)
		}
		return nil
	})
	if len(files) != 0 {
		sort.Strings(files)
		t.Errorf("unexpected files: %v", files)
	}
}

func TestHookSubprocessRecordsEvent(t *testing.T) {
	env, root := isolated(t)
	r := runBosun(t, env, strings.NewReader(`{"session_id":"`+sid+`","hook_event_name":"Stop","last_assistant_message":"SENTINEL"}`), "hook", "--plugin-version", "0.1.0")
	if r.code != 0 {
		t.Fatalf("exit %d", r.code)
	}
	b, err := os.ReadFile(root + "/state/sessions/" + sid + "/events.jsonl")
	if err != nil || !strings.Contains(string(b), `"ev":"Stop"`) || strings.Contains(string(b), "SENTINEL") {
		t.Fatalf("events = %q err=%v", b, err)
	}
}

// The real binary's whole-process time, runtime start included, for one hook.
func TestHookProcessWallTime(t *testing.T) {
	if testing.Short() {
		t.Skip("timing")
	}
	env, _ := isolated(t)
	var worst time.Duration
	for i := 0; i < 20; i++ {
		r := runBosun(t, env, strings.NewReader(`{"session_id":"`+sid+`","hook_event_name":"PreToolUse","tool_name":"Bash"}`), "hook")
		if r.elapsed > worst {
			worst = r.elapsed
		}
	}
	t.Logf("worst of 20 full-process hook runs: %v", worst)
	if worst > 50*time.Millisecond {
		t.Errorf("worst = %v, want under the 50 ms hook budget", worst)
	}
}

func TestStatuslineSubprocessByteIdentical(t *testing.T) {
	env, root := isolated(t)
	script := filepath.Join(root, "sl.sh")
	os.WriteFile(script, []byte("#!/bin/sh\ncat >/dev/null\nprintf '\\033[36mmodel\\033[0m \\303\\251\\000end'\n"), 0o755)
	stdin := `{"session_id":"` + sid + `","cost":{"total_cost_usd":1.5},"model":{"id":"m"}}`

	cmd := exec.Command(script)
	cmd.Stdin = strings.NewReader(stdin)
	want, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	r := runBosun(t, env, strings.NewReader(stdin), "statusline", "--", script)
	if r.code != 0 || !bytes.Equal(r.out, want) {
		t.Fatalf("code=%d\n got %q\nwant %q", r.code, r.out, want)
	}
	b, err := os.ReadFile(root + "/state/sessions/" + sid + "/statusline.json")
	if err != nil || !strings.Contains(string(b), `"cost_usd":1.5`) {
		t.Fatalf("snapshot = %q err=%v", b, err)
	}
}

func TestConfigCommandReportsUnknownKeys(t *testing.T) {
	env, root := isolated(t)
	os.MkdirAll(root+"/config", 0o755)
	os.WriteFile(root+"/config/config.toml", []byte("[rules]\nquite = \"1m\"\nquiet = \"2m\"\n"), 0o600)
	r := runBosun(t, env, nil, "config")
	text := string(r.out)
	if r.code != 0 || !strings.Contains(text, "unknown key rules.quite") || !strings.Contains(text, `quiet = "2m0s"`) {
		t.Fatalf("code=%d\n%s", r.code, text)
	}
}

func TestInstallSubprocessLeavesUserFilesAlone(t *testing.T) {
	env, root := isolated(t)
	settings := root + "/home/.claude/settings.json"
	os.MkdirAll(filepath.Dir(settings), 0o755)
	os.WriteFile(settings, []byte(`{"statusLine":{"type":"command","command":"echo hi"}}`), 0o644)
	tmuxConf := root + "/home/.tmux.conf"
	os.WriteFile(tmuxConf, []byte("set -g mouse on\n"), 0o644)
	r := runBosun(t, env, nil, "install", "--repo", "/some/repo")
	if r.code != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.out)
	}
	for p, want := range map[string]string{settings: `{"statusLine":{"type":"command","command":"echo hi"}}`, tmuxConf: "set -g mouse on\n"} {
		if b, _ := os.ReadFile(p); string(b) != want {
			t.Errorf("%s changed", p)
		}
	}
	if !strings.Contains(string(r.out), "claude plugin marketplace add /some/repo") {
		t.Errorf("output:\n%s", r.out)
	}
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func TestPluginHooksJSON(t *testing.T) {
	var doc struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string   `json:"type"`
				Command string   `json:"command"`
				Args    []string `json:"args"`
				Timeout int      `json:"timeout"`
				Async   *bool    `json:"async"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	readJSON(t, "plugin/hooks/hooks.json", &doc)
	if len(doc.Hooks) != len(hook.Events) {
		t.Errorf("registers %d events, want %d", len(doc.Hooks), len(hook.Events))
	}
	for _, ev := range hook.Events {
		groups := doc.Hooks[ev]
		if len(groups) != 1 || len(groups[0].Hooks) != 1 {
			t.Errorf("%s: want exactly one group with one hook", ev)
			continue
		}
		g, h := groups[0], groups[0].Hooks[0]
		if g.Matcher != "" {
			t.Errorf("%s: matcher %q; bosun registers no matchers", ev, g.Matcher)
		}
		wantArgs := []string{"hook", "--plugin-version", manifestVersion()}
		if h.Type != "command" || h.Command != "bosun" || strings.Join(h.Args, " ") != strings.Join(wantArgs, " ") {
			t.Errorf("%s: %+v, want exec form `bosun hook --plugin-version %s`", ev, h, manifestVersion())
		}
		if h.Timeout != 5 {
			t.Errorf("%s: timeout %d, want 5", ev, h.Timeout)
		}
		if h.Async != nil && *h.Async {
			t.Errorf("%s: async hooks are killed at -p teardown; must be sync", ev)
		}
	}
	if _, ok := doc.Hooks["MessageDisplay"]; ok {
		t.Error("MessageDisplay carries assistant text and must never be registered")
	}
}

func TestManifestVersionsMatchVersionFile(t *testing.T) {
	var plugin struct{ Name, Version string }
	readJSON(t, "plugin/.claude-plugin/plugin.json", &plugin)
	var market struct {
		Name    string
		Plugins []struct{ Name, Source, Version string }
	}
	readJSON(t, ".claude-plugin/marketplace.json", &market)
	if plugin.Name != "bosun" || plugin.Version != manifestVersion() {
		t.Errorf("plugin.json = %+v, VERSION = %s", plugin, manifestVersion())
	}
	if market.Name != "bosun" || len(market.Plugins) != 1 || market.Plugins[0].Name != "bosun" ||
		market.Plugins[0].Source != "./plugin" || market.Plugins[0].Version != manifestVersion() {
		t.Errorf("marketplace.json = %+v", market)
	}
}

// Source-level guards from LESSONS: no tmux info (L-08), no direct WriteFile on
// state paths outside atomicfile (L-41), no os.UserConfigDir (SPEC section 4).
func TestSourceGuards(t *testing.T) {
	forbidden := map[string]string{
		"tmux info":        "L-08: `info` does not exist; use has-session",
		"os.UserConfigDir": "SPEC 4: returns ~/Library/Application Support on macOS",
		"exec.Command(":    "SPEC 2: every exec uses exec.CommandContext with a timeout",
		"os.WriteFile(":    "L-41: state files go through internal/atomicfile (temp file plus rename)",
		"syscall.Flock":    "use internal/flock",
		"unix.LOCK_EX)":    "blocking flock; use LOCK_NB",
	}
	filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		// Dot dirs hold no source of ours; .claude/worktrees has whole checkouts
		// of other branches, which made this test fail on their files.
		if err == nil && d.IsDir() && p != "." && strings.HasPrefix(d.Name(), ".") {
			return fs.SkipDir
		}
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") ||
			strings.HasPrefix(p, "kit") {
			return nil
		}
		b, _ := os.ReadFile(p)
		for needle, why := range forbidden {
			if strings.HasPrefix(p, "internal/atomicfile") && needle == "os.WriteFile(" {
				continue
			}
			if strings.Contains(string(b), needle) {
				t.Errorf("%s contains %q (%s)", p, needle, why)
			}
		}
		return nil
	})
}

// manifestVersion is the VERSION file: what the plugin manifests must carry.
func manifestVersion() string { return strings.TrimSpace(versionFile) }

func TestDevVersion(t *testing.T) {
	with := func(rev string) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: rev}}}, true
		}
	}
	for _, tt := range []struct {
		name string
		read func() (*debug.BuildInfo, bool)
		want string
	}{
		{"revision", with("0123456789abcdef"), "0.1.0-dev+0123456"},
		{"short revision is ignored", with("abc"), "0.1.0-dev"},
		{"no build info", func() (*debug.BuildInfo, bool) { return nil, false }, "0.1.0-dev"},
		{"no vcs settings", func() (*debug.BuildInfo, bool) { return &debug.BuildInfo{}, true }, "0.1.0-dev"},
	} {
		if got := devVersion("0.1.0", tt.read); got != tt.want {
			t.Errorf("%s: %q, want %q", tt.name, got, tt.want)
		}
	}
	if !strings.HasPrefix(version, manifestVersion()) {
		t.Errorf("an unstamped test build has version %q, want %s-dev...", version, manifestVersion())
	}
}

func TestWaitForDetachedChildrenSeesARunningChild(t *testing.T) {
	env, _ := isolated(t)
	cmd := exec.Command(binPath, "statusline")
	cmd.Env = env
	in, _ := cmd.StdinPipe() // holds the process open until closed
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	needle := ""
	for _, e := range env {
		if strings.HasPrefix(e, "BOSUN_STATE_DIR=") {
			needle = e + "\x00"
		}
	}
	seen := false
	for i := 0; i < 100 && !seen; i++ {
		seen = childrenRunning(needle)
		time.Sleep(5 * time.Millisecond)
	}
	in.Close()
	cmd.Wait()
	if !seen {
		t.Error("childrenRunning did not see a live bosun process with the test's state dir")
	}
	if childrenRunning(needle) {
		t.Error("still reported after exit")
	}
}
