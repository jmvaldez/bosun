package ui

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jmvaldez/bosun/internal/eval"
	"github.com/jmvaldez/bosun/internal/ledger"
	"github.com/jmvaldez/bosun/internal/pace"
	"github.com/jmvaldez/bosun/internal/peek"
	"github.com/jmvaldez/bosun/internal/segment"
	"github.com/jmvaldez/bosun/internal/spend"
	"github.com/jmvaldez/bosun/internal/term"
)

type fakeActions struct {
	jumped, diffed, exported []string
	interrupts               []eval.View
	dismissed                [][2]string
	peekText, promptText     string
	interruptMsg             string
	dismissN                 int
	latest                   map[string]peek.Info // by session id
}

func (f *fakeActions) Jump(p string) error { f.jumped = append(f.jumped, p); return nil }
func (f *fakeActions) Diff(c string) error { f.diffed = append(f.diffed, c); return nil }
func (f *fakeActions) Interrupt(v eval.View) (string, error) {
	f.interrupts = append(f.interrupts, v)
	return f.interruptMsg, nil
}
func (f *fakeActions) Dismiss(s, a string) (int, error) {
	f.dismissed = append(f.dismissed, [2]string{s, a})
	return f.dismissN, nil
}
func (f *fakeActions) Export(p, g string) (string, error) {
	f.exported = append(f.exported, p+"/"+g)
	return "/data/exports/spend-" + p + "-" + g + ".csv", nil
}
func (f *fakeActions) Peek(eval.View) string            { return f.peekText }
func (f *fakeActions) PromptLine(string, string) string { return f.promptText }
func (f *fakeActions) Latest(v eval.View) peek.Info     { return f.latest[v.ID] }

var now = time.Date(2026, 10, 26, 14, 2, 0, 0, time.UTC) // a Monday

func view(id, state, where, repo, task string, since time.Duration) eval.View {
	return eval.View{ID: id, State: state, Display: state, Since: now.Add(-since), Where: where, Pane: "%" + id[:1], Repo: repo, Task: task,
		Cwd: "/work/" + repo, Transcript: "/t/" + id + ".jsonl", ServerPID: 1, PID: 100}
}

// fixtureData mirrors the SPEC mockup: 7 sessions, needs-you 2, runaway 1,
// done 1, working 2 (one pane-gone), idle 1, and three subagents under two
// parents.
func fixtureData() Data {
	api := view("1aaaaaaa", "needs-you", "work:1.0", "acme/api", "ABC-142", 5*time.Minute+54*time.Second)
	api.WaitingFor, api.WaitKind, api.Subagents, api.SubsEnded = "permission prompt", "permission", 1, 2
	api.Subs = []eval.SubView{{ID: "ag1", Type: "Explore", Runtime: 3*time.Minute + 40*time.Second, Tools: 22, EstUSD: 0.48}}
	web := view("2bbbbbbb", "needs-you", "home:1.0", "acme/web", "XYZ-88", 2*time.Minute+43*time.Second)
	web.WaitingFor, web.WaitKind = "input needed", "question"
	worker := view("3ccccccc", "working", "work:2.0", "acme/worker", "ABC-142", 14*time.Minute)
	worker.Display, worker.Alerts, worker.Subagents, worker.SubsEnded = "runaway", []string{"subagent 14m > 10m"}, 2, 0
	worker.Subs = []eval.SubView{
		{ID: "ag2", Type: "Explore", Runtime: 14*time.Minute + 2*time.Second, Tools: 61, EstUSD: 2.40, Alert: true},
		{ID: "ag3", Type: "general-purpose", Runtime: 2*time.Minute + 10*time.Second, Tools: 9, EstUSD: 0.35},
	}
	done := view("4ddddddd", "done", "home:1.1", "acme/web", "XYZ-88", 2*time.Minute+17*time.Second)
	done.Edits, done.Subagents, done.SubsEnded = 12, 0, 1
	shell := view("5eeeeeee", "working", "dots:1.0", "me/dotfiles", "", 3*time.Minute+17*time.Second)
	shell.LastTool, shell.ToolOpen = "Bash", true
	idle := view("6fffffff", "idle", "infra:2.0", "acme/infra", "OPS-7", time.Hour+time.Minute)
	gone := view("7ggggggg", "working", "", "acme/api", "ABC-139", 8*time.Minute+22*time.Second)
	gone.PaneGone = true
	sessions := []eval.View{api, web, worker, done, shell, idle, gone}

	snap := ledger.Snapshot{Sessions: map[string]ledger.Session{}, Prompts: map[string]ledger.Prompt{}, Subagents: map[string]ledger.Subagent{}}
	day := func(h, m int) time.Time { return time.Date(2026, 10, 26, h, m, 0, 0, time.UTC) }
	add := func(v eval.View, total float64, trusted bool, reqs ...ledger.Request) {
		snap.Sessions[v.ID] = ledger.Session{ID: v.ID, Repo: v.Repo, Task: v.Task, Trusted: trusted, TotalUSD: total, LastActivityAt: day(14, 0)}
		snap.Requests = append(snap.Requests, reqs...)
	}
	rq := func(sid, msg, prompt, agent, model string, h, m int, in, out int64) ledger.Request {
		return ledger.Request{MessageID: msg, RequestID: "r" + msg, SessionID: sid, PromptID: prompt, AgentID: agent, Model: model, TS: day(h, m), Input: in, Output: out, CacheRead: in * 10}
	}
	snap.Prompts["p1"] = ledger.Prompt{ID: "p1", SessionID: "3ccccccc", N: 1, StartedAt: day(9, 12)}
	snap.Prompts["p2"] = ledger.Prompt{ID: "p2", SessionID: "3ccccccc", N: 2, StartedAt: day(9, 41)}
	snap.Subagents["3ccccccc/ag2"] = ledger.Subagent{SessionID: "3ccccccc", AgentID: "ag2", AgentType: "Explore"}
	add(sessions[0], 3.12, true, rq("1aaaaaaa", "m1", "", "", "claude-opus-5-5", 9, 0, 100_000, 5_000))
	add(sessions[1], 0.94, true)
	reqs := []ledger.Request{rq("3ccccccc", "w1", "p1", "", "claude-opus-5-5", 9, 15, 50_000, 3_000), rq("3ccccccc", "w2", "p2", "", "claude-opus-5-5", 9, 45, 18_000, 2_100)}
	for i := 0; i < 12; i++ { // enough to trigger "+ N more requests"
		reqs = append(reqs, rq("3ccccccc", fmt.Sprintf("x%d", i), "p2", "", "claude-sonnet-5-5", 10, i, 6_000, 1_400))
	}
	reqs = append(reqs, rq("3ccccccc", "s1", "p2", "ag2", "claude-sonnet-5-5", 11, 0, 300_000, 20_000))
	add(sessions[2], 6.80, true, reqs...)
	add(sessions[3], 1.40, true, rq("4ddddddd", "d1", "", "", "claude-haiku-5-5", 12, 0, 20_000, 1_000))
	add(sessions[4], 0.31, false, rq("5eeeeeee", "e1", "", "", "claude-sonnet-5-5", 13, 0, 80_000, 4_000))
	add(sessions[5], 2.05, false)
	add(sessions[6], 0.77, false)

	clk := spend.NewClock("utc")
	cs := spend.Contributions(snap)
	return Data{
		Now: now, Sessions: sessions, Repos: 4,
		Counts: segment.Counts{Sessions: 6, Subs: 3, NeedsYou: 2, Runaway: 1, Done: 1, Working: 1, Idle: 1},
		Today:  spend.DayTotal(cs, clk, "2026-10-26", time.Time{}), Cap: 50,
		Pace: pace.Status{OK: true, Ratio: 0.9, Typical: 17.5, Days: 21},
		Snap: snap, Contribs: cs, Clock: clk,
	}
}

func newModel(t *testing.T, peekOn bool) (*Model, *fakeActions) {
	t.Helper()
	act := &fakeActions{peekText: "Bash: npm test -- refunds", promptText: "fix the refund bug", latest: map[string]peek.Info{
		"1aaaaaaa": {Tool: "Bash", Target: "npm test -- refunds", Pending: true},
		"2bbbbbbb": {Tool: "AskUserQuestion", Question: "Keep idempotency key on retry?", Pending: true},
		"5eeeeeee": {Tool: "Edit", Target: "/work/me/dotfiles/db/migrate/0142_refund_state.sql", Pending: true},
	}}
	m := New(Options{Peek: peekOn, ShowText: peekOn}, act)
	m.SetData(fixtureData())
	return m, act
}

func frame(m *Model, w, h int) string { return strings.Join(Plain(m.View(w, h)), "\n") }

func press(m *Model, keys ...term.Key) (quit bool) {
	for _, k := range keys {
		if m.Update(k) {
			return true
		}
	}
	return false
}

func TestAgentsFrameShape(t *testing.T) {
	m, _ := newModel(t, true)
	lines := m.View(120, 26)
	if len(lines) != 26 {
		t.Fatalf("height = %d", len(lines))
	}
	for i, l := range Plain(lines) {
		if Width(l) != 120 {
			t.Errorf("line %d is %d columns: %q", i, Width(l), l)
		}
	}
	f := frame(m, 120, 26)
	for _, want := range []string{
		"bosun status", "6 sessions · 9 agents · 4 repos", "⚑ 2", "▲ 1", "✓ 1", "today $", "/ $50",
		"TASK", "REPO", "TMUX", "STATE", "TIME", "$ TODAY",
		"ABC-142", "work:1", "permission · npm test -- refunds", "5:54",
		`question · "Keep idempotency key on retry?"`, "2:43",
		"runaway · subagent 14m > 10m", "done · ready to review (12 files)", "2:17",
		"editing db/migrate/0142_refund_state.sql", "3:17", "idle · prompt empty", "(no task)",
		"└ · Explore", "subagent · 3m 40s · 22 tools", "$0.48", "└ ▲ Explore", "subagent · 14m 02s · 61 tools",
		"? pane gone", "pane lost",
		"enter jump", "x ack alert", "! interrupt", "d review diff", "s spend for row", "esc close",
	} {
		if !strings.Contains(f, want) {
			t.Errorf("frame lacks %q\n%s", want, f)
		}
	}
	// Rows keep eval's order (needs-you oldest first, runaway, done, working,
	// idle, pane gone last); the fixture is already in that order.
	order := []string{"work:1 ", "home:1.0 ", "work:2 ", "2:17", "dots:1", "infra:2", "? pane gone"}
	last := -1
	for _, o := range order {
		i := strings.Index(f, o)
		if i < 0 || i < last {
			t.Errorf("row %q out of order in\n%s", o, f)
		}
		last = i
	}
}

// With privacy.show_text off the status view shows enums, never text from the
// transcript (D-27).
func TestStatusViewWithoutShowTextHasNoTranscriptText(t *testing.T) {
	act := &fakeActions{latest: map[string]peek.Info{"1aaaaaaa": {Tool: "Bash", Target: "SENTINEL-CMD", Pending: true},
		"2bbbbbbb": {Tool: "AskUserQuestion", Question: "SENTINEL-Q", Pending: true}}, promptText: "SENTINEL-PROMPT"}
	m := New(Options{Peek: true}, act)
	m.SetData(fixtureData())
	f := frame(m, 140, 30)
	if strings.Contains(f, "SENTINEL") {
		t.Errorf("text shown with show_text off:\n%s", f)
	}
	for _, want := range []string{"permission", "question", "running Bash"} {
		if !strings.Contains(f, want) {
			t.Errorf("frame lacks %q:\n%s", want, f)
		}
	}
}

// The $ TODAY column is what `spend --by session --since today` sums for the
// session.
func TestTodayColumnMatchesTodaysSpendPerSession(t *testing.T) {
	m, _ := newModel(t, true)
	d := fixtureData()
	f := frame(m, 130, 26)
	today := map[string]float64{}
	for _, c := range d.Contribs {
		if d.Clock.Day(c.TS) == "2026-10-26" {
			today[c.Session.ID] += c.USD
		}
	}
	checked := 0
	for id, v := range today {
		if v == 0 {
			continue
		}
		want := usd(v)
		found := false
		for _, line := range strings.Split(f, "\n") {
			if strings.Contains(line, "└") {
				continue
			}
			if t := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), "│")); strings.HasSuffix(t, want) || strings.HasSuffix(t, want+"e") {
				found = true
				checked++
				break
			}
		}
		if !found {
			t.Errorf("session %s: %q not found in a session row\n%s", id, want, f)
		}
	}
	if checked < 5 {
		t.Errorf("only %d sessions matched", checked)
	}
}

func TestHeaderCountsEqualSegmentCounts(t *testing.T) {
	m, _ := newModel(t, true)
	c := m.data.Counts
	line := Plain(m.View(130, 26))[1]
	for _, want := range []string{fmt.Sprintf("%d sessions · %d agents", c.Sessions, c.Sessions+c.Subs), fmt.Sprintf("⚑ %d", c.NeedsYou), fmt.Sprintf("▲ %d", c.Runaway),
		fmt.Sprintf("✓ %d", c.Done)} {
		if !strings.Contains(line, want) {
			t.Errorf("header %q lacks %q", line, want)
		}
	}
	segs := segment.Bar(c, segment.Spend{}, segment.Opts{})
	if !strings.Contains(segs, " ⚑ 2 waiting ") || !strings.Contains(segs, " ▲ 1 runaway ") || !strings.Contains(segs, "6 sessions · 9 agents") {
		t.Fatalf("segment %q", segs)
	}
}

func TestEmptyState(t *testing.T) {
	m := New(Options{}, &fakeActions{})
	d := fixtureData()
	d.Sessions, d.Counts = nil, segment.Counts{}
	m.SetData(d)
	if f := frame(m, 100, 20); !strings.Contains(f, "No Claude Code agents running") {
		t.Errorf("frame:\n%s", f)
	}
	press(m, "tab")
	if f := frame(m, 130, 26); !strings.Contains(f, "bosun spend") || !strings.Contains(f, "TOTAL") {
		t.Errorf("the Spend tab must work with no agents:\n%s", f)
	}
}

func TestNavigationJumpAndPaneGone(t *testing.T) {
	m, act := newModel(t, true)
	press(m, "j")
	if _, r, _ := m.selected(); r.sub != 0 {
		t.Errorf("after j the api session's subagent row should be selected: %+v", r)
	}
	// Enter on a subagent row jumps to its parent's pane.
	if !press(m, "enter") || len(act.jumped) != 1 || act.jumped[0] != "%1" {
		t.Fatalf("jumped = %v", act.jumped)
	}
	m, act = newModel(t, true)
	rows := m.agentRows()
	for i := 0; i < len(rows)-1; i++ {
		press(m, "down")
	}
	if v, _, _ := m.selected(); !v.PaneGone {
		t.Fatal("setup: last row should be the pane-gone session")
	}
	if press(m, "enter") || len(act.jumped) != 0 || !strings.Contains(m.Status(), "pane gone") {
		t.Errorf("enter on a pane-gone row must be disabled: %v %q", act.jumped, m.Status())
	}
	press(m, "up", "k")
	if m.agentSel != len(rows)-3 {
		t.Errorf("sel = %d", m.agentSel)
	}
}

func TestOldestWaiting(t *testing.T) {
	m, act := newModel(t, true)
	if !press(m, "a") || len(act.jumped) != 1 || act.jumped[0] != "%1" {
		t.Errorf("a jumped to %v, want the oldest needs-you (%%1)", act.jumped)
	}
}

func TestDismissTargetsTheRowAndSubagent(t *testing.T) {
	m, act := newModel(t, true)
	act.dismissN = 1
	press(m, "x")
	press(m, "j", "j", "j", "j") // api, sub, web, worker, worker/ag2
	if v, r, _ := m.selected(); v.ID != "3ccccccc" || r.sub != 0 {
		t.Fatalf("selected %s %+v", v.ID, r)
	}
	press(m, "x")
	if len(act.dismissed) != 2 || act.dismissed[0] != [2]string{"1aaaaaaa", ""} || act.dismissed[1] != [2]string{"3ccccccc", "ag2"} {
		t.Errorf("dismissed = %v", act.dismissed)
	}
	if !strings.Contains(m.Status(), "dismissed 1") {
		t.Errorf("status = %q", m.Status())
	}
	act.dismissN = 0
	press(m, "x")
	if !strings.Contains(m.Status(), "no active alert") {
		t.Errorf("status = %q", m.Status())
	}
}

func TestInterruptConfirmation(t *testing.T) {
	m, act := newModel(t, true)
	press(m, "!")
	if p := frame(m, 120, 26); !strings.Contains(p, "interrupt work:1.0 api ABC-142? y/n") {
		t.Errorf("prompt missing:\n%s", p)
	}
	press(m, "n")
	if len(act.interrupts) != 0 || m.Status() != "nothing sent" {
		t.Errorf("n must send nothing: %v %q", act.interrupts, m.Status())
	}
	act.interruptMsg = "sent"
	press(m, "!", "y")
	if len(act.interrupts) != 1 || act.interrupts[0].Pane != "%1" || m.Status() != "sent" {
		t.Errorf("interrupts = %+v status %q", act.interrupts, m.Status())
	}
	// On a subagent row the prompt says it stops the parent's turn.
	m, act = newModel(t, true)
	press(m, "j", "!")
	if p := frame(m, 140, 26); !strings.Contains(p, "interrupts the parent's turn") {
		t.Errorf("subagent prompt:\n%s", p)
	}
	press(m, "esc") // any key but y cancels
	if len(act.interrupts) != 0 {
		t.Error("esc at the prompt sent something")
	}
}

func TestPeekOnAndOff(t *testing.T) {
	m, _ := newModel(t, true)
	if strings.Contains(frame(m, 120, 26), "⤷") {
		t.Fatal("peek line shown before v")
	}
	press(m, "v")
	if f := frame(m, 120, 26); !strings.Contains(f, "⤷ Bash: npm test -- refunds") {
		t.Errorf("peek line missing:\n%s", f)
	}
	press(m, "v")
	if strings.Contains(frame(m, 120, 26), "⤷") {
		t.Error("peek still shown after toggling off")
	}
	// ui.peek = false: v never reads the transcript and nothing shows.
	off, act := newModel(t, false)
	act.peekText = "SENTINEL-PEEK"
	press(off, "v")
	if strings.Contains(frame(off, 120, 26), "SENTINEL-PEEK") || !strings.Contains(off.Status(), "peek is off") {
		t.Errorf("peek off: %q", off.Status())
	}
}

func TestDiffClosesAndSpendOpensOnTheSession(t *testing.T) {
	m, act := newModel(t, true)
	if !press(m, "d") || len(act.diffed) != 1 || act.diffed[0] != "/work/acme/api" {
		t.Errorf("diffed = %v", act.diffed)
	}
	m, _ = newModel(t, true)
	press(m, "j", "j", "j", "s") // the worker session
	if m.Tab() != TabSpend {
		t.Fatal("s did not open the Spend tab")
	}
	vis := m.visible()
	if m.spendSel >= len(vis) || vis[m.spendSel].kind != kSession || vis[m.spendSel].session != "3ccccccc" {
		t.Fatalf("selected %+v", vis[m.spendSel])
	}
	if f := frame(m, 130, 30); !strings.Contains(f, "prompt #2") {
		t.Errorf("the session should be expanded:\n%s", f)
	}
}

func TestSpendTreeFoldPeriodsAndGroups(t *testing.T) {
	m, act := newModel(t, true)
	press(m, "tab")
	collapsed := len(m.visible())
	press(m, "l") // expand the first group
	if len(m.visible()) <= collapsed {
		t.Fatal("l did not expand")
	}
	press(m, "h")
	if len(m.visible()) != collapsed {
		t.Error("h did not collapse")
	}
	// Walk down to a request row and check the "+ N more requests" fold exists.
	press(m, "g", "g", "g", "g") // task -> day -> repo -> model -> session
	if m.groupBy() != "session" {
		t.Fatalf("group = %s", m.groupBy())
	}
	for i := 0; i < 10 && !strings.Contains(frame(m, 130, 40), "prompt #2"); i++ {
		press(m, "l", "j")
	}
	m.revealSession("3ccccccc")
	for _, n := range m.visible() {
		if n.kind == kPrompt {
			m.expanded[n.key] = true
		}
	}
	f := frame(m, 130, 60)
	for _, want := range []string{"prompt #1", "prompt #2", "req opus-5.5", "req sonnet-5.5", "+ 10 more requests", "Explore", "· 1 req"} {
		if !strings.Contains(f, want) {
			t.Errorf("tree lacks %q:\n%s", want, f)
		}
	}
	// p (or r) cycles today, 7d, 30d and the header follows.
	for i, want := range []string{"· 7d", "· 30d", "· today"} {
		press(m, []term.Key{"p", "r", "p"}[i])
		if !strings.Contains(frame(m, 130, 30), want) {
			t.Errorf("after p: lacks %s", want)
		}
	}
	press(m, "e")
	if len(act.exported) != 1 || act.exported[0] != "today/session" || !strings.Contains(m.Status(), "exports/spend-today-session.csv") {
		t.Errorf("exported %v status %q", act.exported, m.Status())
	}
}

func TestSpendTotalsAddUp(t *testing.T) {
	m, _ := newModel(t, true)
	press(m, "tab")
	f := frame(m, 180, 40)
	var total float64
	for _, c := range m.data.Contribs {
		if m.data.Clock.Day(c.TS) == "2026-10-26" {
			total += c.USD
		}
	}
	if !strings.Contains(f, "TOTAL") || !strings.Contains(f, usd(total)) {
		t.Errorf("TOTAL %s missing:\n%s", usd(total), f)
	}
	// BY MODEL lists rows that add up to today's total (the share line aside).
	left := Plain(m.leftPane(30, 40))
	var sum float64
	in := false
	for _, l := range left {
		switch {
		case strings.HasPrefix(l, "BY MODEL"):
			in = true
		case strings.HasPrefix(l, "subagents share"):
			in = false
		case in:
			var v float64
			fmt.Sscanf(strings.TrimSpace(l[strings.LastIndex(l, "$"):]), "$%f", &v)
			sum += v
		}
	}
	if diff := sum - total; diff > 0.011 || diff < -0.011 {
		t.Errorf("by-model sums to %.2f, TOTAL is %.2f: %v", sum, total, left)
	}
	if !strings.Contains(f, "estimates at API rates") || !strings.Contains(f, "% of $50 daily budget") {
		t.Errorf("header or budget line:\n%s", f)
	}
}

func TestNarrowCollapsesSummaryAboveTree(t *testing.T) {
	m, _ := newModel(t, true)
	press(m, "tab")
	wide := frame(m, 140, 36)
	narrow := frame(m, 90, 36)
	if !strings.Contains(wide, " │ ") || !strings.Contains(wide, "LAST 7 DAYS") || !strings.Contains(wide, "BY MODEL") {
		t.Errorf("wide frame lacks the summary pane:\n%s", wide)
	}
	if strings.Contains(narrow, "BY MODEL") || strings.Contains(narrow, "│ │") {
		t.Errorf("narrow frame kept the side pane:\n%s", narrow)
	}
	lines := strings.Split(narrow, "\n")
	if !strings.Contains(lines[3], "TODAY") || !strings.Contains(lines[4], "LAST 7 DAYS") || !strings.Contains(narrow, "NAME") {
		t.Errorf("narrow summary lines:\n%s", narrow)
	}
	for i, l := range lines {
		if Width(l) != 90 {
			t.Errorf("narrow line %d is %d columns", i, Width(l))
		}
	}
	t.Logf("wide:\n%s\nnarrow:\n%s", wide, narrow)
}

// Prompt text is inline under show_text, revealed per row by v with peek only,
// and absent with both off (D-27).
func TestPromptTextInTheTree(t *testing.T) {
	m, _ := newModel(t, true) // show_text on
	press(m, "tab")
	m.revealSession("3ccccccc")
	if f := frame(m, 140, 40); !strings.Contains(f, "prompt #2 fix the refund bug") {
		t.Errorf("prompt first line missing:\n%s", f)
	}
	// Peek only: hidden until v.
	act := &fakeActions{promptText: "fix the refund bug"}
	pk := New(Options{Peek: true}, act)
	pk.SetData(fixtureData())
	press(pk, "tab")
	pk.revealSession("3ccccccc")
	if strings.Contains(frame(pk, 140, 40), "refund bug") {
		t.Error("prompt text shown before v")
	}
	for i, n := range pk.visible() {
		if n.kind == kPrompt {
			pk.spendSel = i
			break
		}
	}
	press(pk, "v")
	if !strings.Contains(frame(pk, 140, 40), "fix the refund bug") {
		t.Error("v did not reveal the prompt")
	}
	press(pk, "v")
	if strings.Contains(frame(pk, 140, 40), "refund bug") {
		t.Error("prompt text still shown after toggling off")
	}
	// Both off: never.
	off := New(Options{}, &fakeActions{promptText: "SENTINEL-PROMPT"})
	off.SetData(fixtureData())
	press(off, "tab")
	off.revealSession("3ccccccc")
	press(off, "v")
	if strings.Contains(frame(off, 140, 40), "SENTINEL-PROMPT") {
		t.Error("prompt text shown with show_text and peek off")
	}
	if !strings.Contains(frame(off, 140, 40), "prompt #2 09:41") {
		t.Errorf("the prompt row should show its time instead:\n%s", frame(off, 140, 40))
	}
}

func TestQuitKeys(t *testing.T) {
	for _, k := range []term.Key{"q", "esc", "ctrl-c"} {
		m, _ := newModel(t, true)
		if !m.Update(k) {
			t.Errorf("%s did not quit", k)
		}
	}
	m, _ := newModel(t, true)
	if m.Update("tab") || m.Tab() != TabSpend {
		t.Error("tab")
	}
	if m.Update("tab") || m.Tab() != TabAgents {
		t.Error("tab back")
	}
}

func TestSentinelsNeverAppearWithPeekOff(t *testing.T) {
	m, act := newModel(t, false)
	act.peekText, act.promptText = "SENTINEL", "SENTINEL"
	for _, keys := range [][]term.Key{{}, {"v"}, {"tab"}, {"v"}, {"g"}, {"p"}} {
		press(m, keys...)
		for _, w := range []int{90, 140} {
			if strings.Contains(frame(m, w, 40), "SENTINEL") {
				t.Fatalf("sentinel in frame after %v at width %d", keys, w)
			}
		}
	}
	_ = os.Stdout
}

// Prompt and request rows in the tree equal `spend --by prompt` / `--by request`.
func TestTreeRowsEqualSpendByPromptAndRequest(t *testing.T) {
	m, _ := newModel(t, true)
	press(m, "tab")
	m.revealSession("3ccccccc")
	for _, n := range m.visible() {
		if n.kind == kPrompt {
			m.expanded[n.key] = true
		}
	}
	byPrompt, _ := spend.Group(m.data.Contribs, "prompt", m.data.Clock)
	want := map[string]float64{}
	for _, r := range byPrompt.Rows {
		want[r.Key] = r.USD
	}
	seen := 0
	for _, n := range m.visible() {
		if n.kind == kPrompt && n.session == "3ccccccc" {
			var num int
			fmt.Sscanf(n.label, "prompt #%d", &num)
			key := fmt.Sprintf("3ccccccc prompt #%d", num)
			if diff := n.usd - want[key]; diff > 1e-9 || diff < -1e-9 {
				t.Errorf("%s: tree %.4f, spend --by prompt %.4f", key, n.usd, want[key])
			}
			seen++
		}
	}
	if seen != 2 {
		t.Fatalf("prompt nodes = %d", seen)
	}
	byReq, _ := spend.Group(m.data.Contribs, "request", m.data.Clock)
	reqUSD := map[string]float64{}
	for _, r := range byReq.Rows {
		reqUSD[r.Key] = r.USD
	}
	for _, n := range m.visible() {
		if n.kind == kRequest {
			id := strings.TrimPrefix(n.key[strings.LastIndex(n.key, "/r:")+3:], "")
			if diff := n.usd - reqUSD[id+"/r"+id]; diff > 1e-9 || diff < -1e-9 {
				t.Errorf("request %s: tree %.4f, spend --by request %.4f", id, n.usd, reqUSD[id+"/r"+id])
			}
		}
	}
}

func TestColorsOnlyWhenAsked(t *testing.T) {
	act := &fakeActions{}
	plain := New(Options{Color: false}, act)
	plain.SetData(fixtureData())
	for _, l := range plain.View(120, 26) {
		if strings.Contains(l, "\x1b") {
			t.Fatalf("escape in an uncolored frame: %q", l)
		}
	}
	color := New(Options{Color: true}, act)
	color.SetData(fixtureData())
	var f string
	for _, l := range color.View(120, 26) {
		f += l + "\n"
	}
	// Runaway is red, needs-you amber, done green; the selected needs-you row
	// carries an amber band.
	for name, want := range map[string]string{"red runaway": cRed + "▲", "amber needs-you": cAmber + "⚑", "green done": cGreen + "✓", "band": bandAmber} {
		if !strings.Contains(f, want) {
			t.Errorf("no %s in the colored frame", name)
		}
	}
	for i, l := range color.View(120, 26) {
		if Width(l) != 120 {
			t.Errorf("colored line %d is %d columns", i, Width(l))
		}
	}
}

// A live session with no task shows Claude Code's session name, dim, cut to the
// column; with show_text off, or no name yet, it stays "(no task)".
func TestUntaskedRowShowsSessionNameDim(t *testing.T) {
	const name = "SENTINEL-NAME polishing the\x1b[31m refund handler for the quarter close"
	d := fixtureData()
	d.Names = map[string]string{"5eeeeeee": name, "1aaaaaaa": "SENTINEL-TASKED"}

	m := New(Options{Color: true, ShowText: true}, &fakeActions{})
	m.SetData(d)
	raw := strings.Join(m.View(120, 26), "\n")
	if !strings.Contains(raw, dim+"SENTINEL-NAME") {
		t.Errorf("name is not drawn dim:\n%s", raw)
	}
	if strings.Contains(raw, "SENTINEL-TASKED") {
		t.Error("a tasked row shows the session name")
	}
	for i, l := range Plain(m.View(120, 26)) {
		if Width(l) != 120 {
			t.Errorf("line %d is %d columns: %q", i, Width(l), l)
		}
	}

	for name, opts := range map[string]Options{"show_text off": {Color: true}, "ShowText on, no name": {ShowText: true}} {
		m := New(opts, &fakeActions{})
		d := fixtureData()
		if name == "show_text off" {
			d.Names = map[string]string{"5eeeeeee": "SENTINEL-NAME"}
		}
		m.SetData(d)
		f := frame(m, 120, 26)
		if strings.Contains(f, "SENTINEL") || !strings.Contains(f, "(no task)") {
			t.Errorf("%s: want the (no task) fallback:\n%s", name, f)
		}
	}
}

// A working row names what the session is doing: the open tool call with its
// subject, how long it has run, subagents it waits on, background tasks, or thinking.
func TestWorkingRowDescribesActivity(t *testing.T) {
	base := view("3ccccccc", "working", "work:2.0", "acme/worker", "ABC-1", 2*time.Minute)
	cases := []struct {
		name string
		edit func(*eval.View)
		info peek.Info
		want string
	}{
		{"thinking", func(v *eval.View) {}, peek.Info{}, "thinking"},
		{"open tool, enum only", func(v *eval.View) { v.LastTool, v.ToolOpen = "Grep", true }, peek.Info{}, "searching Grep"},
		{"open tool with subject", func(v *eval.View) {
			v.LastTool, v.ToolOpen, v.ToolSince = "Bash", true, now.Add(-42*time.Second)
		}, peek.Info{Tool: "Bash", Target: "go test ./...", Pending: true}, "running go test ./... (0:42)"},
		{"subagents", func(v *eval.View) { v.Subagents = 2 }, peek.Info{}, "waiting on 2 subagents"},
		{"background", func(v *eval.View) { v.Background = true }, peek.Info{}, "background tasks running"},
	}
	for _, c := range cases {
		v := base
		c.edit(&v)
		m := New(Options{ShowText: true, Peek: true}, &fakeActions{latest: map[string]peek.Info{v.ID: c.info}})
		m.SetData(Data{Now: now, Sessions: []eval.View{v}})
		f := frame(m, 140, 12)
		if !strings.Contains(f, c.want) || !strings.Contains(f, "2:00") {
			t.Errorf("%s: frame lacks %q or the turn time:\n%s", c.name, c.want, f)
		}
	}
}

func TestTmuxCellPaneOnlyWhenWindowShared(t *testing.T) {
	a := view("1aaaaaaa", "working", "s:1.0", "r", "", 0)
	b := view("2bbbbbbb", "working", "s:1.1", "r", "", 0)
	c := view("3ccccccc", "working", "s:2.0", "r", "", 0)
	all := []eval.View{a, b, c}
	for _, tc := range []struct {
		v    eval.View
		want string
	}{{a, "s:1.0"}, {b, "s:1.1"}, {c, "s:2"}} {
		if got := tmuxCell(tc.v, all); got != tc.want {
			t.Errorf("tmuxCell(%s) = %q, want %q", tc.v.Where, got, tc.want)
		}
	}
	gone := b
	gone.PaneGone = true
	if got := tmuxCell(a, []eval.View{a, gone}); got != "s:1" {
		t.Errorf("a gone pane still forced the suffix: %q", got)
	}
}

// Repo, task, agent type and session names reach the screen through Fit, which
// passes OSC sequences through, so they must be cleaned first. Plain() would hide
// a leak: assert on the raw frame.
func TestUntrustedTextNeverReachesTheFrameRaw(t *testing.T) {
	const evil = "\x1b]52;c;aGk=\x07"
	d := fixtureData()
	for i := range d.Sessions {
		d.Sessions[i].Repo = "evil" + evil
		d.Sessions[i].Task = "task" + evil
		for j := range d.Sessions[i].Subs {
			d.Sessions[i].Subs[j].Type = "Type" + evil
		}
	}
	d.Names = map[string]string{"5eeeeeee": "name" + evil}
	for id, s := range d.Snap.Sessions {
		s.Repo, s.Task = "evil"+evil, "task"+evil
		d.Snap.Sessions[id] = s
	}
	m := New(Options{Color: true, ShowText: true}, &fakeActions{})
	m.SetData(d)
	if raw := strings.Join(m.View(140, 30), "\n"); strings.Contains(raw, "\x1b]") || strings.Contains(raw, "\x07") {
		t.Errorf("agents frame carries an OSC sequence:\n%q", raw)
	}
	press(m, "s")
	if raw := strings.Join(m.View(140, 30), "\n"); strings.Contains(raw, "\x1b]") || strings.Contains(raw, "\x07") {
		t.Errorf("spend frame carries an OSC sequence:\n%q", raw)
	}
}
