package rules

import (
	"path/filepath"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

func cond(rule, scope string) Cond { return Cond{Rule: rule, Scope: scope, Session: "s1"} }

func TestOneAlertPerRuleAndScope(t *testing.T) {
	s := Store{}
	f, _ := s.Reconcile([]Cond{cond(Quiet, "s1")}, t0, "2026-10-23", false)
	if len(f) != 1 || len(f[0].Alert.ID) != 5 {
		t.Fatalf("fires = %+v", f)
	}
	f, _ = s.Reconcile([]Cond{cond(Quiet, "s1")}, t0.Add(time.Second), "2026-10-23", false)
	if len(f) != 0 {
		t.Error("an alert that already holds must not fire again")
	}
	f, _ = s.Reconcile([]Cond{cond(Quiet, "s1"), cond(Quiet, "s2")}, t0.Add(2*time.Second), "2026-10-23", false)
	if len(f) != 1 || f[0].Alert.Scope != "s2" {
		t.Errorf("a second scope is its own alert: %+v", f)
	}
}

func TestRearmsAfterClear(t *testing.T) {
	s := Store{}
	s.Reconcile([]Cond{cond(Quiet, "s1")}, t0, "d", false)
	first := s[Key(Quiet, "s1")].ID
	_, cleared := s.Reconcile(nil, t0.Add(time.Minute), "d", false)
	if len(cleared) != 1 || len(s) != 0 {
		t.Fatalf("cleared = %+v store = %+v", cleared, s)
	}
	f, _ := s.Reconcile([]Cond{cond(Quiet, "s1")}, t0.Add(2*time.Minute), "d", false)
	if len(f) != 1 || f[0].Alert.ID == first {
		t.Errorf("re-trip must fire a new episode: %+v (first %s)", f, first)
	}
}

func TestDismissHidesUntilClearThenRearms(t *testing.T) {
	s := Store{}
	s.Reconcile([]Cond{cond(Quiet, "s1")}, t0, "d", false)
	id := s[Key(Quiet, "s1")].ID
	if got := s.Dismiss(id, false); len(got) != 1 {
		t.Fatal("dismiss found nothing")
	}
	if len(s.Active()) != 0 || len(s.BySession()) != 0 {
		t.Error("a dismissed alert must be hidden")
	}
	f, _ := s.Reconcile([]Cond{cond(Quiet, "s1")}, t0.Add(time.Second), "d", false)
	if len(f) != 0 || !s[Key(Quiet, "s1")].Dismissed {
		t.Error("dismissal must hold while the condition holds, with no new fire")
	}
	s.Reconcile(nil, t0.Add(time.Minute), "d", false)
	f, _ = s.Reconcile([]Cond{cond(Quiet, "s1")}, t0.Add(2*time.Minute), "d", false)
	if len(f) != 1 || s[Key(Quiet, "s1")].Dismissed {
		t.Error("the alert must fire and show again after clearing and re-tripping")
	}
}

func TestDismissAll(t *testing.T) {
	s := Store{}
	s.Reconcile([]Cond{cond(Quiet, "s1"), cond(SubagentRuntime, "s1/a1")}, t0, "d", false)
	if got := s.Dismiss("", true); len(got) != 2 {
		t.Errorf("dismissed %d", len(got))
	}
}

func TestSessionCostFiresAtEachNextMultiple(t *testing.T) {
	s := Store{}
	c := func(level int) Cond { return Cond{Rule: SessionCost, Scope: "s1", Session: "s1", Level: level} }
	if f, _ := s.Reconcile([]Cond{c(1)}, t0, "d", false); len(f) != 1 {
		t.Fatal("level 1 should fire")
	}
	if f, _ := s.Reconcile([]Cond{c(1)}, t0.Add(time.Second), "d", false); len(f) != 0 {
		t.Error("same level fired twice")
	}
	s.Dismiss("", true)
	f, _ := s.Reconcile([]Cond{c(2)}, t0.Add(time.Minute), "d", false)
	if len(f) != 1 || f[0].Alert.Level != 2 {
		t.Fatalf("level 2 should fire: %+v", f)
	}
	if s[Key(SessionCost, "s1")].Dismissed {
		t.Error("a later fire must show even though the earlier level was dismissed")
	}
}

// A sticky alert (pace, budget) fires once per day however its condition flaps.
func TestStickyAlertHeldUntilDayRollover(t *testing.T) {
	s := Store{}
	c := Cond{Rule: DailyPace, Scope: "acct", Sticky: true, Day: "2026-10-23"}
	if f, _ := s.Reconcile([]Cond{c}, t0, "2026-10-23", false); len(f) != 1 {
		t.Fatal("first trip should fire")
	}
	s.Reconcile(nil, t0.Add(time.Hour), "2026-10-23", false) // ratio dips below
	if f, _ := s.Reconcile([]Cond{c}, t0.Add(2*time.Hour), "2026-10-23", false); len(f) != 0 {
		t.Error("a second alert fired on the same day")
	}
	_, cleared := s.Reconcile(nil, t0.Add(24*time.Hour), "2026-10-24", false)
	if len(cleared) != 1 {
		t.Error("the sticky alert should clear when the day rolls over")
	}
}

func TestCatchUpFlagRidesOnTheFire(t *testing.T) {
	s := Store{}
	f, _ := s.Reconcile([]Cond{cond(Quiet, "s1")}, t0, "d", true)
	if len(f) != 1 || !f[0].CatchUp {
		t.Errorf("catch-up fire must be marked CatchUp: %+v", f)
	}
}

func TestSaveLoadAndLog(t *testing.T) {
	dir := t.TempDir()
	s := Store{}
	s.Reconcile([]Cond{cond(Quiet, "s1")}, t0, "d", false)
	if err := s.Save(filepath.Join(dir, "alerts.json")); err != nil {
		t.Fatal(err)
	}
	if got := Load(filepath.Join(dir, "alerts.json")); len(got) != 1 {
		t.Errorf("loaded %v", got)
	}
	if len(Load(filepath.Join(dir, "none"))) != 0 {
		t.Error("missing file should be empty")
	}
	if err := AppendLog(filepath.Join(dir, "alerts.jsonl"), *s[Key(Quiet, "s1")], t0); err != nil {
		t.Fatal(err)
	}
}
