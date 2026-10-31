package panewrite

import (
	"time"

	"github.com/jmvaldez/bosun/internal/platform"
)

const (
	defaultSettle  = 300 * time.Millisecond
	defaultConfirm = 10 * time.Second
	pollEvery      = 250 * time.Millisecond
)

// NotSubmitted is the reason when no UserPromptSubmit event followed the paste.
const NotSubmitted = "not submitted"

// Writer is the part of tmux.Client that Submit needs.
type Writer interface {
	Tmux
	PasteText(pane, text string) error
	SendKeys(pane string, keys []string) error
}

// Deps are Submit's collaborators. Zero Settle and Confirm mean 300ms and 10s;
// a nil Sleep means time.Sleep, which tests replace with a fake clock.
type Deps struct {
	Tmux  Writer
	Procs platform.Procs
	// Submitted reports whether a UserPromptSubmit event for the session landed
	// after since. The caller reads it from events.jsonl.
	Submitted func(since time.Time) bool
	Settle    time.Duration
	Confirm   time.Duration
	Sleep     func(time.Duration)
}

// Result is the outcome of a Submit that reached the pane.
type Result struct {
	Submitted bool
	Why       string
}

// Submit verifies the pane, pastes text as one bracketed paste, waits for the
// program to settle, presses Enter and waits for a UserPromptSubmit event after
// since. If none arrives within Confirm it presses Enter once more and waits
// again. A refused guard returns Submitted false with the reason and writes
// nothing; a paste or key error is returned as err.
func Submit(d Deps, target Target, text string, since time.Time) (Result, error) {
	if ok, why := Verify(d.Tmux, d.Procs, target); !ok {
		return Result{Why: why}, nil
	}
	settle, confirm, sleep := d.Settle, d.Confirm, d.Sleep
	if settle <= 0 {
		settle = defaultSettle
	}
	if confirm <= 0 {
		confirm = defaultConfirm
	}
	if sleep == nil {
		sleep = time.Sleep
	}
	if err := d.Tmux.PasteText(target.Pane, text); err != nil {
		return Result{}, err
	}
	sleep(settle)
	for attempt := 0; attempt < 2; attempt++ {
		if err := d.Tmux.SendKeys(target.Pane, []string{"C-m"}); err != nil {
			return Result{}, err
		}
		for waited := time.Duration(0); waited < confirm; waited += pollEvery {
			sleep(pollEvery)
			if d.Submitted(since) {
				return Result{Submitted: true}, nil
			}
		}
	}
	return Result{Why: NotSubmitted}, nil
}
