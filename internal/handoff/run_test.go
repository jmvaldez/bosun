package handoff

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jmvaldez/bosun/internal/config"
	"github.com/jmvaldez/bosun/internal/hook"
	"github.com/jmvaldez/bosun/internal/live"
	"github.com/jmvaldez/bosun/internal/paths"
	"github.com/jmvaldez/bosun/internal/tmux"
)

const sid = "sess-1"

var t0 = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

type fakeProcs struct{}

func (fakeProcs) StartToken(int) (string, error)   { return "tok", nil }
func (fakeProcs) StartTime(int) (time.Time, error) { return time.Time{}, nil }
func (fakeProcs) Alive(pid int, tok string) bool   { return pid == 501 && tok == "tok" }
func (fakeProcs) Parent(pid int) (int, error) {
	if pid == 501 {
		return 500, nil
	}
	return 1, nil
}

type fakeTmux struct{ log []string }

func (f *fakeTmux) Panes() ([]tmux.Pane, error) {
	return []tmux.Pane{{ServerPID: 4242, ID: "%3", PanePID: 500}}, nil
}
func (f *fakeTmux) PasteText(pane, text string) error {
	f.log = append(f.log, "paste "+text)
	return nil
}
func (f *fakeTmux) SendKeys(pane string, keys []string) error {
	f.log = append(f.log, "keys "+strings.Join(keys, " "))
	return nil
}

// world is the scripted environment. Time advances only when Run sleeps; at
// fires its steps once the clock reaches their offset from t0.
type world struct {
	now      time.Time
	tm       *fakeTmux
	state    string // the session's state in the live view
	gone     bool
	events   []hook.Event
	mtime    time.Time
	size     int64
	noSubmit bool
	launches [][4]string
	launchEr error
	steps    []step
	d        Deps
}

type step struct {
	at time.Duration
	do func(w *world)
}

func (w *world) Now() time.Time { return w.now }
func (w *world) Sleep(d time.Duration) {
	w.now = w.now.Add(d)
	// Enter submits the pasted prompt, which the hook reports.
	if !w.noSubmit && strings.Contains(strings.Join(w.tm.log, "|"), "keys C-m") && !w.has("UserPromptSubmit") {
		w.event("UserPromptSubmit")
	}
	for i := range w.steps {
		if s := &w.steps[i]; s.do != nil && !w.now.Before(t0.Add(s.at)) {
			do := s.do
			s.do = nil
			do(w)
		}
	}
}

func (w *world) has(ev string) bool {
	for _, e := range w.events {
		if e.Ev == ev {
			return true
		}
	}
	return false
}

func (w *world) event(ev string) {
	w.events = append(w.events, hook.Event{Ev: ev, SID: sid, TS: w.now.Format(time.RFC3339Nano)})
}

func (w *world) writeDoc() { w.mtime, w.size = w.now, 120 }

func newWorld(t *testing.T) *world {
	t.Helper()
	root := t.TempDir()
	w := &world{now: t0, tm: &fakeTmux{}, state: live.Done}
	w.d = Deps{
		Clock: w, Tmux: w.tm, Procs: fakeProcs{},
		Paths: paths.Paths{State: root},
		Cfg:   config.Handoff{Deadline: 5 * time.Minute},
		Live: func() live.File {
			s := &live.Session{ID: sid, State: w.state, Task: "ABC-1", Cwd: "/repo", PID: 501, StartTok: "tok", PaneGone: w.gone,
				Loc: live.Locator{Pane: "%3", SPID: 4242}}
			return live.File{Sessions: map[string]*live.Session{sid: s}}
		},
		Events: func(string) []hook.Event { return w.events },
		Stat: func(string) (time.Time, int64, error) {
			if w.size == 0 && w.mtime.IsZero() {
				return time.Time{}, 0, os.ErrNotExist
			}
			return w.mtime, w.size, nil
		},
		Doc: func(*live.Session) (string, error) { return "/repo/.bosun/handoff-" + sid + ".md", nil },
		Launch: func(dir, task, from, doc string) error {
			w.launches = append(w.launches, [4]string{dir, task, from, doc})
			return w.launchEr
		},
	}
	os.MkdirAll(paths.Paths{State: root}.Session(sid), 0o700)
	return w
}

func (w *world) logLines(t *testing.T) []string {
	t.Helper()
	b, _ := os.ReadFile(w.d.Paths.HandoffLog())
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func TestHappyPath(t *testing.T) {
	w := newWorld(t)
	w.steps = []step{{3 * time.Second, func(w *world) { w.writeDoc(); w.event("Stop") }}}
	st, err := Run(w.d, sid)
	if err != nil || st.Phase != PhaseDone {
		t.Fatalf("Run = %+v, %v; want done", st, err)
	}
	if len(w.launches) != 1 || w.launches[0] != [4]string{"/repo", "ABC-1", sid, "/repo/.bosun/handoff-" + sid + ".md"} {
		t.Errorf("launches = %v", w.launches)
	}
	if got := Read(w.d.Paths.Handoff(sid)); got.Phase != PhaseDone || got.Doc == "" || got.NewWindow != "ABC-1" {
		t.Errorf("state file = %+v", got)
	}
	if !strings.Contains(w.tm.log[0], "handoff-"+sid+".md") || !strings.Contains(w.tm.log[0], "## Open questions") {
		t.Errorf("prompt = %q", w.tm.log[0])
	}
	lines := w.logLines(t)
	if len(lines) != 1 || !strings.Contains(lines[0], `"outcome":"ok"`) || !strings.Contains(lines[0], `"phase_reached":"launching"`) {
		t.Errorf("log = %q", lines)
	}
}

func TestNotSubmitted(t *testing.T) {
	w := newWorld(t)
	w.noSubmit = true
	st, err := Run(w.d, sid)
	if err == nil || st.Phase != PhaseFailed || st.Why != WhyNotSubmitted {
		t.Fatalf("Run = %+v, %v; want failed/not-submitted", st, err)
	}
	if len(w.launches) != 0 {
		t.Error("launched after a failure")
	}
}

func TestNeedsYouPausesTheDeadline(t *testing.T) {
	w := newWorld(t)
	// The permission prompt for the doc itself holds the session for 6 minutes,
	// longer than the 5 minute deadline; the handoff still succeeds.
	w.steps = []step{
		{2 * time.Second, func(w *world) { w.state = live.NeedsYou }},
		{6*time.Minute + 2*time.Second, func(w *world) { w.state = live.Working; w.writeDoc(); w.event("Stop") }},
	}
	st, err := Run(w.d, sid)
	if err != nil || st.Phase != PhaseDone {
		t.Fatalf("Run = %+v, %v; want done", st, err)
	}
	if st.PausedFor < 5*time.Minute {
		t.Errorf("PausedFor = %v, want about 6 minutes", st.PausedFor)
	}
	if !strings.Contains(w.logLines(t)[0], `"paused_ms":3`) {
		t.Errorf("log = %q", w.logLines(t))
	}
}

func TestDeadline(t *testing.T) {
	w := newWorld(t)
	st, err := Run(w.d, sid)
	if err == nil || st.Why != WhyDeadline {
		t.Fatalf("Run = %+v, %v; want failed/deadline", st, err)
	}
}

func TestPaneGone(t *testing.T) {
	w := newWorld(t)
	w.steps = []step{{3 * time.Second, func(w *world) { w.gone = true }}}
	st, err := Run(w.d, sid)
	if err == nil || st.Why != WhyPaneGone {
		t.Fatalf("Run = %+v, %v; want failed/pane-gone", st, err)
	}
}

func TestStopWithNoDoc(t *testing.T) {
	w := newWorld(t)
	w.steps = []step{{3 * time.Second, func(w *world) { w.event("Stop") }}}
	st, err := Run(w.d, sid)
	if err == nil || st.Why != WhyDocStale {
		t.Fatalf("Run = %+v, %v; want failed/doc-stale", st, err)
	}
}

func TestStopWithStaleDoc(t *testing.T) {
	w := newWorld(t)
	w.mtime, w.size = t0.Add(-time.Hour), 500 // an older handoff's doc
	w.steps = []step{{3 * time.Second, func(w *world) { w.event("Stop") }}}
	st, err := Run(w.d, sid)
	if err == nil || st.Why != WhyDocStale {
		t.Fatalf("Run = %+v, %v; want failed/doc-stale", st, err)
	}
}

func TestLaunchFails(t *testing.T) {
	w := newWorld(t)
	w.launchEr = errors.New("tmux: boom")
	w.steps = []step{{3 * time.Second, func(w *world) { w.writeDoc(); w.event("Stop") }}}
	st, err := Run(w.d, sid)
	if err == nil || st.Why != WhyLaunchFailed {
		t.Fatalf("Run = %+v, %v; want failed/launch-failed", st, err)
	}
	if !strings.Contains(w.logLines(t)[0], `"phase_reached":"launching"`) {
		t.Errorf("log = %q", w.logLines(t))
	}
}

func TestSecondRunRefusesWhileWriting(t *testing.T) {
	w := newWorld(t)
	Write(w.d.Paths.Handoff(sid), State{Phase: PhaseWriting, Started: t0, Deadline: t0.Add(time.Minute)})
	if _, err := Run(w.d, sid); !errors.Is(err, ErrRunning) {
		t.Fatalf("err = %v, want already running", err)
	}
	if len(w.tm.log) != 0 {
		t.Errorf("wrote to the pane: %q", w.tm.log)
	}
	// A state whose deadline is long past belongs to a process that died.
	Write(w.d.Paths.Handoff(sid), State{Phase: PhaseWriting, Started: t0.Add(-time.Hour), Deadline: t0.Add(-55 * time.Minute)})
	w.steps = []step{{3 * time.Second, func(w *world) { w.writeDoc(); w.event("Stop") }}}
	if st, err := Run(w.d, sid); err != nil || st.Phase != PhaseDone {
		t.Fatalf("Run over a dead state = %+v, %v", st, err)
	}
}

func TestCancel(t *testing.T) {
	w := newWorld(t)
	w.steps = []step{{3 * time.Second, func(w *world) {
		if err := Cancel(w.d.Paths, sid, w.now); err != nil {
			t.Errorf("Cancel: %v", err)
		}
	}}}
	st, err := Run(w.d, sid)
	if err == nil || st.Phase != PhaseFailed || st.Why != WhyCancelled {
		t.Fatalf("Run = %+v, %v; want failed/cancelled", st, err)
	}
	if got := Read(w.d.Paths.Handoff(sid)); got.Why != WhyCancelled {
		t.Errorf("state file = %+v", got)
	}
	if err := Cancel(w.d.Paths, sid, w.now); err == nil {
		t.Error("Cancel with nothing running succeeded")
	}
}

func TestRefusals(t *testing.T) {
	for _, c := range []struct {
		name  string
		state string
		force bool
		ok    bool
	}{
		{"done", live.Done, false, true},
		{"idle", live.Idle, false, true},
		{"working", live.Working, false, false},
		{"working forced", live.Working, true, true},
		{"needs-you forced", live.NeedsYou, true, false},
	} {
		s := &live.Session{State: c.state, Task: "ABC-1", Loc: live.Locator{Pane: "%3"}}
		if err := Eligible(s, c.force); (err == nil) != c.ok {
			t.Errorf("%s: Eligible = %v, want ok=%v", c.name, err, c.ok)
		}
	}
	if Eligible(&live.Session{State: live.Done, Loc: live.Locator{Pane: "%3"}}, false) == nil {
		t.Error("a session with no task was eligible")
	}
	if Eligible(&live.Session{State: live.Done, Task: "X-1", Loc: live.Locator{Pane: "%3"}, PaneGone: true}, false) == nil {
		t.Error("a session whose pane is gone was eligible")
	}
}

func TestPromptOverride(t *testing.T) {
	if got := Prompt(config.Handoff{}, "/d.md"); !strings.Contains(got, "/d.md") || strings.Contains(got, "{{doc}}") {
		t.Errorf("default prompt = %q", got)
	}
	if got := Prompt(config.Handoff{Prompt: "write {{doc}} now"}, "/d.md"); got != "write /d.md now" {
		t.Errorf("override = %q", got)
	}
}

func TestStateNeverHoldsText(t *testing.T) {
	w := newWorld(t)
	w.steps = []step{{3 * time.Second, func(w *world) { w.writeDoc(); w.event("Stop") }}}
	if _, err := Run(w.d, sid); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(w.d.Paths.Handoff(sid))
	if strings.Contains(string(b), "Goal") {
		t.Errorf("state file holds prompt text: %s", b)
	}
	if fi, _ := os.Stat(w.d.Paths.Handoff(sid)); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", fi.Mode().Perm())
	}
}
