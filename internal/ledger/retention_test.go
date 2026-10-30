package ledger

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jmvaldez/bosun/internal/hook"
	"github.com/jmvaldez/bosun/internal/task"
)

func (e *env) ingestAt(now time.Time, live ...string) {
	e.t.Helper()
	isLive := map[string]bool{}
	for _, id := range live {
		isLive[id] = true
	}
	if _, err := e.db.Ingest(IngestOpts{Paths: e.paths, Parser: task.Default(), IsLive: func(id string) bool { return isLive[id] }, Now: func() time.Time { return now }}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) sessionDir(id string) string {
	d := e.paths.Session(id)
	os.MkdirAll(d, 0o700)
	os.WriteFile(filepath.Join(d, "events.jsonl"), []byte("x\n"), 0o600)
	return d
}

// SPEC: sessions/<id>/ is pruned 7 days after end and ingest, and needs ended_at.
func TestRetentionPrunesEndedSessionDirsAfterSevenDays(t *testing.T) {
	e := newEnv(t)
	for i, sid := range []string{sidA, sidB, sidC} {
		e.write(e.proj+"/"+sid+".jsonl", uniq(basicSession(e, sid, int64(i+1), 1), sid), e.base)
	}
	dirs := map[string]string{}
	for _, sid := range []string{sidA, sidB, sidC} {
		dirs[sid] = e.sessionDir(sid)
	}
	// A ended just over seven days before now; B six days and an hour before;
	// C is still running.
	writeEvents(t, e, sidA, hook.Event{Ev: "SessionStart", TS: e.at(0)}, hook.Event{Ev: "SessionEnd", TS: e.at(10)})
	writeEvents(t, e, sidB, hook.Event{Ev: "SessionStart", TS: e.at(0)}, hook.Event{Ev: "SessionEnd", TS: e.base.Add(24 * time.Hour).Format("2006-01-02T15:04:05.000Z")})
	writeEvents(t, e, sidC, hook.Event{Ev: "SessionStart", TS: e.at(0)})
	now := e.base.Add(7*24*time.Hour + time.Hour)
	e.ingestAt(now, sidC)

	got, err := e.db.Retain(e.paths, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionDirs != 1 {
		t.Errorf("pruned %d dirs, want only A's", got.SessionDirs)
	}
	if _, err := os.Stat(dirs[sidB]); err != nil {
		t.Error("a session that ended under seven days ago was pruned")
	}
	if _, err := os.Stat(dirs[sidA]); !os.IsNotExist(err) {
		t.Error("an ended session's dir was kept past seven days")
	}
	if _, err := os.Stat(dirs[sidC]); err != nil {
		t.Error("a running session's dir was pruned")
	}
	// The ledger rows survive the dir.
	if s := e.session(sidA); s.EndedAt.IsZero() {
		t.Error("the session row lost its end")
	}
}

func TestRetentionNeedsEndAndIngestAndSkipsSymlinks(t *testing.T) {
	e := newEnv(t)
	e.write(e.proj+"/"+sidA+".jsonl", basicSession(e, sidA, 1, 1), e.base)
	dir := e.sessionDir(sidA)
	writeEvents(t, e, sidA, hook.Event{Ev: "SessionStart", TS: e.at(0)}, hook.Event{Ev: "SessionEnd", TS: e.at(10)})
	// Ended eight days ago but never ingested after: nothing is pruned.
	now := e.base.Add(8 * 24 * time.Hour)
	if got, _ := e.db.Retain(e.paths, now); got.SessionDirs != 0 {
		t.Error("pruned a dir the ledger has not ingested")
	}
	// Ingest happened before the end: still nothing.
	e.db.SetMeta("last_ingest", e.base.Format(time.RFC3339))
	if got, _ := e.db.Retain(e.paths, now); got.SessionDirs != 0 {
		t.Error("pruned a dir whose end came after the last ingest")
	}
	e.ingestAt(now)
	// A symlink in place of the dir is left alone.
	os.RemoveAll(dir)
	target := t.TempDir()
	os.WriteFile(filepath.Join(target, "keep"), []byte("x"), 0o600)
	os.Symlink(target, dir)
	if got, _ := e.db.Retain(e.paths, now); got.SessionDirs != 0 {
		t.Error("followed a symlink")
	}
	if _, err := os.Stat(filepath.Join(target, "keep")); err != nil {
		t.Error("deleted through a symlink")
	}
}

// Requests past 400 days are rolled up, never lost from a session's total, and
// not re-inserted by a later ingest.
func TestRetentionRollsUpOldRequestsWithoutLosingSpend(t *testing.T) {
	e := newEnv(t)
	old := e.base // the "now" below is 500 days later
	now := old.Add(500 * 24 * time.Hour)
	e.write(e.proj+"/"+sidA+".jsonl", tl{
		user("u1", "p1", e.at(0)),
		assistant("a1", "msg1", "req1", "claude-sonnet-5-5", e.at(1), 1_000_000, 0, 0, 0, 0), // $2
		assistant("a2", "msg2", "req2", "claude-sonnet-5-5", e.at(2), 0, 100_000, 0, 0, 0),   // $1
		assistant("a3", "msg3", "req3", "claude-haiku-5-5", e.at(3), 10_000, 0, 0, 0, 0),     // $0.001
		costState(1, 3.50),
	}, old)
	// Pretend these rows were ingested when they were fresh.
	e.ingestAt(old.Add(time.Hour))
	before := ownTotal(t, e)
	if got, err := e.db.Retain(e.paths, now); err != nil || got.Requests != 3 {
		t.Fatalf("retain: %+v %v", got, err)
	}
	if n := len(e.requests(sidA)); n != 0 {
		t.Errorf("requests left = %d", n)
	}
	snap, _ := e.db.Load(time.Time{})
	if len(snap.Rollups) != 2 { // sonnet and haiku on the same UTC day
		t.Fatalf("rollups = %+v", snap.Rollups)
	}
	var sum float64
	for _, r := range snap.Rollups {
		sum += r.USD
	}
	if !near(sum, 3.001) {
		t.Errorf("rollup usd = %v", sum)
	}
	if after := ownTotal(t, e); !near(before, after) {
		t.Errorf("session total changed: %v -> %v", before, after)
	}
	// A second run adds nothing, and re-ingesting the untouched transcript does not bring the rows back.
	if got, _ := e.db.Retain(e.paths, now); got.Requests != 0 {
		t.Errorf("second retain pruned %d", got.Requests)
	}
	os.Chtimes(e.proj+"/"+sidA+".jsonl", now, now)
	e.ingestAt(now)
	if n := len(e.requests(sidA)); n != 0 {
		t.Errorf("pruned rows came back (%d)", n)
	}
	snap, _ = e.db.Load(time.Time{})
	var sum2 float64
	for _, r := range snap.Rollups {
		sum2 += r.USD
	}
	if !near(sum2, sum) {
		t.Errorf("rollups changed on re-ingest: %v -> %v", sum, sum2)
	}
}

func ownTotal(t *testing.T, e *env) float64 {
	t.Helper()
	return OwnUSD(e.session(sidA))
}

// Backfill is the ordinary ingest limited to recent files; twice gives the
// same ledger, and the limit leaves older transcripts unread.
func TestBackfillSinceLimitsFilesAndIsIdempotent(t *testing.T) {
	e := newEnv(t)
	e.write(e.proj+"/"+sidA+".jsonl", uniq(basicSession(e, sidA, 1, 1), sidA), e.base.Add(-40*24*time.Hour)) // old
	e.write(e.proj+"/"+sidB+".jsonl", uniq(basicSession(e, sidB, 2, 2), sidB), e.base)
	run := func() {
		if _, err := e.db.Ingest(IngestOpts{Paths: e.paths, Parser: task.Default(), Since: e.base.Add(-30 * 24 * time.Hour),
			Now: func() time.Time { return e.base.Add(24 * time.Hour) }}); err != nil {
			t.Fatal(err)
		}
	}
	run()
	first := dump(t, e)
	snap, _ := e.db.Load(time.Time{})
	if len(snap.Sessions) != 1 || snap.Sessions[sidB].ID == "" {
		t.Fatalf("sessions = %v; the 40-day-old transcript should be skipped", snap.Sessions)
	}
	run()
	if second := dump(t, e); second != first {
		t.Errorf("backfill twice changed the ledger:\n%s\n---\n%s", first, second)
	}
	// Without the limit the older transcript comes in too.
	if _, err := e.db.Ingest(IngestOpts{Paths: e.paths, Parser: task.Default(), Now: func() time.Time { return e.base.Add(24 * time.Hour) }}); err != nil {
		t.Fatal(err)
	}
	if snap, _ := e.db.Load(time.Time{}); len(snap.Sessions) != 2 {
		t.Errorf("sessions = %d", len(snap.Sessions))
	}
}
