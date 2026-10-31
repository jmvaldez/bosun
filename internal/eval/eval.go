// Package eval is the evaluator: it folds new hook events into live.json,
// reconciles with agents.json, kill -0 and tmux, runs the alert rules, writes
// tab markers, and returns what the segment, `status` and `jump` need. It runs
// under eval.lock, from `bosun status`, the popup's tick and every command's
// catch-up.
package eval

import (
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jmvaldez/bosun/internal/claude"
	"github.com/jmvaldez/bosun/internal/config"
	"github.com/jmvaldez/bosun/internal/flock"
	"github.com/jmvaldez/bosun/internal/live"
	"github.com/jmvaldez/bosun/internal/pace"
	"github.com/jmvaldez/bosun/internal/paths"
	"github.com/jmvaldez/bosun/internal/platform"
	"github.com/jmvaldez/bosun/internal/rules"
	"github.com/jmvaldez/bosun/internal/segment"
	"github.com/jmvaldez/bosun/internal/task"
	"github.com/jmvaldez/bosun/internal/tmux"
)

const (
	// activeWindow bounds discovery: session dirs idle for longer are skipped
	// unless already tracked, so a state dir full of history stays cheap.
	activeWindow = 24 * time.Hour
	// refreshRetry caps detached refresh spawns when refresh keeps failing.
	refreshRetry = 3 * time.Second
	// missingReadsToDrop and missingQuiet drop a pid-less session that
	// agents.json no longer lists.
	missingReadsToDrop = 2
	missingQuiet       = time.Minute
)

type Deps struct {
	Ad   platform.Adapters
	Cfg  config.Config
	Tmux tmux.Client
	// Exe is the absolute path of the running bosun, used to spawn children.
	Exe string
	// Notify is true for the periodic evaluations: `status --tmux` and the
	// popup's tick. A catch-up run (attach, jump, spend) may find alerts and
	// show them, but they never become desktop notifications.
	Notify bool
}

// SubView is one running subagent as the popup shows it.
type SubView struct {
	ID      string        `json:"id"`
	Type    string        `json:"type"`
	Runtime time.Duration `json:"runtime_ns"`
	Tools   int           `json:"tools"`
	EstUSD  float64       `json:"est_usd"`
	Alert   bool          `json:"runaway,omitempty"`
}

// View is one session as `status --json` and the popup report it. Sessions
// whose pane is gone are listed (flagged) but never counted.
type View struct {
	ID         string    `json:"id"`
	State      string    `json:"state"`
	Display    string    `json:"display"`
	Since      time.Time `json:"since"`
	PaneGone   bool      `json:"pane_gone,omitempty"`
	Alerts     []string  `json:"alerts,omitempty"`
	Where      string    `json:"where,omitempty"`
	Pane       string    `json:"pane,omitempty"`
	ServerPID  int       `json:"server_pid,omitempty"`
	PID        int       `json:"pid,omitempty"`
	StartTok   string    `json:"start_token,omitempty"`
	Cwd        string    `json:"cwd,omitempty"`
	Transcript string    `json:"transcript,omitempty"`
	Repo       string    `json:"repo,omitempty"`
	Task       string    `json:"task,omitempty"`
	WaitingFor string    `json:"waiting_for,omitempty"`
	WaitKind   string    `json:"wait_kind,omitempty"`
	LastTool   string    `json:"last_tool,omitempty"`
	// ToolOpen is true while a main-thread tool call has no result yet;
	// ToolSince is when it began.
	ToolOpen   bool      `json:"tool_open,omitempty"`
	ToolSince  time.Time `json:"tool_since,omitempty"`
	Background bool      `json:"background,omitempty"`
	Subagents  int       `json:"subagents_running"`
	SubsEnded  int       `json:"subagents_ended"`
	Subs       []SubView `json:"subs,omitempty"`
	Edits      int       `json:"edits"`
	Quiet      string    `json:"quiet,omitempty"`
	// CtxPct is the context fill (0-100); absent when unknown.
	CtxPct *float64 `json:"ctx_pct,omitempty"`
}

type Result struct {
	Counts   segment.Counts `json:"counts"`
	Sessions []View         `json:"sessions"`
	Pace     pace.Status    `json:"pace"`
	// Alerts are the active, undismissed alerts, account-level ones included.
	Alerts []rules.Alert `json:"alerts,omitempty"`
	// Stale is true when eval.lock could not be taken; the sessions and counts
	// are the last live.json, while Alerts and Pace are read fresh from disk.
	Stale bool `json:"stale,omitempty"`
}

// Evaluate runs one evaluation. It never blocks: if another evaluator holds
// eval.lock the last live.json is served as is.
func Evaluate(d Deps) Result {
	p := d.Ad.Paths
	if p.State == "" {
		return Result{}
	}
	lock, err := flock.TryLock(p.Lock("eval"), 0)
	if err != nil {
		now := d.Ad.Clock.Now()
		store := rules.Load(p.Alerts())
		r := summarize(live.Read(p.Live()), nil, store, now)
		// Without these a stale result reports no alerts and no pace while
		// Counts.Runaway may be nonzero.
		r.Pace = d.paceStatus(now)
		for _, a := range store.Active() {
			r.Alerts = append(r.Alerts, *a)
		}
		r.Stale = true
		return r
	}
	defer lock.Unlock()

	now := d.Ad.Clock.Now()
	f := live.Read(p.Live())
	before := attentionStates(f)
	d.foldAll(&f, now)
	d.reconcileLiveness(&f, now)
	d.reconcileAgents(&f, now)
	panes, tmuxOK := d.reconcileTmux(&f, now)
	res := d.evalRules(&f, now)
	d.retractCleared(f, before)
	if tmuxOK {
		d.writeMarks(&f, panes)
	}
	prune(&f, now)

	f.UpdatedAt = now
	_ = live.Write(p.Live(), f)
	out := summarize(f, panes, rules.Load(p.Alerts()), now)
	out.Pace, out.Alerts = res.pace, res.alerts
	return out
}

// attentionStates records each session's state before this tick, to spot a
// needs-you or done that has cleared.
func attentionStates(f live.File) map[string]string {
	m := map[string]string{}
	for id, s := range f.Sessions {
		if !s.Ended {
			m[id] = s.State
		}
	}
	return m
}

// foldAll applies new events for every session dir.
func (d Deps) foldAll(f *live.File, now time.Time) {
	entries, err := os.ReadDir(d.Ad.Paths.Sessions())
	if err != nil {
		return
	}
	for _, e := range entries {
		id := e.Name()
		if !e.IsDir() || !paths.ValidSessionID(id) {
			continue
		}
		evPath := d.Ad.Paths.Events(id)
		fi, err := os.Stat(evPath)
		if err != nil {
			continue
		}
		s := f.Sessions[id]
		if s == nil {
			if now.Sub(fi.ModTime()) > activeWindow {
				continue
			}
			s = &live.Session{ID: id, State: live.Idle}
			f.Sessions[id] = s
		}
		_, _ = live.FoldFile(evPath, s)
		d.applyTag(s)
	}
}

// reconcileLiveness drops sessions whose process is gone: kill -0 plus the
// start token, so a reused pid does not keep a dead session alive.
func (d Deps) reconcileLiveness(f *live.File, _ time.Time) {
	for _, s := range f.Sessions {
		if s.Ended || s.PID <= 0 || d.Ad.Procs == nil {
			continue
		}
		if !d.Ad.Procs.Alive(s.PID, s.StartTok) {
			s.Ended = true
		}
	}
}

// reconcileAgents applies the agents.json cache and spawns a detached refresh
// when it is stale and someone is watching.
func (d Deps) reconcileAgents(f *live.File, now time.Time) {
	cache, stale := claude.ReadAgents(d.Ad.Paths.AgentsCache(), now, claude.AgentsTTL)
	active := false
	rows := map[string]claude.Agent{}
	for _, r := range cache.Rows {
		// A pid row beats a background row with the same session id.
		if old, ok := rows[r.SessionID]; !ok || old.Background {
			rows[r.SessionID] = r
		}
	}
	for _, s := range f.Sessions {
		if s.Ended {
			continue
		}
		active = true
		if cache.FetchedAt.IsZero() {
			continue
		}
		r, found := rows[s.ID]
		s.ReconcileAgents(live.AgentRow{Found: found, Background: r.Background, Status: r.Status, WaitingFor: r.WaitingFor}, cache.FetchedAt)
		if s.PID <= 0 && s.MissingReads >= missingReadsToDrop && now.Sub(s.LastEvent) > missingQuiet {
			s.Ended = true
		}
	}
	if stale && active && d.Ad.Spawner != nil && d.Exe != "" && now.Sub(f.RefreshTried) >= refreshRetry {
		f.RefreshTried = now
		_ = d.Ad.Spawner.Spawn(d.Exe, "refresh")
	}
}

// reconcileTmux sets PaneGone from the server's panes and turns seen `done`
// panes idle. When tmux cannot be asked the previous answers stand.
func (d Deps) reconcileTmux(f *live.File, now time.Time) ([]tmux.Pane, bool) {
	panes, err := d.Tmux.Panes()
	if err != nil || len(panes) == 0 {
		return nil, false
	}
	server := panes[0].ServerPID
	byID := map[string]tmux.Pane{}
	for _, p := range panes {
		byID[p.ID] = p
	}
	for _, s := range f.Sessions {
		// A live other server's pane ids mean nothing here: leave such a session
		// as it was, so this pass can neither clear nor re-fire its alerts.
		s.Foreign = s.Loc.Pane != "" && s.Loc.SPID != 0 && s.Loc.SPID != server && d.Ad.Procs.Alive(s.Loc.SPID, "")
		if s.Foreign {
			continue
		}
		_, ok := byID[s.Loc.Pane]
		s.PaneGone = s.Loc.Pane == "" || s.Loc.SPID != server || !ok
	}
	if clients, err := d.Tmux.Clients(); err == nil {
		for _, s := range f.Sessions {
			if s.Visible() && !s.Foreign && s.State == live.Done && tmux.Viewing(clients, s.Loc.Pane) {
				s.MarkSeen(now)
			}
		}
	}
	return panes, true
}

// writeMarks sets @bosun-wait on each window to its top state, and only writes
// what changed. Hooks never call tmux; this is the only writer.
func (d Deps) writeMarks(f *live.File, panes []tmux.Pane) {
	server := panes[0].ServerPID
	windowOf := map[string]string{}
	exists := map[string]bool{}
	for _, p := range panes {
		windowOf[p.ID] = p.WindowID
		exists[p.WindowID] = true
	}
	want := map[string]string{}
	for _, s := range f.Sessions {
		if !s.Visible() || s.Foreign || s.Display() == live.Idle {
			continue
		}
		w := windowOf[s.Loc.Pane]
		if cur, ok := want[w]; !ok || live.Rank(s.Display()) < live.Rank(cur) {
			want[w] = s.Display()
		}
	}
	all := live.ReadMarks(d.Ad.Paths.Marks())
	prev := all.For(server)
	var changes []tmux.WindowMark
	next := map[string]string{}
	for w, v := range want {
		next[w] = v
		if prev[w] != v {
			changes = append(changes, tmux.WindowMark{WindowID: w, Value: v})
		}
	}
	for w := range prev {
		if _, still := want[w]; !still && exists[w] {
			changes = append(changes, tmux.WindowMark{WindowID: w})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].WindowID < changes[j].WindowID })
	if len(changes) > 0 {
		if err := d.Tmux.SetMarks(changes); err != nil {
			return // keep the old marks.json so the next tick retries
		}
	}
	out := live.Marks{Servers: map[string]map[string]string{strconv.Itoa(server): next}}
	for k, w := range all.Servers {
		if pid, err := strconv.Atoi(k); err == nil && pid != server && d.Ad.Procs.Alive(pid, "") {
			out.Servers[k] = w // another live server's markers are its own to manage
		}
	}
	_ = live.WriteMarks(d.Ad.Paths.Marks(), out)
}

// prune forgets ended sessions a day after their last event; discovery skips
// their dirs by mtime, so they do not come back.
func prune(f *live.File, now time.Time) {
	for id, s := range f.Sessions {
		if s.Ended && now.Sub(s.LastEvent) > activeWindow {
			delete(f.Sessions, id)
		}
	}
}

func summarize(f live.File, panes []tmux.Pane, alerts rules.Store, now time.Time) Result {
	where := map[string]string{}
	for _, p := range panes {
		where[p.ID] = p.Where()
	}
	byAlert := alerts.BySession()
	var r Result
	for _, s := range f.Sessions {
		if s.Ended || s.Loc.Pane == "" || s.Foreign {
			continue
		}
		v := View{
			ID: s.ID, State: s.State, Display: s.Display(), Since: s.StateSince, PaneGone: s.PaneGone,
			Where: where[s.Loc.Pane], Pane: s.Loc.Pane, ServerPID: s.Loc.SPID, PID: s.PID, StartTok: s.StartTok,
			Cwd: s.Cwd, Transcript: s.Transcript, Repo: s.Repo, Task: s.Task, WaitingFor: s.WaitingFor, WaitKind: s.WaitKind, LastTool: s.LastTool,
			ToolOpen: s.ToolOpen, Background: s.BGPending,
			Subagents: len(s.Subs), SubsEnded: s.SubsEnded, Edits: s.Edits, CtxPct: s.CtxPct,
		}
		if s.ToolOpen {
			v.ToolSince = s.LastToolAt
		}
		for _, a := range byAlert[s.ID] {
			v.Alerts = append(v.Alerts, a.Detail)
			if a.Rule == rules.Quiet {
				v.Quiet = strings.TrimPrefix(a.Detail, "quiet ")
			}
		}
		alerted := map[string]bool{}
		for _, a := range byAlert[s.ID] {
			if a.Agent != "" {
				alerted[a.Agent] = true
			}
		}
		for id, sub := range s.Subs {
			v.Subs = append(v.Subs, SubView{ID: id, Type: sub.Type, Runtime: now.Sub(sub.Start), Tools: sub.Tools, EstUSD: sub.CostUSD, Alert: alerted[id]})
		}
		sort.Slice(v.Subs, func(i, j int) bool { return v.Subs[i].Runtime > v.Subs[j].Runtime })
		r.Sessions = append(r.Sessions, v)
		if s.PaneGone {
			continue
		}
		r.Counts.Sessions++
		r.Counts.Subs += len(s.Subs)
		switch s.Display() {
		case live.NeedsYou:
			r.Counts.NeedsYou++
		case live.Runaway:
			r.Counts.Runaway++
		case live.Handoff:
			r.Counts.Handoff++
		case live.Done:
			r.Counts.Done++
		case live.Working:
			r.Counts.Working++
		case live.Quiet:
			r.Counts.Quiet++
		default:
			r.Counts.Idle++
		}
	}
	sort.Slice(r.Sessions, func(i, j int) bool {
		a, b := r.Sessions[i], r.Sessions[j]
		if a.PaneGone != b.PaneGone {
			return !a.PaneGone
		}
		if live.Rank(a.Display) != live.Rank(b.Display) {
			return live.Rank(a.Display) < live.Rank(b.Display)
		}
		if !a.Since.Equal(b.Since) {
			return a.Since.Before(b.Since)
		}
		return a.ID < b.ID
	})
	return r
}

// applyTag re-reads the tag file every pass: a tag outranks BaseTask (see
// live.Session.BaseTask), and removing it falls back to BaseTask.
func (d Deps) applyTag(s *live.Session) {
	if s.BaseTask == "" && s.BaseSrc == "" && s.TaskSrc != task.SourceTag {
		s.BaseTask, s.BaseSrc = s.Task, s.TaskSrc // live.json from before tags existed
	}
	if key := task.ReadTag(d.Ad.Paths.Tag(s.ID)); key != "" {
		s.Task, s.TaskSrc = key, task.SourceTag
		return
	}
	s.Task, s.TaskSrc = s.BaseTask, s.BaseSrc
}
