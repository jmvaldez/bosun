package refresh

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jmvaldez/bosun/internal/config"
	"github.com/jmvaldez/bosun/internal/paths"
	"github.com/jmvaldez/bosun/internal/platform"
)

type call struct {
	timeout time.Duration
	argv    []string
}

type fakeRunner struct {
	mu    sync.Mutex
	calls []call
	out   []byte
	err   error
}

func (f *fakeRunner) Run(timeout time.Duration, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call{timeout, append([]string{name}, args...)})
	return f.out, f.err
}

type fakeIdentity struct {
	key   string
	err   error
	calls int
}

func (f *fakeIdentity) Account() (string, error) { f.calls++; return f.key, f.err }

type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

func setup(t *testing.T) (platform.Adapters, *fakeRunner, *fakeIdentity, config.Config) {
	t.Helper()
	root := t.TempDir()
	run := &fakeRunner{out: []byte(agentsJSON)}
	id := &fakeIdentity{key: "me@example.com"}
	ad := platform.Adapters{
		Paths:    paths.Paths{State: root + "/state", Cache: root + "/cache"},
		Procs:    platform.NewProcs(),
		Runner:   run,
		Identity: id,
		Clock:    fixedClock{time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)},
	}
	cfg := config.Default()
	cfg.Claude.Bin = "/fake/claude"
	return ad, run, id, cfg
}

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
	bgIdleish := Row{Background: true, State: "done", Status: "idle"}
	if bgIdleish.IdleSignal() {
		t.Error("background rows never give the idle-over-working signal")
	}
}

func TestRunWritesCacheWithoutPromptDerivedText(t *testing.T) {
	ad, run, id, cfg := setup(t)
	run.out = []byte(fixture())
	var out, errw bytes.Buffer
	if code := Run(ad, cfg, &out, &errw); code != 0 {
		t.Fatalf("code %d: %s", code, errw.String())
	}
	if len(run.calls) != 1 || strings.Join(run.calls[0].argv, " ") != "/fake/claude agents --json" {
		t.Fatalf("calls = %+v", run.calls)
	}
	if run.calls[0].timeout <= 0 || run.calls[0].timeout > 10*time.Second {
		t.Errorf("timeout = %v", run.calls[0].timeout)
	}
	b, err := os.ReadFile(ad.Paths.AgentsCache())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "SENTINEL") {
		t.Errorf("name leaked into the cache: %s", b)
	}
	c, stale := ReadCache(ad.Paths.AgentsCache(), ad.Clock.Now().Add(time.Second), TTL)
	if stale || len(c.Rows) != 5 {
		t.Errorf("stale=%v rows=%d", stale, len(c.Rows))
	}
	if _, stale := ReadCache(ad.Paths.AgentsCache(), ad.Clock.Now().Add(6*time.Second), TTL); !stale {
		t.Error("cache should be stale after the TTL")
	}
	if got, ok := ReadIdentity(ad.Paths.Identity()); !ok || got.AccountKey != "me@example.com" {
		t.Errorf("identity = %+v ok=%v", got, ok)
	}
	if id.calls != 1 {
		t.Errorf("identity calls = %d", id.calls)
	}
}

func TestIdentityNotRefetchedWithinTTL(t *testing.T) {
	ad, _, id, cfg := setup(t)
	for i := 0; i < 3; i++ {
		Run(ad, cfg, &bytes.Buffer{}, &bytes.Buffer{})
	}
	if id.calls != 1 {
		t.Errorf("identity asked %d times in one TTL", id.calls)
	}
}

func TestAgentsFailureKeepsOldCache(t *testing.T) {
	ad, run, _, cfg := setup(t)
	run.out = []byte(fixture())
	Run(ad, cfg, &bytes.Buffer{}, &bytes.Buffer{})
	before, _ := os.ReadFile(ad.Paths.AgentsCache())
	run.err = errors.New("boom")
	var errw bytes.Buffer
	if code := Run(ad, cfg, &bytes.Buffer{}, &errw); code == 0 {
		t.Error("want nonzero when claude agents fails")
	}
	after, _ := os.ReadFile(ad.Paths.AgentsCache())
	if !bytes.Equal(before, after) {
		t.Error("failed refresh replaced the cache")
	}
}

func TestBadAgentsOutput(t *testing.T) {
	ad, run, _, cfg := setup(t)
	run.out = []byte(`{"not":"an array"}`)
	if code := Run(ad, cfg, &bytes.Buffer{}, &bytes.Buffer{}); code == 0 {
		t.Error("want nonzero")
	}
	if _, err := os.Stat(ad.Paths.AgentsCache()); !os.IsNotExist(err) {
		t.Error("cache written from bad output")
	}
}

func TestSecondRefreshDedupesOnLock(t *testing.T) {
	ad, run, _, cfg := setup(t)
	// Hold the lock the way a concurrent refresh would.
	held := heldLock(t, filepath.Join(ad.Paths.Locks(), "refresh.lock"))
	defer held()
	if code := Run(ad, cfg, &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Errorf("code = %d", code)
	}
	if len(run.calls) != 0 {
		t.Error("a refresh ran while another held the lock")
	}
}

func TestMissingCacheIsStale(t *testing.T) {
	c, stale := ReadCache(filepath.Join(t.TempDir(), "nope.json"), time.Now(), TTL)
	if !stale || len(c.Rows) != 0 {
		t.Error("missing cache must read as stale and empty")
	}
}

func TestResolveClaudeFromPath(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755)
	t.Setenv("PATH", dir)
	got, err := ResolveClaude(config.Default())
	if err != nil || got != bin {
		t.Fatalf("got %q err %v", got, err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := ResolveClaude(config.Default()); err == nil {
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
