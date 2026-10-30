package eval

import (
	"errors"
	"fmt"

	"github.com/jmvaldez/bosun/internal/flock"
	"github.com/jmvaldez/bosun/internal/paths"
	"github.com/jmvaldez/bosun/internal/rules"
)

// Dismiss applies rules.Store.Dismiss to alerts.json under eval.lock, so it
// cannot race the evaluator; it waits at most flock.MaxWait. Retracting posted
// notices is the caller's job.
func Dismiss(p paths.Paths, id string, all bool) ([]rules.Alert, error) {
	lock, err := flock.TryLock(p.Locks()+"/eval.lock", flock.MaxWait)
	if errors.Is(err, flock.ErrBusy) {
		return nil, fmt.Errorf("the evaluator is busy; try again")
	}
	if err != nil {
		return nil, err
	}
	defer lock.Unlock()
	store := rules.Load(p.Alerts())
	got := store.Dismiss(id, all)
	if len(got) == 0 {
		if _, exists := findByID(store, id); !all && !exists {
			return nil, fmt.Errorf("no alert %q", id)
		}
		return nil, nil
	}
	return got, store.Save(p.Alerts())
}

func findByID(s rules.Store, id string) (*rules.Alert, bool) {
	for _, a := range s {
		if a.ID == id {
			return a, true
		}
	}
	return nil, false
}

// DismissSession dismisses every active alert of one session, or, when agent is
// set, only that subagent's alerts. It is the popup's `x`.
func DismissSession(p paths.Paths, session, agent string) ([]rules.Alert, error) {
	lock, err := flock.TryLock(p.Locks()+"/eval.lock", flock.MaxWait)
	if errors.Is(err, flock.ErrBusy) {
		return nil, fmt.Errorf("the evaluator is busy; try again")
	}
	if err != nil {
		return nil, err
	}
	defer lock.Unlock()
	store := rules.Load(p.Alerts())
	var out []rules.Alert
	for _, a := range store {
		if a.Session == session && (agent == "" || a.Agent == agent) && !a.Dismissed {
			a.Dismissed = true
			out = append(out, *a)
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, store.Save(p.Alerts())
}
