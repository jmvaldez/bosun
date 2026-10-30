package ledger

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jmvaldez/bosun/internal/hook"
	"github.com/jmvaldez/bosun/internal/paths"
	"github.com/jmvaldez/bosun/internal/statusline"
	"github.com/jmvaldez/bosun/internal/task"
)

const sentinel = "SENTINEL-LEDGER-TEXT-9d2e"

type env struct {
	t     *testing.T
	root  string
	paths paths.Paths
	db    *DB
	proj  string // projects/<proj>
	base  time.Time
}

func newEnv(t *testing.T) *env {
	root := t.TempDir()
	e := &env{t: t, root: root, base: time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)}
	e.paths = paths.Paths{State: root + "/state", Claude: root + "/claude", Cache: root + "/cache"}
	e.proj = e.paths.Claude + "/projects/-work-api"
	os.MkdirAll(e.proj, 0o755)
	db, err := Open(e.paths.State + "/bosun.db")
	if err != nil {
		t.Fatal(err)
	}
	e.db = db
	t.Cleanup(func() { db.Close() })
	return e
}

func (e *env) at(min int) string {
	return e.base.Add(time.Duration(min) * time.Minute).Format("2006-01-02T15:04:05.000Z")
}

func (e *env) ingest(live ...string) Stats {
	e.t.Helper()
	isLive := map[string]bool{}
	for _, id := range live {
		isLive[id] = true
	}
	st, err := e.db.Ingest(IngestOpts{
		Paths: e.paths, Parser: task.Default(), Account: "me@example.com",
		IsLive: func(id string) bool { return isLive[id] },
		Now:    func() time.Time { return e.base.Add(48 * time.Hour) },
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return st
}

// ---- transcript builders

type tl []map[string]any

func user(uuid, prompt string, ts string, extra ...map[string]any) map[string]any {
	m := map[string]any{"type": "user", "uuid": uuid, "promptId": prompt, "timestamp": ts, "isSidechain": false,
		"turnOrigin": "human", "promptSource": "typed", "gitBranch": "HEAD",
		"message": map[string]any{"role": "user", "content": sentinel + " prompt text"}}
	for _, x := range extra {
		for k, v := range x {
			m[k] = v
		}
	}
	return m
}

func assistant(uuid, msg, req, model, ts string, in, out, cr, c5, c1 int64, extra ...map[string]any) map[string]any {
	m := map[string]any{"type": "assistant", "uuid": uuid, "requestId": req, "timestamp": ts, "isSidechain": false,
		"message": map[string]any{"id": msg, "model": model, "content": []any{map[string]any{"type": "text", "text": sentinel + " reply"}},
			"usage": map[string]any{"input_tokens": in, "output_tokens": out, "cache_read_input_tokens": cr,
				"cache_creation_input_tokens": c5 + c1,
				"cache_creation":              map[string]any{"ephemeral_5m_input_tokens": c5, "ephemeral_1h_input_tokens": c1},
				"speed":                       "standard", "inference_geo": "global"}}}
	for _, x := range extra {
		for k, v := range x {
			m[k] = v
		}
	}
	return m
}

func costState(start int64, total float64) map[string]any {
	return map[string]any{"type": "cost-state", "startTime": start, "totalCostUSD": total,
		"modelUsage": map[string]any{"claude-sonnet-5-5": map[string]any{"costUSD": total}}}
}

// uniq gives every request in the lines ids of their own, since the builders
// reuse msg1.. across sessions and request ids are global.
func uniq(lines tl, suffix string) tl {
	for _, l := range lines {
		if m, ok := l["message"].(map[string]any); ok && l["type"] == "assistant" {
			m["id"] = fmt.Sprint(m["id"], suffix)
			l["requestId"] = fmt.Sprint(l["requestId"], suffix)
		}
	}
	return lines
}

func (e *env) write(path string, lines tl, mtime time.Time) {
	e.t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	var b bytes.Buffer
	for _, l := range lines {
		j, _ := json.Marshal(l)
		b.Write(j)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		e.t.Fatal(err)
	}
	os.Chtimes(path, mtime, mtime)
}

func (e *env) appendRaw(path, raw string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	f.WriteString(raw)
	f.Close()
}

func (e *env) session(id string) Session {
	e.t.Helper()
	s, ok, err := e.db.Session(id)
	if err != nil || !ok {
		e.t.Fatalf("session %s: ok=%v err=%v", id, ok, err)
	}
	return s
}

func (e *env) requests(sessionID string) []Request {
	snap, err := e.db.Load(time.Time{})
	if err != nil {
		e.t.Fatal(err)
	}
	var out []Request
	for _, r := range snap.Requests {
		if r.SessionID == sessionID {
			out = append(out, r)
		}
	}
	return out
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

const (
	sidA = "aaaaaaaa-0000-0000-0000-00000000000a"
	sidB = "bbbbbbbb-0000-0000-0000-00000000000b"
	sidC = "cccccccc-0000-0000-0000-00000000000c"
)

// basic: two prompts, three requests (one with three lines), a cost-state.
func basicSession(e *env, sid string, startTime int64, total float64) tl {
	return tl{
		user("u1", "p1", e.at(0)),
		assistant("a1", "msg1", "req1", "claude-sonnet-5-5", e.at(1), 1000, 10, 5000, 0, 2000),
		assistant("a1b", "msg1", "req1", "claude-sonnet-5-5", e.at(1), 1000, 400, 5000, 0, 2000), // same request, output grew
		assistant("a1c", "msg1", "req1", "claude-sonnet-5-5", e.at(1), 1000, 250, 5000, 0, 2000),
		user("u2", "p2", e.at(5)),
		assistant("a2", "msg2", "req2", "claude-sonnet-5-5", e.at(6), 500, 100, 7000, 0, 0),
		assistant("a3", "msg3", "req3", "claude-haiku-5-5", e.at(7), 100, 50, 1000, 0, 0),
		costState(startTime, total),
	}
}

func TestIngestBasicsAndTrust(t *testing.T) {
	e := newEnv(t)
	e.write(e.proj+"/"+sidA+".jsonl", basicSession(e, sidA, 1000, 0.0123), e.base)
	st := e.ingest()
	if st.Requests != 5 || st.Files != 1 {
		t.Fatalf("stats = %+v", st) // 5 request lines, 3 distinct requests
	}
	reqs := e.requests(sidA)
	if len(reqs) != 3 {
		t.Fatalf("requests = %d, want 3 (deduped on message id + request id)", len(reqs))
	}
	var r1 Request
	for _, r := range reqs {
		if r.MessageID == "msg1" {
			r1 = r
		}
	}
	// LESSONS L-33: keep the line with the largest output_tokens, not the first.
	if r1.Output != 400 || r1.Cache1h != 2000 || r1.Cache5m != 0 {
		t.Errorf("request = %+v", r1)
	}
	s := e.session(sidA)
	// LESSONS L-28: a session with a cost-state is trusted and ended.
	if !s.Trusted || !near(OwnUSD(s), 0.0123) || s.EndedAt.IsZero() {
		t.Errorf("session = %+v own=%v", s, OwnUSD(s))
	}
	if s.Account != "me@example.com" {
		t.Errorf("account = %q", s.Account)
	}
}

func TestPromptNumbersAndRequestPromptAssignment(t *testing.T) {
	e := newEnv(t)
	lines := basicSession(e, sidA, 1000, 1)
	// A synthetic task-notification prompt must not take a number or steal
	// the following requests (D-8).
	lines = append(lines[:len(lines)-1],
		user("u3", "pSynthetic", e.at(8), map[string]any{"turnOrigin": "task_notification", "promptSource": "system"}),
		assistant("a4", "msg4", "req4", "claude-sonnet-5-5", e.at(9), 10, 10, 0, 0, 0),
		user("u4", "p3", e.at(10)),
		assistant("a5", "msg5", "req5", "claude-sonnet-5-5", e.at(11), 10, 10, 0, 0, 0),
		costState(1000, 1))
	e.write(e.proj+"/"+sidA+".jsonl", lines, e.base)
	e.ingest()
	snap, _ := e.db.Load(time.Time{})
	nums := map[string]int{}
	for _, p := range snap.Prompts {
		nums[p.ID] = p.N
	}
	if nums["p1"] != 1 || nums["p2"] != 2 || nums["p3"] != 3 || len(nums) != 3 {
		t.Errorf("prompt numbers = %v", nums)
	}
	for _, r := range snap.Requests {
		want := map[string]string{"msg1": "p1", "msg2": "p2", "msg3": "p2", "msg4": "p2", "msg5": "p3"}[r.MessageID]
		if r.PromptID != want {
			t.Errorf("%s: prompt %q, want %q", r.MessageID, r.PromptID, want)
		}
	}
}

func TestReingestIsANoOp(t *testing.T) {
	e := newEnv(t)
	e.write(e.proj+"/"+sidA+".jsonl", basicSession(e, sidA, 1000, 0.5), e.base)
	e.ingest()
	before := dump(t, e)
	st := e.ingest()
	if st.Files != 0 || st.Requests != 0 {
		t.Errorf("second run did work: %+v", st)
	}
	if after := dump(t, e); after != before {
		t.Errorf("ledger changed on re-ingest:\n%s\n---\n%s", before, after)
	}
}

func dump(t *testing.T, e *env) string {
	t.Helper()
	snap, err := e.db.Load(time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for id, s := range snap.Sessions {
		lines = append(lines, fmt.Sprintf("S %s %+v", id, s))
	}
	for _, r := range snap.Requests {
		lines = append(lines, fmt.Sprintf("R %+v", r))
	}
	for _, p := range snap.Prompts {
		lines = append(lines, fmt.Sprintf("P %+v", p))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// Offsets advance only past complete lines (SPEC ingest rules).
func TestPartialTrailingLineIsReadOnceComplete(t *testing.T) {
	e := newEnv(t)
	p := e.proj + "/" + sidA + ".jsonl"
	e.write(p, tl{user("u1", "p1", e.at(0)), assistant("a1", "msg1", "req1", "claude-sonnet-5-5", e.at(1), 10, 10, 0, 0, 0)}, e.base)
	full, _ := json.Marshal(assistant("a2", "msg2", "req2", "claude-sonnet-5-5", e.at(2), 20, 20, 0, 0, 0))
	e.appendRaw(p, string(full[:40]))
	os.Chtimes(p, e.base.Add(time.Minute), e.base.Add(time.Minute))
	e.ingest()
	if n := len(e.requests(sidA)); n != 1 {
		t.Fatalf("requests = %d; the partial line must not be applied", n)
	}
	e.appendRaw(p, string(full[40:])+"\n")
	os.Chtimes(p, e.base.Add(2*time.Minute), e.base.Add(2*time.Minute))
	e.ingest()
	if n := len(e.requests(sidA)); n != 2 {
		t.Errorf("requests = %d after the line completed, want 2", n)
	}
}

func TestBadLinesAndTimestampsAreSkipped(t *testing.T) {
	e := newEnv(t)
	p := e.proj + "/" + sidA + ".jsonl"
	e.write(p, tl{user("u1", "p1", e.at(0)), assistant("a1", "msg1", "req1", "claude-sonnet-5-5", "garbage-ts", 10, 10, 0, 0, 0)}, e.base)
	e.appendRaw(p, "{not json\n"+`{"type":"assistant"`+"\n")
	e.write(p+".keep", nil, e.base)
	os.Remove(p + ".keep")
	e.ingest()
	if n := len(e.requests(sidA)); n != 0 {
		t.Errorf("a row with a bad timestamp was stored (%d); it must be skipped, not stamped now", n)
	}
}

func TestOrphanedAndSupersededFilesAreSkipped(t *testing.T) {
	e := newEnv(t)
	e.write(e.proj+"/"+sidA+".jsonl", basicSession(e, sidA, 1000, 1), e.base)
	e.write(e.proj+"/"+sidB+".jsonl.orphaned-1", basicSession(e, sidB, 1000, 1), e.base)
	e.write(e.proj+"/"+sidC+".superseded-2.jsonl", basicSession(e, sidC, 1000, 1), e.base)
	e.ingest()
	snap, _ := e.db.Load(time.Time{})
	if len(snap.Sessions) != 1 {
		t.Errorf("sessions = %d, want only the live transcript", len(snap.Sessions))
	}
}

// LESSONS L-32: subagent files are all sidechain lines and must be ingested,
// attributed to the parent session and the prompt that started them.
func TestSubagentRequestsAttributedToParent(t *testing.T) {
	e := newEnv(t)
	e.write(e.proj+"/"+sidA+".jsonl", tl{
		user("u1", "p1", e.at(0)),
		assistant("a1", "msg1", "req1", "claude-sonnet-5-5", e.at(1), 100, 10, 0, 0, 0),
		costState(1, 0.5),
	}, e.base)
	agent := e.proj + "/" + sidA + "/subagents/agent-ag1.jsonl"
	e.write(agent, tl{
		user("su1", "p1", e.at(2), map[string]any{"isSidechain": true, "agentId": "ag1"}),
		assistant("sa1", "smsg1", "sreq1", "claude-haiku-5-5", e.at(3), 200, 20, 0, 0, 0, map[string]any{"isSidechain": true, "agentId": "ag1"}),
		assistant("sa2", "smsg2", "sreq2", "claude-haiku-5-5", e.at(4), 300, 30, 0, 0, 0, map[string]any{"isSidechain": true, "agentId": "ag1"}),
	}, e.base.Add(time.Minute))
	os.WriteFile(strings.TrimSuffix(agent, ".jsonl")+".meta.json", []byte(`{"agentType":"Explore","description":"`+sentinel+`"}`), 0o644)
	e.ingest()
	reqs := e.requests(sidA)
	sub := 0
	for _, r := range reqs {
		if r.AgentID == "ag1" {
			sub++
			if r.PromptID != "p1" || r.SessionID != sidA {
				t.Errorf("subagent row = %+v", r)
			}
		}
	}
	if sub != 2 {
		t.Fatalf("subagent rows = %d, want 2", sub)
	}
	snap, _ := e.db.Load(time.Time{})
	if got := snap.Subagents[sidA+"/ag1"]; got.AgentType != "Explore" {
		t.Errorf("subagent = %+v", got)
	}
}

// LESSONS L-31: names chosen so alphabetical order is the wrong order.
func TestPredecessorIngestsBeforeForkRegardlessOfName(t *testing.T) {
	e := newEnv(t)
	parentID, forkID := "ffffffff-0000-0000-0000-0000000000ff", "11111111-0000-0000-0000-000000000011"
	parent := tl{
		{"type": "queue-operation", "timestamp": e.at(-1)}, // not copied into a fork; makes the parent's file start earlier
		user("shared-u1", "p1", e.at(0)),
		assistant("pa1", "pmsg1", "preq1", "claude-sonnet-5-5", e.at(1), 1_000_000, 100_000, 0, 0, 0), // $2 + $1
		costState(5000, 10),
	}
	fork := tl{
		user("shared-u1", "p1", e.at(0)), // copied prefix, same uuid
		assistant("pa1", "pmsg1", "preq1", "claude-sonnet-5-5", e.at(1), 1_000_000, 100_000, 0, 0, 0),
		user("fork-u2", "pf2", e.at(30)),
		assistant("fa1", "fmsg1", "freq1", "claude-sonnet-5-5", e.at(31), 100_000, 10_000, 0, 0, 0),
		costState(5000, 12), // includes the parent's $10
	}
	// The parent kept going after the fork, so its file was modified later than
	// the fork's: modification time must not decide who owns the shared rows.
	e.write(e.proj+"/"+parentID+".jsonl", parent, e.base.Add(2*time.Hour))
	e.write(e.proj+"/"+forkID+".jsonl", fork, e.base.Add(time.Hour))
	e.ingest()

	ps, fs := e.session(parentID), e.session(forkID)
	if fs.ForkedFrom != parentID {
		t.Fatalf("forked_from = %q", fs.ForkedFrom)
	}
	// LESSONS L-30: parent 10, fork 2, sum 12; the parent is counted once.
	if !near(OwnUSD(ps), 10) || !near(OwnUSD(fs), 2) || !near(OwnUSD(ps)+OwnUSD(fs), 12) {
		t.Errorf("parent own %v, fork own %v", OwnUSD(ps), OwnUSD(fs))
	}
	if !near(fs.TotalUSD, 12) || !near(fs.InheritedUSD, 10) {
		t.Errorf("fork stores gross total/inherited %v/%v", fs.TotalUSD, fs.InheritedUSD)
	}
	// The copied request belongs to the session that wrote it first.
	if n := len(e.requests(forkID)); n != 1 {
		t.Errorf("fork owns %d rows, want only its own 1", n)
	}
	if n := len(e.requests(parentID)); n != 1 {
		t.Errorf("parent owns %d rows", n)
	}
}

// Fork inheritance from the status line's first total beats a guess from the
// parent, when the parent kept going after the fork.
func TestForkInheritedFromFirstStatuslineTotal(t *testing.T) {
	e := newEnv(t)
	parentID, forkID := sidA, sidB
	e.write(e.proj+"/"+parentID+".jsonl", tl{
		user("shared-u1", "p1", e.at(0)),
		assistant("pa1", "pmsg1", "preq1", "claude-sonnet-5-5", e.at(1), 1, 1, 0, 0, 0),
		assistant("pa2", "pmsg2", "preq2", "claude-sonnet-5-5", e.at(60), 1, 1, 0, 0, 0), // parent continues later
		costState(1, 15),
	}, e.base)
	e.write(e.proj+"/"+forkID+".jsonl", tl{
		user("shared-u1", "p1", e.at(0)),
		assistant("pa1", "pmsg1", "preq1", "claude-sonnet-5-5", e.at(1), 1, 1, 0, 0, 0),
		assistant("fa1", "fmsg1", "freq1", "claude-sonnet-5-5", e.at(30), 1, 1, 0, 0, 0),
		costState(1, 13),
	}, e.base.Add(time.Hour))
	writeSnapshot(t, e, forkID, statusline.Snapshot{V: 1, TS: e.at(31), SessionID: forkID, CostUSD: 13, MaxUSD: 13, FirstUSD: 10, FirstTS: e.at(10)})
	e.ingest()
	fs := e.session(forkID)
	if !near(fs.InheritedUSD, 10) || !near(OwnUSD(fs), 3) {
		t.Errorf("fork inherited %v own %v; want 10 and 3", fs.InheritedUSD, OwnUSD(fs))
	}
}

func writeSnapshot(t *testing.T, e *env, id string, s statusline.Snapshot) {
	t.Helper()
	dir := e.paths.Session(id)
	os.MkdirAll(dir, 0o755)
	b, _ := json.Marshal(s)
	os.WriteFile(dir+"/statusline.json", b, 0o644)
}

func writeEvents(t *testing.T, e *env, id string, evs ...hook.Event) {
	t.Helper()
	dir := e.paths.Session(id)
	os.MkdirAll(dir, 0o755)
	var b bytes.Buffer
	for _, ev := range evs {
		ev.V, ev.SID = 1, id
		j, _ := json.Marshal(ev)
		b.Write(j)
		b.WriteByte('\n')
	}
	os.WriteFile(dir+"/events.jsonl", b.Bytes(), 0o644)
}

// LESSONS L-29: the session total comes from the status line or cost-state;
// the transcript rows are breakdown only.
func TestTotalComesFromSnapshotNotRows(t *testing.T) {
	e := newEnv(t)
	e.write(e.proj+"/"+sidA+".jsonl", tl{
		user("u1", "p1", e.at(0)),
		assistant("a1", "msg1", "req1", "claude-sonnet-5-5", e.at(1), 1_000_000, 0, 0, 0, 0), // $2 of rows
	}, e.base)
	writeSnapshot(t, e, sidA, statusline.Snapshot{V: 1, TS: e.at(2), SessionID: sidA, CostUSD: 2.60, MaxUSD: 2.60})
	writeEvents(t, e, sidA, hook.Event{Ev: "SessionStart", TS: e.at(0)})
	e.ingest(sidA) // running
	s := e.session(sidA)
	if !s.Trusted || !near(OwnUSD(s), 2.60) || !s.EndedAt.IsZero() {
		t.Errorf("running session = %+v", s)
	}
	e.ingest() // no longer live
	s = e.session(sidA)
	if s.EndedAt.IsZero() {
		t.Error("ended_at not set once the session stopped being live")
	}
}

// /login reset: the status line banked the pre-login spend; the ledger keeps it.
func TestLoginResetKeepsBankedSpend(t *testing.T) {
	e := newEnv(t)
	e.write(e.proj+"/"+sidA+".jsonl", tl{user("u1", "p1", e.at(0))}, e.base)
	writeSnapshot(t, e, sidA, statusline.Snapshot{V: 1, TS: e.at(9), SessionID: sidA, CostUSD: 0.40, BankedUSD: 5.00, MaxUSD: 0.40})
	e.ingest(sidA)
	if s := e.session(sidA); !near(OwnUSD(s), 5.40) || !near(s.BankedUSD, 5.00) {
		t.Errorf("running after reset: %+v", s)
	}
	// At exit cost-state holds only the post-login group; banked spend survives.
	e.write(e.proj+"/"+sidA+".jsonl", tl{user("u1", "p1", e.at(0)), costState(2000, 0.40)}, e.base.Add(time.Hour))
	e.ingest()
	if s := e.session(sidA); !near(OwnUSD(s), 5.40) {
		t.Errorf("ended after reset: own = %v", OwnUSD(s))
	}
	// If cost-state kept both groups, banked must not be counted twice.
	e.write(e.proj+"/"+sidA+".jsonl", tl{user("u1", "p1", e.at(0)), costState(1000, 5.00), costState(2000, 0.40)}, e.base.Add(2*time.Hour))
	e.ingest()
	if s := e.session(sidA); !near(OwnUSD(s), 5.40) {
		t.Errorf("both groups in cost-state: own = %v, want 5.40 (no double count)", OwnUSD(s))
	}
}

func TestUnwrappedSessionIsAnEstimate(t *testing.T) {
	e := newEnv(t)
	e.write(e.proj+"/"+sidA+".jsonl", tl{
		user("u1", "p1", e.at(0)),
		assistant("a1", "msg1", "req1", "claude-sonnet-5-5", e.at(1), 1_000_000, 0, 0, 0, 0),
	}, e.base)
	e.ingest()
	s := e.session(sidA)
	if s.Trusted || !near(OwnUSD(s), 2) {
		t.Errorf("session = %+v", s)
	}
}

func TestTaskSourcePrecedenceAndRederivation(t *testing.T) {
	e := newEnv(t)
	e.write(e.proj+"/"+sidA+".jsonl", append(basicSession(e, sidA, 1, 1), map[string]any{
		"type": "user", "uuid": "ux", "promptId": "px", "timestamp": e.at(20), "gitBranch": "feat/zzz-9-from-transcript", "message": map[string]any{}}), e.base)
	// Hook knew the branch; its default-pattern key is re-derived with the configured parser.
	writeEvents(t, e, sidA, hook.Event{Ev: "SessionStart", TS: e.at(0), Start: &hook.StartExtra{
		Branch: "feat/ops-7-thing", Task: "OPS-7", TaskSrc: "branch", Account: "hook@example.com"}})
	e.ingest()
	s := e.session(sidA)
	if s.Task != "OPS-7" || s.TaskSource != "branch" || s.Account != "hook@example.com" {
		t.Errorf("session = %+v", s)
	}
	// A tag overrides the branch.
	os.WriteFile(e.paths.Session(sidA)+"/tag", []byte("ABC-1\n"), 0o644)
	e.ingest()
	if s := e.session(sidA); s.Task != "ABC-1" || s.TaskSource != "tag" {
		t.Errorf("after tag: %+v", s)
	}
	// Removing the tag must not demote a stored higher source (CASE guard).
	os.Remove(e.paths.Session(sidA) + "/tag")
	e.ingest()
	if s := e.session(sidA); s.Task != "ABC-1" || s.TaskSource != "tag" {
		t.Errorf("a lower source overwrote the tag: %+v", s)
	}
	// Transcript-only sessions backfill from gitBranch.
	e.write(e.proj+"/"+sidB+".jsonl", tl{
		{"type": "user", "uuid": "u9", "promptId": "p9", "timestamp": e.at(0), "gitBranch": "feat/zzz-9-from-transcript", "message": map[string]any{}},
		costState(3, 1)}, e.base)
	e.ingest()
	if s := e.session(sidB); s.Task != "ZZZ-9" || s.TaskSource != "transcript" {
		t.Errorf("backfill: %+v", s)
	}
}

// Env and tag sources are never re-derived.
func TestEnvTaskIsKept(t *testing.T) {
	e := newEnv(t)
	e.write(e.proj+"/"+sidA+".jsonl", basicSession(e, sidA, 1, 1), e.base)
	writeEvents(t, e, sidA, hook.Event{Ev: "SessionStart", TS: e.at(0), Start: &hook.StartExtra{Branch: "ops-7", Task: "ENV-1", TaskSrc: "env"}})
	e.ingest()
	if s := e.session(sidA); s.Task != "ENV-1" || s.TaskSource != "env" {
		t.Errorf("session = %+v", s)
	}
}

// LESSONS L-14: concurrent ingests on a fresh DB, repeatedly, leave the same rows.
func TestConcurrentIngestIsStable(t *testing.T) {
	e := newEnv(t)
	for i, sid := range []string{sidA, sidB, sidC} {
		e.write(e.proj+"/"+sid+".jsonl", uniq(basicSession(e, sid, int64(i+1), float64(i+1)), sid), e.base.Add(time.Duration(i)*time.Minute))
		writeEvents(t, e, sid, hook.Event{Ev: "SessionStart", TS: e.at(0), Start: &hook.StartExtra{Branch: "feat/abc-1", Task: "ABC-1", TaskSrc: "branch", Account: "x@example.com"}})
	}
	for round := 0; round < 20; round++ {
		path := fmt.Sprintf("%s/db-%d/bosun.db", e.root, round)
		var wg sync.WaitGroup
		for w := 0; w < 3; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				db, err := Open(path)
				if err != nil {
					t.Error(err)
					return
				}
				defer db.Close()
				if _, err := db.Ingest(IngestOpts{Paths: e.paths, Parser: task.Default(), Account: "me@example.com",
					Now: func() time.Time { return e.base.Add(48 * time.Hour) }}); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		// One more settling run, then the rows must match a reference.
		db, _ := Open(path)
		db.Ingest(IngestOpts{Paths: e.paths, Parser: task.Default(), Account: "me@example.com", Now: func() time.Time { return e.base.Add(48 * time.Hour) }})
		snap, err := db.Load(time.Time{})
		db.Close()
		if err != nil {
			t.Fatal(err)
		}
		if len(snap.Sessions) != 3 || len(snap.Requests) != 9 {
			t.Fatalf("round %d: sessions=%d requests=%d", round, len(snap.Sessions), len(snap.Requests))
		}
		for id, s := range snap.Sessions {
			if s.Task != "ABC-1" || s.TaskSource != "branch" || s.Account != "x@example.com" {
				t.Fatalf("round %d: %s task/account corrupted: %+v", round, id, s)
			}
		}
	}
}

func TestIngestLockIsNonBlocking(t *testing.T) {
	e := newEnv(t)
	e.write(e.proj+"/"+sidA+".jsonl", basicSession(e, sidA, 1, 1), e.base)
	held := holdLock(t, e.paths.Locks()+"/ingest.lock")
	defer held()
	start := time.Now()
	st, err := e.db.Ingest(IngestOpts{Paths: e.paths, Parser: task.Default()})
	if err != nil || !st.Skipped || time.Since(start) > time.Second {
		t.Errorf("stats=%+v err=%v after %v", st, err, time.Since(start))
	}
}

// LESSONS L-16: a budgeted ingest returns in time and finishes over later runs.
func TestBudgetedIngestResumes(t *testing.T) {
	e := newEnv(t)
	const n = 40
	for i := 0; i < n; i++ {
		sid := fmt.Sprintf("%08d-0000-0000-0000-000000000000", i)
		e.write(e.proj+"/"+sid+".jsonl", uniq(basicSession(e, sid, int64(i+1), 1), sid), e.base.Add(time.Duration(i)*time.Second))
	}
	clock := e.base
	calls := 0
	opts := IngestOpts{Paths: e.paths, Parser: task.Default(), Budget: 10 * time.Second,
		Now: func() time.Time { calls++; return clock.Add(time.Duration(calls) * 4 * time.Second) }} // each look at the clock costs 4 s
	runs := 0
	for ; runs < 100; runs++ {
		st, err := e.db.Ingest(opts)
		if err != nil {
			t.Fatal(err)
		}
		if !st.BudgetHit {
			break
		}
		if st.Files == 0 {
			t.Fatal("a budgeted run made no progress")
		}
	}
	if runs < 2 {
		t.Errorf("finished in %d runs; the budget never bit", runs+1)
	}
	snap, _ := e.db.Load(time.Time{})
	if len(snap.Sessions) != n {
		t.Errorf("sessions = %d after resuming, want %d", len(snap.Sessions), n)
	}
}

// TestPromptFilter for the ledger: the sentinel sits in prompt text, assistant
// text and a subagent description; it must be in no database file.
func TestNoTextReachesTheDatabaseFiles(t *testing.T) {
	e := newEnv(t)
	e.write(e.proj+"/"+sidA+".jsonl", basicSession(e, sidA, 1, 1), e.base)
	agent := e.proj + "/" + sidA + "/subagents/agent-ag1.jsonl"
	e.write(agent, tl{assistant("sa1", "smsg1", "sreq1", "claude-haiku-5-5", e.at(3), 1, 1, 0, 0, 0, map[string]any{"agentId": "ag1"})}, e.base)
	os.WriteFile(strings.TrimSuffix(agent, ".jsonl")+".meta.json", []byte(`{"agentType":"Explore","description":"`+sentinel+`"}`), 0o644)
	e.ingest()
	e.db.sql.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	n := 0
	filepath.WalkDir(e.paths.State, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
			if b, _ := os.ReadFile(p); bytes.Contains(b, []byte("SENTINEL")) {
				t.Errorf("text reached %s", p)
			}
		}
		return nil
	})
	if n == 0 {
		t.Fatal("no files scanned")
	}
}

func TestMigrationsRunOnceAndReopen(t *testing.T) {
	e := newEnv(t)
	e.db.Close()
	db, err := Open(e.paths.State + "/bosun.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v int
	db.sql.QueryRow("PRAGMA user_version").Scan(&v)
	if v != len(migrations) {
		t.Errorf("user_version = %d", v)
	}
	var mode string
	db.sql.QueryRow("PRAGMA journal_mode").Scan(&mode)
	if mode != "wal" {
		t.Errorf("journal_mode = %s", mode)
	}
}

// LESSONS L-30: one definition of the subtraction. A grep over the sources.
func TestInheritedSubtractedInExactlyOnePlace(t *testing.T) {
	var hits []string
	filepath.WalkDir("../..", func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && (d.Name() == ".claude" || d.Name() == ".git") {
			return filepath.SkipDir // agent worktrees hold copies of the sources
		}
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") ||
			strings.Contains(p, "/kit/") {
			return nil
		}
		b, _ := os.ReadFile(p)
		for i, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, "InheritedUSD") && strings.Contains(line, "-") &&
				!strings.HasPrefix(strings.TrimSpace(line), "//") && !strings.Contains(line, "inherited_usd") &&
				(strings.Contains(line, "- ") || strings.Contains(line, " -")) && strings.Contains(line, "return") {
				hits = append(hits, fmt.Sprintf("%s:%d: %s", p, i+1, strings.TrimSpace(line)))
			}
		}
		return nil
	})
	if len(hits) != 1 || !strings.Contains(hits[0], "session.go") {
		t.Errorf("fork inheritance is subtracted at %v; want exactly OwnUSD in session.go", hits)
	}
}

// D-21, shaped like a real resumed session (519b89a9): the transcript starts
// hours before the only cost-state startTime, so Claude's total misses the
// earlier process's spend. Rows before the startTime are added back as an
// estimate; rows after it stay covered by Claude's figure.
func TestRowsBeforeEarliestStartTimeAreAddedBackAsEstimate(t *testing.T) {
	e := newEnv(t)
	startMS := e.base.Add(10 * time.Hour).UnixMilli() // the resumed process began here
	e.write(e.proj+"/"+sidA+".jsonl", tl{
		user("u1", "p1", e.at(0)),
		assistant("a1", "msg1", "req1", "claude-sonnet-5-5", e.at(60), 1_000_000, 0, 0, 0, 0), // $2.00, before the startTime
		assistant("a2", "msg2", "req2", "claude-sonnet-5-5", e.at(120), 0, 100_000, 0, 0, 0),  // $1.00, before the startTime
		user("u2", "p2", e.at(12*60)),
		assistant("a3", "msg3", "req3", "claude-sonnet-5-5", e.at(12*60+1), 1_000_000, 0, 0, 0, 0), // $2.00, covered
		costState(startMS, 2.50), // Claude's figure: the later process only
	}, e.base)
	e.ingest()
	s := e.session(sidA)
	if !near(OwnUSD(s), 5.50) { // 2.50 + 3.00 added back
		t.Errorf("OwnUSD = %v, want 2.50 + 3.00", OwnUSD(s))
	}
	if !s.Trusted || s.CoveredFrom.UnixMilli() != startMS {
		t.Errorf("session = %+v", s)
	}
	// The rows themselves are unchanged and the covered row is not added twice.
	var sum float64
	for _, r := range e.requests(sidA) {
		sum += PriceRequest(r).USD
	}
	if !near(sum, 5.00) {
		t.Errorf("rows = %v", sum)
	}
}

func TestNoAddBackWhenTheTranscriptStartsAtTheStartTime(t *testing.T) {
	e := newEnv(t)
	e.write(e.proj+"/"+sidA+".jsonl", basicSession(e, sidA, e.base.Add(-time.Minute).UnixMilli(), 7), e.base)
	e.ingest()
	if s := e.session(sidA); !near(OwnUSD(s), 7) {
		t.Errorf("OwnUSD = %v", OwnUSD(s))
	}
}

func TestSnapshotOwnUSDSubtractsOnlyForForks(t *testing.T) {
	snap := statusline.Snapshot{CostUSD: 12, BankedUSD: 1, FirstUSD: 10}
	if got := SnapshotOwnUSD(snap, false); !near(got, 13) {
		t.Errorf("not a fork: %v", got)
	}
	if got := SnapshotOwnUSD(snap, true); !near(got, 3) {
		t.Errorf("fork: %v", got)
	}
}

// Review finding 7: two transcripts whose earliest timestamps tie must still be
// ordered parent first, whatever their names and modification times say.
func TestForkOrderOnATieUsesLinesBeforeTheSharedPrefix(t *testing.T) {
	e := newEnv(t)
	parentID, forkID := "ffffffff-0000-0000-0000-0000000000ff", "11111111-0000-0000-0000-000000000011"
	shared := tl{
		user("shared-u1", "p1", e.at(0)),
		assistant("pa1", "pmsg1", "preq1", "claude-sonnet-5-5", e.at(1), 1_000_000, 100_000, 0, 0, 0),
	}
	// A line with no timestamp, so the earliest timestamps tie; the fork never
	// copied it.
	parent := append(tl{{"type": "mode", "mode": "default"}}, shared...)
	parent = append(parent, costState(5000, 10))
	fork := append(append(tl{}, shared...),
		user("fork-u2", "pf2", e.at(30)),
		assistant("fa1", "fmsg1", "freq1", "claude-sonnet-5-5", e.at(31), 100_000, 10_000, 0, 0, 0),
		costState(5000, 12))
	e.write(e.proj+"/"+parentID+".jsonl", parent, e.base.Add(2*time.Hour))
	e.write(e.proj+"/"+forkID+".jsonl", fork, e.base.Add(time.Hour))
	e.ingest()
	if fs := e.session(forkID); fs.ForkedFrom != parentID {
		t.Fatalf("forked_from = %q, want the parent", fs.ForkedFrom)
	}
	if n := len(e.requests(parentID)); n != 1 {
		t.Errorf("parent owns %d rows, want the shared one", n)
	}
}

func TestForkOrderOnATieUsesTheSessionStartSource(t *testing.T) {
	e := newEnv(t)
	parentID, forkID := "ffffffff-0000-0000-0000-0000000000ff", "11111111-0000-0000-0000-000000000011"
	shared := tl{
		user("shared-u1", "p1", e.at(0)),
		assistant("pa1", "pmsg1", "preq1", "claude-sonnet-5-5", e.at(1), 1_000_000, 100_000, 0, 0, 0),
	}
	parent := append(append(tl{}, shared...), costState(5000, 10))
	fork := append(append(tl{}, shared...),
		user("fork-u2", "pf2", e.at(30)),
		assistant("fa1", "fmsg1", "freq1", "claude-sonnet-5-5", e.at(31), 100_000, 10_000, 0, 0, 0),
		costState(5000, 12))
	e.write(e.proj+"/"+parentID+".jsonl", parent, e.base.Add(2*time.Hour))
	e.write(e.proj+"/"+forkID+".jsonl", fork, e.base.Add(time.Hour))
	dir := e.paths.Session(forkID)
	os.MkdirAll(dir, 0o700)
	os.WriteFile(dir+"/events.jsonl", []byte(`{"v":1,"ts":"`+e.at(0)+`","ev":"SessionStart","sid":"`+forkID+`","src":"fork"}`+"\n"), 0o600)
	e.ingest()
	if fs := e.session(forkID); fs.ForkedFrom != parentID {
		t.Fatalf("forked_from = %q, want the parent", fs.ForkedFrom)
	}
}

func TestReadAgentTypeKeepsIdentifiersOnly(t *testing.T) {
	for in, want := range map[string]string{
		"Explore": "Explore", "plug:agent-1": "plug:agent-1",
		"evil\u001b]52;c;aGk=\u0007": "", "a\u202eb": "", strings.Repeat("x", 65): "",
	} {
		p := filepath.Join(t.TempDir(), "agent.meta.json")
		b, _ := json.Marshal(map[string]string{"agentType": in})
		os.WriteFile(p, b, 0o644)
		if got := readAgentType(p); got != want {
			t.Errorf("readAgentType(%q) = %q, want %q", in, got, want)
		}
	}
}
