package statusline

import (
	"encoding/json"
	"io"
	"os"
)

// maxSnapshot bounds a snapshot read; a real one is a few hundred bytes.
const maxSnapshot = 64 << 10

// ReadSnapshot loads a status line snapshot with a bounded read. ok is false
// when the file is missing, unreadable or not JSON.
func ReadSnapshot(path string) (Snapshot, bool) {
	var s Snapshot
	b, err := readFile(path)
	if err != nil {
		return s, false
	}
	return s, json.Unmarshal(b, &s) == nil
}

// Ctx returns the context fill percentage (0-100). ok is false when the status
// line did not report one, which means unknown, never 0.
func (s Snapshot) Ctx() (pct float64, ok bool) {
	if s.CtxPct == nil {
		return 0, false
	}
	return *s.CtxPct, true
}

func readFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxSnapshot))
}
