package spend

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/jmvaldez/bosun/internal/ledger"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func utc(y int, m time.Month, d, h, mi int) time.Time {
	return time.Date(y, m, d, h, mi, 0, 0, time.UTC)
}

// fixture: one trusted session with two prompts, a subagent, three priced models
// and one unknown, and a total that exceeds the rows (side requests), crossing
// UTC midnight.
func fixture() ledger.Snapshot {
	s := ledger.Session{ID: "aaaaaaaa-0000-0000-0000-000000000001", Repo: "acme/api", Task: "ABC-1", Trusted: true,
		StartedAt: utc(2026, 10, 20, 23, 0), LastActivityAt: utc(2026, 10, 21, 1, 0), TotalUSD: 9.00}
	req := func(msg string, ts time.Time, model, prompt, agent string, in, out int64) ledger.Request {
		return ledger.Request{MessageID: msg, RequestID: "r-" + msg, SessionID: s.ID, PromptID: prompt, AgentID: agent, Model: model, TS: ts, Input: in, Output: out}
	}
	return ledger.Snapshot{
		Sessions: map[string]ledger.Session{s.ID: s},
		Requests: []ledger.Request{
			req("m1", utc(2026, 10, 20, 23, 30), "claude-sonnet-5-5", "p1", "", 1_000_000, 0),   // $2.00
			req("m2", utc(2026, 10, 21, 0, 30), "claude-opus-5-5", "p2", "", 0, 100_000),        // $2.00
			req("m3", utc(2026, 10, 21, 0, 45), "claude-haiku-5-5", "p2", "ag1", 1_000_000, 0),  // $0.50 (haiku 5.5 long-context rate: input over 100k)
			req("m4", utc(2026, 10, 21, 0, 50), "claude-nonexistent-0", "p2", "", 1_000_000, 0), // fallback $3.00, estimated
		},
		Prompts:   map[string]ledger.Prompt{"p1": {ID: "p1", SessionID: s.ID, N: 1}, "p2": {ID: "p2", SessionID: s.ID, N: 2}},
		Subagents: map[string]ledger.Subagent{s.ID + "/ag1": {SessionID: s.ID, AgentID: "ag1", AgentType: "Explore"}},
	}
}

func total(r Result) float64 { return r.Total.USD }

// Every grouping sums the same contributions, so the total is OwnUSD no matter
// how the rows are cut, and the unattributed remainder is what makes it so.
func TestEveryGroupingTotalsOwnUSD(t *testing.T) {
	cs := Contributions(fixture())
	for _, by := range Groupings {
		r, err := Group(cs, by, NewClock("utc"))
		if err != nil {
			t.Fatalf("%s: %v", by, err)
		}
		if !near(total(r), 9.00) {
			t.Errorf("--by %s total = %v, want OwnUSD 9.00", by, total(r))
		}
		var sum float64
		for _, row := range r.Rows {
			sum += row.USD
		}
		if !near(sum, 9.00) {
			t.Errorf("--by %s rows sum to %v", by, sum)
		}
	}
}

func TestPromptRowsPlusUnattributedEqualOwnUSD(t *testing.T) {
	r, _ := Group(Contributions(fixture()), "prompt", NewClock("utc"))
	got := map[string]float64{}
	for _, row := range r.Rows {
		got[row.Key] = row.USD
	}
	// Rows priced per their own model: p1 sonnet $2.00; p2 opus $2.00 + haiku $0.50 + fallback $3.00.
	if !near(got["aaaaaaaa prompt #1"], 2.00) || !near(got["aaaaaaaa prompt #2"], 5.50) {
		t.Errorf("prompt rows = %v", got)
	}
	if !near(got["aaaaaaaa unattributed"], 1.50) {
		t.Errorf("unattributed = %v, want 9.00 - 7.50 = 1.50", got["aaaaaaaa unattributed"])
	}
}

func TestUnattributedCanBeNegativeAndNothingIsScaled(t *testing.T) {
	snap := fixture()
	s := snap.Sessions["aaaaaaaa-0000-0000-0000-000000000001"]
	s.TotalUSD = 6.00 // rows add up to 7.50
	snap.Sessions[s.ID] = s
	r, _ := Group(Contributions(snap), "model", NewClock("utc"))
	got := map[string]float64{}
	for _, row := range r.Rows {
		got[row.Key] = row.USD
	}
	// The remainder follows the session's subagent rows (haiku, ag1) instead of
	// sitting in its own row, and no real row is scaled.
	if !near(got["claude-haiku-5-5"], 0.50-1.50) || !near(got["claude-opus-5-5"], 2.00) || !near(r.Total.USD, 6.00) {
		t.Errorf("model rows = %v total %v", got, r.Total.USD)
	}
}

func TestModelRowsPricedPerModelAndFlagEstimates(t *testing.T) {
	r, _ := Group(Contributions(fixture()), "model", NewClock("utc"))
	by := map[string]Row{}
	for _, row := range r.Rows {
		by[row.Key] = row
	}
	// haiku is the subagent's model, so it carries the $1.50 remainder, as est.
	if !near(by["claude-sonnet-5-5"].USD, 2) || !near(by["claude-haiku-5-5"].USD, 0.50+1.50) || !near(by["claude-opus-5-5"].USD, 2) {
		t.Errorf("rows = %+v", by)
	}
	if !by["claude-nonexistent-0"].Est || by["claude-sonnet-5-5"].Est || !near(by["claude-haiku-5-5"].EstUSD, 1.50) {
		t.Error("the unknown model's row and the apportioned remainder are estimates; real rows are not")
	}
}

func TestSubagentGroupingSplitsMainAndSubagents(t *testing.T) {
	r, _ := Group(Contributions(fixture()), "subagent", NewClock("utc"))
	got := map[string]float64{}
	for _, row := range r.Rows {
		got[row.Key] = row.USD
	}
	if !near(got["aaaaaaaa Explore#ag1"], 0.50+1.50) || !near(got["aaaaaaaa main"], 7.00) {
		t.Errorf("rows = %v", got)
	}
}

// LESSONS L-36 and SPEC acceptance: a session crossing midnight splits by row
// timestamps, and switching spend.day moves rows between days.
func TestDaySplitAcrossMidnightInBothModes(t *testing.T) {
	cs := Contributions(fixture())
	day := func(mode string, loc *time.Location) map[string]float64 {
		c := NewClock(mode)
		if loc != nil {
			c.Loc = loc
		}
		r, _ := Group(cs, "day", c)
		out := map[string]float64{}
		for _, row := range r.Rows {
			out[row.Key] = row.USD
		}
		return out
	}
	// UTC: m1 on the 20th ($2.00); m2..m4 and the remainder (apportioned to m3's bucket, 00:45 on the 21st) on the 21st.
	got := day("utc", nil)
	if !near(got["2026-10-20"], 2.00) || !near(got["2026-10-21"], 7.00) {
		t.Errorf("utc days = %v", got)
	}
	// A negative offset (UTC-8, like Pacific): 23:30Z is 15:30 on the 20th, 00:30Z is 16:30 on the 20th,
	// and the remainder at 00:45Z is 16:45 on the 20th: everything lands on the 20th.
	pst := time.FixedZone("PST", -8*3600)
	got = day("local", pst)
	if len(got) != 1 || !near(got["2026-10-20"], 9.00) {
		t.Errorf("UTC-8 days = %v", got)
	}
	// A positive offset (UTC+9): 23:30Z is 08:30 on the 21st, so all of it is the 21st.
	jst := time.FixedZone("JST", 9*3600)
	got = day("local", jst)
	if len(got) != 1 || !near(got["2026-10-21"], 9.00) {
		t.Errorf("UTC+9 days = %v", got)
	}
}

func TestSinceBoundsAreUTCInstants(t *testing.T) {
	pst := time.FixedZone("PST", -8*3600)
	c := Clock{Mode: "local", Loc: pst}
	now := utc(2026, 10, 21, 3, 0) // 19:00 on the 20th in PST
	since, err := c.Since("today", now)
	if err != nil {
		t.Fatal(err)
	}
	if !since.Equal(utc(2026, 10, 20, 8, 0)) { // midnight PST on the 20th
		t.Errorf("today starts at %v", since)
	}
	if got, _ := c.Since("7d", now); !got.Equal(utc(2026, 10, 14, 8, 0)) {
		t.Errorf("7d starts at %v", got)
	}
	if got, _ := c.Since("2026-10-01", now); !got.Equal(utc(2026, 10, 1, 8, 0)) {
		t.Errorf("date starts at %v", got)
	}
	if got, _ := c.Since("", now); !got.IsZero() {
		t.Error("empty since should be unbounded")
	}
	for _, bad := range []string{"soon", "0d", "-3d", "2026-13-01"} {
		if _, err := c.Since(bad, now); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestFilterBySinceAndTask(t *testing.T) {
	cs := Contributions(fixture())
	got := Filter(cs, utc(2026, 10, 21, 0, 0), "")
	r, _ := Group(got, "model", NewClock("utc"))
	if !near(r.Total.USD, 7.00) { // m2..m4 plus the unattributed on the 21st
		t.Errorf("since total = %v", r.Total.USD)
	}
	if got := Filter(cs, time.Time{}, "abc-1"); len(got) != len(cs) {
		t.Error("task filter should be case-insensitive")
	}
	if got := Filter(cs, time.Time{}, "OPS-9"); len(got) != 0 {
		t.Error("task filter let other tasks through")
	}
}

func TestUntrustedSessionIsAllEstimate(t *testing.T) {
	snap := fixture()
	s := snap.Sessions["aaaaaaaa-0000-0000-0000-000000000001"]
	s.Trusted, s.TotalUSD = false, 123 // a stale stored total must not matter
	snap.Sessions[s.ID] = s
	r, _ := Group(Contributions(snap), "task", NewClock("utc"))
	if !near(r.Total.USD, 7.50) || !r.Total.Est || !near(r.Total.EstUSD, 7.50) {
		t.Errorf("total = %+v", r.Total)
	}
}

func TestForkSubtractsInheritanceOnce(t *testing.T) {
	snap := fixture()
	s := snap.Sessions["aaaaaaaa-0000-0000-0000-000000000001"]
	s.InheritedUSD = 4.00
	snap.Sessions[s.ID] = s
	r, _ := Group(Contributions(snap), "session", NewClock("utc"))
	if !near(r.Total.USD, 5.00) {
		t.Errorf("total = %v, want 9 - 4", r.Total.USD)
	}
}

func TestDayTotalForSegmentAndSamples(t *testing.T) {
	cs := Contributions(fixture())
	clock := NewClock("utc")
	today := DayTotal(cs, clock, "2026-10-21", time.Time{})
	if !near(today.USD, 7.00) || !near(today.EstUSD, 4.50) || !near(today.EstShare, 4.5/7.0) {
		t.Errorf("today = %+v", today)
	}
	// At 00:40 only m2 has happened that day; the remainder lands with m3 at 00:45.
	if soFar := DayTotal(cs, clock, "2026-10-21", utc(2026, 10, 21, 0, 40)); !near(soFar.USD, 2.00) {
		t.Errorf("so far at 00:40 = %+v", soFar)
	}
}

func TestUnknownGroupingErrors(t *testing.T) {
	if _, err := Group(nil, "colour", NewClock("utc")); err == nil {
		t.Error("an unknown grouping must error, not alias another")
	}
	if IsGrouping("") || !IsGrouping("subagent") {
		t.Error("IsGrouping")
	}
}

// Every --by returns its own shape (LESSONS L-47): different keys.
func TestGroupingsHaveDistinctShapes(t *testing.T) {
	cs := Contributions(fixture())
	seen := map[string]string{}
	for _, by := range Groupings {
		r, _ := Group(cs, by, NewClock("utc"))
		var keys []string
		for _, row := range r.Rows {
			keys = append(keys, row.Key)
		}
		sig := strings.Join(keys, "|")
		if prev, dup := seen[sig]; dup {
			t.Errorf("--by %s returns the same rows as --by %s: %s", by, prev, sig)
		}
		seen[sig] = by
	}
}

func TestCSVAndJSONCarryTheSameRows(t *testing.T) {
	r, _ := Group(Contributions(fixture()), "model", NewClock("utc"))
	var jb, cb bytes.Buffer
	if err := JSON(&jb, r); err != nil {
		t.Fatal(err)
	}
	if err := CSV(&cb, r); err != nil {
		t.Fatal(err)
	}
	var back Result
	if err := json.Unmarshal(jb.Bytes(), &back); err != nil {
		t.Fatal(err)
	}
	recs, err := csv.NewReader(&cb).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != len(back.Rows)+2 { // header, rows, total
		t.Fatalf("csv has %d records for %d rows", len(recs), len(back.Rows))
	}
	for i, row := range back.Rows {
		if recs[i+1][0] != row.Key {
			t.Errorf("row %d: csv key %q, json key %q", i, recs[i+1][0], row.Key)
		}
	}
	if recs[len(recs)-1][0] != "TOTAL" {
		t.Error("csv lacks the total row")
	}
}

func TestTextShowsEstimateMarkers(t *testing.T) {
	r, _ := Group(Contributions(fixture()), "model", NewClock("utc"))
	var b bytes.Buffer
	Text(&b, r)
	out := b.String()
	if !strings.Contains(out, "TOTAL") || !strings.Contains(out, "est.") || !strings.Contains(out, "API-rate estimate") {
		t.Errorf("output:\n%s", out)
	}
}

func TestAddedBackRowsAreEstimates(t *testing.T) {
	snap := fixture()
	s := snap.Sessions["aaaaaaaa-0000-0000-0000-000000000001"]
	s.CoveredFrom = utc(2026, 10, 21, 0, 40) // m1 and m2 predate it
	snap.Sessions[s.ID] = s
	r, _ := Group(Contributions(snap), "model", NewClock("utc"))
	by := map[string]Row{}
	for _, row := range r.Rows {
		by[row.Key] = row
	}
	if !by["claude-sonnet-5-5"].Est || !by["claude-opus-5-5"].Est {
		t.Error("rows before CoveredFrom must be marked est.")
	}
	if !near(by["claude-haiku-5-5"].EstUSD, 1.50) {
		t.Error("the covered haiku row stays trusted; only its apportioned remainder is est.")
	}
	if !near(r.Total.EstUSD, 2.00+2.00+3.00+1.50) {
		t.Errorf("est share dollars = %v", r.Total.EstUSD)
	}
}

// Rolled-up history still counts: the session total is unchanged, so the
// unattributed remainder is computed against the rolled rows too.
func TestRollupsCountAsRowsAndKeepUnattributedRight(t *testing.T) {
	snap := fixture()
	s := snap.Sessions["aaaaaaaa-0000-0000-0000-000000000001"]
	snap.Rollups = []ledger.Rollup{{SessionID: s.ID, Day: "2025-01-01", Model: "claude-opus-5-5", TS: utc(2025, 1, 1, 9, 0), Requests: 40, Input: 1_000_000, USD: 4.00}}
	s.TotalUSD = 13.00 // 9.00 plus the 4.00 that has been rolled up
	snap.Sessions[s.ID] = s
	cs := Contributions(snap)
	r, _ := Group(cs, "session", NewClock("utc"))
	if !near(r.Total.USD, 13.00) {
		t.Errorf("total = %v, want the session total", r.Total.USD)
	}
	byDay, _ := Group(cs, "day", NewClock("utc"))
	got := map[string]float64{}
	for _, row := range byDay.Rows {
		got[row.Key] = row.USD
	}
	if !near(got["2025-01-01"], 4.00) {
		t.Errorf("the rolled-up day = %v", got)
	}
	var unattr float64
	for _, c := range cs {
		if c.Kind == Unattributed {
			unattr = c.USD
		}
	}
	if !near(unattr, 1.50) { // unchanged from before the rollup
		t.Errorf("unattributed = %v", unattr)
	}
}

// D-28: a long session whose status line total exceeds what its transcripts
// record. The gap is split by day, model and agent over the subagent rows, so
// yesterday's money is not booked on today, and nothing real is scaled.
func TestRemainderIsApportionedByDayModelAndAgent(t *testing.T) {
	s := ledger.Session{ID: "bbbbbbbb-0000-0000-0000-000000000002", Repo: "acme/api", Task: "ABC-2", Trusted: true,
		LastActivityAt: utc(2026, 10, 24, 15, 0), TotalUSD: 40.00}
	req := func(msg string, ts time.Time, model, agent string, in int64) ledger.Request {
		return ledger.Request{MessageID: msg, RequestID: "r-" + msg, SessionID: s.ID, AgentID: agent, Model: model, TS: ts, Input: in}
	}
	snap := ledger.Snapshot{
		Sessions: map[string]ledger.Session{s.ID: s},
		Requests: []ledger.Request{
			req("main1", utc(2026, 10, 23, 10, 0), "claude-opus-5-5", "", 1_000_000),     // $4.00
			req("sub1", utc(2026, 10, 23, 11, 0), "claude-sonnet-5-5", "ag1", 1_000_000), // $2.00 on the 23rd
			req("sub2", utc(2026, 10, 24, 12, 0), "claude-haiku-5-5", "ag2", 1_000_000),  // $0.50 on the 24th (4x less)
		},
		Subagents: map[string]ledger.Subagent{s.ID + "/ag1": {SessionID: s.ID, AgentID: "ag1", AgentType: "Explore"}, s.ID + "/ag2": {SessionID: s.ID, AgentID: "ag2", AgentType: "Plan"}},
	}
	cs := Contributions(snap)
	var rows, rem float64
	for _, c := range cs {
		if c.Kind == RequestRow {
			rows += c.USD
		} else {
			rem += c.USD
			if !c.Est {
				t.Errorf("an apportioned part is not marked est.: %+v", c)
			}
		}
	}
	if !near(rows+rem, 40.00) {
		t.Fatalf("rows %v + remainder %v != 40", rows, rem)
	}
	// Totals unchanged by grouping; days and models follow the subagent rows.
	clk := NewClock("utc")
	byDay, _ := Group(cs, "day", clk)
	days := map[string]float64{}
	for _, r := range byDay.Rows {
		days[r.Key] = r.USD
	}
	subSonnet, subHaiku := 2.00, 0.50
	main := rows - subSonnet - subHaiku
	wantDay23 := main + subSonnet + rem*subSonnet/(subSonnet+subHaiku)
	wantDay24 := subHaiku + rem*subHaiku/(subSonnet+subHaiku)
	if !near(days["2026-10-23"], wantDay23) || !near(days["2026-10-24"], wantDay24) || !near(byDay.Total.USD, 40.00) {
		t.Errorf("days = %v, want 23rd %.4f and 24th %.4f", days, wantDay23, wantDay24)
	}
	byModel, _ := Group(cs, "model", clk)
	models := map[string]float64{}
	for _, r := range byModel.Rows {
		models[r.Key] = r.USD
	}
	if _, ok := models["unattributed"]; ok {
		t.Errorf("a session with rows must not leave an unattributed model row: %v", models)
	}
	if !near(models["claude-sonnet-5-5"], subSonnet+rem*subSonnet/(subSonnet+subHaiku)) || !near(models["claude-haiku-5-5"], subHaiku+rem*subHaiku/(subSonnet+subHaiku)) {
		t.Errorf("models = %v", models)
	}
	bySub, _ := Group(cs, "subagent", clk)
	subs := map[string]float64{}
	for _, r := range bySub.Rows {
		subs[r.Key] = r.USD
	}
	if !near(subs["bbbbbbbb Explore#ag1"], subSonnet+rem*subSonnet/(subSonnet+subHaiku)) || !near(subs["bbbbbbbb main"], main) {
		t.Errorf("subagents = %v", subs)
	}
	// With no subagent the remainder follows all rows; with no rows it stays one
	// remainder on the last activity day.
	noSub := snap
	noSub.Requests = snap.Requests[:1]
	for _, c := range Contributions(noSub) {
		if c.Kind == Unattributed && (c.Req.Model != "claude-opus-5-5" || c.Req.AgentID != "") {
			t.Errorf("no-subagent remainder = %+v", c)
		}
	}
	empty := snap
	empty.Requests = nil
	cs = Contributions(empty)
	if len(cs) != 1 || cs[0].Kind != Unattributed || !near(cs[0].USD, 40) || cs[0].TS != utc(2026, 10, 24, 15, 0) {
		t.Errorf("no rows: %+v", cs)
	}
}

func TestRenderDropsTerminalEscapesFromKeys(t *testing.T) {
	snap := fixture()
	sess := snap.Sessions["aaaaaaaa-0000-0000-0000-000000000001"]
	sess.Repo = "evil\x1b]52;c;aGk=\x07"
	snap.Sessions[sess.ID] = sess
	r, err := Group(Contributions(snap), "repo", NewClock("utc"))
	if err != nil {
		t.Fatal(err)
	}
	var tb, cb bytes.Buffer
	Text(&tb, r)
	if err := CSV(&cb, r); err != nil {
		t.Fatal(err)
	}
	for name, out := range map[string]string{"text": tb.String(), "csv": cb.String()} {
		if strings.ContainsAny(out, "\x1b\x07") || !strings.Contains(out, "evil]52;c;aGk=") {
			t.Errorf("%s output: %q", name, out)
		}
	}
}
