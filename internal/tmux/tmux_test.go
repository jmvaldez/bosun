package tmux

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jmvaldez/bosun/internal/platform"
)

type fakeRunner struct {
	calls [][]string
	out   string
	err   error
	to    time.Duration
}

func (f *fakeRunner) Run(timeout time.Duration, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	f.to = timeout
	return []byte(f.out), f.err
}

func TestSocketFromEnv(t *testing.T) {
	for in, want := range map[string]string{
		"/tmp/tmux-1000/default,4242,0": "/tmp/tmux-1000/default",
		"/tmp/a,b/sock,17,3":            "/tmp/a,b/sock",
		"":                              "",
		"nonsense":                      "",
	} {
		if got := SocketFromEnv(in); got != want {
			t.Errorf("SocketFromEnv(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParsePanes(t *testing.T) {
	ps := ParsePanes("4242\t%1\t@1\t999\t0\t1\twork\n4242\t%2\t@2\t1000\t3\t0\tsess\twith tab\nbad line\n\n")
	if len(ps) != 2 || ps[0].ID != "%1" || ps[0].Where() != "work:0.1" || ps[1].Session != "sess\twith tab" {
		t.Fatalf("panes = %+v", ps)
	}
}

func TestViewing(t *testing.T) {
	cs := ParseClients("c1\t%1\tattached,focused\tfocus,mouse\nc2\t%2\tattached\tmouse\nc3\t%3\tattached\tfocus\n")
	for pane, want := range map[string]bool{
		"%1": true,  // focused client
		"%2": true,  // no focus feature: showing the pane is the best signal
		"%3": false, // can report focus and is not focused
		"%9": false,
	} {
		if got := Viewing(cs, pane); got != want {
			t.Errorf("Viewing(%s) = %v, want %v", pane, got, want)
		}
	}
}

// Viewing is derived from FocusVerdict; check every combination of up to two
// clients on the pane against the rule written out independently.
func TestViewingMatchesFocusVerdictRule(t *testing.T) {
	states := []ClientInfo{
		{PaneID: "%1", Flags: "attached", TermFeatures: "mouse"},
		{PaneID: "%1", Flags: "attached,focused", TermFeatures: "mouse"},
		{PaneID: "%1", Flags: "attached", TermFeatures: "focus"},
		{PaneID: "%1", Flags: "attached,focused", TermFeatures: "focus"},
		{PaneID: "%2", Flags: "attached,focused", TermFeatures: "focus"},
	}
	check := func(cs []ClientInfo) {
		want := false
		for _, c := range cs {
			if c.PaneID == "%1" && (!hasWord(c.TermFeatures, "focus") || hasWord(c.Flags, "focused")) {
				want = true
			}
		}
		if got := Viewing(cs, "%1"); got != want {
			t.Errorf("Viewing(%+v) = %v, want %v", cs, got, want)
		}
	}
	check(nil)
	for _, a := range states {
		check([]ClientInfo{a})
		for _, b := range states {
			check([]ClientInfo{a, b})
		}
	}
}

func TestSetMarksIsOneCallWithDeadline(t *testing.T) {
	f := &fakeRunner{}
	c := Client{Runner: f, Socket: "/s"}
	err := c.SetMarks([]WindowMark{{"@1", "done"}, {"@2", ""}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"tmux", "-S", "/s", "set-option", "-w", "-t", "@1", "@bosun-wait", "done", ";", "set-option", "-wu", "-t", "@2", "@bosun-wait"}
	if len(f.calls) != 1 || !reflect.DeepEqual(f.calls[0], want) {
		t.Fatalf("calls = %v", f.calls)
	}
	if f.to != Timeout || Timeout != 500*time.Millisecond {
		t.Errorf("timeout = %v", f.to)
	}
	if err := (Client{Runner: f}).SetMarks(nil); err != nil || len(f.calls) != 1 {
		t.Error("no marks should mean no exec")
	}
}

func TestSwitchClientArgs(t *testing.T) {
	f := &fakeRunner{}
	Client{Runner: f, Bin: "/usr/bin/tmux"}.SwitchClient("/dev/pts/3", "%7")
	if got := strings.Join(f.calls[0], " "); got != "/usr/bin/tmux switch-client -c /dev/pts/3 -t %7" {
		t.Errorf("argv = %s", got)
	}
}

func TestFocusVerdict(t *testing.T) {
	cs := ParseClients("c1\t%1\tattached,focused\tfocus\nc2\t%2\tattached\tmouse\nc3\t%3\tattached\tfocus\nc4\t%1\tattached\tfocus\n")
	for pane, want := range map[string]platform.Verdict{
		"%1": platform.Focused,    // a focus-capable client is focused on it
		"%2": platform.Unknown,    // shown on a client that cannot report focus
		"%3": platform.NotFocused, // shown, focus-capable, not focused
		"%9": platform.NotFocused, // nobody shows it
	} {
		if got := FocusVerdict(cs, pane); got != want {
			t.Errorf("FocusVerdict(%s) = %v, want %v", pane, got, want)
		}
	}
	if got := (Client{Runner: &fakeRunner{err: errors.New("hung")}}).Check("%1"); got != platform.Unknown {
		t.Errorf("tmux error = %v, want Unknown", got)
	}
}

func TestNewWindowKeepsDirOutOfDashC(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	f := &fakeRunner{}
	if err := (Client{Runner: f}).NewWindow("/w/it's #(x) here", "git diff"); err != nil {
		t.Fatal(err)
	}
	want := []string{"tmux", "new-window", "-c", home, "-n", "diff",
		`cd -- '/w/it'\''s #(x) here' && git diff`}
	if len(f.calls) != 1 || !reflect.DeepEqual(f.calls[0], want) {
		t.Errorf("calls = %q, want %q", f.calls, want)
	}
}

func TestNewNamedWindowArgs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	f := &fakeRunner{}
	err := (Client{Runner: f}).NewNamedWindow("/w/a b", "a#b", []string{"K=#(x)"}, "claude")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"tmux", "new-window", "-c", home, "-n", "a##b", "-e", "K=#(x)", "cd -- '/w/a b' && claude"}
	if len(f.calls) != 1 || !reflect.DeepEqual(f.calls[0], want) {
		t.Errorf("calls = %q, want %q", f.calls, want)
	}
}

func TestWindowDirFallsBackToRoot(t *testing.T) {
	t.Setenv("HOME", "")
	if got := windowDir(); got != "/" {
		t.Errorf("windowDir() = %q, want /", got)
	}
}
