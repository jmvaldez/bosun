package heartbeat

import (
	"sync"
	"testing"
	"time"

	"github.com/jmvaldez/bosun/internal/paths"
)

func TestRingRoundTripsAndCaps(t *testing.T) {
	p := paths.Paths{State: t.TempDir()}
	now := time.Now()
	for i := 0; i < 100; i++ {
		if err := Record(p, now, "Stop", "0.1.0", time.Duration(i)*time.Microsecond); err != nil {
			t.Fatal(err)
		}
	}
	hb, err := Read(p.Heartbeat())
	if err != nil {
		t.Fatal(err)
	}
	if got := len(hb.LatencyUS["Stop"]); got != 100 {
		t.Fatalf("samples = %d, want 100", got)
	}
	if hb.PluginVersion != "0.1.0" {
		t.Errorf("version = %q", hb.PluginVersion)
	}
	for i := 0; i < 100; i++ {
		Record(p, now, "Stop", "0.1.0", time.Millisecond)
	}
	hb, _ = Read(p.Heartbeat())
	if got := len(hb.LatencyUS["Stop"]); got != RingSize {
		t.Fatalf("ring not capped: %d", got)
	}
}

func TestConcurrentRecordsAreNotLost(t *testing.T) {
	p := paths.Paths{State: t.TempDir()}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := Record(p, time.Now(), "PreToolUse", "", time.Microsecond); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	hb, _ := Read(p.Heartbeat())
	if got := len(hb.LatencyUS["PreToolUse"]); got != 8 {
		t.Fatalf("samples = %d, want 8", got)
	}
}

func TestQuantiles(t *testing.T) {
	hb := Heartbeat{LatencyUS: map[string][]int64{"a": {1000, 2000, 3000}, "b": {4000, 100000}}}
	p50, p95, n := hb.Quantiles()
	if n != 5 || p50 != 3*time.Millisecond || p95 != 100*time.Millisecond {
		t.Fatalf("p50=%v p95=%v n=%d", p50, p95, n)
	}
	if _, _, n := (Heartbeat{}).Quantiles(); n != 0 {
		t.Error("empty heartbeat should have no samples")
	}
}
