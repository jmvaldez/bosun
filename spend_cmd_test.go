package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmvaldez/bosun/internal/ledger"
)

// writeTranscript lays down a tiny Claude Code transcript under the isolated
// HOME so `bosun spend` reads it as ~/.claude/projects/....
func writeTranscript(t *testing.T, root, sid string, total float64, mtime time.Time) {
	t.Helper()
	dir := filepath.Join(root, "home", ".claude", "projects", "-work-api")
	os.MkdirAll(dir, 0o755)
	ts := func(m int) string {
		return mtime.Add(time.Duration(m) * time.Minute).UTC().Format("2006-01-02T15:04:05.000Z")
	}
	lines := []map[string]any{
		{"type": "user", "uuid": "u1-" + sid, "promptId": "p1-" + sid, "timestamp": ts(0), "message": map[string]any{"content": "SENTINEL-SPEND"}},
		{"type": "assistant", "uuid": "a1-" + sid, "requestId": "req-" + sid, "timestamp": ts(1),
			"message": map[string]any{"id": "msg-" + sid, "model": "claude-sonnet-5-5",
				"usage": map[string]any{"input_tokens": 1_000_000, "output_tokens": 100_000}}}, // $2 + $1
		{"type": "cost-state", "startTime": 7, "totalCostUSD": total},
	}
	var b strings.Builder
	for _, l := range lines {
		j, _ := json.Marshal(l)
		b.Write(j)
		b.WriteByte('\n')
	}
	p := filepath.Join(dir, sid+".jsonl")
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(p, mtime, mtime)
}

func TestSpendCommandEndToEnd(t *testing.T) {
	env, root := isolated(t)
	now := time.Now().UTC()
	writeTranscript(t, root, "aaaaaaaa-0000-0000-0000-00000000000a", 4.50, now.Add(-2*time.Hour))
	writeTranscript(t, root, "bbbbbbbb-0000-0000-0000-00000000000b", 1.25, now.Add(-time.Hour))

	for _, by := range []string{"day", "task", "repo", "session", "prompt", "request", "model", "subagent"} {
		r := runBosun(t, env, nil, "spend", "--by", by, "--json")
		if r.code != 0 {
			t.Fatalf("--by %s: exit %d\n%s", by, r.code, r.out)
		}
		var res struct {
			By    string
			Total struct{ USD float64 }
		}
		if err := json.Unmarshal(r.out, &res); err != nil {
			t.Fatalf("--by %s: %v\n%s", by, err, r.out)
		}
		// Totals are the cost-state totals (4.50 + 1.25), not the $3 of rows each.
		if res.By != by || math.Abs(res.Total.USD-5.75) > 1e-6 {
			t.Errorf("--by %s: %+v", by, res)
		}
	}
	// Re-ingest is a no-op: same answer on a second run.
	a := runBosun(t, env, nil, "spend", "--by", "session", "--json")
	b := runBosun(t, env, nil, "spend", "--by", "session", "--json")
	if string(a.out) != string(b.out) {
		t.Error("two runs disagree")
	}
	csvOut := runBosun(t, env, nil, "spend", "--by", "model", "--csv")
	if !strings.HasPrefix(string(csvOut.out), "key,sessions,requests,") || !strings.Contains(string(csvOut.out), "TOTAL,2,2,") {
		t.Errorf("csv:\n%s", csvOut.out)
	}
	if bad := runBosun(t, env, nil, "spend", "--by", "colour"); bad.code == 0 {
		t.Error("an unknown --by must fail")
	}
	// Nothing from the transcripts' text reached bosun's files.
	filepath.WalkDir(root+"/state", func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if data, _ := os.ReadFile(p); strings.Contains(string(data), "SENTINEL") {
				t.Errorf("transcript text reached %s", p)
			}
		}
		return nil
	})
}

func TestStatusSegmentShowsTodaysSpend(t *testing.T) {
	env, root := isolated(t)
	day := time.Now().Format("2006-01-02")
	os.MkdirAll(root+"/state", 0o755)
	os.WriteFile(root+"/state/today.json", []byte(fmt.Sprintf(`{"day":%q,"usd":14.2,"est_usd":1.4,"est_share":0.1}`, day)), 0o600)
	r := runBosun(t, env, nil, "status", "--tmux")
	if !strings.Contains(string(r.out), "$14.20 est.") {
		t.Errorf("segment = %q", r.out)
	}
	// Yesterday's file must not linger on today's bar.
	os.WriteFile(root+"/state/today.json", []byte(`{"day":"2001-01-01","usd":99}`), 0o600)
	if r := runBosun(t, env, nil, "status", "--tmux"); len(r.out) != 0 {
		t.Errorf("stale today.json rendered: %q", r.out)
	}
	// A configured budget turns it into $X/$cap.
	os.WriteFile(root+"/state/today.json", []byte(fmt.Sprintf(`{"day":%q,"usd":15.82}`, day)), 0o600)
	os.MkdirAll(root+"/config", 0o755)
	os.WriteFile(root+"/config/config.toml", []byte(testConfig+"[rules]\ndaily_budget = 50\n"), 0o600)
	if r := runBosun(t, env, nil, "status", "--tmux"); !strings.Contains(string(r.out), "$15.82 / $50") {
		t.Errorf("budget segment = %q", r.out)
	}
}

// refresh keeps today.json and the ledger current even when claude is missing.
func TestRefreshWritesTodayJSON(t *testing.T) {
	env, root := isolated(t)
	writeTranscript(t, root, "aaaaaaaa-0000-0000-0000-00000000000a", 4.50, time.Now().Add(-time.Minute))
	runBosun(t, env, nil, "refresh") // exits 1 without claude on PATH; the spend step still runs
	b, err := os.ReadFile(root + "/state/today.json")
	if err != nil || !strings.Contains(string(b), `"usd":4.5`) {
		t.Fatalf("today.json = %q err=%v", b, err)
	}
	db, err := ledger.Open(root + "/state/bosun.db")
	if err != nil {
		t.Fatalf("no ledger written: %v", err)
	}
	defer db.Close()
	day := time.Now().Format("2006-01-02")
	if samples, err := db.Samples(day); err != nil || len(samples) != 1 || samples[0].Cumulative != 4.5 {
		t.Errorf("spend_samples for %s = %+v err=%v", day, samples, err)
	}
}

// A row at 02:00Z lands on different days under a negative and a positive
// offset, and `spend.day = "utc"` pins it to the UTC day whatever TZ says
// (LESSONS L-36, SPEC M2 day-split acceptance).
func TestDaySplitFollowsTZAndSpendDay(t *testing.T) {
	if _, err := os.Stat("/usr/share/zoneinfo/Asia/Tokyo"); err != nil {
		t.Skip("no zoneinfo")
	}
	when := time.Date(2026, 10, 21, 2, 0, 0, 0, time.UTC)
	days := func(tz, spendDay string) []string {
		env, root := isolated(t)
		env = append(env, "TZ="+tz)
		writeTranscript(t, root, "aaaaaaaa-0000-0000-0000-00000000000a", 3.00, when)
		if spendDay != "" {
			os.MkdirAll(root+"/config", 0o755)
			os.WriteFile(root+"/config/config.toml", []byte(testConfig+"[spend]\nday = \""+spendDay+"\"\n"), 0o600)
		}
		r := runBosun(t, env, nil, "spend", "--by", "day", "--json")
		var res struct{ Rows []struct{ Key string } }
		if err := json.Unmarshal(r.out, &res); err != nil {
			t.Fatalf("%v\n%s", err, r.out)
		}
		var keys []string
		for _, row := range res.Rows {
			keys = append(keys, row.Key)
		}
		return keys
	}
	// The transcript's two lines are a minute apart, so one day each time.
	if got := days("America/Los_Angeles", ""); len(got) != 1 || got[0] != "2026-10-20" {
		t.Errorf("UTC-7 local days = %v", got)
	}
	if got := days("Asia/Tokyo", ""); len(got) != 1 || got[0] != "2026-10-21" {
		t.Errorf("UTC+9 local days = %v", got)
	}
	if got := days("America/Los_Angeles", "utc"); len(got) != 1 || got[0] != "2026-10-21" {
		t.Errorf("spend.day=utc under UTC-7 = %v", got)
	}
}

func TestBackfillCommandTwiceGivesTheSameSpend(t *testing.T) {
	env, root := isolated(t)
	writeTranscript(t, root, "aaaaaaaa-0000-0000-0000-00000000000a", 4.50, time.Now().Add(-90*24*time.Hour)) // outside 30d
	writeTranscript(t, root, "bbbbbbbb-0000-0000-0000-00000000000b", 1.25, time.Now().Add(-2*24*time.Hour))
	r := runBosun(t, env, nil, "backfill")
	if r.code != 0 || !strings.Contains(string(r.out), "backfill since 30d: 1 transcripts read") {
		t.Fatalf("exit %d\n%s", r.code, r.out)
	}
	second := runBosun(t, env, nil, "backfill")
	if second.code != 0 || !strings.Contains(string(second.out), "0 requests") {
		t.Errorf("the second backfill should read nothing new:\n%s", second.out)
	}
	if r := runBosun(t, env, nil, "backfill", "--since", "nonsense"); r.code != 2 {
		t.Errorf("a bad --since: exit %d", r.code)
	}
}

func TestFreeFormTagGroupsAndFiltersSpend(t *testing.T) {
	env, root := isolated(t)
	const tagged = "aaaaaaaa-0000-0000-0000-00000000000a"
	now := time.Now().UTC()
	writeTranscript(t, root, tagged, 4.50, now.Add(-2*time.Hour))
	writeTranscript(t, root, "bbbbbbbb-0000-0000-0000-00000000000b", 1.25, now.Add(-time.Hour))
	os.MkdirAll(filepath.Join(root, "state", "sessions", tagged), 0o755)
	if r := runBosun(t, env, nil, "tag", "Bosun", "Init", "--session", tagged); r.code != 0 {
		t.Fatalf("tag: exit %d\n%s", r.code, r.out)
	}
	r := runBosun(t, env, nil, "spend", "--by", "task", "--json")
	var res struct {
		Rows []struct {
			Key string
			USD float64
		}
	}
	if err := json.Unmarshal(r.out, &res); err != nil {
		t.Fatalf("%v\n%s", err, r.out)
	}
	got := map[string]float64{}
	for _, row := range res.Rows {
		got[row.Key] = row.USD
	}
	if math.Abs(got["Bosun Init"]-4.50) > 1e-6 || math.Abs(got["untasked"]-1.25) > 1e-6 {
		t.Errorf("task rows = %v", got)
	}
	for _, flt := range []string{"Bosun Init", "bosun init"} {
		r := runBosun(t, env, nil, "spend", "--by", "task", "--task", flt, "--json")
		if !strings.Contains(string(r.out), `"Bosun Init"`) || strings.Contains(string(r.out), "untasked") {
			t.Errorf("--task %q:\n%s", flt, r.out)
		}
	}
	csvOut := runBosun(t, env, nil, "spend", "--by", "task", "--csv")
	if !strings.Contains(string(csvOut.out), "Bosun Init,1,") {
		t.Errorf("csv:\n%s", csvOut.out)
	}
}
