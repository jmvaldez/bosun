package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/jmvaldez/bosun/internal/config"
	"github.com/jmvaldez/bosun/internal/eval"
	"github.com/jmvaldez/bosun/internal/hook"
	"github.com/jmvaldez/bosun/internal/install"
	"github.com/jmvaldez/bosun/internal/paths"
	"github.com/jmvaldez/bosun/internal/platform"
	"github.com/jmvaldez/bosun/internal/refresh"
	"github.com/jmvaldez/bosun/internal/statusline"
	"github.com/jmvaldez/bosun/internal/term"
	"github.com/jmvaldez/bosun/internal/tmux"
)

// command is one entry of the command table. catchUp is the single place that
// says which commands run a catch-up evaluation before they act (LESSONS L-26):
// every user-facing command that reads or acts on session state, never the hot
// paths. `status` is its own evaluation.
type command struct {
	name    string
	catchUp bool
	run     func(a *app, args []string) int
}

func commands() []command {
	return []command{
		{"hook", false, func(a *app, args []string) int { return hook.Run(a.ad, args, a.in) }},
		{"statusline", false, func(a *app, args []string) int { return statusline.Run(a.ad, args, a.in, a.out, a.errw) }},
		{"refresh", false, (*app).refresh},
		{"install", false, (*app).install},
		{"setup", false, (*app).setup},
		{"tmux-sync", false, (*app).tmuxSync},
		{"config", false, (*app).config},
		{"status", false, (*app).status},
		{"jump", true, (*app).jump},
		{"spend", true, (*app).spend},
		{"ack", true, (*app).ack},
		{"ui", true, (*app).ui},
		{"tag", true, (*app).tag},
		{"backfill", false, (*app).backfill},
		{"doctor", false, (*app).doctor},
		{"uninstall", false, (*app).uninstall},
		{"run", false, (*app).run},
		{"notify", false, (*app).notifyCmd},
		{"notify-dispatch", false, (*app).notifyDispatch},
		{"notify-click", false, (*app).notifyClick},
		{"version", false, func(a *app, _ []string) int { fmt.Fprintf(a.out, "bosun %s\n", version); return 0 }},
	}
}

// readOnly lists commands that must not write into bosun's dirs, so they leave
// the purge marker to the next command that does.
var readOnly = map[string]bool{"doctor": true, "uninstall": true, "version": true, "config": true}

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
			if !readOnly[c.name] {
				paths.EnsureMarkers(a.ad.Paths)
			}
			return rc
		}
	}
	fmt.Fprintf(a.errw, "bosun: unknown command %q\n", name)
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
	bin, _ := refresh.ResolveClaude(cfg)
	a.ad.Identity = platform.ClaudeIdentity{Runner: a.ad.Runner, Bin: bin}
}

func (a *app) refresh(_ []string) int {
	cfg := a.loadConfig()
	a.withIdentity(cfg)
	return refresh.Run(a.ad, cfg, a.out, a.errw)
}

func (a *app) install(args []string) int {
	opt := install.Options{Version: version}
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "--repo" && i+1 < len(args):
			i++
			opt.Repo = args[i]
		case arg == "--settings" && i+1 < len(args):
			i++
			opt.Settings = args[i]
		case arg == "--tmux-conf" && i+1 < len(args):
			i++
			opt.TmuxConf = args[i]
		case arg == "--apply" || arg == "--yes" || arg == "--show-diff" || arg == "-v":
			fmt.Fprintf(a.errw, "bosun install: %s was removed; run `bosun setup`\n", arg)
			return 2
		default:
			fmt.Fprintf(a.errw, "bosun install: unknown argument %q\n", arg)
			return 2
		}
	}
	opt.Config = a.loadConfig()
	a.withIdentity(opt.Config)
	a.exePaths(&opt)
	return install.Run(a.ad, opt, a.out)
}

func (a *app) applyEnv(cfg config.Config, yes bool) install.Env {
	claudeBin, _ := refresh.ResolveClaude(cfg)
	tmuxBin, _ := exec.LookPath("tmux")
	tty := isTerminal(a.in)
	// Bold depends on where the output goes, not on where input comes from.
	color := isTerminal(a.out) && a.ad.Getenv("NO_COLOR") == ""
	tenv := a.ad.Getenv("TMUX")
	return install.Env{
		Home: a.ad.Getenv("HOME"), Color: color,
		Runner: a.ad.Runner, ClaudeBin: claudeBin, TmuxBin: tmuxBin, TmuxSocket: tmux.SocketFromEnv(tenv), InTmux: tenv != "",
		Stdin: a.in, TTY: tty, Yes: yes, Now: a.ad.Clock.Now,
	}
}

func (a *app) config(_ []string) int {
	cfg, res, err := config.Load(a.ad.Paths.ConfigFile())
	if err != nil {
		fmt.Fprintf(a.out, "# error: %s: %v (showing defaults)\n\n", a.ad.Paths.ConfigFile(), err)
	}
	fmt.Fprint(a.out, config.Format(cfg, res))
	if err != nil {
		return 1
	}
	return 0
}

// tmuxClient talks to the server the caller runs under ($TMUX), or to --socket.
func (a *app) tmuxClient(socket, bin string) tmux.Client {
	if socket == "" {
		socket = tmux.SocketFromEnv(a.ad.Getenv("TMUX"))
	}
	if bin == "" {
		if p, err := exec.LookPath("tmux"); err == nil {
			bin = p
		}
	}
	return tmux.Client{Runner: a.ad.Runner, Bin: bin, Socket: socket}
}

func (a *app) evalDeps(notify bool) eval.Deps {
	exe, _ := os.Executable()
	return eval.Deps{Ad: a.ad, Cfg: a.loadConfig(), Tmux: a.tmuxClient("", ""), Exe: exe, Notify: notify}
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
