package notify

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jmvaldez/bosun/internal/config"
	"github.com/jmvaldez/bosun/internal/flock"
	"github.com/jmvaldez/bosun/internal/ledger"
	"github.com/jmvaldez/bosun/internal/live"
	"github.com/jmvaldez/bosun/internal/paths"
	"github.com/jmvaldez/bosun/internal/peek"
	"github.com/jmvaldez/bosun/internal/platform"
	"github.com/jmvaldez/bosun/internal/rules"
	"github.com/jmvaldez/bosun/internal/statusline"
)

// lockWait is the notify lock's polling deadline (LESSONS L-12).
const lockWait = 2 * time.Second

type Outcome string

const (
	Posted     Outcome = "posted"
	Retracted  Outcome = "retracted"
	Overflow   Outcome = "overflow"
	Suppressed Outcome = "suppressed" // the user is looking at the pane
	Dropped    Outcome = "dropped"    // the edge is gone, a repeat, or not wanted
	Off        Outcome = "off"
	Busy       Outcome = "busy" // another dispatch held the lock past the deadline
)

type Deps struct {
	Paths    paths.Paths
	Cfg      config.Config
	Notifier Notifier       // nil: notifications are off
	Focus    platform.Focus // nil: focus unknown, so notify
	Clock    platform.Clock
	// Spawn starts a detached `bosun <args>` child; nil turns click handling off.
	Spawn func(args ...string) error
	// Locate names a pane's tmux session:window for the notice's second line;
	// nil or an empty answer leaves it out.
	Locate func(pane string) string
}

// Dispatch handles one request under the notify lock (flow: kit/SPEC.md §6).
// The child that runs it is detached and short lived; nothing here blocks past
// the lock and backend deadlines.
func Dispatch(d Deps, r Request) (Outcome, string, error) {
	if d.Notifier == nil {
		return Off, "no notification backend", nil
	}
	lock, err := flock.TryLock(d.Paths.Lock("notify"), lockWait)
	if errors.Is(err, flock.ErrBusy) {
		return Busy, "another dispatch holds the lock", nil
	}
	if err != nil {
		return Off, "", err
	}
	defer lock.Unlock()

	now := d.Clock.Now()
	st := LoadState(d.Paths.Notify())
	st.prune(now)
	defer func() { _ = st.Save(d.Paths.Notify()) }()

	if r.Retract {
		return d.retract(&st, r)
	}
	if r.Test {
		return d.post(&st, Message{Title: "bosun", Subtitle: "test", Body: "notifications work", Group: "bosun.test"}, "", now)
	}
	if reason := d.recheck(&r); reason != "" {
		// A done notice that is not worth posting still ends the needs-you
		// notice that preceded it in the same group.
		if r.Kind == Done || r.Kind == Failed {
			if o, _, err := d.retract(&st, Request{Group: Group(r.Kind, r.Session)}); err == nil && o == Retracted {
				return Retracted, reason, nil
			}
		}
		return Dropped, reason, nil
	}
	m, err := Render(r)
	if err != nil {
		return Dropped, "", err
	}
	// SPEC §6 table: only attention notices are focus-suppressed; runaway alerts
	// post regardless.
	if r.Kind == NeedsYou || r.Kind == Done || r.Kind == Failed {
		if d.Focus != nil && d.Focus.Check(sessionPane(d.Paths, r.Session)) == platform.Focused {
			return Suppressed, "the pane is focused", nil
		}
	}
	if p, ok := st.Posted[m.Group]; ok && r.Edge != "" && p.Edge == r.Edge {
		return Dropped, "already posted for this edge", nil
	}
	if len(st.Window) >= max(d.Cfg.Notify.MaxPerMinute, 1) {
		return d.overflow(&st, now)
	}
	return d.post(&st, m, r.Edge, now)
}

func (d Deps) post(st *State, m Message, edge string, now time.Time) (Outcome, string, error) {
	n := Notice{Title: m.Title, Subtitle: m.Subtitle, Body: m.Body, Line2: m.Line2, Icon: m.Icon, Group: m.Group,
		Sound: m.Sound && d.Cfg.Notify.Sound != ""}
	actions, markup := d.serverCaps(st, now)
	n.Markup = markup
	click := m.Session != "" && d.Spawn != nil && actions
	if click {
		n.Action = "Open in tmux"
	}
	var listenUntil time.Time
	if p, ok := st.Posted[m.Group]; ok && d.Notifier.Capabilities().Replace {
		n.ReplaceID = p.ID
		listenUntil = p.ListenUntil
	}
	id, err := d.Notifier.Post(n)
	if err != nil {
		return Dropped, "", err
	}
	// A replacement returns the same id and emits no NotificationClosed, so the
	// listener spawned for the original post is still watching it; a second one
	// would run `bosun jump` twice on one click. Spawn again only once that
	// listener has hit its ClickTimeout deadline. The deadline is not extended:
	// a notice replaced late gets a fresh listener, one replaced early keeps
	// the old listener's remaining time.
	listening := id == n.ReplaceID && now.Before(listenUntil)
	entry := Entry{ID: id, Edge: edge, At: now}
	if listening {
		entry.ListenUntil = listenUntil
	}
	st.Window = append(st.Window, now)
	if click && id != 0 && !listening {
		entry.ListenUntil = now.Add(ClickTimeout)
		// A notify-click child waits for the click (see ClickTimeout).
		_ = d.Spawn("notify-click", "--id", strconv.FormatUint(uint64(id), 10), "--session", m.Session)
	}
	st.Posted[m.Group] = entry
	return Posted, m.Body, nil
}

// overflow posts the single self-replacing notice that names the popup key.
func (d Deps) overflow(st *State, now time.Time) (Outcome, string, error) {
	m := OverflowMessage(d.Cfg.Tmux.PopupKey)
	if p, ok := st.Posted[OverflowGroup]; ok && now.Sub(p.At) < time.Minute {
		return Dropped, "rate limited; overflow notice already shown", nil
	}
	n := Notice{Title: m.Title, Subtitle: m.Subtitle, Body: m.Body, Group: m.Group}
	if p, ok := st.Posted[OverflowGroup]; ok && d.Notifier.Capabilities().Replace {
		n.ReplaceID = p.ID
	}
	id, err := d.Notifier.Post(n)
	if err != nil {
		return Dropped, "", err
	}
	st.Posted[OverflowGroup] = Entry{ID: id, At: now}
	return Overflow, m.Body, nil
}

func (d Deps) retract(st *State, r Request) (Outcome, string, error) {
	group := r.Group
	if group == "" {
		group = Group(r.Kind, r.Session)
	}
	p, ok := st.Posted[group]
	if !ok {
		return Dropped, "nothing posted for " + group, nil
	}
	delete(st.Posted, group)
	if !d.Notifier.Capabilities().Retract {
		return Dropped, "backend cannot retract", nil
	}
	if err := d.Notifier.Retract(p.ID, group); err != nil {
		return Dropped, "", err
	}
	return Retracted, group, nil
}

// recheck confirms the edge still holds and fills the values a hook-started
// dispatch could not know. It returns a reason to drop, or "".
func (d Deps) recheck(r *Request) string {
	switch r.Kind {
	case NeedsYou, Done, Failed:
		return d.recheckAttention(r)
	case DailyPace, DailyBudget, SubagentRuntime, SubagentCost, SessionCost, Quiet:
		// Look the alert up by id: its key includes the subagent for subagent rules.
		for _, a := range rules.Load(d.Paths.Alerts()) {
			if a.ID == r.AlertID && !a.Dismissed {
				d.enrichRunaway(r, a)
				return ""
			}
		}
		return "the alert is gone or dismissed"
	}
	return ""
}

func (d Deps) recheckAttention(r *Request) string {
	s := FoldSession(d.Paths, r.Session)
	_, snapErr := os.Stat(d.Paths.StatuslineSnap(r.Session))
	if !s.Interactive(snapErr == nil) {
		return "non-interactive session (D-10)"
	}
	if r.Repo == "" {
		r.Repo = s.Repo
	}
	if r.Task == "" {
		r.Task = s.Task
	}
	if d.Locate != nil && s.Loc.Pane != "" {
		r.Where = d.Locate(s.Loc.Pane)
	}
	switch r.Kind {
	case NeedsYou:
		if s.State != live.NeedsYou {
			return "no longer waiting on you"
		}
		if r.Detail == "" {
			r.Detail = "permission prompt"
		}
		r.Edge = s.StateSince.UTC().Format(time.RFC3339Nano) // one notice per waiting episode
		if w := d.Clock.Now().Sub(s.StateSince); w > 0 {
			r.Waited = &w
		}
		d.readPending(r, s)
	case Done, Failed:
		if s.State != live.Done || s.Failed != (r.Kind == Failed) {
			return "the turn is not in that state"
		}
		turn := time.Duration(0)
		if !s.TurnStart.IsZero() {
			turn = s.StateSince.Sub(s.TurnStart)
		}
		if r.Kind == Done && turn < d.Cfg.Notify.DoneMinTurn {
			return "turn shorter than notify.done_min_turn"
		}
		edits := s.Edits
		r.Edits = &edits
		r.Duration = &turn
		if own, ok := OwnUSD(d.Paths, s); ok {
			r.OwnUSD = &own
		}
		r.Edge = s.StateSince.UTC().Format(time.RFC3339Nano)
	}
	return ""
}

// readPending reads what a waiting session is asking from the transcript, now,
// for one rendering (D-27). It is skipped with privacy.show_text off.
func (d Deps) readPending(r *Request, s *live.Session) {
	if !d.Cfg.Privacy.ShowText || s.Transcript == "" {
		return
	}
	info := peek.Latest(s.Transcript)
	switch {
	case info.Tool == "AskUserQuestion" || s.WaitKind == "question":
		r.Question = info.Question
	case info.Pending && info.Target != "":
		r.Tool, r.Target = info.Tool, info.Target
		if s.Cwd != "" {
			r.Target = strings.TrimPrefix(r.Target, s.Cwd+"/")
		}
	}
}

// enrichRunaway fills both numbers of a runaway subagent notice from the live
// subagent, so one banner can say "14m and $3.12 (limits 10m / $2)".
func (d Deps) enrichRunaway(r *Request, a *rules.Alert) {
	if r.Kind != SubagentRuntime && r.Kind != SubagentCost || a.Session == "" || a.Agent == "" {
		return
	}
	sub := FoldSession(d.Paths, a.Session).Subs[a.Agent]
	if sub == nil {
		return
	}
	if r.Duration == nil {
		dur := d.Clock.Now().Sub(sub.Start)
		r.Duration = &dur
	}
	if r.USD == nil && sub.CostUSD > 0 {
		v := sub.CostUSD
		r.USD = &v
	}
	if r.LimitDur == nil && d.Cfg.Rules.SubagentRuntime > 0 {
		v := d.Cfg.Rules.SubagentRuntime
		r.LimitDur = &v
	}
	if r.LimitUSD == nil && d.Cfg.Rules.SubagentCost > 0 {
		v := d.Cfg.Rules.SubagentCost
		r.LimitUSD = &v
	}
}

// FoldSession rebuilds a session's state from live.json plus any events not yet
// applied, without writing anything. A session not in live.json at all (a
// sub-second turn) is folded from its first event: its done or failed edge
// still holds (LESSONS L-27).
func FoldSession(p paths.Paths, id string) *live.Session {
	f := live.Read(p.Live())
	s := f.Sessions[id]
	if s == nil {
		s = &live.Session{ID: id, State: live.Idle}
	}
	_, _ = live.FoldFile(p.Events(id), s)
	return s
}

// OwnUSD returns the session's own spend from its status line snapshot, through
// ledger.SnapshotOwnUSD so a notice and a rule cannot disagree; ok is false when
// there is no usable snapshot for this session.
func OwnUSD(p paths.Paths, s *live.Session) (float64, bool) {
	snap, ok := statusline.ReadSnapshot(p.StatuslineSnap(s.ID))
	if !ok || snap.SessionID != s.ID {
		return 0, false
	}
	return ledger.SnapshotOwnUSD(snap, s.Forked), true
}

func sessionPane(p paths.Paths, id string) string { return FoldSession(p, id).Loc.Pane }

func (o Outcome) String() string { return string(o) }

// serverCaps probes the notification server's capabilities once a day (cached
// in notify.json): whether it can deliver a click ("actions") and whether the
// body may carry markup ("body-markup").
func (d Deps) serverCaps(st *State, now time.Time) (actions, markup bool) {
	g, ok := d.Notifier.(GDBus)
	if !ok {
		return false, false
	}
	if st.Actions != "" && st.Markup != "" && now.Sub(st.ActionsAt) < 24*time.Hour {
		return st.Actions == "yes", st.Markup == "yes"
	}
	a, m := g.ServerCaps()
	st.Actions, st.Markup, st.ActionsAt = yesNo(a), yesNo(m), now
	return a, m
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
