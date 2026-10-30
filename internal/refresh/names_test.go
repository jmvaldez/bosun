package refresh

import (
	"errors"
	"sync"
	"testing"
	"time"
)

type namesRunner struct {
	mu    sync.Mutex
	calls int
	out   string
	err   error
	args  []string
}

func (r *namesRunner) Run(_ time.Duration, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.args = append([]string{name}, args...)
	return []byte(r.out), r.err
}

func (r *namesRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// waitNames polls until the background fetch lands a name for id.
func waitNames(t *testing.T, n *Names, now time.Time, id string) string {
	t.Helper()
	for i := 0; i < 400; i++ {
		if got := n.Get(now)[id]; got != "" {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no name for %s arrived", id)
	return ""
}

func TestParseNamesStripsControlCharacters(t *testing.T) {
	got, err := ParseNames([]byte(`[{"sessionId":"s1","name":"fix\u001b[31m the\n\tbug\u0007"},{"sessionId":"s2","name":""},{"sessionId":"","name":"x"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["s1"] != "fix [31m the bug" {
		t.Errorf("names = %q", got)
	}
}

func TestNamesFetchesInBackgroundAndCachesForTTL(t *testing.T) {
	run := &namesRunner{out: `[{"sessionId":"s1","name":"tidy the parser"}]`}
	n := NewNames(run, "/bin/claude", TTL)
	t0 := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	if got := n.Get(t0); got != nil {
		t.Errorf("first Get blocked on the fetch and returned %q", got)
	}
	if got := waitNames(t, n, t0, "s1"); got != "tidy the parser" {
		t.Errorf("name = %q", got)
	}
	n.Get(t0.Add(time.Second))
	if run.count() != 1 {
		t.Errorf("fetched %d times inside the TTL, want 1", run.count())
	}
	n.Get(t0.Add(TTL + time.Second))
	for i := 0; i < 400 && run.count() < 2; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if run.count() != 2 {
		t.Errorf("fetched %d times after the TTL, want 2", run.count())
	}
	if want := []string{"/bin/claude", "agents", "--json"}; len(run.args) != 3 || run.args[1] != want[1] || run.args[2] != want[2] {
		t.Errorf("argv = %q", run.args)
	}
}

func TestNamesKeepsOldOnFailure(t *testing.T) {
	run := &namesRunner{out: `[{"sessionId":"s1","name":"old name"}]`}
	n := NewNames(run, "claude", time.Second)
	t0 := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	n.Get(t0)
	waitNames(t, n, t0, "s1")
	run.mu.Lock()
	run.err = errors.New("claude: timeout")
	run.mu.Unlock()
	n.Get(t0.Add(2 * time.Second))
	for i := 0; i < 400 && run.count() < 2; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	if got := n.Get(t0.Add(2 * time.Second))["s1"]; got != "old name" {
		t.Errorf("name after a failed fetch = %q", got)
	}
}
