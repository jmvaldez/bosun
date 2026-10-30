// Package heartbeat records when hooks last ran and how long they took, so
// doctor can tell a silent plugin from a slow one.
package heartbeat

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"sort"
	"time"

	"github.com/jmvaldez/bosun/internal/atomicfile"
	"github.com/jmvaldez/bosun/internal/flock"
	"github.com/jmvaldez/bosun/internal/paths"
)

// RingSize caps the samples kept per event.
const RingSize = 128

// lockWait is short on purpose: a hook that cannot get the lock skips its
// sample rather than eat into the 50 ms budget.
const lockWait = 25 * time.Millisecond

// Heartbeat is the content of heartbeat.json. LatencyUS holds one ring per event
// and is exported and tagged so it survives the JSON round trip (LESSONS L-15).
type Heartbeat struct {
	Last          time.Time          `json:"last"`
	PluginVersion string             `json:"plugin_version"`
	LatencyUS     map[string][]int64 `json:"latency_us"`
}

// Record adds one sample under flock. It returns flock.ErrBusy when the lock
// stayed held past lockWait.
func Record(p paths.Paths, now time.Time, event, pluginVersion string, latency time.Duration) error {
	if p.State == "" {
		return errors.New("heartbeat: no state dir")
	}
	l, err := flock.TryLock(p.Lock("heartbeat"), lockWait)
	if err != nil {
		return err
	}
	defer l.Unlock()

	hb, _ := Read(p.Heartbeat()) // a corrupt file restarts the ring
	if hb.LatencyUS == nil {
		hb.LatencyUS = map[string][]int64{}
	}
	hb.Last = now.UTC()
	if pluginVersion != "" {
		hb.PluginVersion = pluginVersion
	}
	ring := append(hb.LatencyUS[event], latency.Microseconds())
	if len(ring) > RingSize {
		ring = ring[len(ring)-RingSize:]
	}
	hb.LatencyUS[event] = ring
	data, err := json.Marshal(hb)
	if err != nil {
		return err
	}
	return atomicfile.Write(p.Heartbeat(), data, 0o600)
}

// Read loads heartbeat.json. A missing file is an empty heartbeat, not an error.
func Read(path string) (Heartbeat, error) {
	var hb Heartbeat
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return hb, nil
	}
	if err != nil {
		return hb, err
	}
	err = json.Unmarshal(data, &hb)
	return hb, err
}

// Quantiles returns p50 and p95 across every event's samples, each the sample
// at the rounded index q*(n-1), and the sample count.
func (h Heartbeat) Quantiles() (p50, p95 time.Duration, n int) {
	var all []int64
	for _, s := range h.LatencyUS {
		all = append(all, s...)
	}
	if len(all) == 0 {
		return 0, 0, 0
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	at := func(q float64) time.Duration {
		return time.Duration(all[int(q*float64(len(all)-1)+0.5)]) * time.Microsecond
	}
	return at(0.50), at(0.95), len(all)
}
