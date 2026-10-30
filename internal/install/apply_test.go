package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jmvaldez/bosun/internal/config"
	"github.com/jmvaldez/bosun/internal/runner"
	"github.com/jmvaldez/bosun/internal/tmuxconf"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmvaldez/bosun/internal/platform"
)

type call struct {
	name string
	args []string
}

// fakeRunner stands in for claude and tmux: it records every call and fails the
// ones listed in fail (matched on the joined args).
type fakeRunner struct {
	calls []call
	fail  map[string]error
	out   map[string]string // stdout by joined args
	// after runs once a call has succeeded, like claude rewriting settings.json.
	after func(args []string)
}

func (f *fakeRunner) Run(_ time.Duration, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, call{filepath.Base(name), args})
	if err := f.fail[strings.Join(args, " ")]; err != nil {
		return nil, err
	}
	if f.after != nil {
		f.after(args)
	}
	return []byte(f.out[strings.Join(args, " ")]), nil
}

// claudeEdits makes the fake claude behave like the real one: it keeps
// installed_plugins.json and known_marketplaces.json, and rewrites settings.json
// (as text) between bosun's plan and bosun's write.
func claudeEdits(settings string) func(args []string) {
	dir := filepath.Dir(settings)
	writeJSON := func(name string, v any) {
		os.MkdirAll(dir+"/plugins", 0o755)
		b, _ := json.Marshal(v)
		os.WriteFile(dir+"/plugins/"+name, b, 0o644)
	}
	plugins := func() map[string]any {
		var f struct {
			Plugins map[string]any `json:"plugins"`
		}
		if b, err := os.ReadFile(dir + "/plugins/installed_plugins.json"); err == nil {
			json.Unmarshal(b, &f)
		}
		if f.Plugins == nil {
			f.Plugins = map[string]any{}
		}
		return f.Plugins
	}
	markets := func() map[string]any {
		m := map[string]any{}
		if b, err := os.ReadFile(dir + "/plugins/known_marketplaces.json"); err == nil {
			json.Unmarshal(b, &m)
		}
		return m
	}
	setEnabled := func(on bool) {
		b, err := os.ReadFile(settings)
		if err != nil || !json.Valid(b) {
			return // the real claude would fail here too
		}
		text := string(b)
		top := skipWS(b, 0)
		ms, closeIdx := objectMembers(b, top)
		val := `{}`
		if on {
			val = `{"bosun@bosun": true}`
		}
		if i := find(ms, "enabledPlugins"); i >= 0 {
			text = string(splice(b, ms[i].valStart, ms[i].valEnd, val))
		} else if on {
			text = string(insertMember(b, top, closeIdx, ms, "enabledPlugins", val, "  "))
		}
		os.WriteFile(settings, []byte(text), 0o644)
	}
	return func(args []string) {
		if len(args) < 2 || args[0] != "plugin" {
			return
		}
		switch strings.Join(args[1:min(3, len(args))], " ") {
		case "marketplace add":
			m := markets()
			m["bosun"] = map[string]any{}
			writeJSON("known_marketplaces.json", m)
		case "marketplace remove":
			m := markets()
			delete(m, "bosun")
			writeJSON("known_marketplaces.json", m)
		}
		switch args[1] {
		case "install":
			if strings.Contains(strings.Join(args, " "), "--marketplace") {
				m := markets() // claude adds the marketplace from the path itself
				m["bosun"] = map[string]any{}
				writeJSON("known_marketplaces.json", m)
			}
			p := plugins()
			p["bosun@bosun"] = []any{}
			writeJSON("installed_plugins.json", map[string]any{"version": 2, "plugins": p})
			setEnabled(true)
		case "enable":
			setEnabled(true)
		case "uninstall":
			p := plugins()
			delete(p, "bosun@bosun")
			writeJSON("installed_plugins.json", map[string]any{"version": 2, "plugins": p})
			setEnabled(false)
		}
	}
}

func (f *fakeRunner) has(sub string) bool {
	for _, c := range f.calls {
		if strings.Contains(c.name+" "+strings.Join(c.args, " "), sub) {
			return true
		}
	}
	return false
}

type world struct {
	t        *testing.T
	ad       platform.Adapters
	opt      Options
	env      Env
	fr       *fakeRunner
	root     string
	settings string
	tmuxConf string
}

// newWorld builds a fake home with settings.json and ~/.tmux.conf; tmux "\x00"
// means no tmux.conf at all ("" is an empty one).
func newWorld(t *testing.T, settings, tmux string) *world {
	ad, root := setup(t)
	repo := root + "/repo"
	os.MkdirAll(repo+"/.claude-plugin", 0o755)
	os.WriteFile(repo+"/.claude-plugin/marketplace.json", []byte("{}"), 0o644)
	ad.Getenv = func(k string) string {
		if k == "HOME" {
			return root + "/home"
		}
		return ""
	}
	w := &world{t: t, ad: ad, root: root, fr: &fakeRunner{fail: map[string]error{}, out: map[string]string{}}}
	w.settings = writeSettings(t, ad, settings)
	if tmux != "\x00" {
		w.tmuxConf = root + "/home/.tmux.conf"
		os.MkdirAll(root+"/home", 0o755)
		os.WriteFile(w.tmuxConf, []byte(tmux), 0o644)
	}
	w.opt = Options{Repo: repo, Bosun: "/bin/bosun", Version: "0.1.0"}
	w.fr.after = claudeEdits(w.settings)
	w.env = Env{Runner: w.fr, ClaudeBin: "/bin/claude", TmuxBin: "/bin/tmux", InTmux: true, Yes: true,
		Now: func() time.Time { return time.Date(2026, 10, 25, 9, 8, 7, 0, time.UTC) }}
	return w
}

// install leaves the machine as the removed `install --apply` did: the plugin
// and status line steps (which setup still shares), then the source-file line
// in the user's tmux.conf, which only that route wrote and uninstall must
// still remove.
func (w *world) install() (string, int) {
	var out bytes.Buffer
	steps := []Step{pluginInstallStep(w.ad, w.opt, w.env), statusLineStep(w.ad, w.opt, w.env)}
	rc := runSteps(w.env, "bosun install", "Everything is already set up.", steps, &out)
	w.addSourceLine()
	return out.String(), rc
}

// addSourceLine writes the old route's tmux.conf edit: the line right after the
// tpm run line, else at the end, in ~/.tmux.conf (created if absent).
func (w *world) addSourceLine() {
	path := w.root + "/home/.tmux.conf"
	text := ""
	if b, err := os.ReadFile(path); err == nil {
		text = string(b)
	}
	line := sourceLine(w.ad) + "\n"
	if n := tpmRunLine(text); n > 0 {
		lines := strings.SplitAfter(text, "\n")
		text = strings.Join(lines[:n], "") + line + strings.Join(lines[n:], "")
	} else {
		text += line
	}
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) uninstall() (string, int) {
	var out bytes.Buffer
	rc := UninstallApply(w.ad, w.opt, w.env, &out)
	return out.String(), rc
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func backups(t *testing.T, p string) []string {
	m, _ := filepath.Glob(p + ".bak-bosun-*")
	return m
}

const userSettings = `{
  "model": "opus",
  "statusLine": {
    "type": "command",
    "command": "jq -r '.model.id'",
    "padding": 0
  },
  "zzz": [1, 2, {"a": "}"}]
}
`

const userTmux = "set -g mouse on\nset -g @plugin 'tmux-plugins/tpm'\nrun '~/.tmux/plugins/tpm/tpm'\nbind r source-file ~/.tmux.conf\n"

func TestUninstallReversesTheOldInstallRoute(t *testing.T) {
	w := newWorld(t, userSettings, userTmux)
	out, rc := w.install()
	if rc != 0 {
		t.Fatalf("rc %d\n%s", rc, out)
	}
	for _, want := range []string{"claude plugin install bosun --marketplace " + w.opt.Repo} {
		if !w.fr.has(strings.TrimPrefix(want, "claude ")) {
			t.Errorf("missing call %q in %v", want, w.fr.calls)
		}
	}
	// Status line: only the command changed; every other byte is the user's.
	got := read(t, w.settings)
	if !strings.Contains(got, `"padding": 0`) || !strings.Contains(got, `"model": "opus"`) || !strings.Contains(got, "  \"zzz\": [1, 2, {\"a\": \"}\"}],\n  \"enabledPlugins\"") {
		t.Errorf("settings lost content:\n%s", got)
	}
	if cur, _ := currentStatusLine(w.settings); UnwrapOK(cur.Command) != "jq -r '.model.id'" {
		t.Errorf("command = %q", cur.Command)
	}
	// The backup is of what was replaced: the file as claude had just left it.
	if b := backups(t, w.settings); len(b) != 1 || !strings.Contains(read(t, b[0]), "jq -r") || !strings.Contains(read(t, b[0]), "bosun@bosun") {
		t.Errorf("settings backup = %v", b)
	}
	if !strings.Contains(got, `"bosun@bosun": true`) {
		t.Errorf("the plugin enable was lost:\n%s", got)
	}
	// tmux: right after the tpm run line.
	conf := w.ad.Paths.TmuxConf()
	wantTmux := "set -g mouse on\nset -g @plugin 'tmux-plugins/tpm'\nrun '~/.tmux/plugins/tpm/tpm'\nsource-file -q " + conf + "\nbind r source-file ~/.tmux.conf\n"
	if got := read(t, w.tmuxConf); got != wantTmux {
		t.Errorf("tmux.conf = %q", got)
	}

	// Uninstall restores both files.
	w.fr.calls = nil
	out, rc = w.uninstall()
	if rc != 0 {
		t.Fatalf("uninstall rc %d\n%s", rc, out)
	}
	// claude, not bosun, owns enabledPlugins: it is {} now, everything else is back.
	if got := read(t, w.settings); got != strings.Replace(userSettings, "}]\n}\n", "}],\n  \"enabledPlugins\": {}\n}\n", 1) {
		t.Errorf("settings after uninstall:\n%s", got)
	}
	if got := read(t, w.tmuxConf); got != userTmux {
		t.Errorf("tmux.conf after uninstall:\n%s", got)
	}
	if b := backups(t, w.tmuxConf); len(b) != 1 || read(t, b[0]) != wantTmux {
		t.Errorf("tmux backup = %v", b)
	}
	if !w.fr.has("plugin uninstall bosun@bosun") || !w.fr.has("plugin marketplace remove bosun") {
		t.Errorf("plugin not removed: %v", w.fr.calls)
	}
	// And again: nothing left to do.
	w.fr.calls = nil
	out, _ = w.uninstall()
	if !strings.Contains(out, "Nothing to remove.") || len(w.fr.calls) != 3 { // the three read-only tmux probes
		t.Errorf("second uninstall:\n%s %v", out, w.fr.calls)
	}
}

func UnwrapOK(c string) string { o, _ := UnwrapCommand(c); return o }

func TestApplyInvalidJSONRefusesTheStepAndChangesNothing(t *testing.T) {
	w := newWorld(t, `{"statusLine": {`, userTmux)
	out, rc := w.install()
	if rc == 0 || !strings.Contains(out, "not valid JSON") {
		t.Errorf("rc %d\n%s", rc, out)
	}
	if read(t, w.settings) != `{"statusLine": {` || len(backups(t, w.settings)) != 0 {
		t.Error("an invalid settings.json was touched")
	}
}

func TestApplyMissingStatusLineIsCreatedAndRemovedAgain(t *testing.T) {
	for name, body := range map[string]string{
		"multi-line": "{\n  \"model\": \"opus\"\n}\n",
		"one-line":   `{"model":"opus"}`,
		"empty":      "{}",
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t, body, "")
			if out, rc := w.install(); rc != 0 {
				t.Fatalf("rc %d\n%s", rc, out)
			}
			cur, err := currentStatusLine(w.settings)
			if err != nil || cur.Type != "command" || UnwrapOK(cur.Command) != "" || !alreadyWrapped(cur.Command) {
				t.Fatalf("statusLine = %+v err %v\n%s", cur, err, read(t, w.settings))
			}
			if strings.Contains(body, "model") && !strings.Contains(read(t, w.settings), `"model"`) {
				t.Error("existing key lost")
			}
			if out, rc := w.uninstall(); rc != 0 {
				t.Fatalf("uninstall rc %d\n%s", rc, out)
			}
			if strings.Contains(read(t, w.settings), "statusLine") {
				t.Errorf("statusLine survived uninstall:\n%s", read(t, w.settings))
			}
		})
	}
	// No settings.json at all: one is created, with no backup (nothing to save).
	w := newWorld(t, "{}", "")
	os.Remove(w.settings)
	if out, rc := w.install(); rc != 0 {
		t.Fatalf("rc %d\n%s", rc, out)
	}
	if cur, _ := currentStatusLine(w.settings); !alreadyWrapped(cur.Command) || len(backups(t, w.settings)) != 0 {
		t.Error("settings.json was not created cleanly")
	}
}

func TestUninstallRemovesOnlyTheExactSourceLine(t *testing.T) {
	w := newWorld(t, "{}", "")
	w.install()
	c := w.ad.Paths.TmuxConf()
	os.WriteFile(w.tmuxConf, []byte("# source-file -q "+c+" (old)\nsource-file -q "+c+"\nset -g x y\n"), 0o644)
	w.uninstall()
	if got := read(t, w.tmuxConf); got != "# source-file -q "+c+" (old)\nset -g x y\n" {
		t.Errorf("tmux.conf = %q", got)
	}
	// The line is found in the XDG conf too, the file tmux reads when ~/.tmux.conf is absent.
	w = newWorld(t, "{}", "\x00")
	x := w.root + "/home/.config/tmux/tmux.conf"
	os.MkdirAll(filepath.Dir(x), 0o755)
	os.WriteFile(x, []byte("set -g mouse on\nsource-file -q "+w.ad.Paths.TmuxConf()+"\n"), 0o644)
	w.uninstall()
	if got := read(t, x); got != "set -g mouse on\n" {
		t.Errorf("xdg tmux.conf = %q", got)
	}
}

func TestApplyPluginSkippedWhenEnabledAndWhenNoClaude(t *testing.T) {
	w := newWorld(t, `{"enabledPlugins":{"bosun@bosun":true}}`, "")
	out, _ := w.install()
	if w.fr.has("plugin") || !strings.Contains(out, "Already done: plugin.") {
		t.Errorf("calls %v\n%s", w.fr.calls, out)
	}
	w = newWorld(t, "{}", "")
	w.env.ClaudeBin = ""
	out, rc := w.install()
	if rc == 0 || !strings.Contains(out, "`claude` is not on PATH") {
		t.Errorf("rc %d\n%s", rc, out)
	}
	// A failed install is reported, not hidden.
	w = newWorld(t, "{}", "")
	w.fr.fail["plugin install bosun --marketplace "+w.opt.Repo] = errors.New("boom")
	if out, rc := w.install(); rc == 0 || !strings.Contains(out, "plugin install: ") {
		t.Errorf("rc %d\n%s", rc, out)
	}
}

func TestApplyConfirmation(t *testing.T) {
	// Non-TTY stdin without --yes: plan printed, nothing changed.
	w := newWorld(t, userSettings, userTmux)
	w.install()
	wrapped, conf := read(t, w.settings), read(t, w.tmuxConf)
	w.fr.calls = nil
	w.env.Yes = false
	w.env.Stdin = strings.NewReader("y\n")
	out, rc := w.uninstall()
	if rc == 0 || !strings.Contains(out, "stdin is not a terminal") || read(t, w.settings) != wrapped || read(t, w.tmuxConf) != conf {
		t.Errorf("rc %d\n%s", rc, out)
	}
	if !strings.Contains(out, "Restore your original status line in") || !strings.Contains(out, "- source-file -q") || strings.Contains(out, "jq -r") {
		t.Errorf("plan not shown:\n%s", out)
	}
	// TTY: no answers decline, y applies.
	w.env.TTY = true
	w.env.Stdin = strings.NewReader("\n")
	out, rc = w.uninstall()
	if rc == 0 || !strings.Contains(out, "Apply? [y/N]") || read(t, w.settings) != wrapped {
		t.Errorf("declined run: rc %d\n%s", rc, out)
	}
	w.env.Stdin = strings.NewReader("y\n")
	if out, rc = w.uninstall(); rc != 0 || read(t, w.settings) == wrapped || read(t, w.tmuxConf) != userTmux {
		t.Errorf("accepted run: rc %d\n%s", rc, out)
	}
}

func TestSettingsEditKeepsTheFilesStyle(t *testing.T) {
	for name, body := range map[string]string{
		"tabs":       "{\n\t\"a\": 1\n}\n",
		"four":       "{\n    \"a\": 1\n}\n",
		"no newline": `{"a": 1}`,
	} {
		out, err := setStatusLineCommand([]byte(body), "x")
		if err != nil {
			t.Fatal(name, err)
		}
		back, err := removeStatusLine(out)
		if err != nil || string(back) != body {
			t.Errorf("%s: round trip\n%s\n=>\n%s", name, out, back)
		}
	}
	if _, err := setStatusLineCommand([]byte(`[1]`), "x"); err == nil {
		t.Error("a top-level array was accepted")
	}
	// An empty object: the new member goes one unit in from the closing brace's
	// indent, not at the brace's own column.
	got, err := setStatusLineCommand([]byte("{\n}\n"), "x")
	if want := "{\n  \"statusLine\": {\n    \"type\": \"command\",\n    \"command\": \"x\"\n  }\n}\n"; err != nil || string(got) != want {
		t.Errorf("empty object: got %q (%v), want %q", got, err, want)
	}
	if _, err := setStatusLineCommand([]byte(`{"statusLine": "x"}`), "x"); err == nil {
		t.Error("a string statusLine was accepted")
	}
}

// What re-sourcing cannot remove: keys and the hook in the live server.
func TestUninstallUnbindsOnlyBosunKeysAndHook(t *testing.T) {
	w := newWorld(t, "{}", "")
	w.opt.Config.Tmux.PopupKey, w.opt.Config.Tmux.JumpKey = "W", "a"
	w.fr.out["list-keys -T prefix"] = "bind-key -T prefix W display-popup -E -w 90% -h 60% -T ' bosun ' '/x/bosun ui'\n" +
		"bind-key -T prefix a run-shell -b 'echo mine'\n" + // rebound by the user
		"bind-key -T prefix c new-window\n"
	w.fr.out["show-hooks -g"] = "client-attached[42] run-shell -b \"/x/bosun status --catch-up\"\n"
	out, rc := w.uninstall()
	if rc != 0 {
		t.Fatalf("rc %d\n%s", rc, out)
	}
	if !w.fr.has("unbind-key -T prefix W") || !w.fr.has("set-hook -gu client-attached[42]") {
		t.Errorf("calls %v", w.fr.calls)
	}
	if w.fr.has("unbind-key -T prefix a") || w.fr.has("unbind-key -T prefix c") {
		t.Errorf("unbound a key that is not bosun's: %v", w.fr.calls)
	}
	if !strings.Contains(out, "Unbind prefix W and bosun's attach hook in the running tmux") || strings.Contains(out, "windows keep") {
		t.Errorf("plan or output:\n%s", out)
	}
	// A hook index holding someone else's command is left alone.
	w = newWorld(t, "{}", "")
	w.fr.out["show-hooks -g"] = "client-attached[42] run-shell 'echo theirs'\n"
	w.uninstall()
	if w.fr.has("set-hook") {
		t.Errorf("unset a hook that is not bosun's: %v", w.fr.calls)
	}
}

// The same against a private tmux server: install's conf binds the keys and the
// hook; uninstall --apply takes exactly those away.
func TestUninstallCleansALivePrivateServer(t *testing.T) {
	if os.Getenv("BOSUN_TEST_TMUX") == "" {
		t.Skip("set BOSUN_TEST_TMUX=1 to run against a private tmux server")
	}
	tmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("no tmux")
	}
	label := fmt.Sprintf("bosun-test-un%d", os.Getpid())
	run := func(args ...string) string {
		out, _ := exec.Command(tmux, append([]string{"-L", label, "-f", "/dev/null"}, args...)...).CombinedOutput()
		return string(out)
	}
	if out, err := exec.Command(tmux, "-L", label, "-f", "/dev/null", "new-session", "-d").CombinedOutput(); err != nil {
		t.Skipf("tmux: %v %s", err, out)
	}
	defer run("kill-server")
	sock := strings.TrimSpace(run("display", "-p", "#{socket_path}"))

	w := newWorld(t, "{}", "")
	w.env.TmuxBin, w.env.TmuxSocket, w.env.Runner = tmux, sock, runner.Exec{}
	w.opt.Config = config.Default()
	w.opt.Bosun = "/x/bosun"
	run("bind", "-T", "prefix", "z", "run-shell", "-b", "echo user-key")
	w.env.ClaudeBin = "" // only tmux is real here
	conf := w.ad.Paths.TmuxConf()
	text, err := tmuxconf.Generate(tmuxconf.Options{Version: "t", Bosun: "/x/bosun", JumpKey: w.opt.Config.Tmux.JumpKey, PopupKey: w.opt.Config.Tmux.PopupKey, SpendKey: w.opt.Config.Tmux.SpendKey})
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Dir(conf), 0o755)
	os.WriteFile(conf, []byte(text), 0o644)
	run("source-file", conf)
	if !strings.Contains(run("list-keys", "-T", "prefix"), "/x/bosun ui") || !strings.Contains(run("show-hooks", "-g"), "bosun status") {
		t.Fatalf("setup: the conf did not bind\n%s", run("list-keys", "-T", "prefix"))
	}
	out, _ := w.uninstall() // the plugin step is refused (no claude); the keys step is what is checked
	t.Log(out)
	keys, hooks := run("list-keys", "-T", "prefix"), run("show-hooks", "-g")
	if strings.Contains(keys, "/x/bosun") || strings.Contains(hooks, "bosun status") {
		t.Errorf("bosun left behind:\n%s\n%s", keys, hooks)
	}
	if !strings.Contains(keys, "user-key") {
		t.Error("the user's own key was unbound")
	}
}

// Lost update: claude plugin install rewrites settings.json between the plan and
// the status line write; the write must keep that, and the backup holds it.
func TestStatusLineStepKeepsWhatThePluginStepJustWrote(t *testing.T) {
	w := newWorld(t, "{\n  \"enabledPlugins\": {},\n  \"model\": \"opus\"\n}\n", "")
	if out, rc := w.install(); rc != 0 {
		t.Fatalf("rc %d\n%s", rc, out)
	}
	got := read(t, w.settings)
	if !strings.Contains(got, `"bosun@bosun": true`) || !strings.Contains(got, "exec bosun statusline") || !strings.Contains(got, `"model": "opus"`) {
		t.Errorf("a step overwrote another's change:\n%s", got)
	}
	if b := backups(t, w.settings); len(b) != 1 || !strings.Contains(read(t, b[0]), `"bosun@bosun": true`) {
		t.Errorf("backup is not of the content that was replaced: %v", b)
	}
	// The mirror: uninstall's restore and the plugin removal both stick.
	if out, rc := w.uninstall(); rc != 0 {
		t.Fatalf("uninstall rc %d\n%s", rc, out)
	}
	got = read(t, w.settings)
	if strings.Contains(got, "bosun") || !strings.Contains(got, `"model": "opus"`) {
		t.Errorf("after uninstall:\n%s", got)
	}
}

func TestStatusLineChangedUnderneathIsNotWritten(t *testing.T) {
	w := newWorld(t, userSettings, "")
	w.env.Yes = true
	steps := []Step{statusLineStep(w.ad, w.opt, w.env)}
	os.WriteFile(w.settings, []byte(strings.Replace(userSettings, "jq -r '.model.id'", "something else", 1)), 0o644)
	_, err := steps[0].Do()
	if err == nil || !strings.Contains(err.Error(), "changed since the plan") {
		t.Errorf("err = %v", err)
	}
	if strings.Contains(read(t, w.settings), "bosun") || len(backups(t, w.settings)) != 0 {
		t.Error("wrote over a status line the plan did not describe")
	}
}

func TestInstalledButDisabledPluginIsEnabledNotReinstalled(t *testing.T) {
	w := newWorld(t, "{}", "")
	os.MkdirAll(filepath.Dir(w.settings)+"/plugins", 0o755)
	os.WriteFile(filepath.Dir(w.settings)+"/plugins/installed_plugins.json", []byte(`{"version":2,"plugins":{"bosun@bosun":[]}}`), 0o644)
	out, rc := w.install()
	if rc != 0 || !w.fr.has("plugin enable bosun@bosun") || w.fr.has("plugin install") || w.fr.has("marketplace add") {
		t.Errorf("rc %d calls %v\n%s", rc, w.fr.calls, out)
	}
	if !strings.Contains(read(t, w.settings), `"bosun@bosun": true`) {
		t.Error("not enabled")
	}
}

func TestUninstallRemovesAnInstalledButDisabledPluginAndItsMarketplace(t *testing.T) {
	w := newWorld(t, "{}", "")
	dir := filepath.Dir(w.settings) + "/plugins"
	os.MkdirAll(dir, 0o755)
	os.WriteFile(dir+"/installed_plugins.json", []byte(`{"version":2,"plugins":{"bosun@bosun":[]}}`), 0o644)
	os.WriteFile(dir+"/known_marketplaces.json", []byte(`{"bosun":{},"other":{}}`), 0o644)
	out, rc := w.uninstall()
	if rc != 0 || !w.fr.has("plugin uninstall bosun@bosun") || !w.fr.has("plugin marketplace remove bosun") || strings.Contains(out, "Already done: plugin") {
		t.Errorf("rc %d calls %v\n%s", rc, w.fr.calls, out)
	}
	// Each separately: a marketplace left behind with nothing installed.
	w = newWorld(t, "{}", "")
	dir = filepath.Dir(w.settings) + "/plugins"
	os.MkdirAll(dir, 0o755)
	os.WriteFile(dir+"/known_marketplaces.json", []byte(`{"bosun":{}}`), 0o644)
	w.uninstall()
	if w.fr.has("plugin uninstall") || !w.fr.has("plugin marketplace remove bosun") {
		t.Errorf("calls %v", w.fr.calls)
	}
}
