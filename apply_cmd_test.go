package main

import (
	"bytes"
	"github.com/jmvaldez/bosun/internal/paths"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shims puts fake claude and tmux first on PATH. Each logs its arguments, so
// the test sees what would have run and nothing real is reached.
func shims(t *testing.T, env []string, root string) (log string) {
	t.Helper()
	dir := filepath.Join(root, "shims")
	os.MkdirAll(dir, 0o755)
	log = filepath.Join(root, "shim.log")
	// tmux must answer source-file; claude must accept plugin commands.
	os.WriteFile(filepath.Join(dir, "tmux"), []byte("#!/bin/sh\necho \"tmux $*\" >> '"+log+"'\n[ \"$1\" = -S ] && shift 2\n[ \"$1\" = source-file ] && exit 0\nexit 1\n"), 0o755)
	os.WriteFile(filepath.Join(dir, "claude"), []byte(claudeShim(log)), 0o755)
	for i, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			env[i] = "PATH=" + dir + ":/usr/bin:/bin"
		}
	}
	return log
}

// oldRouteInstall leaves HOME as the removed `install --apply` did: `setup`
// for the plugin and status line, plus the source-file line that only the old
// route added to ~/.tmux.conf. Uninstall must still undo all of it.
func oldRouteInstall(t *testing.T, env []string, root, repo string) {
	t.Helper()
	if r := runBosun(t, env, strings.NewReader(""), "setup", "--yes", "--repo", repo); r.code != 0 {
		t.Fatalf("setup: exit %d\n%s", r.code, r.out)
	}
	conf := root + "/home/.tmux.conf"
	b, _ := os.ReadFile(conf)
	text := string(b)
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	line := "source-file -q " + root + "/data/tmux.conf\n"
	if i := strings.Index(text, "tpm/tpm'\n"); i >= 0 {
		i += len("tpm/tpm'\n")
		text = text[:i] + line + text[i:]
	} else {
		text += line
	}
	if err := os.WriteFile(conf, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// install --apply and its flags are gone; the message points at setup.
func TestInstallApplyWasRemoved(t *testing.T) {
	env, root := isolated(t)
	home := root + "/home"
	os.MkdirAll(home+"/.claude", 0o755)
	const orig = "{\n  \"statusLine\": {\n    \"type\": \"command\",\n    \"command\": \"echo hi\"\n  }\n}\n"
	os.WriteFile(home+"/.claude/settings.json", []byte(orig), 0o644)
	for _, flag := range []string{"--apply", "--yes", "--show-diff", "-v"} {
		var out bytes.Buffer
		a := &app{out: &out, errw: &out}
		want := "bosun install: " + flag + " was removed; run `bosun setup`"
		if rc := a.install([]string{flag}); rc != 2 || strings.TrimSpace(out.String()) != want {
			t.Errorf("install %s: exit %d, want 2 and %q\n%s", flag, rc, want, out.String())
		}
	}
	r := runBosun(t, env, strings.NewReader(""), "install", "--apply", "--yes", "--repo", ".")
	if r.code != 2 {
		t.Errorf("install --apply --yes: exit %d\n%s", r.code, r.out)
	}
	if b, _ := os.ReadFile(home + "/.claude/settings.json"); string(b) != orig {
		t.Error("settings.json changed")
	}
	if _, err := os.Stat(root + "/data/tmux.conf"); err == nil {
		t.Error("a rejected install still wrote bosun's files")
	}
}

func TestUninstallApplyReversesSetupAndTheOldInstall(t *testing.T) {
	env, root := isolated(t)
	log := shims(t, env, root)
	home := root + "/home"
	os.MkdirAll(home+"/.claude", 0o755)
	settings := home + "/.claude/settings.json"
	const orig = "{\n  \"statusLine\": {\n    \"type\": \"command\",\n    \"command\": \"echo hi\"\n  }\n}\n"
	const userTmux = "run '~/.tmux/plugins/tpm/tpm'\n"
	os.WriteFile(settings, []byte(orig), 0o644)
	os.WriteFile(home+"/.tmux.conf", []byte(userTmux), 0o644)
	repo, _ := filepath.Abs(".")
	oldRouteInstall(t, env, root, repo)
	if b, _ := os.ReadFile(home + "/.tmux.conf"); !strings.Contains(string(b), "source-file -q "+root+"/data/tmux.conf") {
		t.Fatalf("tmux.conf = %s", b)
	}

	// Without --yes and without a terminal: refused, nothing changed.
	wrapped, _ := os.ReadFile(settings)
	r := runBosun(t, env, strings.NewReader(""), "uninstall", "--apply")
	if !strings.Contains(string(r.out), "stdin is not a terminal") {
		t.Fatalf("exit %d\n%s", r.code, r.out)
	}
	if b, _ := os.ReadFile(settings); string(b) != string(wrapped) {
		t.Fatal("settings.json changed without confirmation")
	}

	r = runBosun(t, env, strings.NewReader(""), "uninstall", "--apply", "--yes")
	if r.code != 0 {
		t.Fatalf("uninstall exit %d\n%s", r.code, r.out)
	}
	b, _ := os.ReadFile(settings)
	if !strings.Contains(string(b), `"command": "echo hi"`) || strings.Contains(string(b), "bosun") {
		t.Errorf("settings after uninstall = %s", b)
	}
	conf, _ := os.ReadFile(home + "/.tmux.conf")
	if string(conf) != userTmux {
		t.Errorf("tmux.conf after uninstall = %q", conf)
	}
	l, _ := os.ReadFile(log)
	if !strings.Contains(string(l), "claude plugin uninstall bosun@bosun") {
		t.Errorf("shim log:\n%s", l)
	}
}

func TestYesNeedsApply(t *testing.T) {
	env, _ := isolated(t)
	for _, c := range []string{"install", "uninstall"} {
		if r := runBosun(t, env, nil, c, "--yes"); r.code != 2 {
			t.Errorf("%s --yes without --apply exited %d", c, r.code)
		}
	}
}

// A refused or declined confirmation changes nothing, --purge included.
func TestUninstallApplyPurgeWaitsForTheYes(t *testing.T) {
	env, root := isolated(t)
	shims(t, env, root)
	os.MkdirAll(root+"/home", 0o755)
	repo, _ := filepath.Abs(".")
	oldRouteInstall(t, env, root, repo)
	for _, d := range []string{"state", "data", "config"} {
		os.MkdirAll(root+"/"+d, 0o700) // purge needs the dirs marked as bosun's
		os.WriteFile(root+"/"+d+"/"+paths.MarkerName, nil, 0o600)
	}
	r := runBosun(t, env, strings.NewReader(""), "uninstall", "--apply", "--purge", "--purge-config")
	for _, d := range []string{"state", "data", "config"} {
		if _, err := os.Stat(root + "/" + d); err != nil {
			t.Errorf("%s deleted without a confirmation:\n%s", d, r.out)
		}
	}
	if !strings.Contains(string(r.out), "Delete ") {
		t.Errorf("purge is not in the plan:\n%s", r.out)
	}
	if r := runBosun(t, env, strings.NewReader(""), "uninstall", "--apply", "--yes", "--purge", "--purge-config"); r.code != 0 {
		t.Fatalf("uninstall --yes: %d\n%s", r.code, r.out)
	}
	for _, d := range []string{"state", "data", "config"} {
		if _, err := os.Stat(root + "/" + d); err == nil {
			t.Errorf("%s survived --yes --purge", d)
		}
	}
}

// claudeShim models the real CLI's bookkeeping: plugin install/enable/uninstall
// edit $HOME/.claude/settings.json as text and keep installed_plugins.json and
// known_marketplaces.json.
func claudeShim(log string) string {
	return `#!/bin/sh
echo "claude $*" >> '` + log + `'
[ "$1" = plugin ] || exit 1
D="$HOME/.claude"; S="$D/settings.json"; P="$D/plugins"
mkdir -p "$P"
enable() {
  if [ ! -f "$S" ]; then printf '{\n  "enabledPlugins": {"bosun@bosun": true}\n}\n' > "$S"
  elif grep -q enabledPlugins "$S"; then sed -i 's/"enabledPlugins": *{[^}]*}/"enabledPlugins": {"bosun@bosun": true}/' "$S"
  else sed -i '0,/{/s/{/{\n  "enabledPlugins": {"bosun@bosun": true},/' "$S"; fi
}
case "$2 $3" in
  "marketplace add") echo '{"bosun":{}}' > "$P/known_marketplaces.json"; exit 0;;
  "marketplace remove") echo '{}' > "$P/known_marketplaces.json"; exit 0;;
esac
case "$2" in
  install) echo '{"version":2,"plugins":{"bosun@bosun":[]}}' > "$P/installed_plugins.json"; echo '{"bosun":{}}' > "$P/known_marketplaces.json"; enable;;
  enable) enable;;
  uninstall) echo '{"version":2,"plugins":{}}' > "$P/installed_plugins.json"
    [ -f "$S" ] && sed -i 's/"bosun@bosun": *true//' "$S";;
esac
exit 0
`
}

func TestFreshMachineSetupDoctorUninstallRoundTrip(t *testing.T) {
	repo, _ := filepath.Abs(".")
	for _, tc := range []struct {
		name               string
		settings, tmuxConf string // empty: the file does not exist
	}{
		{"empty HOME", "", ""},
		{"existing files", "{\n  \"enabledPlugins\": {},\n  \"statusLine\": {\"type\": \"command\", \"command\": \"echo hi\"}\n}\n",
			"set -g mouse on\nrun '~/.tmux/plugins/tpm/tpm'\nbind r source-file ~/.tmux.conf\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, root := isolated(t)
			shims(t, env, root)
			home := root + "/home"
			os.MkdirAll(home, 0o755)
			settings, conf := home+"/.claude/settings.json", home+"/.tmux.conf"
			if tc.settings != "" {
				os.MkdirAll(home+"/.claude", 0o755)
				os.WriteFile(settings, []byte(tc.settings), 0o644)
			}
			if tc.tmuxConf != "" {
				os.WriteFile(conf, []byte(tc.tmuxConf), 0o644)
			}
			oldRouteInstall(t, env, root, repo)
			r := runBosun(t, env, nil, "doctor")
			for _, want := range []string{"OK    plugin", "OK    statusline"} {
				if !strings.Contains(string(r.out), want) {
					t.Errorf("doctor lacks %q:\n%s", want, r.out)
				}
			}
			for _, d := range []string{"state", "data", "config"} {
				os.MkdirAll(root+"/"+d, 0o700)
				os.WriteFile(root+"/"+d+"/"+paths.MarkerName, nil, 0o600)
			}
			r = runBosun(t, env, strings.NewReader(""), "uninstall", "--apply", "--yes", "--purge", "--purge-config")
			if r.code != 0 || strings.Contains(string(r.out), "\x1b[") {
				t.Fatalf("uninstall: exit %d\n%s", r.code, r.out)
			}
			sb, _ := os.ReadFile(settings)
			cb, _ := os.ReadFile(conf)
			if tc.settings != "" && string(sb) != tc.settings {
				t.Errorf("settings.json not restored:\n%s", sb)
			}
			if tc.tmuxConf != "" && string(cb) != tc.tmuxConf {
				t.Errorf("tmux.conf not restored:\n%s", cb)
			}
			if tc.settings == "" && (strings.Contains(string(sb), "bosun") || strings.Contains(string(sb), "statusLine")) {
				t.Errorf("traces in settings.json:\n%s", sb)
			}
			if strings.Contains(string(cb), "source-file -q") {
				t.Errorf("source line left in tmux.conf:\n%s", cb)
			}
			for _, d := range []string{"state", "data", "config"} {
				if _, err := os.Stat(root + "/" + d); err == nil {
					t.Errorf("%s survived the purge", d)
				}
			}
		})
	}
}

// Bold follows stdout being a terminal, not stdin and not a pipe.
func TestApplyOutputHasNoEscapesWhenPiped(t *testing.T) {
	env, root := isolated(t)
	shims(t, env, root)
	for _, args := range [][]string{{"uninstall", "--apply"}} {
		r := runBosun(t, env, strings.NewReader(""), args...)
		if strings.Contains(string(r.out), "\x1b") {
			t.Errorf("%v printed escapes to a pipe:\n%q", args, r.out)
		}
	}
}
