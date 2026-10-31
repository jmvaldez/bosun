package main

// Command table, dispatch and the app helpers that more than one command shares.
// Each command itself lives in cmd_<name>.go.

import (
	"fmt"
	"io"
	"os"

	"github.com/jmvaldez/bosun/internal/claude"
	"github.com/jmvaldez/bosun/internal/config"
	"github.com/jmvaldez/bosun/internal/eval"
	"github.com/jmvaldez/bosun/internal/hook"
	"github.com/jmvaldez/bosun/internal/install"
	"github.com/jmvaldez/bosun/internal/paths"
	"github.com/jmvaldez/bosun/internal/platform"
	"github.com/jmvaldez/bosun/internal/statusline"
	"github.com/jmvaldez/bosun/internal/task"
	"github.com/jmvaldez/bosun/internal/term"
	"github.com/jmvaldez/bosun/internal/tmux"
)

// command is one entry of the command table. catchUp is the single place that
// says which commands run a catch-up evaluation before they act (LESSONS L-26):
// every user-facing command that reads or acts on session state, never the hot
// paths. `status` is its own evaluation. readOnly marks commands that must not
// write into bosun's dirs, so they leave the purge marker to the next command
// that does.
type command struct {
	name     string
	catchUp  bool
	readOnly bool
	run      func(a *app, args []string) int
}

func commands() []command {
	return []command{
		{name: "hook", run: func(a *app, args []string) int { return hook.Run(a.ad, args, a.in) }},
		{name: "statusline", run: func(a *app, args []string) int { return statusline.Run(a.ad, args, a.in, a.out, a.errw) }},
		{name: "refresh", run: (*app).refresh},
		{name: "install", run: (*app).install},
		{name: "setup", run: (*app).setup},
		{name: "tmux-sync", run: (*app).tmuxSync},
		{name: "config", readOnly: true, run: (*app).config},
		{name: "status", run: (*app).status},
		{name: "jump", catchUp: true, run: (*app).jump},
		{name: "spend", catchUp: true, run: (*app).spend},
		{name: "ack", catchUp: true, run: (*app).ack},
		{name: "ui", catchUp: true, run: (*app).ui},
		{name: "tag", catchUp: true, run: (*app).tag},
		{name: "backfill", run: (*app).backfill},
		{name: "doctor", readOnly: true, run: (*app).doctor},
		{name: "uninstall", readOnly: true, run: (*app).uninstall},
		{name: "run", run: (*app).runTask},
		{name: "handoff", catchUp: true, run: (*app).handoff},
		{name: "notify", run: (*app).notifyCmd},
		{name: "notify-dispatch", run: (*app).notifyDispatch},
		{name: "notify-click", run: (*app).notifyClick},
		{name: "version", readOnly: true, run: func(a *app, _ []string) int { fmt.Fprintf(a.out, "bosun %s\n", version); return 0 }},
	}
}

func (a *app) dispatch(args []string) int {
	if len(args) == 0 {
		return a.bare()
	}
	name, rest := args[0], args[1:]
	switch name {
	case "--version", "-v":
		name = "version"
	case "help", "--help", "-h":
		usage(a.out)
		return 0
	}
	for _, c := range commands() {
		if c.name == name {
			if c.catchUp {
				a.catchUp()
			}
			rc := c.run(a, rest)
			if !c.readOnly {
				paths.EnsureMarkers(a.ad.Paths)
			}
			return rc
		}
	}
	fmt.Fprintf(a.errw, "bosun: unknown command %q\n", name)
	usage(a.errw)
	return 2
}

// bare is `bosun` with no arguments. On a terminal, before bosun is set up, it
// runs the setup flow first; either way it then does what it always did, except
// that cancelling the question ends it there.
func (a *app) bare() int {
	if in, ok := a.in.(*os.File); ok && isTerminal(in) && isTerminal(a.out) && !install.IsSetUp(a.ad, install.Options{}) {
		if rc, cancelled := a.runSetup(nil); cancelled {
			return rc
		}
		fmt.Fprintln(a.out)
	}
	usage(a.errw)
	return 2
}

func (a *app) loadConfig() config.Config {
	cfg, _, _ := config.Load(a.ad.Paths.ConfigFile())
	return cfg
}

// withIdentity resolves claude once and wires the identity adapter. Only
// commands off the hot path call it.
func (a *app) withIdentity(cfg config.Config) {
	bin, _ := claude.Resolve(cfg)
	a.ad.Identity = platform.ClaudeIdentity{Runner: a.ad.Runner, Bin: bin}
}

// parser is the task-key parser built from the configured [task] section.
func (a *app) parser() *task.Parser {
	return task.FromConfig(a.loadConfig().Task)
}

// tmuxClient talks to the server the caller runs under ($TMUX), or to --socket.
func (a *app) tmuxClient(socket, bin string) tmux.Client {
	if socket == "" {
		socket = tmux.SocketFromEnv(a.ad.Getenv("TMUX"))
	}
	if bin == "" {
		if p, err := a.tmuxBin(); err == nil {
			bin = p
		}
	}
	return tmux.Client{Runner: a.ad.Runner, Bin: bin, Socket: socket}
}

func (a *app) evalDeps(notify bool) eval.Deps {
	return a.evalDepsWith(a.loadConfig(), a.tmuxClient("", ""), notify)
}

// evalDepsWith is evalDeps for a caller that already holds the config and the
// tmux client.
func (a *app) evalDepsWith(cfg config.Config, tc tmux.Client, notify bool) eval.Deps {
	exe, _ := a.exe()
	return eval.Deps{Ad: a.ad, Cfg: cfg, Tmux: tc, Exe: exe, Notify: notify}
}

// evaluate runs one evaluation. Only the periodic status job (`status --tmux`)
// passes notify; every other caller shows alerts but never posts them. The
// popup tick notifies through uiSource.evalDeps instead.
func (a *app) evaluate(notify bool) eval.Result { return eval.Evaluate(a.evalDeps(notify)) }

func usage(w io.Writer) {
	fmt.Fprint(w, `usage: bosun <command>

  status [--tmux | --json | --catch-up]   evaluate and print the segment, JSON, or text
  jump [--queue] [--session ID]           switch to the next waiting pane
  spend [--by K] [--since D] [--task T] [--json | --csv]
                                          spend breakdown; K is day, task, repo, session, prompt, request, model, subagent
  ui                                      open the popup (agents and spend)
  tag TEXT [--session ID] | --clear       set or clear a session's task (free text, up to 64 bytes)
  run --task TEXT [--dir P] [--worktree BRANCH] [-- ARGS]
                                          open a tasked Claude Code window in tmux
  handoff SID|TASK [--force] [--cancel] [--wait]
                                          have a session write a handoff doc and continue it in a new window
  backfill [--since 30d]                  ingest transcript history into the ledger
  doctor [--json]                         read-only self-check; exits non-zero on any failure
  uninstall [--apply [--yes]] [--purge] [--purge-config]
                                          print removal steps (--apply makes them); --purge deletes bosun's own dirs
  ack ID | --all                           dismiss runaway alerts until their conditions clear
  notify --test                           post one test notice
  statusline -- CMD...                    record the session total, then run CMD with the same stdin
  setup [--yes | --no]                    first-run setup: asks whether to show live session cost
  install                                 print the edits to make by hand, write bosun's own files
  config                                  print the effective config
  version                                 print the version

Hidden commands: hook (called by the Claude Code plugin), refresh, notify-dispatch, tmux-sync.
`)
}

// isTerminal reports whether x is a file attached to a terminal. /dev/null is a
// character device but not a terminal, so the check asks the tty driver.
func isTerminal(x any) bool {
	f, ok := x.(*os.File)
	if !ok {
		return false
	}
	_, _, err := term.Size(int(f.Fd()))
	return err == nil
}
