package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jmvaldez/bosun/internal/atomicfile"
	"github.com/jmvaldez/bosun/internal/config"
	"github.com/jmvaldez/bosun/internal/eval"
	"github.com/jmvaldez/bosun/internal/ledger"
	"github.com/jmvaldez/bosun/internal/notify"
	"github.com/jmvaldez/bosun/internal/peek"
	"github.com/jmvaldez/bosun/internal/refresh"
	"github.com/jmvaldez/bosun/internal/spend"
	"github.com/jmvaldez/bosun/internal/tmux"
	"github.com/jmvaldez/bosun/internal/ui"
)

// The popup redraws every second. spendTTL keeps the ledger reload off that
// path, and ingestTTL spaces out the transcript ingest that feeds the ledger.
const (
	spendTTL  = 5 * time.Second
	ingestTTL = 30 * time.Second
)

// uiSource builds Data for the popup: the evaluator's view of live sessions
// every tick, and the ledger's spend every few seconds.
type uiSource struct {
	a   *app
	cfg config.Config
	// lastSessions is the latest frame's session list, for lookups between frames.
	lastSessions []eval.View
	tc           tmux.Client
	db           *ledger.DB
	clock        spend.Clock
	// names is nil unless privacy.show_text is on and claude resolves (D-27).
	names  *refresh.Names
	spendC struct {
		at       time.Time
		ingested time.Time
		snap     ledger.Snapshot
		cs       []spend.Contribution
	}
}

// evalDeps makes the popup's tick a normal evaluation. A popup left open all
// day is the only evaluator running while the status line is hidden, so one
// without Exe and Notify would swallow every alert fire and never retract one.
// Focus suppression still applies in the dispatcher.
func (s *uiSource) evalDeps() eval.Deps {
	exe, _ := os.Executable()
	return eval.Deps{Ad: s.a.ad, Cfg: s.cfg, Tmux: s.tc, Exe: exe, Notify: true}
}

func (s *uiSource) load() ui.Data {
	now := s.a.ad.Clock.Now()
	res := eval.Evaluate(s.evalDeps())
	if s.db != nil && now.Sub(s.spendC.at) >= spendTTL {
		if now.Sub(s.spendC.ingested) >= ingestTTL {
			_, _ = s.db.Ingest(refresh.IngestOpts(s.a.ad, s.cfg))
			s.spendC.ingested = now
		}
		if snap, err := s.db.Load(time.Time{}); err == nil {
			s.spendC.snap = snap
			s.spendC.cs = refresh.Contributions(s.a.ad, snap, s.clock)
		}
		s.spendC.at = now
	}
	repos := map[string]bool{}
	for _, v := range res.Sessions {
		if v.Repo != "" {
			repos[v.Repo] = true
		}
	}
	var names map[string]string
	if s.names != nil {
		names = s.names.Get(now)
	}
	return ui.Data{
		Names: names, Now: now, Sessions: res.Sessions, Counts: res.Counts, Repos: len(repos),
		Today: spend.DayTotal(s.spendC.cs, s.clock, s.clock.Day(now), time.Time{}), Cap: s.cfg.Rules.DailyBudget, Pace: res.Pace,
		Snap: s.spendC.snap, Contribs: s.spendC.cs, Clock: s.clock,
	}
}

// uiActions is the popup's side effects, wired to tmux and the state files.
type uiActions struct {
	a      *app
	cfg    config.Config
	tc     tmux.Client
	client string
	src    *uiSource
	// In-process memos of text read at render time; never persisted (D-27).
	latest  map[string]latestCache
	prompts map[string]string
}

func (u *uiActions) Jump(pane string) error { return u.tc.SwitchClient(u.client, pane) }

// Diff opens a window in the repo with `git diff --stat` then the full diff in
// less. A ui.diff_cmd the owner changed runs as given.
func (u *uiActions) Diff(cwd string) error {
	cmd := u.cfg.UI.DiffCmd
	if cmd == config.Default().UI.DiffCmd {
		q := shellQuote(cwd)
		cmd = "git -C " + q + " diff --stat --color=always; git -C " + q + " diff --color=always | less -R"
	}
	return u.tc.NewWindow(cwd, cmd)
}

func (u *uiActions) Interrupt(v eval.View) (string, error) {
	return ui.Interrupt(ui.InterruptDeps{Tmux: u.tc, Procs: u.a.ad.Procs, Keys: u.cfg.UI.InterruptKeys}, v)
}

func (u *uiActions) Dismiss(session, agent string) (int, error) {
	got, err := eval.DismissSession(u.a.ad.Paths, session, agent)
	if err != nil {
		return 0, err
	}
	if d, _ := u.a.notifyDeps(); d.Notifier != nil {
		for _, al := range got {
			_, _, _ = notify.Dispatch(d, notify.Request{Retract: true, Group: notify.Group(notify.Kind(al.Rule), al.Session)})
		}
	}
	return len(got), nil
}

// Export writes the Spend tab's current view as CSV, the same rows
// `bosun spend --csv --since P --by G` prints for that period and grouping.
func (u *uiActions) Export(period, group string) (string, error) {
	if u.src.db == nil {
		return "", fmt.Errorf("no ledger")
	}
	snap, err := u.src.db.Load(time.Time{})
	if err != nil {
		return "", err
	}
	since, err := u.src.clock.Since(period, u.a.ad.Clock.Now())
	if err != nil {
		return "", err
	}
	res, err := spend.Group(spend.Filter(refresh.Contributions(u.a.ad, snap, u.src.clock), since, ""), group, u.src.clock)
	if err != nil {
		return "", err
	}
	res.Since = period
	var buf bytes.Buffer
	if err := spend.CSV(&buf, res); err != nil {
		return "", err
	}
	name := fmt.Sprintf("spend-%s-%s-%s.csv", period, group, u.a.ad.Clock.Now().Format("20060102-150405"))
	path := filepath.Join(u.a.ad.Paths.Data, "exports", name)
	return path, atomicfile.Write(path, buf.Bytes(), 0o600)
}

// Peek reads the pending tool call at render time; the caller holds it for one
// frame only.
func (u *uiActions) Peek(v eval.View) string {
	if !u.cfg.UI.Peek || v.Transcript == "" {
		return ""
	}
	line, _ := peek.Pending(v.Transcript)
	return line
}

// Latest is the newest tool call of a session, read now (D-27). The result is
// memoized in this process for as long as the transcript has the same size and
// modification time; nothing is written anywhere.
func (u *uiActions) Latest(v eval.View) peek.Info {
	if !u.cfg.Privacy.ShowText || v.Transcript == "" {
		return peek.Info{}
	}
	fi, err := os.Stat(v.Transcript)
	if err != nil {
		return peek.Info{}
	}
	key := v.Transcript
	stamp := fmt.Sprintf("%d/%d", fi.Size(), fi.ModTime().UnixNano())
	if c, ok := u.latest[key]; ok && c.stamp == stamp {
		return c.info
	}
	if u.latest == nil {
		u.latest = map[string]latestCache{}
	}
	info := peek.Latest(v.Transcript)
	u.latest[key] = latestCache{stamp, info}
	return info
}

type latestCache struct {
	stamp string
	info  peek.Info
}

func (u *uiActions) PromptLine(session, prompt string) string {
	if !u.cfg.Privacy.ShowText && !u.cfg.UI.Peek {
		return ""
	}
	if line, ok := u.prompts[session+"/"+prompt]; ok {
		return line
	}
	line := u.promptLine(session, prompt)
	if u.prompts == nil {
		u.prompts = map[string]string{}
	}
	u.prompts[session+"/"+prompt] = line
	return line
}

func (u *uiActions) promptLine(session, prompt string) string {
	path := ""
	for _, v := range u.src.lastSessions {
		if v.ID == session {
			path = v.Transcript
		}
	}
	if path == "" && u.src.db != nil {
		path = u.src.db.TranscriptPath(session)
	}
	if path == "" {
		return ""
	}
	return peek.PromptFirstLine(path, prompt)
}

func (a *app) ui(args []string) int {
	startSpend := false
	for _, arg := range args {
		if arg != "--spend" {
			fmt.Fprintln(a.errw, "usage: bosun ui [--spend]")
			return 2
		}
		startSpend = true
	}
	cfg := a.loadConfig()
	tc := a.tmuxClient("", "")
	client, _ := tc.ClientName() // works inside display-popup, unlike $TMUX_PANE
	src := &uiSource{a: a, cfg: cfg, tc: tc, clock: spend.NewClock(cfg.Spend.Day)}
	if a.ad.Paths.State != "" {
		if db, err := ledger.Open(a.ad.Paths.Ledger()); err == nil {
			defer db.Close()
			src.db = db
		}
	}
	if cfg.Privacy.ShowText {
		if bin, err := refresh.ResolveClaude(cfg); err == nil {
			src.names = refresh.NewNames(a.ad.Runner, bin, refresh.TTL)
		}
	}
	act := &uiActions{a: a, cfg: cfg, tc: tc, client: client, src: src}
	m := ui.New(ui.Options{Peek: cfg.UI.Peek, Color: true,
		ShowText: cfg.Privacy.ShowText, ASCII: cfg.UI.Glyphs == "ascii"}, act)
	if startSpend {
		m.SetTab(ui.TabSpend)
	}
	err := ui.Run(os.Stdin, a.out, m, func() ui.Data {
		d := src.load()
		src.lastSessions = d.Sessions
		return d
	}, time.Second)
	if err != nil {
		fmt.Fprintf(a.errw, "bosun ui: %v\n", err)
		return 1
	}
	return 0
}
