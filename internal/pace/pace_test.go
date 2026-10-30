package pace

import (
	"testing"

	"github.com/jmvaldez/bosun/internal/config"
)

func day(name string, pts ...Point) Day { return Day{Day: name, Points: pts} }

func baseline(n int, at10 float64) File {
	var f File
	for i := 0; i < n; i++ {
		f.Days = append(f.Days, day("2026-10-"+string(rune('0'+i/10))+string(rune('0'+i%10)), Point{Minute: 60, USD: at10}, Point{Minute: 600, USD: at10 * 3}))
	}
	return f
}

func TestAtIsAStepFunction(t *testing.T) {
	d := day("d", Point{10, 1}, Point{20, 5})
	for min, want := range map[int]float64{0: 0, 9: 0, 10: 1, 19: 1, 20: 5, 900: 5} {
		if got := d.At(min); got != want {
			t.Errorf("At(%d) = %v, want %v", min, got, want)
		}
	}
}

// SPEC section 5: silent under 7 days; amber at 1.25x; alert at 1.5x and $20 over.
func TestSilentUnderMinDays(t *testing.T) {
	st := Compute(baseline(6, 10), "2026-11-01", 500, 120, config.Default().Pace)
	if st.OK || st.Amber || st.Alert {
		t.Errorf("6 baseline days must be silent: %+v", st)
	}
	if st.Days != 6 {
		t.Errorf("days = %d", st.Days)
	}
}

func TestAmberAndAlertThresholds(t *testing.T) {
	c := config.Default().Pace
	f := baseline(7, 40) // typical $40 by minute 60
	tests := []struct {
		today         float64
		amber, alert  bool
		ratio, overBy float64
	}{
		{40, false, false, 1.0, 0},
		{49, false, false, 1.225, 9},
		{50, true, false, 1.25, 10},       // amber at exactly 1.25x
		{60, true, true, 1.5, 20},         // alert at 1.5x and $20 over
		{59.9, true, false, 1.4975, 19.9}, // 1.5x not reached, and under $20 over
		{61.75, true, true, 1.54375, 21.75},
	}
	for _, tt := range tests {
		st := Compute(f, "2026-11-01", tt.today, 90, c)
		if !st.OK || st.Amber != tt.amber || st.Alert != tt.alert {
			t.Errorf("today %v: %+v, want amber %v alert %v", tt.today, st, tt.amber, tt.alert)
		}
	}
}

func TestAlertNeedsMinOverEvenAtHighRatio(t *testing.T) {
	f := baseline(7, 2) // typical $2: a 5x day is only $8 over
	st := Compute(f, "2026-11-01", 10, 90, config.Default().Pace)
	if !st.Amber || st.Alert {
		t.Errorf("%+v", st)
	}
}

func TestExcludesTodayAndEmptyDaysAndTakesMedian(t *testing.T) {
	f := File{Days: []Day{
		day("2026-10-01", Point{60, 1}, Point{600, 3}), day("2026-10-02", Point{60, 2}), day("2026-10-03", Point{60, 100}),
		day("2026-10-04", Point{60, 4}), day("2026-10-05", Point{60, 3}), day("2026-10-06", Point{60, 5}),
		day("2026-10-07", Point{60, 6}), day("2026-10-08"), // empty: not a baseline day
		day("2026-11-01", Point{60, 9999}), // today: excluded
	}}
	c := config.Default().Pace
	st := Compute(f, "2026-11-01", 6, 60, c)
	if !st.OK || st.Days != 7 || st.Typical != 4 { // median of 1,2,3,4,5,6,100
		t.Errorf("%+v", st)
	}
}

func TestWindowLimitsBaselineDays(t *testing.T) {
	c := config.Default().Pace
	c.WindowDays = 7
	f := baseline(20, 10)
	if st := Compute(f, "2027-01-01", 10, 60, c); st.Days != 7 {
		t.Errorf("days = %d", st.Days)
	}
}

func TestDisabled(t *testing.T) {
	c := config.Default().Pace
	c.Enabled = false
	if st := Compute(baseline(10, 10), "x", 1000, 60, c); !st.Disabled || st.Alert {
		t.Errorf("%+v", st)
	}
}

func TestMedianOfEvenCount(t *testing.T) {
	c := config.Default().Pace
	c.MinDays = 2
	f := File{Days: []Day{day("a", Point{0, 2}), day("b", Point{0, 4})}}
	if st := Compute(f, "t", 3, 0, c); st.Typical != 3 {
		t.Errorf("%+v", st)
	}
}
