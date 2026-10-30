// Package pace answers "is today running hotter than usual": today's spend so
// far against the median of earlier days at the same time of day.
package pace

import (
	"encoding/json"
	"os"
	"sort"
	"time"

	"github.com/jmvaldez/bosun/internal/atomicfile"
	"github.com/jmvaldez/bosun/internal/config"
)

// Point is the cumulative spend at a minute of the day.
type Point struct {
	Minute int     `json:"m"`
	USD    float64 `json:"usd"`
}

// Day is one baseline day's cumulative curve, sorted by minute.
type Day struct {
	Day    string  `json:"day"`
	Points []Point `json:"points"`
}

// At returns the cumulative spend at a minute: the last point at or before it.
func (d Day) At(minute int) float64 {
	v := 0.0
	for _, p := range d.Points {
		if p.Minute > minute {
			break
		}
		v = p.USD
	}
	return v
}

// Total is the day's final spend.
func (d Day) Total() float64 {
	if len(d.Points) == 0 {
		return 0
	}
	return d.Points[len(d.Points)-1].USD
}

// File is pace.json, rebuilt once a day by the refresher.
type File struct {
	BuiltDay string `json:"built_day"`
	Days     []Day  `json:"days"`
}

// Read loads pace.json; missing means no baseline.
func Read(path string) File {
	var f File
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &f)
	}
	return f
}

func Write(path string, f File) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return atomicfile.Write(path, b, 0o600)
}

type Status struct {
	OK       bool // false when disabled or with fewer than min_days baseline days: no pace output
	Ratio    float64
	Typical  float64
	Days     int
	Amber    bool
	Alert    bool // ratio >= alert and over typical by min_over
	OverBy   float64
	Disabled bool
}

// Compute works out today's pace. Baseline days are those in the file with
// spend above zero, excluding today, newest window_days of them.
func Compute(f File, today string, todayUSD float64, minute int, c config.Pace) Status {
	if !c.Enabled {
		return Status{Disabled: true}
	}
	var days []Day
	for _, d := range f.Days {
		if d.Day != today && d.Total() > 0 {
			days = append(days, d)
		}
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Day > days[j].Day })
	if len(days) > c.WindowDays {
		days = days[:c.WindowDays]
	}
	if len(days) < c.MinDays {
		return Status{Days: len(days)}
	}
	vals := make([]float64, len(days))
	for i, d := range days {
		vals[i] = d.At(minute)
	}
	sort.Float64s(vals)
	typical := vals[len(vals)/2]
	if len(vals)%2 == 0 {
		typical = (vals[len(vals)/2-1] + vals[len(vals)/2]) / 2
	}
	st := Status{OK: true, Typical: typical, Days: len(days)}
	if typical <= 0 {
		return st // nothing was spent by now on a typical day; a ratio would be meaningless
	}
	st.Ratio = todayUSD / typical
	st.OverBy = todayUSD - typical
	st.Amber = st.Ratio >= c.Amber
	st.Alert = st.Ratio >= c.Alert && st.OverBy >= c.MinOver
	return st
}

// MinuteOfDay is the minute index into a day that starts at dayStart.
func MinuteOfDay(now, dayStart time.Time) int { return int(now.Sub(dayStart) / time.Minute) }
