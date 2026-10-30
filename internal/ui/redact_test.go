package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jmvaldez/bosun/internal/spend"
)

// mockDataOff is the mock with live cost off (D-31): the quiet and idle
// sessions have no trusted figure, so their dollars are hidden and the totals
// leave them out.
func mockDataOff() Data {
	d := mockData()
	for _, id := range []string{"e5c7a4fb", "f6d8b5ac"} {
		s := d.Snap.Sessions[id]
		s.Trusted = false
		d.Snap.Sessions[id] = s
	}
	for i, c := range d.Contribs {
		if s, ok := d.Snap.Sessions[c.Session.ID]; ok {
			d.Contribs[i].Session = s
		}
	}
	d.Contribs = spend.Redact(d.Contribs)
	d.Today = spend.DayTotal(d.Contribs, d.Clock, "2026-10-22", time.Time{})
	return d
}

func TestGoldenLiveCostOff(t *testing.T) {
	for _, w := range []int{180, 100} {
		m := New(Options{Peek: true, ShowText: true}, &mockActions{})
		m.SetData(mockDataOff())
		f := frame(m, w, 24)
		golden(t, fmt.Sprintf("status_off_%d.golden", w), f)
		press(m, "tab")
		f = frame(m, w, 40)
		golden(t, fmt.Sprintf("spend_off_%d.golden", w), f)
		for i, l := range strings.Split(f, "\n") {
			if Width(l) != w {
				t.Errorf("width %d line %d is %d columns", w, i, Width(l))
			}
		}
	}
}

func TestLiveCostOffTotalsLeaveHiddenSessionsOut(t *testing.T) {
	d := mockDataOff()
	if got := fmt.Sprintf("%.2f", d.Today.USD); got != "18.85" || d.Today.Redacted != 2 { // 23.40 - 3.10 - 1.45
		t.Errorf("today = %+v", d.Today)
	}
	m := New(Options{}, &mockActions{})
	m.SetData(d)
	if c := m.today["f6d8b5ac"]; !c.Hidden || c.USD != 0 {
		t.Errorf("hidden session's cell = %+v", c)
	}
}
