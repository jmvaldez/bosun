package eval

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/jmvaldez/bosun/internal/ledger"
	"github.com/jmvaldez/bosun/internal/live"
	"github.com/jmvaldez/bosun/internal/notify"
	"github.com/jmvaldez/bosun/internal/pace"
	"github.com/jmvaldez/bosun/internal/rules"
	"github.com/jmvaldez/bosun/internal/spend"
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
					sub.Cost = newTracker()
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
		if step := cfg.Rules.SessionCost; step > 0 {
			if own, ok := d.sessionOwn(s); ok {
				if level := int(own / step); level >= 1 {
					add(rules.Cond{Rule: rules.SessionCost, Scope: s.ID, Session: s.ID, Level: level,
						Detail: "session " + smallUSD(own)},
						ruleInfo{session: s, step: float64(level) * step})
				}
			}
		}
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

	run := store.BySession()
	for id, s := range f.Sessions {
		s.Runaway, s.QuietAlert = false, false
		for _, a := range run[id] {
			if a.Rule == rules.Quiet {
				s.QuietAlert = true
			} else {
				s.Runaway = true
			}
		}
	}
	res := ruleResult{pace: st}
	for _, a := range store.Active() {
		res.alerts = append(res.alerts, *a)
	}
	return res
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
	dur, usd, step, ratio, capUSD := in.dur, in.usd, in.step, in.ratio, in.cap
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

// sessionOwn is the session's own spend from its status line snapshot;
// ledger.SnapshotOwnUSD makes the fork decision, shared with notices.
func (d Deps) sessionOwn(s *live.Session) (float64, bool) {
	b, err := readSmall(filepath.Join(d.Ad.Paths.Session(s.ID), "statusline.json"))
	if err != nil {
		return 0, false
	}
	snap, ok := statuslineSnap(b)
	if !ok || snap.SessionID != s.ID {
		return 0, false
	}
	return ledger.SnapshotOwnUSD(snap, s.Forked), true
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
