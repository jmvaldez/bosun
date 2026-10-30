package install

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jmvaldez/bosun/internal/platform"
	"github.com/jmvaldez/bosun/internal/plugstate"
	"github.com/jmvaldez/bosun/internal/tmuxconf"
)

// D-31: `bosun setup` is the first-run flow. It asks one question, whether to
// show live session cost, and does the rest without a plan or a second prompt:
// the plugin, the status line wrap if the answer is yes, and bosun's own tmux
// conf, loaded into the running tmux. The user's tmux config is never edited.

// NotInteractive is what setup prints when it has no terminal to ask on and no
// --yes or --no to answer for it.
const NotInteractive = "run `bosun setup` in a terminal"

const noServerNote = "no tmux server is running; it loads the config when it starts"

// SetupOptions says how setup gets its one answer.
type SetupOptions struct {
	// Answer is the --yes (true) or --no (false) answer; nil means ask.
	Answer *bool
	// Ask shows the question with def highlighted: Yes on a first run, else the
	// current state. Nil means there is no terminal to ask on.
	Ask func(def bool) (bool, error)
}

// LiveCostOn reports whether settings.json's statusLine is bosun's wrap, which
// is what live cost on means; nothing else stores the choice.
func LiveCostOn(ad platform.Adapters, opt Options) bool {
	return plugstate.LiveCost(settingsPath(ad, opt))
}

// SetUp reports whether the plugin is installed and enabled, which is what
// separates a first run from a later one.
func SetUp(ad platform.Adapters, opt Options) bool {
	return pluginEnabled(ad, opt) == 1
}

// Setup runs the flow and returns the exit code, and whether the question was
// cancelled: a cancel prints "Nothing changed." and nothing else follows it, so
// the caller must not treat it as a step that failed. Declining or cancelling
// the question changes nothing. An answer given at the prompt leaves one line
// recording it where the prompt was; --yes and --no leave none. With no terminal and no answer it prints
// NotInteractive and changes nothing; it exits 0 when bosun is already set up
// (there is nothing a script needs to react to) and 2 when it is not.
func Setup(ad platform.Adapters, opt Options, env Env, so SetupOptions, out io.Writer) (rc int, cancelled bool) {
	current := LiveCostOn(ad, opt)
	var answer bool
	switch {
	case so.Answer != nil:
		answer = *so.Answer
	case so.Ask == nil:
		fmt.Fprintln(out, NotInteractive)
		if SetUp(ad, opt) {
			return 0, false
		}
		return 2, false
	default:
		a, err := so.Ask(!SetUp(ad, opt) || current)
		if err != nil {
			fmt.Fprintln(out, "Nothing changed.")
			return 1, true
		}
		answer = a
		word := "No"
		if a {
			word = "Yes"
		}
		fmt.Fprintf(out, "%s %s\n", LiveCostQuestion, env.bold(word))
	}

	fail := func(what string, reason any) {
		fmt.Fprintf(out, "! %s: %v\n", what, reason)
		rc = 1
	}
	ok := func(format string, a ...any) { fmt.Fprintf(out, "✓ "+format+"\n", a...) }

	// bosun's own files: only failures are shown.
	var code int
	for _, line := range writeOwnedFiles(ad, &opt, &code) {
		if strings.HasPrefix(line, "FAILED") {
			fmt.Fprintln(out, line)
		}
	}
	if code != 0 {
		rc = code
	}
	conf := ad.Paths.TmuxConf()

	if lines, err := runStep(pluginInstallStep(ad, opt, env)); err != nil {
		fail("plugin", err)
	} else {
		ok("Claude Code plugin installed")
		notes(out, lines)
	}

	if _, err := os.Stat(conf); err == nil {
		switch loaded, note := sourceConf(env, conf); {
		case loaded:
			ok("tmux configured")
		case note == noServerNote:
			ok("tmux configured (it loads when a Claude Code session starts in tmux)")
		default:
			fail("tmux", note)
		}
	}

	step, what := statusLineStep(ad, opt, env), "on"
	if !answer {
		step, what = statusLineRestoreStep(ad, opt, env), "off"
	}
	if lines, err := runStep(step); err != nil {
		fail("live cost", err)
	} else {
		ok("live cost %s", what)
		notes(out, lines)
	}
	return rc, false
}

// runStep runs a step without a plan or a prompt. A step with nothing to do
// succeeds; one that refuses fails with its reason.
func runStep(s Step) ([]string, error) {
	switch {
	case s.Refuse != "":
		return nil, fmt.Errorf("%s", s.Refuse)
	case s.Done || s.Do == nil:
		return nil, nil
	}
	return s.Do()
}

// notes prints what a step reported, such as where it left a backup.
func notes(out io.Writer, lines []string) {
	for _, l := range lines {
		if b, ok := strings.CutPrefix(l, "backup "); ok {
			l = "backup: " + b
		}
		fmt.Fprintf(out, "    %s\n", l)
	}
}

// SyncTmux is what the SessionStart hook does inside tmux: make sure the
// running server has loaded the conf this binary generates. It regenerates the
// conf, asks the server which version it holds, and sources the conf only when
// that differs, so a server that is current costs one tmux call. It prints
// nothing and returns nothing; the hook must never fail.
func SyncTmux(ad platform.Adapters, opt Options, env Env) {
	if !env.InTmux || env.TmuxBin == "" || env.Runner == nil {
		return
	}
	if _, ok := writeTmuxConf(ad, &opt); !ok {
		return
	}
	out, err := env.Runner.Run(tmuxTimeout, env.TmuxBin, tmuxArgs(env, "show-options", "-gv", tmuxconf.LoadedOption)...)
	if err == nil && strings.TrimSpace(string(out)) == opt.Version && !opt.ConfChanged {
		return
	}
	sourceConf(env, ad.Paths.TmuxConf())
}
