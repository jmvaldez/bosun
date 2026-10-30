package eval

import (
	"encoding/json"
	"io"
	"os"

	"github.com/jmvaldez/bosun/internal/statusline"
	"github.com/jmvaldez/bosun/internal/subcost"
)

func newTracker() *subcost.Tracker { return &subcost.Tracker{} }

func readSmall(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 64<<10))
}

func statuslineSnap(b []byte) (statusline.Snapshot, bool) {
	var s statusline.Snapshot
	return s, json.Unmarshal(b, &s) == nil
}
