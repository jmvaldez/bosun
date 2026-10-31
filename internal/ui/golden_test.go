package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmvaldez/bosun/internal/eval"
	"github.com/jmvaldez/bosun/internal/ledger"
	"github.com/jmvaldez/bosun/internal/pace"
	"github.com/jmvaldez/bosun/internal/peek"
	"github.com/jmvaldez/bosun/internal/segment"
	"github.com/jmvaldez/bosun/internal/spend"
)

// mockNow is the owner's mock: Thursday 22 October, mid afternoon.
var mockNow = time.Date(2026, 10, 22, 14, 2, 0, 0, time.UTC)

// mockActions answers text reads for the mock fixture. The strings are what the
// mock shows; none is stored anywhere.
type mockActions struct{ fakeActions }

func (mockActions) Latest(v eval.View) peek.Info {
	switch v.ID {
	case "a1f3c9d0":
		return peek.Info{Tool: "Bash", Target: "npm test -- refunds", Pending: true}
	case "b2e4d1c8":
		return peek.Info{Tool: "AskUserQuestion", Question: "Keep idempotency key on retry?", Pending: true}
	case "d4a6f3ea":
		return peek.Info{Tool: "Edit", Target: "/code/payments-api/db/migrate/0142_refund_state.sql", Pending: true}
	}
	return peek.Info{}
}

func (mockActions) PromptLine(session, prompt string) string {
	return map[string]string{
		"a1f3c9d0/a1p1": "plan the refund state machine",
		"a1f3c9d0/a1p2": "implement handler + tests",
		"a1f3c9d0/a1p3": "run refund tests",
	}[session+"/"+prompt]
}

// mockData is the data behind build-log/mockups/2-status.png and 3-spend.png:
// six sessions, two of them waiting, a runaway subagent, a done one, a quiet
// one, an idle one; $23.40 today against a $50 budget and $187.70 over the
// week.
func mockData() Data {
	clk := spend.NewClock("utc")
	at := func(h, m, s int) time.Time { return time.Date(2026, 10, 22, h, m, s, 0, time.UTC) }
	v := func(id, task, repo, where, state string, since time.Duration, cwd string) eval.View {
		return eval.View{ID: id, State: state, Display: state, Since: mockNow.Add(-since), Where: where, Pane: "%" + id[:1], Task: task, Repo: "acme/" + repo,
			Cwd: cwd, Transcript: "/t/" + id + ".jsonl", ServerPID: 1, PID: 100}
	}
	s1 := v("a1f3c9d0", "refund-cancel", "payments-api", "payments:1.0", "needs-you", 42*time.Second, "/code/payments-api")
	s1.WaitKind = "permission"
	s1.Subagents = 1
	s1.Subs = []eval.SubView{{ID: "ag1", Type: "Explore", Runtime: 14*time.Minute + 8*time.Second, Tools: 61, EstUSD: 3.12, Alert: true}}
	s2 := v("b2e4d1c8", "webhook-retry", "payments-api", "payments:2.0", "needs-you", 6*time.Minute+15*time.Second, "/code/payments-api")
	s2.WaitKind = "question"
	s3 := v("c3b5e2d9", "cancel-flow-ui", "member-portal", "portal:2.0", "done", 2*time.Minute+3*time.Second, "/code/member-portal")
	s3.Edits = 12
	s4 := v("d4a6f3ea", "migrations", "payments-api", "payments:3.0", "working", 4*time.Minute, "/code/payments-api")
	s4.LastTool, s4.ToolOpen, s4.Subagents = "Edit", true, 1
	s4.Subs = []eval.SubView{{ID: "ag2", Type: "test-writer", Runtime: 2*time.Minute + 40*time.Second, Tools: 9, EstUSD: 0.41}}
	s5 := v("e5c7a4fb", "tf-upgrade", "infra-terraform", "infra:1.0", "quiet", 6*time.Minute, "/code/infra-terraform")
	s5.Quiet = "6m00s"
	s6 := v("f6d8b5ac", "", "pitwall", "side:1.0", "idle", time.Hour, "/code/pitwall")
	sessions := []eval.View{s1, s2, s3, s4, s5, s6}

	snap := ledger.Snapshot{Sessions: map[string]ledger.Session{}, Prompts: map[string]ledger.Prompt{}, Subagents: map[string]ledger.Subagent{}}
	var cs []spend.Contribution
	for _, sv := range sessions {
		snap.Sessions[sv.ID] = ledger.Session{ID: sv.ID, Repo: sv.Repo, Task: sv.Task, Trusted: true, StartedAt: at(9, 12, 0)}
	}
	n := 0
	req := func(sv eval.View, prompt string, pn int, agent, model string, h, m int, usdv float64, in, out, cache int64) {
		n++
		s := snap.Sessions[sv.ID]
		r := ledger.Request{MessageID: fmt.Sprintf("m%d", n), RequestID: fmt.Sprintf("r%d", n), SessionID: sv.ID, PromptID: prompt, AgentID: agent,
			Model: model, TS: at(h, m, n%60), Input: in, Output: out, CacheRead: cache}
		at := ""
		if agent != "" {
			at = map[string]string{"ag1": "Explore", "ag2": "test-writer"}[agent]
			snap.Subagents[sv.ID+"/"+agent] = ledger.Subagent{SessionID: sv.ID, AgentID: agent, AgentType: at}
		}
		cs = append(cs, spend.Contribution{Kind: spend.RequestRow, Session: s, Req: r, TS: r.TS, USD: usdv, PromptN: pn, AgentType: at})
	}
	const opus, sonnet, haiku = "claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"
	req(s1, "a1p1", 1, "", opus, 9, 15, 2.10, 52_000, 3_100, 410_000)
	req(s1, "a1p2", 2, "", opus, 9, 41, 0.84, 18_200, 2_100, 412_000)
	req(s1, "a1p2", 2, "", opus, 9, 52, 0.62, 6_000, 1_400, 430_000)
	for i := 0; i < 16; i++ {
		usdv := 0.14
		if i == 15 {
			usdv = 0.17
		}
		req(s1, "a1p2", 2, "", opus, 10, i, usdv, 6_000, 1_200, 400_000)
	}
	for i := 0; i < 38; i++ {
		usdv := 0.08
		if i == 37 {
			usdv = 0.16
		}
		req(s1, "a1p2", 2, "ag1", sonnet, 11, i, usdv, 30_000, 900, 120_000)
	}
	req(s1, "a1p3", 3, "", opus, 13, 50, 0.91, 22_000, 2_600, 380_000)
	req(s2, "b2p1", 1, "", opus, 10, 5, 2.00, 40_000, 2_500, 300_000)
	req(s2, "b2p2", 2, "", opus, 12, 20, 2.20, 44_000, 2_700, 320_000)
	req(s3, "c3p1", 1, "", haiku, 12, 0, 0.77, 60_000, 4_000, 200_000)
	req(s3, "c3p1", 1, "", sonnet, 12, 30, 1.07, 30_000, 2_000, 250_000)
	req(s4, "d4p1", 1, "", opus, 11, 10, 2.54, 48_000, 3_000, 350_000)
	for i := 0; i < 9; i++ {
		usdv := 0.04
		if i == 8 {
			usdv = 0.09
		}
		req(s4, "d4p1", 1, "ag2", sonnet, 13, i, usdv, 8_000, 600, 40_000)
	}
	req(s5, "e5p1", 1, "", opus, 9, 30, 1.50, 30_000, 2_000, 280_000)
	req(s5, "e5p1", 1, "", sonnet, 9, 50, 1.60, 28_000, 1_800, 260_000)
	req(s6, "f6p1", 1, "", opus, 8, 40, 1.45, 26_000, 1_700, 240_000)
	// Six earlier days, oldest first: Fri..Wed.
	old := ledger.Session{ID: "0ld00000", Repo: "acme/payments-api", Task: "history", Trusted: true}
	for i, v := range []float64{28.0, 7.0, 1.7, 38.0, 46.0, 43.6} {
		ts := time.Date(2026, 10, 16+i, 12, 0, 0, 0, time.UTC)
		cs = append(cs, spend.Contribution{Kind: spend.RequestRow, Session: old, TS: ts, USD: v,
			Req: ledger.Request{MessageID: fmt.Sprintf("old%d", i), SessionID: old.ID, Model: opus, TS: ts}})
	}
	return Data{
		Now: mockNow, Sessions: sessions, Repos: 3,
		Counts: segment.Counts{Sessions: 6, Subs: 2, NeedsYou: 2, Runaway: 1, Done: 1, Working: 1, Quiet: 1, Idle: 1},
		Today:  spend.DayTotal(cs, clk, "2026-10-22", time.Time{}), Cap: 50,
		Pace: pace.Status{OK: true, Ratio: 0.9, Typical: 17.5, Days: 21},
		Snap: snap, Contribs: cs, Clock: clk,
	}
}

func mockModel() *Model {
	m := New(Options{Peek: true, ShowText: true}, &mockActions{})
	m.SetData(mockData())
	return m
}

// golden compares got with testdata/<name>; BOSUN_UPDATE_GOLDEN=1 rewrites it.
func golden(t *testing.T, name, got string) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if os.Getenv("BOSUN_UPDATE_GOLDEN") != "" {
		os.MkdirAll("testdata", 0o755)
		os.WriteFile(p, []byte(got+"\n"), 0o644)
	}
	want, err := os.ReadFile(p)
	if err != nil || strings.TrimRight(string(want), "\n") != strings.TrimRight(got, "\n") {
		t.Errorf("%s differs (BOSUN_UPDATE_GOLDEN=1 rewrites it)\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

func TestGoldenStatusView(t *testing.T) {
	for _, w := range []int{180, 100} {
		m := mockModel()
		f := frame(m, w, 24)
		golden(t, fmt.Sprintf("status_%d.golden", w), f)
		for i, l := range strings.Split(f, "\n") {
			if Width(l) != w {
				t.Errorf("width %d line %d is %d columns", w, i, Width(l))
			}
		}
	}
}

func TestGoldenSpendView(t *testing.T) {
	for _, w := range []int{180, 100} {
		m := mockModel()
		press(m, "tab")
		m.revealSession("a1f3c9d0")
		for _, n := range m.visible() {
			if n.kind == kPrompt && n.session == "a1f3c9d0" && n.num == 2 {
				m.expanded[n.key] = true
			}
		}
		f := frame(m, w, 40)
		golden(t, fmt.Sprintf("spend_%d.golden", w), f)
		for i, l := range strings.Split(f, "\n") {
			if Width(l) != w {
				t.Errorf("width %d line %d is %d columns", w, i, Width(l))
			}
		}
	}
}

func TestMockTotalsMatchTheOwnersNumbers(t *testing.T) {
	d := mockData()
	if got := fmt.Sprintf("%.2f", d.Today.USD); got != "23.40" {
		t.Errorf("today = %s", got)
	}
	m := mockModel()
	_, sum := m.week()
	if got := fmt.Sprintf("%.2f", sum); got != "187.70" {
		t.Errorf("week = %s", got)
	}
}

// D-28: an apportioned remainder folds into BY MODEL as est., and the subagent
// share includes it; only a remainder with no model keeps its own line.
func TestApportionedRemainderFoldsIntoByModel(t *testing.T) {
	d := mockData()
	d.Contribs = append(d.Contribs, spend.Contribution{Kind: spend.Unattributed, Session: d.Snap.Sessions["a1f3c9d0"], TS: at(13, 0), USD: 5.00, Est: true,
		AgentType: "Explore", Req: ledger.Request{SessionID: "a1f3c9d0", Model: "claude-sonnet-5-5", AgentID: "ag1"}})
	m := New(Options{ShowText: true}, &mockActions{})
	m.SetData(d)
	left := strings.Join(Plain(m.leftPane(30, 40)), "\n")
	if !strings.Contains(left, "sonnet-5.5") || !strings.Contains(left, "$11.20 est.") || strings.Contains(left, "unattributed") {
		t.Errorf("left pane:\n%s", left)
	}
	if !strings.Contains(left, "subagents share            30%") { // (3.12 + 0.41 + 5.00) / 28.40
		t.Errorf("the share must include the apportioned part:\n%s", left)
	}
}

func at(h, m int) time.Time { return time.Date(2026, 10, 22, h, m, 0, 0, time.UTC) }

// One session with an unknown fill, one at 41% and one at 78%: the CTX column
// shows `—`, the plain figure and the red one.
func ctxModel() *Model {
	d := mockData()
	d.Sessions = append([]eval.View(nil), d.Sessions...)
	at := func(p float64) *float64 { return &p }
	d.Sessions[0].CtxPct = at(41)
	d.Sessions[2].CtxPct = at(78)
	m := New(Options{Peek: true, ShowText: true}, &mockActions{})
	m.SetData(d)
	return m
}

func TestGoldenStatusViewContextFill(t *testing.T) {
	for _, w := range []int{180, 100} {
		f := frame(ctxModel(), w, 24)
		golden(t, fmt.Sprintf("status_ctx_%d.golden", w), f)
		for i, l := range strings.Split(f, "\n") {
			if Width(l) != w {
				t.Errorf("width %d line %d is %d columns", w, i, Width(l))
			}
		}
	}
}

func TestContextCellColour(t *testing.T) {
	m := New(Options{Color: true}, &mockActions{})
	c := agentCols{ctx: ctxWidth}
	at := func(p float64) eval.View { return eval.View{CtxPct: &p} }
	for _, tc := range []struct {
		v    eval.View
		want string
	}{
		{at(41), " 41% "},
		{at(50), cAmber + " 50%"},
		{at(69), cAmber + " 69%"},
		{at(70), cRed + " 70%"},
		{at(100), cRed + "100%"},
		{eval.View{}, cGrey + "   —"},
	} {
		if got := m.ctxCell(tc.v, c); !strings.HasPrefix(got, tc.want) {
			t.Errorf("ctxCell = %q, want prefix %q", got, tc.want)
		}
	}
	if got := m.ctxCell(at(90), agentCols{}); got != "" {
		t.Errorf("dropped column rendered %q", got)
	}
}

func TestContextColumnDropsWhenNarrow(t *testing.T) {
	if agentWidths(89).ctx != 0 || agentWidths(90).ctx == 0 {
		t.Error("CTX column should drop below 90 columns")
	}
}
