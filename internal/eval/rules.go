package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jmvaldez/bosun/internal/ledger"
	"github.com/jmvaldez/bosun/internal/live"
	"github.com/jmvaldez/bosun/internal/notify"
	"github.com/jmvaldez/bosun/internal/pace"
	"github.com/jmvaldez/bosun/internal/rules"
	"github.com/jmvaldez/bosun/internal/spend"
	"github.com/jmvaldez/bosun/internal/statusline"
	"github.com/jmvaldez/bosun/internal/subcost"
)

type ruleResult struct {
	pace   pace.Status
	alerts []rules.Alert
}

// evalRules turns this tick's conditions into alerts, marks runaway sessions,
// and starts notify-dispatch for new alerts. Rules run on the evaluator's fake-
// able clock; all state lives in alerts.json.
func (d Deps) evalRules(f *live.File, now time.Time) ruleResult {
	p := d.Ad.Paths
	cfg := d.Cfg
	clock := spend.NewClock(cfg.Spend.Day)
	day := clock.Day(now)

	var conds []rules.Cond
	info := map[string]ruleInfo{} // alert key -> what to tell the notifier
	add := func(c rules.Cond, in ruleInfo) {
		conds = append(conds, c)
		info[rules.Key(c.Rule, c.Scope)] = in
	}

	for _, s := range f.Sessions {
		if s.Ended || !s.Visible() {
			continue
		}
		d.sessionConds(s, now, add)
	}

	today := spend.ReadToday(p.Today(), day)
	st := d.paceStatus(now)
	if st.Alert {
		add(rules.Cond{Rule: rules.DailyPace, Scope: "account/" + day, Sticky: true, Day: day,
			Detail: fmt.Sprintf("today $%.2f, %.1fx typical", today.USD, st.Ratio)},
			ruleInfo{usd: today.USD, ratio: st.Ratio})
	}
	if cap := cfg.Rules.DailyBudget; cap > 0 && today.USD >= cap {
		add(rules.Cond{Rule: rules.DailyBudget, Scope: "account/" + day, Sticky: true, Day: day,
			Detail: fmt.Sprintf("today $%.2f of $%.2f", today.USD, cap)},
			ruleInfo{usd: today.USD, cap: cap})
	}

	store := rules.Load(p.Alerts())
	fires, cleared := store.Reconcile(conds, now, day, !d.Notify)
	// Save before spawning: the dispatcher re-checks alerts.json.
	_ = store.Save(p.Alerts())
	for _, fire := range fires {
		_ = rules.AppendLog(p.AlertsLog(), fire.Alert, now)
		if !fire.CatchUp {
			d.dispatchAlert(fire.Alert, info[rules.Key(fire.Alert.Rule, fire.Alert.Scope)], now)
		}
	}
	for _, a := range cleared {
		d.retract(notify.Group(notify.Kind(a.Rule), a.Session))
	}

	markRunaway(f, store)
	res := ruleResult{pace: st}
	for _, a := range store.Active() {
		res.alerts = append(res.alerts, *a)
	}
	return res
}

// sessionConds adds the conditions that hold for one live session: subagent
// runtime and cost, quiet, handoff, and session cost. It reads the session's status line
// snapshot once and also records the context fill from it.
func (d Deps) sessionConds(s *live.Session, now time.Time, add func(rules.Cond, ruleInfo)) {
	cfg := d.Cfg
	snap, snapOK := d.readSnap(s)
	s.CtxPct = nil
	if pct, ok := snap.Ctx(); snapOK && ok {
		s.CtxPct = &pct
	}
	for aid, sub := range s.Subs {
		scope := s.ID + "/" + aid
		age := now.Sub(sub.Start)
		if cfg.Rules.SubagentRuntime > 0 && age >= cfg.Rules.SubagentRuntime {
			add(rules.Cond{Rule: rules.SubagentRuntime, Scope: scope, Session: s.ID, Agent: aid,
				Detail: fmt.Sprintf("subagent %s > %s", notify.FormatDuration(age), notify.FormatDuration(cfg.Rules.SubagentRuntime))},
				ruleInfo{session: s, agent: sub.Type, dur: age})
		}
		if tp := subagentTranscript(s.Transcript, aid); tp != "" {
			if sub.Cost == nil {
				sub.Cost = &subcost.Tracker{}
			}
			sub.CostUSD = sub.Cost.Update(tp)
		}
		if cfg.Rules.SubagentCost > 0 && sub.CostUSD >= cfg.Rules.SubagentCost {
			add(rules.Cond{Rule: rules.SubagentCost, Scope: scope, Session: s.ID, Agent: aid,
				Detail: fmt.Sprintf("subagent $%.2f est. > $%.2f", sub.CostUSD, cfg.Rules.SubagentCost)},
				ruleInfo{session: s, agent: sub.Type, usd: sub.CostUSD})
		}
	}
	if cfg.Rules.Quiet > 0 && s.State == live.Working && !s.ToolOpen && !s.BGPending && len(s.Subs) == 0 {
		if gap := now.Sub(s.LastEvent); gap >= cfg.Rules.Quiet {
			add(rules.Cond{Rule: rules.Quiet, Scope: s.ID, Session: s.ID, Detail: "quiet " + notify.FormatDuration(gap)},
				ruleInfo{session: s, dur: gap})
		}
	}
	if at := cfg.Handoff.SuggestAt; at > 0 && s.CtxPct != nil && *s.CtxPct/100 >= at &&
		(s.State == live.Done || s.State == live.Idle) && !handoffStarted(d.Ad.Paths.Session(s.ID)) {
		add(rules.Cond{Rule: rules.Handoff, Scope: s.ID, Session: s.ID, Detail: fmt.Sprintf("ctx %.0f%%", *s.CtxPct)},
			ruleInfo{session: s, ctx: *s.CtxPct})
	}
	if step := cfg.Rules.SessionCost; step > 0 {
		if snapOK {
			own := ledger.SnapshotOwnUSD(snap, s.Forked)
			if level := int(own / step); level >= 1 {
				add(rules.Cond{Rule: rules.SessionCost, Scope: s.ID, Session: s.ID, Level: level,
					Detail: "session " + smallUSD(own)},
					ruleInfo{session: s, step: float64(level) * step})
			}
		}
	}
}

// handoffStarted reports whether a handoff is under way or finished for the
// session, from the phase in its handoff.json. A missing or unreadable file
// means none, so the suggestion stands.
func handoffStarted(sessionDir string) bool {
	b, err := os.ReadFile(filepath.Join(sessionDir, "handoff.json"))
	if err != nil {
		return false
	}
	var st struct{ Phase string }
	if json.Unmarshal(b, &st) != nil {
		return false
	}
	return st.Phase == "writing" || st.Phase == "launching" || st.Phase == "done"
}

// markRunaway sets each session's Runaway, QuietAlert and HandoffSuggested
// flags from the active alerts. Quiet and handoff are advisory, so neither
// counts as a runaway.
func markRunaway(f *live.File, store rules.Store) {
	run := store.BySession()
	for id, s := range f.Sessions {
		s.Runaway, s.QuietAlert, s.HandoffSuggested = false, false, false
		for _, a := range run[id] {
			switch a.Rule {
			case rules.Quiet:
				s.QuietAlert = true
			case rules.Handoff:
				s.HandoffSuggested = true
			default:
				s.Runaway = true
			}
		}
	}
}

// paceStatus reads the stored pace and today's spend; it takes no lock, so the
// stale path in Evaluate can use it too.
func (d Deps) paceStatus(now time.Time) pace.Status {
	p := d.Ad.Paths
	clock := spend.NewClock(d.Cfg.Spend.Day)
	day := clock.Day(now)
	today := spend.ReadToday(p.Today(), day)
	start, _ := clock.DayStart(day)
	return pace.Compute(pace.Read(p.Pace()), day, today.USD, pace.MinuteOfDay(now, start), d.Cfg.Pace)
}

type ruleInfo struct {
	session *live.Session
	agent   string
	dur     time.Duration
	usd     float64
	step    float64
	ratio   float64
	cap     float64
	ctx     float64
}

// dispatchAlert starts notify-dispatch with every value its template needs
// passed as a flag (LESSONS L-38).
func (d Deps) dispatchAlert(a rules.Alert, in ruleInfo, now time.Time) {
	if d.Ad.Spawner == nil || d.Exe == "" || d.Cfg.Notify.Backend == "off" {
		return
	}
	r := notify.Request{Kind: notify.Kind(a.Rule), Edge: a.Since.UTC().Format(time.RFC3339Nano), AlertID: a.ID}
	if in.session != nil {
		r.Session, r.Repo, r.Task = in.session.ID, in.session.Repo, in.session.Task
	}
	dur, usd, step, ratio, capUSD, ctx := in.dur, in.usd, in.step, in.ratio, in.cap, in.ctx
	switch notify.Kind(a.Rule) {
	case notify.SubagentRuntime:
		lim := d.Cfg.Rules.SubagentRuntime
		r.Agent, r.Duration, r.LimitDur = agentName(in.agent), &dur, &lim
	case notify.SubagentCost:
		lim := d.Cfg.Rules.SubagentCost
		r.Agent, r.USD, r.LimitUSD, r.Estimated = agentName(in.agent), &usd, &lim, true
	case notify.SessionCost:
		r.Step = &step
	case notify.Quiet:
		r.Duration = &dur
	case notify.DailyPace:
		r.USD, r.Ratio = &usd, &ratio
	case notify.DailyBudget:
		r.USD, r.CapUSD = &usd, &capUSD
	case notify.Handoff:
		r.CtxPct = &ctx
	}
	_ = d.Ad.Spawner.Spawn(d.Exe, append([]string{"notify-dispatch"}, r.Args()...)...)
}

func agentName(t string) string {
	if t == "" {
		return "subagent"
	}
	return t
}

// retract asks a dispatcher to remove a group's notice.
func (d Deps) retract(group string) {
	if d.Ad.Spawner == nil || d.Exe == "" || d.Cfg.Notify.Backend == "off" || group == "" {
		return
	}
	_ = d.Ad.Spawner.Spawn(d.Exe, "notify-dispatch", "--retract", "--group", group)
}

// retractCleared retracts the attention notice when needs-you or done ends. A
// move between the two keeps the notice: the new state's own dispatch replaces
// it in place, and retracting here could remove the notice it just posted.
func (d Deps) retractCleared(f live.File, before map[string]string) {
	attention := func(state string) bool { return state == live.NeedsYou || state == live.Done }
	for id, was := range before {
		s := f.Sessions[id]
		if s == nil {
			continue
		}
		if attention(was) && !attention(s.State) {
			d.retract(notify.Group(notify.NeedsYou, id))
		}
	}
}

// readSnap is the session's status line snapshot; ok is false when it is
// missing or belongs to another session. ledger.SnapshotOwnUSD makes the fork
// decision for spend, shared with notices.
func (d Deps) readSnap(s *live.Session) (statusline.Snapshot, bool) {
	snap, ok := statusline.ReadSnapshot(d.Ad.Paths.StatuslineSnap(s.ID))
	if !ok || snap.SessionID != s.ID {
		return statusline.Snapshot{}, false
	}
	return snap, true
}

// subagentTranscript is <dir>/<sid>/subagents/agent-<aid>.jsonl beside the main
// transcript <dir>/<sid>.jsonl (FACTS F7).
func subagentTranscript(main, aid string) string {
	if main == "" || !strings.HasSuffix(main, ".jsonl") {
		return ""
	}
	return strings.TrimSuffix(main, ".jsonl") + "/subagents/agent-" + aid + ".jsonl"
}

// smallUSD keeps a sub-dime figure readable ($0.004, not $0.00).
func smallUSD(v float64) string {
	if v < 0.1 {
		return fmt.Sprintf("$%.3f", v)
	}
	return fmt.Sprintf("$%.2f", v)
}
