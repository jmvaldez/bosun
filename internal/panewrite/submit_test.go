package panewrite

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jmvaldez/bosun/internal/tmux"
)

type fakeProcs struct {
	parent map[int]int
	alive  map[int]string
}

func (f fakeProcs) StartToken(pid int) (string, error) { return f.alive[pid], nil }
func (f fakeProcs) StartTime(int) (time.Time, error)   { return time.Time{}, nil }
func (f fakeProcs) Alive(pid int, tok string) bool {
	t, ok := f.alive[pid]
	return ok && (tok == "" || tok == t)
}
func (f fakeProcs) Parent(pid int) (int, error) { return f.parent[pid], nil }

type fakeTmux struct {
	panes    []tmux.Pane
	pasteErr error
	log      []string
}

func (f *fakeTmux) Panes() ([]tmux.Pane, error) { return f.panes, nil }
func (f *fakeTmux) PasteText(pane, text string) error {
	f.log = append(f.log, "paste "+pane+" "+text)
	return f.pasteErr
}
func (f *fakeTmux) SendKeys(pane string, keys []string) error {
	f.log = append(f.log, "keys "+pane+" "+strings.Join(keys, " "))
	return nil
}

// clock advances only when Submit sleeps, and says when the scripted
// confirmation lands.
type clock struct {
	now time.Duration
	at  time.Duration // Submitted turns true from this time; negative means never
}

func (c *clock) sleep(d time.Duration) { c.now += d }
func (c *clock) submitted(time.Time) bool {
	return c.at >= 0 && c.now >= c.at
}

func setup(at time.Duration) (Deps, *fakeTmux, *clock, Target) {
	ft := &fakeTmux{panes: []tmux.Pane{{ServerPID: 4242, ID: "%3", PanePID: 500}}}
	c := &clock{at: at}
	d := Deps{
		Tmux:      ft,
		Procs:     fakeProcs{parent: map[int]int{501: 500, 500: 1}, alive: map[int]string{501: "tok"}},
		Submitted: c.submitted,
		Sleep:     c.sleep,
	}
	return d, ft, c, Target{Pane: "%3", ServerPID: 4242, PID: 501, StartTok: "tok"}
}

func TestSubmitConfirmedFirstTime(t *testing.T) {
	d, ft, _, tg := setup(time.Second)
	r, err := Submit(d, tg, "hi\nthere", time.Time{})
	if err != nil || !r.Submitted {
		t.Fatalf("Submit = %+v, %v; want submitted", r, err)
	}
	want := []string{"paste %3 hi\nthere", "keys %3 C-m"}
	if !slices.Equal(ft.log, want) {
		t.Errorf("writes = %q, want %q", ft.log, want)
	}
}

func TestSubmitConfirmedAfterRetry(t *testing.T) {
	d, ft, _, tg := setup(defaultConfirm + 2*time.Second)
	r, err := Submit(d, tg, "x", time.Time{})
	if err != nil || !r.Submitted {
		t.Fatalf("Submit = %+v, %v; want submitted", r, err)
	}
	want := []string{"paste %3 x", "keys %3 C-m", "keys %3 C-m"}
	if !slices.Equal(ft.log, want) {
		t.Errorf("writes = %q, want %q", ft.log, want)
	}
}

func TestSubmitNeverConfirmed(t *testing.T) {
	d, ft, _, tg := setup(-1)
	r, err := Submit(d, tg, "x", time.Time{})
	if err != nil || r.Submitted || r.Why != NotSubmitted {
		t.Fatalf("Submit = %+v, %v; want not submitted", r, err)
	}
	if n := strings.Count(strings.Join(ft.log, "|"), "C-m"); n != 2 {
		t.Errorf("C-m sent %d times, want 2 (one retry only)", n)
	}
}

func TestSubmitRefusedByGuardWritesNothing(t *testing.T) {
	d, ft, _, tg := setup(0)
	tg.StartTok = "other"
	r, err := Submit(d, tg, "x", time.Time{})
	if err != nil || r.Submitted || r.Why != NotSent {
		t.Fatalf("Submit = %+v, %v; want refusal %q", r, err, NotSent)
	}
	if len(ft.log) != 0 {
		t.Errorf("wrote %q after a refusal", ft.log)
	}
}

func TestSubmitPasteErrorSendsNoKey(t *testing.T) {
	d, ft, _, tg := setup(0)
	ft.pasteErr = errors.New("boom")
	if _, err := Submit(d, tg, "x", time.Time{}); err == nil {
		t.Fatal("paste error was swallowed")
	}
	if slices.Contains(ft.log, "keys %3 C-m") {
		t.Errorf("Enter sent after a failed paste: %q", ft.log)
	}
}
