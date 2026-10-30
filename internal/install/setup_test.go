package install

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jmvaldez/bosun/internal/config"
	"github.com/jmvaldez/bosun/internal/term"
)

func ptr(b bool) *bool { return &b }

func (w *world) setup(so SetupOptions) (string, int) {
	out, rc, _ := w.setupCancel(so)
	return out, rc
}

func (w *world) setupCancel(so SetupOptions) (string, int, bool) {
	w.opt.Config = config.Default()
	var out bytes.Buffer
	rc, cancelled := Setup(w.ad, w.opt, w.env, so, &out)
	return out.String(), rc, cancelled
}

func TestSetupYesInstallsWrapsAndLoadsTmuxWithoutTouchingTheUsersTmuxConf(t *testing.T) {
	w := newWorld(t, userSettings, userTmux)
	out, rc := w.setup(SetupOptions{Answer: ptr(true)})
	if rc != 0 {
		t.Fatalf("rc %d\n%s", rc, out)
	}
	for _, want := range []string{"\u2713 Claude Code plugin installed", "\u2713 tmux configured", "\u2713 live cost on", "backup: "} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if !w.fr.has("plugin install bosun --marketplace "+w.opt.Repo) || !w.fr.has("source-file "+w.ad.Paths.TmuxConf()) {
		t.Errorf("calls %v", w.fr.calls)
	}
	if read(t, w.tmuxConf) != userTmux {
		t.Errorf("the user's tmux conf was edited:\n%s", read(t, w.tmuxConf))
	}
	if !LiveCostOn(w.ad, w.opt) {
		t.Error("live cost should be on")
	}
	if !strings.Contains(read(t, w.ad.Paths.TmuxConf()), "set -g @bosun-version '0.1.0'") {
		t.Error("the generated conf does not record its version")
	}
	if strings.Contains(out, "\ufe0f") {
		t.Error("output carries a variation selector")
	}
}

func TestSetupNoRemovesTheWrapAndRestoresTheOriginal(t *testing.T) {
	w := newWorld(t, userSettings, userTmux)
	if out, rc := w.setup(SetupOptions{Answer: ptr(true)}); rc != 0 {
		t.Fatalf("yes: rc %d\n%s", rc, out)
	}
	out, rc := w.setup(SetupOptions{Answer: ptr(false)})
	if rc != 0 || !strings.Contains(out, "\u2713 live cost off") {
		t.Fatalf("rc %d\n%s", rc, out)
	}
	if LiveCostOn(w.ad, w.opt) {
		t.Error("live cost should be off")
	}
	if cur, _ := currentStatusLine(w.settings); cur.Command != "jq -r '.model.id'" {
		t.Errorf("command = %q, want the original back", cur.Command)
	}
	// Idempotent: choosing No again changes nothing and still succeeds.
	if _, rc := w.setup(SetupOptions{Answer: ptr(false)}); rc != 0 {
		t.Errorf("second no: rc %d", rc)
	}
}

func TestSetupAsksWithTheRightDefault(t *testing.T) {
	w := newWorld(t, userSettings, userTmux)
	var got []bool
	ask := func(yes bool) func(bool) (bool, error) {
		return func(def bool) (bool, error) { got = append(got, def); return yes, nil }
	}
	w.setup(SetupOptions{Ask: ask(false)}) // first run: Yes is highlighted, the answer is No
	w.setup(SetupOptions{Ask: ask(true)})  // set up, live cost off: No is highlighted
	w.setup(SetupOptions{Ask: ask(true)})  // live cost on now: Yes is highlighted
	if len(got) != 3 || !got[0] || got[1] || !got[2] {
		t.Errorf("defaults = %v, want [true false true]", got)
	}
}

func TestSetupRecordsTheAnswerOnlyWhenItAsked(t *testing.T) {
	w := newWorld(t, userSettings, userTmux)
	ask := func(yes bool) SetupOptions {
		return SetupOptions{Ask: func(bool) (bool, error) { return yes, nil }}
	}
	for _, tt := range []struct {
		so   SetupOptions
		want string
	}{{ask(true), "Show live session cost? Yes\n"}, {ask(false), "Show live session cost? No\n"}} {
		out, rc, cancelled := w.setupCancel(tt.so)
		if !strings.HasPrefix(out, tt.want) || rc != 0 || cancelled {
			t.Errorf("asked: rc %d cancelled %v, want a first line %q:\n%s", rc, cancelled, tt.want, out)
		}
	}
	// With colour only the answer is bold.
	w.env.Color = true
	if out, _ := w.setup(ask(true)); !strings.HasPrefix(out, "Show live session cost? \x1b[1mYes\x1b[0m\n") {
		t.Errorf("colour: %q", out)
	}
	for _, a := range []bool{true, false} {
		if out, _ := w.setup(SetupOptions{Answer: ptr(a)}); strings.Contains(out, LiveCostQuestion) {
			t.Errorf("a flag answer left a record line:\n%s", out)
		}
	}
}

func TestSetupCancelledChangesNothing(t *testing.T) {
	w := newWorld(t, userSettings, userTmux)
	out, rc, cancelled := w.setupCancel(SetupOptions{Ask: func(bool) (bool, error) { return false, ErrAborted }})
	if rc != 1 || !cancelled || strings.TrimSpace(out) != "Nothing changed." || len(w.fr.calls) != 0 || read(t, w.settings) != userSettings {
		t.Errorf("rc %d calls %v\n%s", rc, w.fr.calls, out)
	}
	if _, err := os.Stat(w.ad.Paths.TmuxConf()); err == nil {
		t.Error("bosun's tmux conf was written before the question was answered")
	}
}

func TestSetupWithNoTerminalAndNoFlagDoesNotAsk(t *testing.T) {
	w := newWorld(t, userSettings, userTmux)
	out, rc := w.setup(SetupOptions{})
	if rc != 2 || strings.TrimSpace(out) != NotInteractive || len(w.fr.calls) != 0 || read(t, w.settings) != userSettings {
		t.Errorf("not set up: rc %d\n%s", rc, out)
	}
	// Already set up: the same note, but nothing for a script to react to.
	w = newWorld(t, `{"enabledPlugins":{"bosun@bosun":true}}`, userTmux)
	if out, rc := w.setup(SetupOptions{}); rc != 0 || strings.TrimSpace(out) != NotInteractive {
		t.Errorf("set up: rc %d\n%s", rc, out)
	}
}

func TestSetupReportsAFailedPlugin(t *testing.T) {
	w := newWorld(t, userSettings, userTmux)
	w.fr.fail["plugin install bosun --marketplace "+w.opt.Repo] = errors.New("boom")
	out, rc := w.setup(SetupOptions{Answer: ptr(true)})
	if rc != 1 || !strings.Contains(out, "! plugin: plugin install: boom") {
		t.Errorf("rc %d\n%s", rc, out)
	}
	w.env.ClaudeBin = ""
	if out, rc := w.setup(SetupOptions{Answer: ptr(true)}); rc != 1 || !strings.Contains(out, "`claude` is not on PATH") {
		t.Errorf("rc %d\n%s", rc, out)
	}
}

func TestSetupOutsideTmuxWithNoServerSaysWhenItLoads(t *testing.T) {
	w := newWorld(t, userSettings, userTmux)
	w.env.InTmux = false
	w.fr.fail["list-sessions"] = errors.New("no server")
	out, rc := w.setup(SetupOptions{Answer: ptr(true)})
	if rc != 0 || !strings.Contains(out, "\u2713 tmux configured (it loads when a Claude Code session starts in tmux)") || w.fr.has("source-file") {
		t.Errorf("rc %d calls %v\n%s", rc, w.fr.calls, out)
	}
}

// The SessionStart hook's child sources the conf only when the server has not
// loaded this version (D-31).
func TestSyncTmuxSourcesOnlyWhenTheServerIsBehind(t *testing.T) {
	w := newWorld(t, "{}", "\x00")
	w.opt.Config = config.Default()
	Setup(w.ad, w.opt, w.env, SetupOptions{Answer: ptr(true)}, &bytes.Buffer{})
	w.fr.calls = nil

	w.fr.out["show-options -gv @bosun-version"] = "0.1.0\n"
	SyncTmux(w.ad, w.opt, w.env)
	if w.fr.has("source-file") {
		t.Errorf("a current server was reloaded: %v", w.fr.calls)
	}

	w.fr.out["show-options -gv @bosun-version"] = "" // unset: a fresh server
	SyncTmux(w.ad, w.opt, w.env)
	if !w.fr.has("source-file " + w.ad.Paths.TmuxConf()) {
		t.Errorf("a fresh server was not loaded: %v", w.fr.calls)
	}

	w.fr.calls = nil
	w.fr.out["show-options -gv @bosun-version"] = "0.0.9\n" // left behind by an upgrade
	w.opt.Version = "0.1.0"
	SyncTmux(w.ad, w.opt, w.env)
	if !w.fr.has("source-file") {
		t.Errorf("an old server was not reloaded: %v", w.fr.calls)
	}

	// Outside tmux there is no server to ask.
	w.fr.calls, w.env.InTmux = nil, false
	SyncTmux(w.ad, w.opt, w.env)
	if len(w.fr.calls) != 0 {
		t.Errorf("tmux was called outside tmux: %v", w.fr.calls)
	}
}

// ---- the question ----

func keys(ks ...term.Key) KeyReader {
	return func() (term.Key, error) {
		if len(ks) == 0 {
			return "", errors.New("eof")
		}
		k := ks[0]
		ks = ks[1:]
		return k, nil
	}
}

func TestPromptAnswers(t *testing.T) {
	for _, tt := range []struct {
		name string
		def  bool
		keys []term.Key
		want bool
		err  error
	}{
		{"enter takes the default yes", true, []term.Key{"enter"}, true, nil},
		{"enter takes the default no", false, []term.Key{"enter"}, false, nil},
		{"down then enter", true, []term.Key{"down", "enter"}, false, nil},
		{"up then enter", false, []term.Key{"up", "enter"}, true, nil},
		{"down, down, up", true, []term.Key{"down", "down", "up", "enter"}, true, nil},
		{"y answers at once", false, []term.Key{"y"}, true, nil},
		{"N answers at once", true, []term.Key{"N"}, false, nil},
		{"unrelated keys are ignored", true, []term.Key{"x", "left", "enter"}, true, nil},
		{"esc cancels", true, []term.Key{"esc"}, false, ErrAborted},
		{"ctrl-c cancels", true, []term.Key{"ctrl-c"}, false, ErrAborted},
		{"closed input cancels", true, nil, false, ErrAborted},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			got, err := Prompt(keys(tt.keys...), &out, tt.def, false)
			if got != tt.want || !errors.Is(err, tt.err) {
				t.Errorf("got %v, %v; want %v, %v", got, err, tt.want, tt.err)
			}
			if !strings.HasSuffix(out.String(), "\x1b[?25h") {
				t.Errorf("the cursor is left hidden: %q", out.String())
			}
		})
	}
}

func TestSetupScreenText(t *testing.T) {
	want := strings.Join([]string{
		"  bosun setup",
		"",
		"  Show live session cost?",
		"  bosun reads your Claude Code status line to get it.",
		"",
		"  \u203a Yes   exact cost as sessions run",
		"          (wraps your status line, reversible)",
		"    No    running sessions show \u2014, cost appears when they end",
		"",
		"  \u2191/\u2193 select \u00b7 enter confirm",
	}, "\n")
	if got := strings.Join(setupScreen(true, false), "\n"); got != want {
		t.Errorf("screen:\n%s\nwant:\n%s", got, want)
	}
	if got := strings.Join(setupScreen(false, false), "\n"); !strings.Contains(got, "\n  \u203a No ") || strings.Contains(got, "\u203a Yes") {
		t.Errorf("no highlighted:\n%s", got)
	}
	for _, l := range setupScreen(true, true) {
		if strings.ContainsRune(l, '\ufe0f') {
			t.Errorf("variation selector in %q", l)
		}
	}
}
