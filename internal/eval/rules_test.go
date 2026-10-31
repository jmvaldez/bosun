package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jmvaldez/bosun/internal/config"
	"github.com/jmvaldez/bosun/internal/hook"
	"github.com/jmvaldez/bosun/internal/live"
	"github.com/jmvaldez/bosun/internal/notify"
	"github.com/jmvaldez/bosun/internal/pace"
	"github.com/jmvaldez/bosun/internal/rules"
	"github.com/jmvaldez/bosun/internal/spend"
)

func (r *rig) withRules() *rig {
	r.d.Cfg = config.Default()
	r.d.Notify = true
	return r
}

// dispatches returns the posting notify-dispatch invocations spawned so far,
// parsed back from their flags; retractions are checked through r.sp.calls.
func (r *rig) dispatches() []notify.Request {
	r.t.Helper()
	var out []notify.Request
	for _, c := range r.sp.calls {
		if len(c) > 1 && c[1] == "notify-dispatch" {
			req, err := notify.ParseArgs(c[2:])
			if err != nil {
				r.t.Fatalf("spawned args do not parse: %v (%v)", err, c)
			}
			if !req.Retract {
				out = append(out, req)
			}
		}
	}
	return out
}

func (r *rig) writeJSON(path string, v any) {
	b, _ := json.Marshal(v)
	os.MkdirAll(r.d.Ad.Paths.State, 0o700)
	os.WriteFile(path, b, 0o600)
}

func (r *rig) snapshot(sid string, cost, banked float64) {
	r.writeJSON(r.d.Ad.Paths.Session(sid)+"/statusline.json", map[string]any{"session_id": sid, "cost_usd": cost, "banked_usd": banked})
}

func TestSubagentRuntimeAlertFiresOnceWithRealValuesThenRetracts(t *testing.T) {
	r := newRig(t).withRules()
	r.emit("aaa", "%1", "SessionStart", func(e *hook.Event) { e.Start = &hook.StartExtra{Repo: "acme/api", Task: "ABC-1"} })
	r.emit("aaa", "%1", "UserPromptSubmit")
	r.emit("aaa", "%1", "SubagentStart", func(e *hook.Event) { e.AID, e.AType = "ag1", "Explore" })
	Evaluate(r.d)
	r.clk.Advance(9 * time.Minute)
	r.emit("aaa", "%1", "PostToolUse") // keep the session lively
	Evaluate(r.d)
	if len(r.dispatches()) != 0 {
		t.Fatal("fired before the limit")
	}
	r.clk.Advance(2 * time.Minute) // 11 minutes in
	res := Evaluate(r.d)
	ds := r.dispatches()
	if len(ds) != 1 || ds[0].Kind != notify.SubagentRuntime {
		t.Fatalf("dispatches = %+v", ds)
	}
	d := ds[0]
	if d.Agent != "Explore" || d.Repo != "acme/api" || d.Task != "ABC-1" || d.Duration == nil || d.LimitDur == nil ||
		*d.LimitDur != 10*time.Minute || *d.Duration < 10*time.Minute || d.AlertID == "" {
		t.Errorf("request = %+v", d)
	}
	if m, err := notify.Render(d); err != nil || !strings.Contains(m.Body, "Explore in ABC-1: 11m00s") {
		t.Errorf("render: %v %+v", err, m)
	}
	if res.Counts.Runaway != 1 || res.Counts.Working != 0 {
		t.Errorf("counts = %+v; a runaway session counts as runaway only", res.Counts)
	}
	if r.marks()["@1"] != "runaway" {
		t.Errorf("marks = %v", r.marks())
	}
	// No second dispatch while the condition holds.
	r.clk.Advance(time.Minute)
	Evaluate(r.d)
	if len(r.dispatches()) != 1 {
		t.Errorf("dispatches = %d", len(r.dispatches()))
	}
	// The subagent ends: the alert clears and its notice is retracted.
	r.emit("aaa", "%1", "SubagentStop", func(e *hook.Event) { e.AID = "ag1" })
	res = Evaluate(r.d)
	if res.Counts.Runaway != 0 || len(res.Alerts) != 0 {
		t.Errorf("res = %+v", res)
	}
	last := r.sp.calls[len(r.sp.calls)-1]
	if got := strings.Join(last[1:], " "); got != "notify-dispatch --retract --group bosun.aaa.subagent_runtime" {
		t.Errorf("last spawn = %s", got)
	}
}

func TestQuietNeedsAClosedToolAndAGap(t *testing.T) {
	r := newRig(t).withRules()
	r.emit("aaa", "%1", "UserPromptSubmit")
	r.emit("aaa", "%1", "PreToolUse", func(e *hook.Event) { e.Tool = "Bash" })
	r.clk.Advance(10 * time.Minute)
	Evaluate(r.d)
	if len(r.dispatches()) != 0 {
		t.Fatal("a long single tool call is not quiet")
	}
	r.emit("aaa", "%1", "PostToolUse")
	r.clk.Advance(4 * time.Minute)
	Evaluate(r.d)
	if len(r.dispatches()) != 0 {
		t.Fatal("fired before the 5 m gap")
	}
	r.clk.Advance(2 * time.Minute)
	Evaluate(r.d)
	ds := r.dispatches()
	if len(ds) != 1 || ds[0].Kind != notify.Quiet || ds[0].Duration == nil || *ds[0].Duration < 5*time.Minute {
		t.Fatalf("dispatches = %+v", ds)
	}
	// Activity clears it and it re-arms.
	r.emit("aaa", "%1", "PreToolUse", func(e *hook.Event) { e.Tool = "Read" })
	Evaluate(r.d)
	r.emit("aaa", "%1", "PostToolUse")
	r.clk.Advance(6 * time.Minute)
	Evaluate(r.d)
	if len(r.dispatches()) != 2 {
		t.Errorf("a re-tripped quiet alert should fire again, dispatches = %d", len(r.dispatches()))
	}
}

func TestSessionCostFiresAtEachMultiple(t *testing.T) {
	r := newRig(t).withRules()
	r.d.Cfg.Rules.SessionCost = 0.01 // the CHECKLIST's low threshold
	r.emit("aaa", "%1", "UserPromptSubmit")
	r.snapshot("aaa", 0.004, 0)
	Evaluate(r.d)
	if len(r.dispatches()) != 0 {
		t.Fatal("fired under the step")
	}
	r.snapshot("aaa", 0.012, 0)
	Evaluate(r.d)
	r.snapshot("aaa", 0.013, 0)
	Evaluate(r.d)
	r.snapshot("aaa", 0.025, 0)
	Evaluate(r.d)
	ds := r.dispatches()
	if len(ds) != 2 || *ds[0].Step != 0.01 || *ds[1].Step != 0.02 {
		var steps []string
		for _, d := range ds {
			steps = append(steps, fmt.Sprint(*d.Step))
		}
		t.Fatalf("steps = %v, want [0.01 0.02]", steps)
	}
	if m, _ := notify.Render(ds[1]); m.Body != "a session passed $0.02" {
		t.Errorf("body = %q", m.Body)
	}
}

func TestSessionCostIncludesBankedAndExcludesForkInheritance(t *testing.T) {
	r := newRig(t).withRules()
	r.emit("aaa", "%1", "SessionStart", func(e *hook.Event) { e.Src = "fork" })
	r.emit("aaa", "%1", "UserPromptSubmit")
	r.writeJSON(r.d.Ad.Paths.Session("aaa")+"/statusline.json", map[string]any{"session_id": "aaa", "cost_usd": 12.0, "banked_usd": 1.0, "first_usd": 10.0})
	Evaluate(r.d)
	// own = 12 + 1 - 10 = 3, under the $10 step.
	if len(r.dispatches()) != 0 {
		t.Error("a fork's inherited spend counted toward session_cost")
	}
	r.writeJSON(r.d.Ad.Paths.Session("aaa")+"/statusline.json", map[string]any{"session_id": "aaa", "cost_usd": 21.0, "banked_usd": 0, "first_usd": 10.0})
	Evaluate(r.d)
	if ds := r.dispatches(); len(ds) != 1 || *ds[0].Step != 10 {
		t.Errorf("dispatches = %+v", ds)
	}
}

func TestSubagentCostFromItsTranscript(t *testing.T) {
	r := newRig(t).withRules()
	main := r.d.Ad.Paths.State + "/claude/projects/p/aaa.jsonl"
	r.emit("aaa", "%1", "SessionStart", func(e *hook.Event) { e.Start = &hook.StartExtra{Transcript: main} })
	r.emit("aaa", "%1", "SubagentStart", func(e *hook.Event) { e.AID, e.AType = "ag1", "Explore" })
	agent := strings.TrimSuffix(main, ".jsonl") + "/subagents/agent-ag1.jsonl"
	os.MkdirAll(strings.TrimSuffix(agent, "/agent-ag1.jsonl"), 0o755)
	line := func(id string, in int64) string {
		return fmt.Sprintf(`{"type":"assistant","requestId":"r%s","message":{"id":"m%s","model":"claude-sonnet-5-5","usage":{"input_tokens":%d,"output_tokens":0}}}`+"\n", id, id, in)
	}
	os.WriteFile(agent, []byte(line("1", 500_000)), 0o644) // $1.00
	Evaluate(r.d)
	if len(r.dispatches()) != 0 {
		t.Fatal("fired under $2.00")
	}
	f, _ := os.OpenFile(agent, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(line("2", 1_000_000)) // +$2.00 = $3.00
	f.Close()
	Evaluate(r.d)
	ds := r.dispatches()
	if len(ds) != 1 || ds[0].Kind != notify.SubagentCost || *ds[0].USD != 3 || *ds[0].LimitUSD != 2 || !ds[0].Estimated {
		t.Fatalf("dispatches = %+v", ds)
	}
	if m, _ := notify.Render(ds[0]); m.Body != "Explore in a session: $3.00 est. (limit $2)" {
		t.Errorf("body = %q", m.Body)
	}
}

func TestDailyBudgetAndPace(t *testing.T) {
	r := newRig(t).withRules()
	r.d.Cfg.Rules.DailyBudget = 50
	day := spend.NewClock(r.d.Cfg.Spend.Day).Day(r.clk.Now())
	var days []pace.Day
	for i := 1; i <= 7; i++ {
		days = append(days, pace.Day{Day: fmt.Sprintf("2026-10-%02d", i), Points: []pace.Point{{Minute: 0, USD: 40}}})
	}
	r.writeJSON(r.d.Ad.Paths.Pace(), pace.File{BuiltDay: day, Days: days})
	today := func(usd float64) {
		r.writeJSON(r.d.Ad.Paths.Today(), spend.TodayFile{DaySum: spend.DaySum{Day: day, USD: usd}, UpdatedAt: r.clk.Now()})
	}

	today(49) // 1.225x: silent
	if res := Evaluate(r.d); len(r.dispatches()) != 0 || res.Pace.Amber {
		t.Fatalf("silent expected: %+v dispatches %d", res.Pace, len(r.dispatches()))
	}
	today(52) // 1.3x: amber, no alert; over the $50 budget though
	res := Evaluate(r.d)
	if !res.Pace.Amber || res.Pace.Alert {
		t.Errorf("pace = %+v", res.Pace)
	}
	ds := r.dispatches()
	if len(ds) != 1 || ds[0].Kind != notify.DailyBudget || *ds[0].USD != 52 || *ds[0].CapUSD != 50 {
		t.Fatalf("dispatches = %+v", ds)
	}
	today(61) // 1.525x and $21 over: pace alert
	Evaluate(r.d)
	ds = r.dispatches()
	if len(ds) != 2 || ds[1].Kind != notify.DailyPace || *ds[1].USD != 61 || *ds[1].Ratio < 1.5 {
		t.Fatalf("dispatches = %+v", ds)
	}
	// Once per day: dipping and returning does not fire again.
	today(55)
	Evaluate(r.d)
	today(70)
	Evaluate(r.d)
	if n := len(r.dispatches()); n != 2 {
		t.Errorf("dispatches = %d, want 2 (one budget, one pace)", n)
	}
	// Pace alert and budget alert are account-level: they make no runaway session.
	if res := Evaluate(r.d); res.Counts.Runaway != 0 {
		t.Errorf("counts = %+v", res.Counts)
	}
}

func TestPaceSilentUnderMinDays(t *testing.T) {
	r := newRig(t).withRules()
	day := spend.NewClock(r.d.Cfg.Spend.Day).Day(r.clk.Now())
	var days []pace.Day
	for i := 1; i <= 6; i++ {
		days = append(days, pace.Day{Day: fmt.Sprintf("2026-10-%02d", i), Points: []pace.Point{{Minute: 0, USD: 1}}})
	}
	r.writeJSON(r.d.Ad.Paths.Pace(), pace.File{BuiltDay: day, Days: days})
	r.writeJSON(r.d.Ad.Paths.Today(), spend.TodayFile{DaySum: spend.DaySum{Day: day, USD: 500}})
	if res := Evaluate(r.d); res.Pace.OK || len(r.dispatches()) != 0 {
		t.Errorf("6 baseline days must stay silent: %+v", res.Pace)
	}
}

// Catch-up alerts never notify, and are not notified later either.
func TestCatchUpAlertsNeverNotify(t *testing.T) {
	r := newRig(t).withRules()
	r.d.Cfg.Rules.SessionCost = 0.01
	r.emit("aaa", "%1", "UserPromptSubmit")
	r.snapshot("aaa", 0.05, 0)
	r.d.Notify = false
	res := Evaluate(r.d)
	if res.Counts.Runaway != 1 || len(res.Alerts) != 1 {
		t.Errorf("a catch-up alert must still show: %+v", res)
	}
	if len(r.dispatches()) != 0 {
		t.Error("a catch-up run posted a notification")
	}
	r.d.Notify = true
	Evaluate(r.d)
	if len(r.dispatches()) != 0 {
		t.Error("the periodic run notified for an alert catch-up had already found")
	}
}

func TestAckHidesAndRearms(t *testing.T) {
	r := newRig(t).withRules()
	r.d.Cfg.Rules.SessionCost = 0.01
	r.emit("aaa", "%1", "UserPromptSubmit")
	r.snapshot("aaa", 0.05, 0)
	res := Evaluate(r.d)
	if res.Counts.Runaway != 1 || r.marks()["@1"] != "runaway" {
		t.Fatalf("setup: %+v %v", res.Counts, r.marks())
	}
	id := res.Alerts[0].ID
	got, err := Dismiss(r.d.Ad.Paths, id, false)
	if err != nil || len(got) != 1 {
		t.Fatalf("dismiss: %v %v", got, err)
	}
	res = Evaluate(r.d)
	if res.Counts.Runaway != 0 || res.Counts.Working != 1 || len(res.Alerts) != 0 {
		t.Errorf("a dismissed alert must vanish from the counts and popup data: %+v", res)
	}
	if r.marks()["@1"] != "working" {
		t.Errorf("marker after dismiss = %v", r.marks())
	}
	if _, err := Dismiss(r.d.Ad.Paths, "nope0", false); err == nil {
		t.Error("an unknown id must fail")
	}
	// The condition clears (spend drops back under the step: a /login reset),
	// then re-trips: the alert shows and fires again.
	r.snapshot("aaa", 0.001, 0)
	Evaluate(r.d)
	n := len(r.dispatches())
	r.snapshot("aaa", 0.05, 0)
	res = Evaluate(r.d)
	if res.Counts.Runaway != 1 || len(r.dispatches()) != n+1 {
		t.Errorf("re-trip: counts %+v dispatches %d (was %d)", res.Counts, len(r.dispatches()), n)
	}
}

func TestNeedsYouOutranksRunawayAndRunawayLeavesTheQueue(t *testing.T) {
	r := newRig(t).withRules()
	r.d.Cfg.Rules.SessionCost = 0.01
	r.emit("aaa", "%1", "UserPromptSubmit")
	r.emit("aaa", "%1", "Stop") // done
	r.snapshot("aaa", 0.05, 0)
	res := Evaluate(r.d)
	if res.Counts.Runaway != 1 || res.Counts.Done != 0 {
		t.Fatalf("done + runaway should display as runaway: %+v", res.Counts)
	}
	if q := live.Queue(live.Read(r.d.Ad.Paths.Live()).Sessions); len(q) != 0 {
		t.Error("a runaway session is never a jump target")
	}
	r.emit("aaa", "%1", "PermissionRequest")
	res = Evaluate(r.d)
	if res.Counts.NeedsYou != 1 || res.Counts.Runaway != 0 || r.marks()["@1"] != "needs-you" {
		t.Errorf("needs-you must outrank runaway: %+v %v", res.Counts, r.marks())
	}
}

func TestNeedsYouAndDoneRetractWhenTheyClear(t *testing.T) {
	r := newRig(t).withRules()
	r.emit("aaa", "%1", "PermissionRequest")
	Evaluate(r.d)
	r.emit("aaa", "%1", "PostToolUse")
	Evaluate(r.d)
	last := r.sp.calls[len(r.sp.calls)-1]
	if got := strings.Join(last[1:], " "); got != "notify-dispatch --retract --group bosun.aaa.attn" {
		t.Fatalf("last spawn = %s", got)
	}
	// needs-you to done keeps the notice: the done dispatch replaces it in place.
	r.emit("aaa", "%1", "PermissionRequest")
	Evaluate(r.d)
	n0 := len(r.sp.calls)
	r.emit("aaa", "%1", "PostToolUse")
	r.emit("aaa", "%1", "Stop")
	Evaluate(r.d)
	for _, c := range r.sp.calls[n0:] {
		if strings.Contains(strings.Join(c, " "), "--retract") {
			t.Errorf("needs-you -> done retracted: %v", c)
		}
	}
	// done clears when the pane gets focus.
	Evaluate(r.d)
	n := len(r.sp.calls)
	r.tm.clients = "c1\t%1\tattached\tmouse\n"
	Evaluate(r.d)
	if len(r.sp.calls) != n+1 {
		t.Errorf("viewing a done pane should retract its notice, spawns %d -> %d", n, len(r.sp.calls))
	}
}

func TestNoSpawnsWhenNotificationsAreOff(t *testing.T) {
	r := newRig(t).withRules()
	r.d.Cfg.Notify.Backend = "off"
	r.d.Cfg.Rules.SessionCost = 0.01
	r.emit("aaa", "%1", "UserPromptSubmit")
	r.snapshot("aaa", 0.05, 0)
	Evaluate(r.d)
	for _, c := range r.sp.calls {
		if len(c) > 1 && c[1] == "notify-dispatch" {
			t.Errorf("spawned %v with notify.backend = off", c)
		}
	}
}

// A done row's edit count is the turn's Edit, Write, MultiEdit and NotebookEdit
// calls since the last real prompt; subagent and other tool calls do not count.
func TestViewEditsMatchTheTurnsEditToolCalls(t *testing.T) {
	r := newRig(t).withRules()
	r.emit("aaa", "%1", "UserPromptSubmit")
	r.emit("aaa", "%1", "PreToolUse", func(e *hook.Event) { e.Tool = "Edit" })
	r.emit("aaa", "%1", "PreToolUse", func(e *hook.Event) { e.Tool = "Write" })
	r.emit("aaa", "%1", "PreToolUse", func(e *hook.Event) { e.Tool = "MultiEdit" })
	r.emit("aaa", "%1", "PreToolUse", func(e *hook.Event) { e.Tool = "NotebookEdit" })
	r.emit("aaa", "%1", "PreToolUse", func(e *hook.Event) { e.Tool = "Read" })
	r.emit("aaa", "%1", "PreToolUse", func(e *hook.Event) { e.Tool = "Edit"; e.AID = "ag1" }) // a subagent's edit
	r.emit("aaa", "%1", "Stop")
	res := Evaluate(r.d)
	if len(res.Sessions) != 1 || res.Sessions[0].Display != "done" || res.Sessions[0].Edits != 4 {
		t.Errorf("view = %+v", res.Sessions)
	}
	r.emit("aaa", "%1", "UserPromptSubmit") // a new turn starts from zero
	if res := Evaluate(r.d); res.Sessions[0].Edits != 0 {
		t.Errorf("edits after a new prompt = %d", res.Sessions[0].Edits)
	}
}

// Review finding 3: a fork stays a fork after /compact or --resume overwrite the
// SessionStart source, so its inherited spend is still left out of session_cost.
func TestForkInheritanceSurvivesCompactAndResume(t *testing.T) {
	for _, later := range []string{"compact", "resume"} {
		t.Run(later, func(t *testing.T) {
			r := newRig(t).withRules()
			r.emit("aaa", "%1", "SessionStart", func(e *hook.Event) { e.Src = "fork" })
			r.emit("aaa", "%1", "UserPromptSubmit")
			r.emit("aaa", "%1", "SessionStart", func(e *hook.Event) { e.Src = later })
			r.emit("aaa", "%1", "UserPromptSubmit")
			r.writeJSON(r.d.Ad.Paths.Session("aaa")+"/statusline.json", map[string]any{"session_id": "aaa", "cost_usd": 12.0, "first_usd": 10.0})
			Evaluate(r.d)
			if len(r.dispatches()) != 0 {
				t.Error("inherited spend counted toward session_cost after the source changed to " + later)
			}
		})
	}
}

func TestContextFillReachesTheView(t *testing.T) {
	r := newRig(t).withRules()
	r.emit("aaa", "%1", "UserPromptSubmit")
	res := Evaluate(r.d)
	if len(res.Sessions) != 1 || res.Sessions[0].CtxPct != nil {
		t.Fatalf("no snapshot should leave ctx unknown, got %+v", res.Sessions)
	}
	r.writeJSON(r.d.Ad.Paths.Session("aaa")+"/statusline.json", map[string]any{"session_id": "aaa", "cost_usd": 1.0, "ctx_pct": 41.5})
	res = Evaluate(r.d)
	if p := res.Sessions[0].CtxPct; p == nil || *p != 41.5 {
		t.Errorf("ctx_pct = %v, want 41.5", p)
	}
	r.writeJSON(r.d.Ad.Paths.Session("aaa")+"/statusline.json", map[string]any{"session_id": "aaa", "cost_usd": 1.0})
	if res = Evaluate(r.d); res.Sessions[0].CtxPct != nil {
		t.Error("a snapshot without the field must read as unknown, not the last value")
	}
}

// handoffRig runs one session in the named state with the given fill. Ctx is
// the status line's percentage, so 78 means 78% of the window.
func handoffRig(t *testing.T, state string, ctx *float64, suggestAt float64) *rig {
	r := newRig(t).withRules()
	r.d.Cfg.Handoff.SuggestAt = suggestAt
	r.d.Cfg.Rules.SessionCost = 0 // keep cost alerts out of the way
	r.emit("aaa", "%1", "SessionStart")
	switch state {
	case live.Done:
		r.emit("aaa", "%1", "UserPromptSubmit")
		r.emit("aaa", "%1", "Stop")
	case live.Working:
		r.emit("aaa", "%1", "UserPromptSubmit")
	case live.NeedsYou:
		r.emit("aaa", "%1", "UserPromptSubmit")
		r.emit("aaa", "%1", "PermissionRequest")
	}
	r.setCtx(ctx)
	return r
}

func (r *rig) setCtx(ctx *float64) {
	snap := map[string]any{"session_id": "aaa", "cost_usd": 1.0}
	if ctx != nil {
		snap["ctx_pct"] = *ctx
	}
	r.writeJSON(r.d.Ad.Paths.Session("aaa")+"/statusline.json", snap)
}

func handoffs(r *rig) (n int) {
	for _, d := range r.dispatches() {
		if d.Kind == notify.Handoff {
			n++
		}
	}
	return n
}

func TestHandoffSuggestionTable(t *testing.T) {
	pct := func(p float64) *float64 { return &p }
	tests := []struct {
		name      string
		state     string
		ctx       *float64
		suggestAt float64
		want      bool
	}{
		{"done above", live.Done, pct(78), 0.70, true},
		{"done exactly at", live.Done, pct(70), 0.70, true},
		{"done just below", live.Done, pct(69.9), 0.70, false},
		{"idle above", live.Idle, pct(95), 0.70, true},
		{"idle below", live.Idle, pct(10), 0.70, false},
		{"done at a lower threshold", live.Done, pct(30), 0.25, true},
		{"working above", live.Working, pct(90), 0.70, false},
		{"needs-you above", live.NeedsYou, pct(90), 0.70, false},
		{"done, fill unknown", live.Done, nil, 0.70, false},
		{"idle, fill unknown", live.Idle, nil, 0.01, false},
		{"done above, suggest_at 0", live.Done, pct(99), 0, false},
		{"idle above, suggest_at 0", live.Idle, pct(99), 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := handoffRig(t, tt.state, tt.ctx, tt.suggestAt)
			res := Evaluate(r.d)
			got := false
			for _, a := range res.Alerts {
				if a.Rule == rules.Handoff {
					got = true
					if a.Detail == "" || !strings.HasPrefix(a.Detail, "ctx ") {
						t.Errorf("detail = %q, want ctx and a number", a.Detail)
					}
				}
			}
			if got != tt.want {
				t.Errorf("alert active = %v, want %v", got, tt.want)
			}
			if n := handoffs(r); (n == 1) != tt.want || n > 1 {
				t.Errorf("handoff dispatches = %d, want fire = %v", n, tt.want)
			}
			for _, s := range res.Sessions {
				if s.Display == live.Runaway {
					t.Error("a handoff suggestion must never display as runaway")
				}
				if tt.want && s.Display != live.Handoff {
					t.Errorf("display = %q, want handoff", s.Display)
				}
			}
			if tt.want && (res.Counts.Handoff != 1 || res.Counts.Runaway != 0) {
				t.Errorf("counts = %+v", res.Counts)
			}
		})
	}
}

func TestHandoffFiresOnceThenClears(t *testing.T) {
	pct := func(p float64) *float64 { return &p }
	r := handoffRig(t, live.Done, pct(78), 0.70)
	Evaluate(r.d)
	Evaluate(r.d)
	Evaluate(r.d)
	if n := handoffs(r); n != 1 {
		t.Fatalf("dispatches over three ticks = %d, want 1 (Reconcile dedupes)", n)
	}
	// /compact drops the fill: the alert clears, its notice is retracted, and
	// the display falls back to done.
	r.sp.calls = nil
	r.setCtx(pct(12))
	res := Evaluate(r.d)
	if res.Counts.Handoff != 0 || res.Counts.Done != 1 {
		t.Errorf("after compact: %+v", res.Counts)
	}
	retracted := false
	for _, c := range r.sp.calls {
		if len(c) > 1 && c[1] == "notify-dispatch" && strings.Contains(strings.Join(c, " "), "--retract") &&
			strings.Contains(strings.Join(c, " "), "handoff") {
			retracted = true
		}
	}
	if !retracted {
		t.Errorf("the handoff notice was not retracted: %v", r.sp.calls)
	}
	// Fill rises again: re-armed, fires again.
	r.setCtx(pct(80))
	Evaluate(r.d)
	if n := handoffs(r); n != 1 { // r.sp.calls was reset above
		t.Errorf("dispatches after re-arm = %d, want 1", n)
	}
	// The next turn starts: working clears it.
	r.emit("aaa", "%1", "UserPromptSubmit")
	res = Evaluate(r.d)
	if res.Counts.Handoff != 0 || res.Counts.Working != 1 {
		t.Errorf("working must clear the suggestion: %+v", res.Counts)
	}
}

func TestHandoffStartedSkipsTheSuggestion(t *testing.T) {
	pct := func(p float64) *float64 { return &p }
	for phase, want := range map[string]bool{"writing": false, "launching": false, "done": false, "failed": true} {
		r := handoffRig(t, live.Done, pct(90), 0.70)
		r.writeJSON(r.d.Ad.Paths.Session("aaa")+"/handoff.json", map[string]any{"phase": phase})
		if Evaluate(r.d).Counts.Handoff == 1 != want {
			t.Errorf("phase %s: suggested = %v, want %v", phase, !want, want)
		}
	}
}

func TestHandoffStaysInTheJumpQueue(t *testing.T) {
	pct := func(p float64) *float64 { return &p }
	r := handoffRig(t, live.Done, pct(90), 0.70)
	Evaluate(r.d)
	if q := live.Queue(live.Read(r.d.Ad.Paths.Live()).Sessions); len(q) != 1 {
		t.Errorf("queue = %v, want the done session with a handoff suggested", q)
	}
}

// markRunaway must never set Runaway for a store that holds only a handoff
// alert.
func TestMarkRunawayIgnoresHandoffAlerts(t *testing.T) {
	f := &live.File{Sessions: map[string]*live.Session{"s1": {ID: "s1", State: live.Done, Runaway: true}}}
	store := rules.Store{}
	store.Reconcile([]rules.Cond{{Rule: rules.Handoff, Scope: "s1", Session: "s1", Detail: "ctx 78%"}}, time.Unix(0, 0), "d", false)
	markRunaway(f, store)
	s := f.Sessions["s1"]
	if s.Runaway || !s.HandoffSuggested || s.QuietAlert {
		t.Errorf("flags = runaway %v handoff %v quiet %v", s.Runaway, s.HandoffSuggested, s.QuietAlert)
	}
	if got := s.Display(); got != live.Handoff {
		t.Errorf("display = %q", got)
	}
	markRunaway(f, rules.Store{})
	if s.HandoffSuggested {
		t.Error("the flag must clear with the alert")
	}
}
