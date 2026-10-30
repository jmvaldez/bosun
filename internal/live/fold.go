package live

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"

	"github.com/jmvaldez/bosun/internal/hook"
)

// FoldFile applies the complete lines of path past s.Offset. It advances the
// offset only by whole lines consumed, so a partial trailing line (a hook mid
// write) is picked up whole on the next run and no event is skipped or applied
// twice (LESSONS L-22). It reports whether anything was applied.
func FoldFile(path string, s *Session) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return false, err
	}
	if fi.Size() < s.Offset {
		// The file was replaced under us; start over rather than read garbage.
		*s = Session{ID: s.ID, State: Idle}
	}
	if fi.Size() == s.Offset {
		return false, nil
	}
	if _, err := f.Seek(s.Offset, io.SeekStart); err != nil {
		return false, err
	}
	// ReadBytes has no line length limit, unlike bufio.Scanner's 64 KiB default.
	r := bufio.NewReaderSize(f, 64<<10)
	applied := false
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return applied, nil // partial or empty tail: leave it for next time
			}
			return applied, err
		}
		s.Offset += int64(len(line))
		var ev hook.Event
		if json.Unmarshal(line, &ev) != nil {
			continue // one bad line is skipped, never fatal
		}
		if s.Apply(ev) {
			applied = true
		}
	}
}

func parseTS(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
