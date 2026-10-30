package spend

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/jmvaldez/bosun/internal/ledger"
)

// D-31: with live cost off, an untrusted session's dollars are hidden and left
// out of every total; a trusted session is untouched, estimated parts included.
func TestRedactHidesUntrustedAndKeepsTheRestExact(t *testing.T) {
	snap := fixture()
	u := ledger.Session{ID: "bbbbbbbb-0000-0000-0000-000000000002", Repo: "acme/web", Task: "ABC-2", StartedAt: utc(2026, 10, 21, 1, 0)}
	snap.Sessions[u.ID] = u
	snap.Requests = append(snap.Requests, ledger.Request{MessageID: "u1", RequestID: "r-u1", SessionID: u.ID,
		Model: "claude-sonnet-5-5", TS: utc(2026, 10, 21, 1, 5), Input: 1_000_000})
	clk := NewClock("utc")
	all, _ := Group(Contributions(snap), "task", clk)
	if !near(all.Total.USD, 11.00) || all.Total.Redacted != 0 {
		t.Fatalf("unredacted total = %+v", all.Total)
	}
	r, _ := Group(Redact(Contributions(snap)), "task", clk)
	if !near(r.Total.USD, 9.00) || r.Total.Redacted != 1 || r.Total.Sessions != 2 {
		t.Errorf("total = %+v, want $9.00 with one session hidden", r.Total)
	}
	for _, row := range r.Rows {
		switch row.Key {
		case "ABC-1":
			if row.Redacted != 0 || !near(row.USD, 9.00) || !row.Est {
				t.Errorf("trusted row = %+v", row)
			}
		case "ABC-2":
			if row.Redacted != 1 || row.USD != 0 || row.Est || row.Requests != 1 {
				t.Errorf("hidden row = %+v", row)
			}
		}
	}
	day := DayTotal(Redact(Contributions(snap)), clk, "2026-10-21", time.Time{})
	if day.Redacted != 1 {
		t.Errorf("day = %+v", day)
	}
	var b bytes.Buffer
	Text(&b, r)
	for _, want := range []string{"\u2014", "$9.00 est. +\u2014", "1 session without an exact cost; live cost is off"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, b.String())
		}
	}
	for _, c := range Redact(Contributions(snap)) {
		if c.Redacted && (c.USD != 0 || c.Est) {
			t.Errorf("redacted contribution keeps a figure: %+v", c)
		}
	}
}

// An orphan row (no session row yet) has no trusted figure either.
func TestRedactHidesOrphanRows(t *testing.T) {
	snap := fixture()
	snap.Requests = append(snap.Requests, ledger.Request{MessageID: "o1", RequestID: "r-o1", SessionID: "orphan",
		Model: "claude-sonnet-5-5", TS: utc(2026, 10, 21, 2, 0), Input: 1_000_000})
	r, _ := Group(Redact(Contributions(snap)), "session", NewClock("utc"))
	if !near(r.Total.USD, 9.00) || r.Total.Redacted != 1 {
		t.Errorf("total = %+v", r.Total)
	}
}

// A fully hidden row is a bare dash, never $0.00.
func TestTextShowsADashForAFullyHiddenRow(t *testing.T) {
	snap := fixture()
	s := snap.Sessions["aaaaaaaa-0000-0000-0000-000000000001"]
	s.Trusted = false
	snap.Sessions[s.ID] = s
	r, _ := Group(Redact(Contributions(snap)), "task", NewClock("utc"))
	var b bytes.Buffer
	Text(&b, r)
	if strings.Contains(b.String(), "$0.00") || !strings.Contains(b.String(), "\u2014") {
		t.Errorf("output:\n%s", b.String())
	}
}
