package live

import "time"

// AgentRow is the slice of an agents.json row the reconciler needs.
type AgentRow struct {
	Found      bool
	Background bool // a pid-less row (D-11): it proves the session is listed, but never moves its state
	Status     string
	WaitingFor string
}

// idleGrace keeps a freshly submitted prompt from being read as idle by an
// agents.json fetch that raced it.
const idleGrace = time.Second

// notWaitingReadsToLeave is D-7: an interactive "No" at a permission prompt
// fires no hook, so needs-you ends when agents.json says the session is not
// waiting on two consecutive fetches.
const notWaitingReadsToLeave = 2

// ReconcileAgents applies one agents.json fetch. Hooks win on a live session;
// the two exceptions below are the only places agents.json overrides them.
// Each distinct fetch counts once, however many evaluations see it.
func (s *Session) ReconcileAgents(row AgentRow, fetched time.Time) {
	if !fetched.After(s.AgentsFetch) {
		return
	}
	s.AgentsFetch = fetched
	if !row.Found {
		s.MissingReads++
		return
	}
	s.MissingReads = 0
	if row.Background {
		return // the idle rules do not apply to background rows
	}
	s.WaitingFor = ""
	if row.Status == "waiting" {
		s.WaitingFor = row.WaitingFor
	}
	switch s.State {
	case NeedsYou:
		if !fetched.After(s.StateSince) {
			return
		}
		if row.Status == "waiting" {
			s.NotWaitingReads = 0
			return
		}
		s.NotWaitingReads++
		if s.NotWaitingReads >= notWaitingReadsToLeave {
			s.NotWaitingReads = 0
			s.NeedsAID = ""
			s.setState(Idle, fetched)
		}
	case Working:
		// A Ctrl+C fires no hook; agents.json noticing idle is the only signal.
		// Pending background work or a running subagent keeps the session working.
		if row.Status == "idle" && fetched.After(s.StateSince.Add(idleGrace)) && !s.BGPending && len(s.Subs) == 0 {
			s.setState(Idle, fetched)
		}
	}
}

// MarkSeen turns done into idle once the user has looked at the pane.
func (s *Session) MarkSeen(now time.Time) {
	if s.State == Done {
		s.setState(Idle, now)
	}
}
