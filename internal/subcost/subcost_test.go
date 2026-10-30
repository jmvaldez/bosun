package subcost

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func line(id, req string, out int64) string {
	return `{"type":"assistant","requestId":"` + req + `","message":{"id":"` + id + `","model":"claude-sonnet-5-5","content":[{"text":"SENTINEL"}],"usage":{"input_tokens":1000000,"output_tokens":` +
		itoa(out) + `,"cache_read_input_tokens":0}}}` + "\n"
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for ; n > 0; n /= 10 {
		s = string(rune('0'+n%10)) + s
	}
	return s
}

func TestIncrementalDedupeKeepsLargestOutput(t *testing.T) {
	p := filepath.Join(t.TempDir(), "agent.jsonl")
	os.WriteFile(p, []byte(line("m1", "r1", 10)+line("m1", "r1", 100000)+line("m2", "r2", 0)), 0o644)
	var tr Tracker
	// m1: $2 input + 100k output ($1); m2: $2 input.
	if got := tr.Update(p); math.Abs(got-5) > 1e-9 {
		t.Errorf("estimate = %v, want 5", got)
	}
	// Append a partial line, then complete it; counted once.
	full := line("m3", "r3", 0)
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(full[:20])
	f.Close()
	if got := tr.Update(p); math.Abs(got-5) > 1e-9 {
		t.Errorf("partial line counted: %v", got)
	}
	f, _ = os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(full[20:])
	f.Close()
	if got := tr.Update(p); math.Abs(got-7) > 1e-9 {
		t.Errorf("after completing: %v, want 7", got)
	}
	if got := tr.Update(p); math.Abs(got-7) > 1e-9 {
		t.Errorf("an unchanged file changed the estimate: %v", got)
	}
}

func TestMissingFileIsZero(t *testing.T) {
	var tr Tracker
	if got := tr.Update(filepath.Join(t.TempDir(), "none")); got != 0 {
		t.Errorf("got %v", got)
	}
}

func TestShrunkFileRestartsCount(t *testing.T) {
	p := filepath.Join(t.TempDir(), "agent.jsonl")
	os.WriteFile(p, []byte(line("m1", "r1", 0)+line("m2", "r2", 0)), 0o644)
	var tr Tracker
	if got := tr.Update(p); math.Abs(got-4) > 1e-9 {
		t.Fatalf("estimate = %v, want 4", got)
	}
	os.WriteFile(p, []byte(line("m9", "r9", 0)), 0o644) // replaced by a shorter file
	if got := tr.Update(p); math.Abs(got-2) > 1e-9 {
		t.Errorf("after replace = %v, want 2", got)
	}
}
