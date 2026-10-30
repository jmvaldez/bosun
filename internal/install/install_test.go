package install

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmvaldez/bosun/internal/config"
	"github.com/jmvaldez/bosun/internal/paths"
	"github.com/jmvaldez/bosun/internal/platform"
)

type fakeID struct {
	key string
	err error
}

func (f fakeID) Account() (string, error) { return f.key, f.err }

type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC) }

func setup(t *testing.T) (platform.Adapters, string) {
	root := t.TempDir()
	return platform.Adapters{
		Paths:    paths.Paths{Config: root + "/config", State: root + "/state", Cache: root + "/cache", Data: root + "/data", Claude: root + "/claude"},
		Identity: fakeID{key: "me@example.com"},
		Clock:    fixedClock{},
	}, root
}

func writeSettings(t *testing.T, ad platform.Adapters, body string) string {
	t.Helper()
	p := ad.Paths.ClaudeSettings()
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte(body), 0o644)
	return p
}

func TestWrapCommand(t *testing.T) {
	tests := []struct{ in, want string }{
		{"FOO=1 /home/me/sl.sh", "if command -v bosun >/dev/null; then exec bosun statusline -- sh -c 'FOO=1 /home/me/sl.sh'; else exec sh -c 'FOO=1 /home/me/sl.sh'; fi"},
		{"/home/me/sl.sh --mode=short", "if command -v bosun >/dev/null; then exec bosun statusline -- /home/me/sl.sh --mode=short; else exec /home/me/sl.sh --mode=short; fi"},
		{"", "if command -v bosun >/dev/null; then exec bosun statusline; fi"},
		{"~/.claude/sl.sh", "if command -v bosun >/dev/null; then exec bosun statusline -- sh -c '~/.claude/sl.sh'; else exec sh -c '~/.claude/sl.sh'; fi"},
		{"/home/me/sl.sh --short", "if command -v bosun >/dev/null; then exec bosun statusline -- /home/me/sl.sh --short; else exec /home/me/sl.sh --short; fi"},
		{`input=$(cat); echo "$input" | jq -r '.model.id'`, `if command -v bosun >/dev/null; then exec bosun statusline -- sh -c 'input=$(cat); echo "$input" | jq -r '\''.model.id'\'''; else exec sh -c 'input=$(cat); echo "$input" | jq -r '\''.model.id'\'''; fi`},
	}
	for _, tt := range tests {
		if got := WrapCommand(tt.in); got != tt.want {
			t.Errorf("WrapCommand(%q)\n got %s\nwant %s", tt.in, got, tt.want)
		}
	}
}

// The wrapped form must still behave like the original under a real shell,
// including when bosun is missing (the else branch) and for a pipeline.
func TestWrapCommandRunsUnderShell(t *testing.T) {
	for _, tt := range []struct{ orig, in, want string }{
		{`cat | tr a-z A-Z`, "hello", "HELLO"},
		// An env prefix is not a command word: `exec FOO=1 cmd` would fail.
		{`FOO=bar printenv FOO`, "", "bar\n"},
	} {
		runUnderShell(t, tt.orig, tt.in, tt.want)
	}
}

func runUnderShell(t *testing.T, orig, in, want string) {
	t.Helper()
	for _, withBosun := range []bool{false, true} {
		dir := t.TempDir()
		if withBosun {
			// A stand-in bosun that honors `statusline -- CMD...` by running CMD.
			os.WriteFile(filepath.Join(dir, "bosun"), []byte("#!/bin/sh\nshift 2\nexec \"$@\"\n"), 0o755)
		}
		cmd := exec.Command("/bin/sh", "-c", WrapCommand(orig))
		cmd.Env = []string{"PATH=" + dir + ":/usr/bin:/bin"}
		cmd.Stdin = strings.NewReader(in)
		out, err := cmd.Output()
		if err != nil || string(out) != want {
			t.Errorf("%q withBosun=%v: out=%q err=%v", orig, withBosun, out, err)
		}
	}
}

func TestShellQuoteRoundTrips(t *testing.T) {
	for _, s := range []string{`it's`, `a"b`, `$HOME && rm`, "line\nbreak", "", "/home/me/my repo", "a;b", "a*b", "~/x", "a\tb", "a|b", "a`b`", "#c", "a\\b"} {
		out, err := exec.Command("/bin/sh", "-c", "printf %s "+shellQuote(s)).Output()
		if err != nil || string(out) != s {
			t.Errorf("%q round-tripped to %q (%v)", s, out, err)
		}
	}
}

func TestInstallWritesOnlyOwnedFilesAndPrintsEdits(t *testing.T) {
	ad, root := setup(t)
	settings := writeSettings(t, ad, `{"statusLine":{"type":"command","command":"jq -r .model.id"},"permissions":{}}`)
	before, _ := os.ReadFile(settings)
	repo := filepath.Join(root, "repo")
	os.MkdirAll(filepath.Join(repo, ".claude-plugin"), 0o755)
	os.WriteFile(filepath.Join(repo, ".claude-plugin/marketplace.json"), []byte("{}"), 0o644)

	var out bytes.Buffer
	if code := Run(ad, Options{SearchDirs: []string{filepath.Join(repo, "bin")}}, &out); code != 0 {
		t.Fatalf("code %d\n%s", code, out.String())
	}
	text := out.String()
	for _, want := range []string{
		"claude plugin marketplace add " + repo,
		"claude plugin install bosun@bosun",
		"exec bosun statusline -- jq -r .model.id; else exec jq -r .model.id; fi",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
	if after, _ := os.ReadFile(settings); !bytes.Equal(before, after) {
		t.Error("install modified settings.json")
	}

	// Only bosun-owned files appeared under the bosun dirs.
	var written []string
	for _, dir := range []string{ad.Paths.Config, ad.Paths.State, ad.Paths.Cache, ad.Paths.Data} {
		filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				written = append(written, strings.TrimPrefix(p, root))
			}
			return nil
		})
	}
	want := map[string]bool{"/config/config.toml": true, "/state/identity.json": true}
	for _, w := range written {
		if !want[w] {
			t.Errorf("unexpected file %s", w)
		}
		delete(want, w)
	}
	if len(want) != 0 {
		t.Errorf("missing files %v", want)
	}
	// The generated config is inert: every key commented, still parses.
	cfg, res, err := config.Load(ad.Paths.ConfigFile())
	if err != nil || len(res.Unknown)+len(res.Problems) != 0 {
		t.Fatalf("template does not load cleanly: %v %+v", err, res)
	}
	if cfg.Rules.Quiet != config.Default().Rules.Quiet {
		t.Error("template changed a default")
	}
}

func TestInstallKeepsExistingConfig(t *testing.T) {
	ad, _ := setup(t)
	os.MkdirAll(ad.Paths.Config, 0o755)
	os.WriteFile(ad.Paths.ConfigFile(), []byte("[ui]\npeek = false\n"), 0o600)
	var out bytes.Buffer
	Run(ad, Options{}, &out)
	b, _ := os.ReadFile(ad.Paths.ConfigFile())
	if string(b) != "[ui]\npeek = false\n" {
		t.Errorf("config overwritten: %q", b)
	}
	if !strings.Contains(out.String(), "kept") {
		t.Errorf("output: %s", out.String())
	}
}

func TestInstallRefusesToDoubleWrap(t *testing.T) {
	ad, _ := setup(t)
	writeSettings(t, ad, `{"statusLine":{"type":"command","command":"if command -v bosun >/dev/null; then exec bosun statusline -- jq .; else exec jq .; fi"}}`)
	var out bytes.Buffer
	Run(ad, Options{}, &out)
	if !strings.Contains(out.String(), "already wraps") || strings.Contains(out.String(), `"command":`) {
		t.Errorf("output:\n%s", out.String())
	}
}

func TestInstallToleratesMissingSettingsAndIdentityFailure(t *testing.T) {
	ad, _ := setup(t)
	ad.Identity = fakeID{err: errors.New("claude auth status failed")}
	var out bytes.Buffer
	if code := Run(ad, Options{}, &out); code != 0 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(out.String(), "skipped identity.json") || !strings.Contains(out.String(), "Could not read") {
		t.Errorf("output:\n%s", out.String())
	}
	if _, err := os.Stat(ad.Paths.Identity()); !os.IsNotExist(err) {
		t.Error("identity.json written despite failure")
	}
}

func TestInstallNoHome(t *testing.T) {
	var out bytes.Buffer
	ad := platform.Adapters{Clock: fixedClock{}}
	if code := Run(ad, Options{}, &out); code == 0 {
		t.Error("want nonzero when no dirs resolve")
	}
}

const userTmuxConf = "set -g prefix C-a\nset -g @plugin 'jimeh/tmux-themepack'\n\n# KEEP THIS AT THE END\nrun '~/.tmux/plugins/tpm/tpm'\n"

func TestInstallWritesTmuxConfAndPrintsTheOneLine(t *testing.T) {
	ad, root := setup(t)
	userConf := filepath.Join(root, "tmux.conf")
	os.WriteFile(userConf, []byte(userTmuxConf), 0o644)
	var out bytes.Buffer
	opt := Options{Bosun: "/home/me/.local/bin/bosun", Version: "0.1.0", TmuxConf: userConf, Config: config.Default()}
	if code := Run(ad, opt, &out); code != 0 {
		t.Fatalf("code %d\n%s", code, out.String())
	}
	conf, err := os.ReadFile(ad.Paths.TmuxConf())
	if err != nil || !strings.HasPrefix(string(conf), "# bosun 0.1.0\n") || !strings.Contains(string(conf), "/home/me/.local/bin/bosun jump") {
		t.Fatalf("conf = %q err=%v", conf, err)
	}
	text := out.String()
	for _, want := range []string{"source-file -q " + ad.Paths.TmuxConf(), "after the tpm `run` line (line 5)"} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
	if b, _ := os.ReadFile(userConf); string(b) != userTmuxConf {
		t.Error("install modified the user's tmux.conf")
	}
	// Once the user has added the line, install says so and stops asking.
	os.WriteFile(userConf, []byte(userTmuxConf+"source-file -q "+ad.Paths.TmuxConf()+"\n"), 0o644)
	out.Reset()
	Run(ad, opt, &out)
	if !strings.Contains(out.String(), "already sources") {
		t.Errorf("output:\n%s", out.String())
	}
}

func TestInstallRefusesUnquotableBosunPath(t *testing.T) {
	ad, _ := setup(t)
	var out bytes.Buffer
	if code := Run(ad, Options{Bosun: "/tmp/it's/bosun", Config: config.Default()}, &out); code == 0 {
		t.Error("want nonzero")
	}
	if _, err := os.Stat(ad.Paths.TmuxConf()); !os.IsNotExist(err) {
		t.Error("conf written for an unquotable path")
	}
}

func TestUnwrapCommandRoundTrips(t *testing.T) {
	for _, orig := range []string{
		"~/.claude/sl.sh",
		"/home/me/sl.sh --short",
		`input=$(cat); echo "$input" | jq -r '.model.id'`,
		"it's a test | cat",
	} {
		got, ok := UnwrapCommand(WrapCommand(orig))
		if !ok || got != orig {
			t.Errorf("UnwrapCommand(WrapCommand(%q)) = %q, %v", orig, got, ok)
		}
	}
	if _, ok := UnwrapCommand("jq -r .model.id"); ok {
		t.Error("a command that is not bosun's wrapper was accepted")
	}
	if got, ok := UnwrapCommand(WrapCommand("")); !ok || got != "" {
		t.Errorf("wrapped around nothing: %q %v", got, ok)
	}
}
