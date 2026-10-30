package notify

import (
	"encoding/json"

	"github.com/jmvaldez/bosun/internal/statusline"
)

func parseSnapshot(b []byte) (statusline.Snapshot, bool) {
	var s statusline.Snapshot
	return s, json.Unmarshal(b, &s) == nil
}
