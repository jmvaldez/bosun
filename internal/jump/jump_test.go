package jump

import (
	"path/filepath"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

// SPEC section 7 jump walk: start on work:3.0, press 1 work:1.0, press 2
// home:1.0, press 3 home:1.1, press 4 wraps to work:1.0.
func TestSpecWalk(t *testing.T) {
	queue := []string{"work:1.0", "home:1.0", "home:1.1"}
	var w Walk
	cur := "work:3.0"
	for i, want := range []string{"work:1.0", "home:1.0", "home:1.1", "work:1.0"} {
		var got string
		got, w = Next(w, queue, cur, now.Add(time.Duration(i)*time.Second))
		if got != want {
			t.Fatalf("press %d: got %s, want %s", i+1, got, want)
		}
		cur = got
	}
}

// Visiting a done pane idles it and drops it from the queue; the walk must not
// lose its place.
func TestWalkSurvivesDonePanesLeavingTheQueue(t *testing.T) {
	var w Walk
	got, w := Next(w, []string{"a", "b", "c"}, "x", now) // a needs-you, b and c done
	if got != "a" {
		t.Fatalf("got %s", got)
	}
	got, w = Next(w, []string{"a", "b", "c"}, "a", now.Add(time.Second))
	if got != "b" {
		t.Fatalf("got %s", got)
	}
	// b was viewed and went idle; the queue is now a, c.
	got, w = Next(w, []string{"a", "c"}, "b", now.Add(2*time.Second))
	if got != "c" {
		t.Fatalf("got %s, want c (b left the queue)", got)
	}
	got, _ = Next(w, []string{"a"}, "c", now.Add(3*time.Second))
	if got != "a" {
		t.Errorf("got %s, want a (wrap)", got)
	}
}

func TestNewPassWhenUserMovedAway(t *testing.T) {
	_, w := Next(Walk{}, []string{"a", "b", "c"}, "x", now) // jumped to a
	got, _ := Next(w, []string{"a", "b", "c"}, "elsewhere", now.Add(time.Second))
	if got != "a" {
		t.Errorf("got %s, want a", got)
	}
	got, _ = Next(Walk{}, []string{"a", "b", "c"}, "b", now) // standing on a queued pane
	if got != "c" {
		t.Errorf("got %s, want c", got)
	}
}

func TestExpiredWalkRestarts(t *testing.T) {
	_, w := Next(Walk{}, []string{"a", "b"}, "x", now)
	got, _ := Next(w, []string{"a", "b"}, "a", now.Add(10*time.Minute))
	if got != "b" { // fresh pass from the queue: current a is queued, so next is b
		t.Errorf("got %s", got)
	}
}

func TestEmptyQueue(t *testing.T) {
	if got, _ := Next(Walk{}, nil, "x", now); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestStoreRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "jump.json")
	s := Store{"c1": {Panes: []string{"a"}, Idx: 0, At: now}}
	if err := s.Save(p); err != nil {
		t.Fatal(err)
	}
	if got := Load(p)["c1"]; len(got.Panes) != 1 || !got.At.Equal(now) {
		t.Errorf("got %+v", got)
	}
	if len(Load(filepath.Join(t.TempDir(), "none"))) != 0 {
		t.Error("missing file should be empty")
	}
}
