package spend

import (
	"encoding/json"
	"os"
	"time"

	"github.com/jmvaldez/bosun/internal/atomicfile"
)

// TodayFile is today.json: the refresher writes it, the status segment reads
// it, so the 1 s segment never opens the ledger.
type TodayFile struct {
	DaySum
	UpdatedAt time.Time `json:"updated_at"`
}

// WriteToday stores today.json atomically.
func WriteToday(path string, t DaySum, now time.Time) error {
	b, err := json.Marshal(TodayFile{DaySum: t, UpdatedAt: now.UTC()})
	if err != nil {
		return err
	}
	return atomicfile.Write(path, b, 0o600)
}

// ReadToday loads today.json for the given day. A missing file, or one written
// for another day, reads as zero spend: yesterday's total must not linger on a
// new day's bar.
func ReadToday(path, day string) DaySum {
	b, err := os.ReadFile(path)
	if err != nil {
		return DaySum{Day: day}
	}
	var f TodayFile
	if json.Unmarshal(b, &f) != nil || f.Day != day {
		return DaySum{Day: day}
	}
	return f.DaySum
}
