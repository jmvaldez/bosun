// Package spend turns ledger rows into the views: day, task, repo, session,
// prompt, request, model and subagent. Every view is built from the same
// contributions, so each one sums to the same total.
package spend

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jmvaldez/bosun/internal/ledger"
	"github.com/jmvaldez/bosun/internal/plugstate"
)

// Clock decides where one day ends and the next begins (`spend.day`).
type Clock struct {
	Mode string // "local" or "utc"
	Loc  *time.Location
}

// NewClock builds a Clock; local mode uses the process's location.
func NewClock(mode string) Clock {
	if mode == "utc" {
		return Clock{Mode: "utc", Loc: time.UTC}
	}
	return Clock{Mode: "local", Loc: time.Local}
}

// Day returns the YYYY-MM-DD a timestamp falls on. Rows are always placed by
// their own timestamp (LESSONS L-36).
func (c Clock) Day(t time.Time) string { return t.In(c.Loc).Format("2006-01-02") }

// DayStart returns the instant a day begins. Day bounds are converted to UTC
// instants before comparing against timestamps, never compared as strings.
func (c Clock) DayStart(day string) (time.Time, error) {
	t, err := time.ParseInLocation("2006-01-02", day, c.Loc)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

// Since parses a --since value: "today", "7d", "24h", or a YYYY-MM-DD date.
// Empty means no bound.
func (c Clock) Since(v string, now time.Time) (time.Time, error) {
	switch {
	case v == "":
		return time.Time{}, nil
	case v == "today":
		return c.DayStart(c.Day(now))
	case strings.HasSuffix(v, "d"):
		var n int
		if _, err := fmt.Sscanf(v, "%dd", &n); err != nil || n <= 0 {
			return time.Time{}, fmt.Errorf("bad --since %q", v)
		}
		// N days back from the start of today, so "7d" covers today and the six before.
		start, err := c.DayStart(c.Day(now))
		if err != nil {
			return time.Time{}, err
		}
		return start.AddDate(0, 0, -(n - 1)), nil
	}
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
		return now.Add(-d), nil
	}
	t, err := c.DayStart(v)
	if err != nil {
		return time.Time{}, fmt.Errorf("bad --since %q (want today, 7d, 24h, or YYYY-MM-DD)", v)
	}
	return t, nil
}

// Kind tells a request row from the unattributed remainder.
type Kind int

const (
	RequestRow Kind = iota
	Unattributed
)

// Contribution is one dollar amount with everything the groupings need.
type Contribution struct {
	Kind    Kind
	Session ledger.Session
	Req     ledger.Request // zero for Unattributed
	TS      time.Time
	USD     float64
	Est     bool
	// Redacted marks an amount live cost hides (see Redact): USD is zero, so
	// every sum leaves it out, and views draw it as a dash.
	Redacted bool
	// PromptN is the prompt number for request rows; 0 means none.
	PromptN   int
	AgentType string
}

// Contributions builds every contribution from the snapshot, for all time.
//
// Request rows (rolled-up history included) are priced per their own model.
// A trusted session's remainder, OwnUSD minus its rows, can be negative. It is
// real money no transcript row records (subagent messages without a final
// usage line, requests never logged), so apportion splits it over the
// session's days rather than booking it on one (D-28). Rows are never scaled.
// An untrusted session's total is its rows, so it has no remainder. The clock
// decides what a day is; the default is UTC.
func Contributions(snap ledger.Snapshot, clock ...Clock) []Contribution {
	clk := NewClock("utc")
	if len(clock) > 0 && clock[0].Loc != nil {
		clk = clock[0]
	}
	rowsBySession := map[string][]ledger.Request{}
	for _, r := range snap.Requests {
		rowsBySession[r.SessionID] = append(rowsBySession[r.SessionID], r)
	}
	var out []Contribution
	sessionRows := map[string][]Contribution{}
	// Rolled-up history (rows past the retention horizon): aggregated rows that
	// carry their price at roll-up time. They count as rows, so a session's
	// unattributed remainder stays right after its old requests are pruned.
	rolled := map[string]float64{}
	for _, r := range snap.Rollups {
		s := snap.Sessions[r.SessionID]
		s.ID = r.SessionID
		rc := Contribution{Kind: RequestRow, Session: s, TS: r.TS, USD: r.USD, Est: r.Est || !s.Trusted,
			Req: ledger.Request{MessageID: "rollup:" + r.Day + ":" + r.Model, RequestID: r.SessionID, SessionID: r.SessionID,
				Model: r.Model, TS: r.TS, Input: r.Input, Output: r.Output, CacheRead: r.Cache}}
		out = append(out, rc)
		sessionRows[r.SessionID] = append(sessionRows[r.SessionID], rc)
		rolled[r.SessionID] += r.USD
	}
	ids := make([]string, 0, len(snap.Sessions))
	for id := range snap.Sessions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		s := snap.Sessions[id]
		sum := rolled[id]
		for _, r := range rowsBySession[id] {
			p := ledger.PriceRequest(r)
			sum += p.USD
			// Rows before CoveredFrom are an estimate added back to the total (D-21).
			uncovered := !s.CoveredFrom.IsZero() && r.TS.Before(s.CoveredFrom)
			c := Contribution{Kind: RequestRow, Session: s, Req: r, TS: r.TS, USD: p.USD, Est: p.Estimated || !s.Trusted || uncovered}
			if pr, ok := snap.Prompts[r.PromptID]; ok && pr.SessionID == id {
				c.PromptN = pr.N
			}
			if r.AgentID != "" {
				c.AgentType = snap.Subagents[id+"/"+r.AgentID].AgentType
			}
			out = append(out, c)
			sessionRows[id] = append(sessionRows[id], c)
		}
		if s.Trusted {
			out = append(out, apportion(s, ledger.OwnUSD(s)-sum, sessionRows[id], clk)...)
		}
		delete(rowsBySession, id)
	}
	// Rows whose session has no row yet (ingest in progress) still count.
	var orphans []string
	for id := range rowsBySession {
		orphans = append(orphans, id)
	}
	sort.Strings(orphans)
	for _, id := range orphans {
		for _, r := range rowsBySession[id] {
			p := ledger.PriceRequest(r)
			out = append(out, Contribution{Kind: RequestRow, Session: ledger.Session{ID: id}, Req: r, TS: r.TS, USD: p.USD, Est: true})
		}
	}
	return out
}

// ForDisplay is Contributions with live cost applied: when settings.json's
// status line is not bosun's wrap, amounts with no trusted figure are redacted
// (D-31). Every reader of spend goes through it, so the bar, the popup and
// `bosun spend` agree.
func ForDisplay(snap ledger.Snapshot, clock Clock, settings string) []Contribution {
	cs := Contributions(snap, clock)
	if !plugstate.LiveCost(settings) {
		cs = Redact(cs)
	}
	return cs
}

// Redact hides every amount with no trusted figure behind it, for when live
// cost is off (D-31). A session without a trusted total (nothing from the status
// line or cost-state: a running session, a crash, one from before the install)
// is only a transcript estimate. Its contributions keep their tokens but lose
// their dollars and are marked Redacted, so totals exclude them and views show
// a dash. Trusted sessions are untouched, estimated parts and all.
func Redact(cs []Contribution) []Contribution {
	out := make([]Contribution, len(cs))
	for i, c := range cs {
		if !c.Session.Trusted {
			c.USD, c.Est, c.Redacted = 0, false, true
		}
		out[i] = c
	}
	return out
}

// apportion splits a session's unattributed remainder over (day, model, agent)
// buckets weighted by its subagent rows, or by all its rows when it has no
// subagent. With nothing to weigh by it stays one remainder at the session's
// last activity.
func apportion(s ledger.Session, rem float64, rows []Contribution, clk Clock) []Contribution {
	type bucket struct {
		w     float64
		ts    time.Time
		model string
		agent string
		kind  string
	}
	build := func(only func(Contribution) bool, keepAgent bool) (map[string]*bucket, float64) {
		bs := map[string]*bucket{}
		var total float64
		for _, c := range rows {
			if !only(c) || c.USD <= 0 {
				continue
			}
			agent, kind := "", ""
			if keepAgent {
				agent, kind = c.Req.AgentID, c.AgentType
			}
			k := clk.Day(c.TS) + "|" + c.Req.Model + "|" + agent
			b := bs[k]
			if b == nil {
				b = &bucket{model: c.Req.Model, agent: agent, kind: kind}
				bs[k] = b
			}
			b.w += c.USD
			total += c.USD
			if c.TS.After(b.ts) {
				b.ts = c.TS
			}
		}
		return bs, total
	}
	bs, total := build(func(c Contribution) bool { return c.Req.AgentID != "" }, true)
	if total <= 0 {
		bs, total = build(func(Contribution) bool { return true }, false)
	}
	if total <= 0 || rem == 0 {
		return []Contribution{{Kind: Unattributed, Session: s, TS: unattributedTime(s), USD: rem}}
	}
	keys := make([]string, 0, len(bs))
	for k := range bs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []Contribution
	var assigned float64
	for i, k := range keys {
		b := bs[k]
		share := rem * b.w / total
		if i == len(keys)-1 {
			share = rem - assigned // the parts add up to the remainder exactly
		}
		assigned += share
		out = append(out, Contribution{Kind: Unattributed, Session: s, TS: b.ts, USD: share, Est: true, AgentType: b.kind,
			Req: ledger.Request{SessionID: s.ID, Model: b.model, AgentID: b.agent}})
	}
	return out
}

func unattributedTime(s ledger.Session) time.Time {
	for _, t := range []time.Time{s.LastActivityAt, s.EndedAt, s.StartedAt} {
		if !t.IsZero() {
			return t
		}
	}
	return time.Time{}
}

// Filter keeps contributions at or after since and, when taskKey is set, from
// sessions with that task, compared case-insensitively.
func Filter(cs []Contribution, since time.Time, taskKey string) []Contribution {
	var out []Contribution
	for _, c := range cs {
		if !since.IsZero() && c.TS.Before(since) {
			continue
		}
		if taskKey != "" && !strings.EqualFold(c.Session.Task, taskKey) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// DaySum is the total spend of one day, any day: DayTotal fills it for past
// days too, and today.json stores today's.
type DaySum struct {
	Day    string  `json:"day"`
	USD    float64 `json:"usd"`
	EstUSD float64 `json:"est_usd"`
	// EstShare is EstUSD over USD, 0 when nothing was spent.
	EstShare float64 `json:"est_share"`
	// Redacted is how many sessions spent that day with their amounts hidden
	// (Redact); USD leaves them out.
	Redacted int `json:"redacted,omitempty"`
}

// DayTotal totals the contributions that fall on day; a non-zero upTo also
// drops those after it (a day so far, for pace samples).
func DayTotal(cs []Contribution, clock Clock, day string, upTo time.Time) DaySum {
	t := DaySum{Day: day}
	hidden := map[string]bool{}
	for _, c := range cs {
		if c.TS.IsZero() || clock.Day(c.TS) != day || (!upTo.IsZero() && c.TS.After(upTo)) {
			continue
		}
		if c.Redacted {
			hidden[c.Session.ID] = true
		}
		t.USD += c.USD
		if c.Est {
			t.EstUSD += c.USD
		}
	}
	t.Redacted = len(hidden)
	if t.USD > 0 {
		t.EstShare = t.EstUSD / t.USD
	}
	return t
}
