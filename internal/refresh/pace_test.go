package refresh

import (
	"testing"
	"time"

	"github.com/jmvaldez/bosun/internal/config"
	"github.com/jmvaldez/bosun/internal/ledger"
	"github.com/jmvaldez/bosun/internal/spend"
)

func contribution(ts time.Time, usd float64) spend.Contribution {
	return spend.Contribution{Kind: spend.RequestRow, TS: ts, USD: usd}
}

// The baseline follows spend.day: a row at 02:00Z is the 21st in UTC and the
// 20th under UTC-8, so switching the setting moves it between baseline days.
func TestPaceBaselineFollowsSpendDay(t *testing.T) {
	db, err := ledger.Open(t.TempDir() + "/bosun.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cs := []spend.Contribution{
		contribution(time.Date(2026, 10, 21, 2, 0, 0, 0, time.UTC), 5),
		contribution(time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC), 3),
	}
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	pc := config.Default().Pace

	utc, err := buildPace(db, append([]spend.Contribution{}, cs...), spend.Clock{Mode: "utc", Loc: time.UTC}, now, pc)
	if err != nil {
		t.Fatal(err)
	}
	if len(utc.Days) != 1 || utc.Days[0].Day != "2026-10-21" || utc.Days[0].Total() != 8 {
		t.Errorf("utc = %+v", utc.Days)
	}
	pst := time.FixedZone("PST", -8*3600)
	loc, err := buildPace(db, append([]spend.Contribution{}, cs...), spend.Clock{Mode: "local", Loc: pst}, now, pc)
	if err != nil {
		t.Fatal(err)
	}
	if len(loc.Days) != 2 {
		t.Fatalf("UTC-8 should split the rows across two days: %+v", loc.Days)
	}
	got := map[string]float64{}
	for _, d := range loc.Days {
		got[d.Day] = d.Total()
	}
	if got["2026-10-20"] != 5 || got["2026-10-21"] != 3 {
		t.Errorf("days = %v", got)
	}
}

func TestPaceBaselineExcludesTodayAndPrefersSamples(t *testing.T) {
	db, err := ledger.Open(t.TempDir() + "/bosun.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for m := 0; m < minSamples; m++ {
		db.RecordSample("2026-10-21", m*60, float64(m))
	}
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	cs := []spend.Contribution{
		contribution(time.Date(2026, 10, 21, 3, 0, 0, 0, time.UTC), 99), // ignored: the day has samples
		contribution(time.Date(2026, 10, 25, 3, 0, 0, 0, time.UTC), 7),  // today: excluded
		contribution(time.Date(2026, 10, 22, 3, 0, 0, 0, time.UTC), 4),
	}
	f, err := buildPace(db, cs, spend.Clock{Mode: "utc", Loc: time.UTC}, now, config.Default().Pace)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Days) != 2 {
		t.Fatalf("days = %+v", f.Days)
	}
	for _, d := range f.Days {
		switch d.Day {
		case "2026-10-21":
			if d.Total() != float64(minSamples-1) {
				t.Errorf("sampled day total = %v", d.Total())
			}
		case "2026-10-22":
			if d.Total() != 4 {
				t.Errorf("backfilled day total = %v", d.Total())
			}
		default:
			t.Errorf("unexpected day %s", d.Day)
		}
	}
}
