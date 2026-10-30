package claude

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jmvaldez/bosun/internal/config"
	"github.com/jmvaldez/bosun/internal/platform"
)

// Row shapes follow FACTS-CHECK F5: pid rows have status, background rows have
// neither pid nor status but do have id and state. name is prompt-derived.
const agentsJSON = `[
 {"pid":%PID%,"cwd":"/w/a","kind":"interactive","startedAt":1792793684418,"sessionId":"aaaaaaaa-0000-0000-0000-000000000001","name":"SENTINEL-NAME","status":"busy"},
 {"pid":%PID%,"cwd":"/w/b","kind":"interactive","startedAt":1792793684419,"sessionId":"aaaaaaaa-0000-0000-0000-000000000002","name":"SENTINEL-NAME","status":"waiting","waitingFor":"permission prompt"},
 {"pid":%PID%,"cwd":"/w/c","kind":"interactive","startedAt":1792793684420,"sessionId":"aaaaaaaa-0000-0000-0000-000000000003","name":"SENTINEL-NAME","status":"idle"},
 {"id":"2cd7bf42","cwd":"/w/d","kind":"background","startedAt":1788019576728,"sessionId":"2cd7bf42-e3fb-4f2a-aa4f-ad73e4812a13","name":"SENTINEL-NAME","state":"blocked"},
 {"id":"e3d8ac6e","cwd":"/w/e","kind":"background","startedAt":1788048944865,"sessionId":"e3d8ac6e-c348-4fee-97c2-851d8fb01954","name":"SENTINEL-NAME","state":"done"},
 {"pid":%PID%,"cwd":"/w/f","kind":"interactive","startedAt":1,"sessionId":"../evil","status":"busy"}
]`

func fixture() string {
	return strings.ReplaceAll(agentsJSON, "%PID%", strconv.Itoa(os.Getpid()))
}

func TestParseAgentsRows(t *testing.T) {
	rows, err := ParseAgents([]byte(fixture()), platform.NewProcs())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 5 {
		t.Fatalf("rows = %d (the traversal-id row must be dropped)", len(rows))
	}
	if r := rows[0]; r.Background || r.Status != "busy" || r.PID != os.Getpid() || r.StartTok == "" {
		t.Errorf("busy row = %+v", r)
	}
	if r := rows[1]; r.Status != "waiting" || r.WaitingFor != "permission prompt" {
		t.Errorf("waiting row = %+v", r)
	}
	if r := rows[3]; !r.Background || r.PID != 0 || r.State != "blocked" || r.ID != "2cd7bf42" || r.Status != "" {
		t.Errorf("background row = %+v", r)
	}
}

func TestLiveAndIdleSignalForBackgroundRows(t *testing.T) {
	rows, _ := ParseAgents([]byte(fixture()), platform.NewProcs())
	procs := platform.NewProcs()
	if !rows[0].Live(procs) {
		t.Error("pid row for this process should be live")
	}
	dead := rows[0]
	dead.StartTok += "9"
	if dead.Live(procs) {
		t.Error("start token mismatch must read as dead (pid reuse)")
	}
	if !rows[3].Live(procs) {
		t.Error("blocked background row is live (no kill -0 for it)")
	}
	if rows[4].Live(procs) {
		t.Error("done background row is not live")
	}
	if !rows[2].IdleSignal() {
		t.Error("idle pid row should signal idle")
	}
	bgIdleish := Agent{Background: true, State: "done", Status: "idle"}
	if bgIdleish.IdleSignal() {
		t.Error("background rows never give the idle-over-working signal")
	}
}

func TestMissingCacheIsStale(t *testing.T) {
	c, stale := ReadAgents(filepath.Join(t.TempDir(), "nope.json"), time.Now(), AgentsTTL)
	if !stale || len(c.Rows) != 0 {
		t.Error("missing cache must read as stale and empty")
	}
}

func TestResolveFromPath(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755)
	t.Setenv("PATH", dir)
	got, err := Resolve(config.Default())
	if err != nil || got != bin {
		t.Fatalf("got %q err %v", got, err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := Resolve(config.Default()); err == nil {
		t.Error("want error when claude is absent")
	}
}

// The captured `claude agents --json` (redacted) has one pid row and two
// background rows without pid or status.
func TestParseCapturedAgentsFixture(t *testing.T) {
	b, err := os.ReadFile("../../testdata/agents-json.json")
	if err != nil {
		t.Fatal(err)
	}
	// Placeholder ids are rightly rejected as session ids; swap in valid ones.
	b = regexp.MustCompile(`<(session-\d+)>`).ReplaceAll(b, []byte("$1"))
	rows, err := ParseAgents(b, nil)
	if err != nil {
		t.Fatal(err)
	}
	var pidRows, bg int
	for _, r := range rows {
		if r.Background {
			bg++
			if r.PID != 0 || r.Status != "" || r.State == "" {
				t.Errorf("background row = %+v", r)
			}
		} else {
			pidRows++
			if r.PID == 0 || r.Status == "" {
				t.Errorf("pid row = %+v", r)
			}
		}
	}
	if pidRows != 1 || bg != 2 {
		t.Errorf("pid rows = %d, background = %d", pidRows, bg)
	}
}
