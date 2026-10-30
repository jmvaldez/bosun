package refresh

import (
	"sort"
	"time"

	"github.com/jmvaldez/bosun/internal/config"
	"github.com/jmvaldez/bosun/internal/ledger"
	"github.com/jmvaldez/bosun/internal/pace"
	"github.com/jmvaldez/bosun/internal/spend"
)

// minSamples is how many spend_samples a day needs for its curve to come from
// them; below that the curve is rebuilt from request timestamps (backfill days).
const minSamples = 10

// bucket keeps pace.json small: one point per five minutes.
const bucket = 5

// buildPace rebuilds the baseline curves from the most recent window_days days
// that have spend, never today (SPEC section 5). A day with at least
// minSamples spend_samples uses them; otherwise its curve comes from
// contributions, whose unattributed remainder spend.Contributions has already
// apportioned (D-28).
func buildPace(db *ledger.DB, cs []spend.Contribution, clock spend.Clock, now time.Time, c config.Pace) (pace.File, error) {
	today := clock.Day(now)
	byDay := map[string][]spend.Contribution{}
	for _, x := range cs {
		if x.TS.IsZero() {
			continue
		}
		d := clock.Day(x.TS)
		if d != today {
			byDay[d] = append(byDay[d], x)
		}
	}
	days := make([]string, 0, len(byDay))
	for d := range byDay {
		days = append(days, d)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(days)))
	if len(days) > c.WindowDays {
		days = days[:c.WindowDays]
	}
	out := pace.File{BuiltDay: today}
	for _, d := range days {
		start, err := clock.DayStart(d)
		if err != nil {
			return out, err
		}
		samples, err := db.Samples(d)
		if err != nil {
			return out, err
		}
		day := pace.Day{Day: d}
		if len(samples) >= minSamples {
			for _, s := range samples {
				day.Points = append(day.Points, pace.Point{Minute: s.Minute, USD: s.Cumulative})
			}
		} else {
			day.Points = curveFromContributions(byDay[d], start)
		}
		out.Days = append(out.Days, day)
	}
	return out, nil
}

func curveFromContributions(cs []spend.Contribution, start time.Time) []pace.Point {
	sort.Slice(cs, func(i, j int) bool { return cs[i].TS.Before(cs[j].TS) })
	var cum float64
	last := map[int]float64{}
	var minutes []int
	for _, x := range cs {
		cum += x.USD
		m := int(x.TS.Sub(start)/time.Minute) / bucket * bucket
		if _, ok := last[m]; !ok {
			minutes = append(minutes, m)
		}
		last[m] = cum
	}
	sort.Ints(minutes)
	pts := make([]pace.Point, 0, len(minutes))
	for _, m := range minutes {
		pts = append(pts, pace.Point{Minute: m, USD: last[m]})
	}
	return pts
}
