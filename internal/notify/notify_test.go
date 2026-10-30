package notify

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jmvaldez/bosun/internal/config"
	"github.com/jmvaldez/bosun/internal/hook"
	"github.com/jmvaldez/bosun/internal/paths"
	"github.com/jmvaldez/bosun/internal/platform"
	"github.com/jmvaldez/bosun/internal/rules"
)

func f64(v float64) *float64             { return &v }
func dur(v time.Duration) *time.Duration { return &v }
func num(v int) *int                     { return &v }

// LESSONS L-38: each rule's template must render its real numbers, parsed back
// from the flags the child receives.
func TestEveryKindRendersRealValuesFromFlags(t *testing.T) {
	tests := []struct {
		name string
		r    Request
		want string
	}{
		{"needs-you", Request{Kind: NeedsYou, Repo: "acme/api", Task: "ABC-142", Detail: "permission prompt"}, "ABC-142 / acme/api is waiting on you (permission prompt)"},
		{"needs-you input", Request{Kind: NeedsYou, Detail: "input needed"}, "a session is waiting on you (input needed)"},
		{"done", Request{Kind: Done, Edits: num(12), OwnUSD: f64(3.1)}, "a session is done, ready to review"},
		{"done without a trusted total", Request{Kind: Done, Edits: num(0)}, "a session is done, ready to review"},
		{"failed", Request{Kind: Failed, Duration: dur(95 * time.Second)}, "a session failed after 1m35s"},
		{"subagent_runtime", Request{Kind: SubagentRuntime, Agent: "Explore", Duration: dur(14*time.Minute + 2*time.Second), LimitDur: dur(10 * time.Minute)}, "Explore in a session: 14m02s (limit 10m00s)"},
		{"subagent_cost", Request{Kind: SubagentCost, Agent: "Explore", USD: f64(2.4), LimitUSD: f64(2)}, "Explore in a session: $2.40 est. (limit $2)"},
		{"session_cost", Request{Kind: SessionCost, Step: f64(10)}, "a session passed $10"},
		{"session_cost fractional", Request{Kind: SessionCost, Step: f64(0.01)}, "a session passed $0.01"},
		{"quiet", Request{Kind: Quiet, Duration: dur(5*time.Minute + 30*time.Second)}, "a session: no activity for 5m30s"},
		{"daily_pace", Request{Kind: DailyPace, USD: f64(61.75), Ratio: f64(1.54)}, "today $61.75, 1.5x typical"},
		{"daily_budget", Request{Kind: DailyBudget, USD: f64(50.4), CapUSD: f64(50)}, "today $50.40 of $50.00"},
	}
	covered := map[Kind]bool{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			back, err := ParseArgs(tt.r.Args()) // through the flags, as the child sees it
			if err != nil {
				t.Fatal(err)
			}
			m, err := Render(back)
			if err != nil {
				t.Fatal(err)
			}
			if m.Body != tt.want {
				t.Errorf("body = %q, want %q", m.Body, tt.want)
			}
			if strings.Contains(m.Body, "$0.00") && !strings.Contains(tt.want, "$0.00") {
				t.Errorf("zero value in %q", m.Body)
			}
		})
		covered[tt.r.Kind] = true
	}
	for _, k := range Kinds {
		if !covered[k] {
			t.Errorf("kind %s has no render test", k)
		}
	}
}

func TestMissingValuesAreErrorsNeverZeros(t *testing.T) {
	for _, r := range []Request{
		{Kind: NeedsYou}, {Kind: NeedsYou, Detail: "free text from Claude"},
		{Kind: Done}, {Kind: Failed}, {Kind: SubagentRuntime, Agent: "x"}, {Kind: SubagentCost, Agent: "x", USD: f64(1)},
		{Kind: SessionCost}, {Kind: Quiet}, {Kind: DailyPace, USD: f64(1)}, {Kind: DailyBudget, CapUSD: f64(1)},
	} {
		if _, err := Render(r); !errors.Is(err, ErrMissing) {
			t.Errorf("%+v: err = %v, want ErrMissing", r, err)
		}
	}
}

func TestTitleAndSubtitle(t *testing.T) {
	m, _ := Render(Request{Kind: NeedsYou, Repo: "acme/api", Task: "ABC-142", Detail: "dialog"})
	if m.Title != "bosun · needs you" || m.Icon != "dialog-question" || !m.Sound || m.Group != "bosun.s.attn" && m.Group != "bosun..attn" {
		t.Errorf("%+v", m)
	}
	if m, _ := Render(Request{Kind: Quiet, Duration: dur(time.Minute)}); m.Title != "bosun · quiet session" || m.Sound || m.Icon != "dialog-warning" {
		t.Errorf("%+v", m)
	}
	// Control characters never reach a notice.
	m, _ = Render(Request{Kind: Quiet, Repo: "a\x1b[31mb", Duration: dur(time.Minute)})
	if strings.ContainsRune(m.Body, 0x1b) {
		t.Errorf("body = %q", m.Body)
	}
	// C1 controls too: U+009B is the 8-bit CSI.
	m, _ = Render(Request{Kind: Quiet, Repo: "a\u009b31mb\u0085c", Duration: dur(time.Minute)})
	if strings.ContainsRune(m.Body, 0x9b) || strings.ContainsRune(m.Body, 0x85) || !strings.Contains(m.Body, "a31mbc") {
		t.Errorf("body = %q", m.Body)
	}
}

// A replacement keeps the id and emits no NotificationClosed, so the original
// listener still watches it: a second one would run `bosun jump` twice per click.
func TestReplacementDoesNotSpawnASecondListener(t *testing.T) {
	r := newRig(t)
	fr := &fakeRunner{out: map[string]string{"GetCapabilities": "(['actions', 'body', 'sound'],)", "Notify": "(uint32 7,)"}}
	r.d.Notifier = GDBus{R: fr, Bin: "gdbus"}
	var spawned [][]string
	r.d.Spawn = func(args ...string) error { spawned = append(spawned, args); return nil }
	r.emit(0, "SessionStart", entry("cli"))
	r.emit(1, "PermissionRequest")
	r.dispatch(Request{Kind: NeedsYou, Detail: "permission prompt"})
	r.emit(2, "PostToolUse")
	r.emit(3, "PermissionRequest")
	r.clk.t = r.clk.t.Add(time.Minute) // inside the first listener's ClickTimeout
	r.dispatch(Request{Kind: NeedsYou, Detail: "permission prompt"})
	if len(spawned) != 1 {
		t.Fatalf("after a replacement spawned = %v, want the one original listener", spawned)
	}
	// Past the first listener's deadline nothing watches the id any more.
	r.emit(4, "PostToolUse")
	r.emit(5, "PermissionRequest")
	r.clk.t = r.clk.t.Add(ClickTimeout)
	r.dispatch(Request{Kind: NeedsYou, Detail: "permission prompt"})
	if len(spawned) != 2 {
		t.Errorf("after the listener expired spawned = %v, want a fresh one", spawned)
	}
}

func TestGroups(t *testing.T) {
	for _, tt := range []struct {
		k    Kind
		want string
	}{{NeedsYou, "bosun.s1.attn"}, {Done, "bosun.s1.attn"}, {Failed, "bosun.s1.attn"}, {Quiet, "bosun.s1.quiet"},
		{SubagentCost, "bosun.s1.subagent_cost"}, {DailyPace, "bosun.pace"}, {DailyBudget, "bosun.budget"}} {
		if got := Group(tt.k, "s1"); got != tt.want {
			t.Errorf("Group(%s) = %s, want %s", tt.k, got, tt.want)
		}
	}
}

func TestFlagsRoundTripAndRejectTypos(t *testing.T) {
	r := Request{Kind: SubagentCost, Session: "s1", Edge: "e", AlertID: "ab12c", Repo: "r", Task: "T-1", Agent: "Explore",
		USD: f64(2.5), LimitUSD: f64(2), Estimated: true}
	back, err := ParseArgs(r.Args())
	if err != nil || back.Kind != r.Kind || *back.USD != 2.5 || !back.Estimated || back.AlertID != "ab12c" || back.Agent != "Explore" {
		t.Fatalf("back = %+v err=%v", back, err)
	}
	for _, bad := range [][]string{{"--kind", "bogus"}, {"--usd"}, {"--usd", "abc"}, {"--bogus", "1"}, {"--duration", "soon"}} {
		if _, err := ParseArgs(bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

// ---- backends through a fake Runner

type fakeRunner struct {
	mu    sync.Mutex
	calls [][]string
	out   map[string]string // substring of the joined args (not the binary) -> stdout; keys must not overlap
	err   error
	delay time.Duration
	to    time.Duration
}

func (f *fakeRunner) Run(timeout time.Duration, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string{name}, args...))
	f.to = timeout
	delay, err := f.delay, f.err
	f.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	joined := strings.Join(args, " ")
	for k, v := range f.out {
		if strings.Contains(joined, k) {
			return []byte(v), err
		}
	}
	return nil, err
}

func TestGDBusPostReplaceRetractArgv(t *testing.T) {
	fr := &fakeRunner{out: map[string]string{"Notify": "(uint32 42,)\n"}}
	g := GDBus{R: fr, Bin: "/usr/bin/gdbus"}
	id, err := g.Post(Notice{Title: "acme/api ABC-1", Subtitle: "needs you", Body: "waiting on you: dialog", Sound: true, ReplaceID: 7})
	if err != nil || id != 42 {
		t.Fatalf("id=%d err=%v", id, err)
	}
	got := strings.Join(fr.calls[0], "|")
	for _, want := range []string{"/usr/bin/gdbus|call|--session|--dest|org.freedesktop.Notifications", "org.freedesktop.Notifications.Notify|--",
		"|'bosun'|7|''|'acme/api ABC-1'|'needs you\\nwaiting on you: dialog'|[]|{'sound-name': <'message-new-instant'>}|-1"} {
		if !strings.Contains(got, want) {
			t.Errorf("argv lacks %q:\n%s", want, got)
		}
	}
	if fr.to <= 0 || fr.to > 5*time.Second {
		t.Errorf("timeout = %v", fr.to)
	}
	if err := g.Retract(42, "g"); err != nil {
		t.Fatal(err)
	}
	if last := strings.Join(fr.calls[1], "|"); !strings.HasSuffix(last, "org.freedesktop.Notifications.CloseNotification|--|42") {
		t.Errorf("close argv = %s", last)
	}
	// Quotes in text cannot break out of the GVariant string.
	g.Post(Notice{Title: "it's", Body: "a\\b"})
	if last := strings.Join(fr.calls[2], "|"); !strings.Contains(last, `'it\'s'`) || !strings.Contains(last, `'a\\b'`) {
		t.Errorf("escaping: %s", last)
	}
	if _, err := (GDBus{R: &fakeRunner{out: map[string]string{"Notify": "weird"}}}).Post(Notice{}); err == nil {
		t.Error("an unparseable reply must be an error")
	}
	if c := g.Capabilities(); !c.Replace || !c.Retract || !c.Sound {
		t.Errorf("caps = %+v", c)
	}
}

func TestNotifySendNeedsPrintIDForReplace(t *testing.T) {
	fr := &fakeRunner{out: map[string]string{"--print-id": "9\n"}}
	ns := NotifySend{R: fr, Bin: "notify-send", PrintID: true}
	id, err := ns.Post(Notice{Title: "t", Body: "b", ReplaceID: 5})
	if err != nil || id != 9 {
		t.Fatalf("id=%d err=%v", id, err)
	}
	if got := strings.Join(fr.calls[0], " "); !strings.Contains(got, "--print-id --replace-id 5") {
		t.Errorf("argv = %s", got)
	}
	old := NotifySend{R: &fakeRunner{}, Bin: "notify-send"}
	if c := old.Capabilities(); c.Replace || c.Retract {
		t.Errorf("0.7 caps = %+v", c)
	}
	if id, err := old.Post(Notice{Title: "t", ReplaceID: 5}); err != nil || id != 0 {
		t.Errorf("id=%d err=%v", id, err)
	}
}

func TestResolve(t *testing.T) {
	have := func(names ...string) func(string) (string, error) {
		return func(n string) (string, error) {
			for _, h := range names {
				if h == n {
					return "/bin/" + n, nil
				}
			}
			return "", errors.New("not found")
		}
	}
	r := &fakeRunner{out: map[string]string{"--version": "notify-send 0.7.9"}}
	if n, _ := Resolve("auto", platform.Linux, have("gdbus", "notify-send"), r); n == nil || n.Name() != "gdbus" {
		t.Errorf("linux auto should prefer gdbus, got %v", n)
	}
	if n, _ := Resolve("auto", platform.Linux, have("notify-send"), r); n == nil || n.Name() != "notify-send" || n.Capabilities().Replace {
		t.Errorf("0.7.9 notify-send must not claim replace: %v", n)
	}
	r8 := &fakeRunner{out: map[string]string{"--version": "notify-send 0.8.1"}}
	if n, _ := Resolve("auto", platform.Linux, have("notify-send", "gdbus"), r8); n == nil || n.Name() != "gdbus" {
		t.Errorf("got %v", n)
	}
	if n, _ := Resolve("notify-send", platform.Linux, have("notify-send"), r8); n == nil || !n.Capabilities().Replace {
		t.Errorf("0.8 notify-send should replace: %v", n)
	}
	if n, why := Resolve("auto", platform.Linux, have(), r); n != nil || !strings.Contains(why, "install") {
		t.Errorf("no backend must say how to add one: %v %q", n, why)
	}
	if n, _ := Resolve("off", platform.Linux, have("gdbus"), r); n != nil {
		t.Error("off must be off")
	}
	// LESSONS L-42: auto never picks osascript.
	if n, _ := Resolve("auto", platform.Darwin, have("osascript"), r); n != nil {
		t.Errorf("auto picked %s on macOS with only osascript present", n.Name())
	}
	if n, _ := Resolve("auto", platform.Darwin, have("terminal-notifier", "osascript"), r); n == nil || n.Name() != "terminal-notifier" {
		t.Errorf("got %v", n)
	}
	if n, _ := Resolve("osascript", platform.Darwin, have("osascript"), r); n == nil || n.Name() != "osascript" {
		t.Error("osascript must work when asked for")
	}
}

func TestTerminalNotifierArgv(t *testing.T) {
	fr := &fakeRunner{}
	tn := TerminalNotifier{R: fr, Bin: "terminal-notifier"}
	tn.Post(Notice{Title: "t", Subtitle: "s", Body: "b", Group: "bosun.s1.attn", Sound: true})
	tn.Retract(0, "bosun.s1.attn")
	if got := strings.Join(fr.calls[0], " "); got != "terminal-notifier -title t -message b -group bosun.s1.attn -subtitle s -sound default" {
		t.Errorf("post = %s", got)
	}
	if got := strings.Join(fr.calls[1], " "); got != "terminal-notifier -remove bosun.s1.attn" {
		t.Errorf("remove = %s", got)
	}
}

// ---- dispatch

type fakeNotifier struct {
	mu     sync.Mutex
	posts  []Notice
	closed []uint32
	next   uint32
	block  chan struct{} // when set, Post waits on it
}

func (f *fakeNotifier) Name() string { return "fake" }
func (f *fakeNotifier) Capabilities() Capabilities {
	return Capabilities{Replace: true, Retract: true, Sound: true}
}
func (f *fakeNotifier) Post(n Notice) (uint32, error) {
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.posts = append(f.posts, n)
	if n.ReplaceID != 0 {
		return n.ReplaceID, nil
	}
	f.next++
	return 100 + f.next, nil
}
func (f *fakeNotifier) Retract(id uint32, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = append(f.closed, id)
	return nil
}

type clock struct{ t time.Time }

func (c *clock) Now() time.Time { return c.t }

type focus struct{ v platform.Verdict }

func (f focus) Check(string) platform.Verdict { return f.v }

type rig struct {
	t   *testing.T
	d   Deps
	n   *fakeNotifier
	clk *clock
}

const sid = "11111111-2222-3333-4444-555555555555"

func newRig(t *testing.T) *rig {
	root := t.TempDir()
	cfg := config.Default()
	clk := &clock{t: time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)}
	n := &fakeNotifier{}
	return &rig{t, Deps{Paths: paths.Paths{State: root + "/state"}, Cfg: cfg, Notifier: n, Clock: clk}, n, clk}
}

// emit appends one event to the session's events.jsonl, sec seconds after the
// rig's start.
func (r *rig) emit(sec int, ev string, mod ...func(*hook.Event)) {
	r.t.Helper()
	e := hook.Event{V: 1, TS: time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC).Add(time.Duration(sec) * time.Second).Format(time.RFC3339Nano),
		Ev: ev, SID: sid, Tmux: &hook.TmuxLoc{Sock: "/s", SPID: 1, Pane: "%1"}}
	for _, m := range mod {
		m(&e)
	}
	b, _ := json.Marshal(e)
	dir := r.d.Paths.Session(sid)
	os.MkdirAll(dir, 0o700)
	f, _ := os.OpenFile(dir+"/events.jsonl", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	f.Write(append(b, '\n'))
	f.Close()
}

func entry(ep string) func(*hook.Event) {
	return func(e *hook.Event) { e.Start = &hook.StartExtra{Entrypoint: ep, Repo: "acme/api", Task: "ABC-1"} }
}

func (r *rig) dispatch(req Request) (Outcome, string) {
	r.t.Helper()
	req.Session = sid
	o, why, err := Dispatch(r.d, req)
	if err != nil {
		r.t.Fatalf("dispatch: %v", err)
	}
	return o, why
}

func TestNeedsYouPostsWithValuesFromTheSession(t *testing.T) {
	r := newRig(t)
	r.emit(0, "SessionStart", entry("cli"))
	r.emit(1, "UserPromptSubmit")
	r.emit(2, "PermissionRequest")
	if o, why := r.dispatch(Request{Kind: NeedsYou, Detail: "permission prompt"}); o != Posted {
		t.Fatalf("outcome %s %s", o, why)
	}
	p := r.n.posts[0]
	if p.Title != "bosun · needs you" || p.Body != "ABC-1 / acme/api is waiting on you (permission prompt)" || !p.Sound || p.Group != "bosun."+sid+".attn" {
		t.Errorf("notice = %+v", p)
	}
}

// D-10 and the -p denial: a needs-you that cleared before the child looked, or
// a session that is not interactive, produces nothing.
func TestNeedsYouDroppedWhenClearedOrNonInteractive(t *testing.T) {
	r := newRig(t)
	r.emit(0, "SessionStart", entry("cli"))
	r.emit(2, "PermissionRequest")
	r.emit(2, "PostToolBatch")
	if o, _ := r.dispatch(Request{Kind: NeedsYou, Detail: "permission prompt"}); o != Dropped {
		t.Errorf("a cleared edge posted: %s", o)
	}
	r2 := newRig(t)
	r2.emit(0, "SessionStart", entry("sdk-cli"))
	r2.emit(2, "PermissionRequest")
	if o, why := r2.dispatch(Request{Kind: NeedsYou, Detail: "permission prompt"}); o != Dropped || !strings.Contains(why, "non-interactive") {
		t.Errorf("sdk-cli session: %s %s", o, why)
	}
	// An unknown entrypoint with no status line ever seen is treated as non-interactive.
	r3 := newRig(t)
	r3.emit(0, "SessionStart", entry(""))
	r3.emit(2, "PermissionRequest")
	if o, _ := r3.dispatch(Request{Kind: NeedsYou, Detail: "permission prompt"}); o != Dropped {
		t.Errorf("unknown entrypoint without a status line: %s", o)
	}
	os.WriteFile(r3.d.Paths.Session(sid)+"/statusline.json", []byte(`{"session_id":"`+sid+`","cost_usd":1}`), 0o600)
	if o, _ := r3.dispatch(Request{Kind: NeedsYou, Detail: "permission prompt"}); o != Posted {
		t.Errorf("unknown entrypoint with a status line: %s", o)
	}
}

// One notice per waiting episode: the permission_prompt Notification that
// follows a PermissionRequest is not a second notice.
func TestEpisodeDedupe(t *testing.T) {
	r := newRig(t)
	r.emit(0, "SessionStart", entry("cli"))
	r.emit(1, "PermissionRequest")
	if o, _ := r.dispatch(Request{Kind: NeedsYou, Detail: "permission prompt"}); o != Posted {
		t.Fatal(o)
	}
	r.emit(7, "Notification", func(e *hook.Event) { e.NType = "permission_prompt" })
	if o, _ := r.dispatch(Request{Kind: NeedsYou, Detail: "permission prompt"}); o != Dropped {
		t.Errorf("second dispatch in one episode: %s", o)
	}
	if len(r.n.posts) != 1 {
		t.Errorf("posts = %d", len(r.n.posts))
	}
}

func TestDoneThresholdAndValues(t *testing.T) {
	r := newRig(t)
	r.emit(0, "SessionStart", entry("cli"))
	r.emit(1, "UserPromptSubmit")
	r.emit(5, "PreToolUse", func(e *hook.Event) { e.Tool = "Edit" })
	r.emit(6, "Stop")
	if o, why := r.dispatch(Request{Kind: Done}); o != Dropped || !strings.Contains(why, "done_min_turn") {
		t.Errorf("a 5 s turn: %s %s", o, why)
	}
	r.emit(30, "UserPromptSubmit")
	r.emit(35, "PreToolUse", func(e *hook.Event) { e.Tool = "Edit" })
	r.emit(36, "PreToolUse", func(e *hook.Event) { e.Tool = "Write" })
	r.emit(60, "Stop")
	snap, _ := json.Marshal(map[string]any{"session_id": sid, "cost_usd": 3.10, "banked_usd": 0})
	os.WriteFile(r.d.Paths.Session(sid)+"/statusline.json", snap, 0o600)
	if o, why := r.dispatch(Request{Kind: Done}); o != Posted {
		t.Fatalf("a 30 s turn: %s %s", o, why)
	}
	if got := r.n.posts[0]; got.Body != "ABC-1 / acme/api is done, ready to review" || got.Line2 != "$3.10 this session" || got.Sound {
		t.Errorf("notice = %+v (done is silent)", got)
	}
}

// LESSONS L-27: a sub-second session never reached live.json, but its done and
// failed edges still notify.
func TestDoneAndFailedPostWithEmptyLiveJSON(t *testing.T) {
	r := newRig(t)
	r.d.Cfg.Notify.DoneMinTurn = 0
	r.emit(0, "SessionStart", entry("cli"))
	r.emit(1, "UserPromptSubmit")
	r.emit(2, "StopFailure")
	if _, err := os.Stat(r.d.Paths.Live()); !os.IsNotExist(err) {
		t.Fatal("live.json should not exist in this test")
	}
	if o, why := r.dispatch(Request{Kind: Failed}); o != Posted {
		t.Fatalf("failed: %s %s", o, why)
	}
	if got := r.n.posts[0]; got.Body != "ABC-1 / acme/api failed after 1s" || !got.Sound {
		t.Errorf("notice = %+v", got)
	}
}

func TestFocusSuppressesAttentionButNotRunaway(t *testing.T) {
	r := newRig(t)
	r.d.Focus = focus{platform.Focused}
	r.emit(0, "SessionStart", entry("cli"))
	r.emit(1, "PermissionRequest")
	if o, _ := r.dispatch(Request{Kind: NeedsYou, Detail: "permission prompt"}); o != Suppressed {
		t.Errorf("focused needs-you: %s", o)
	}
	// Unknown notifies.
	r.d.Focus = focus{platform.Unknown}
	if o, _ := r.dispatch(Request{Kind: NeedsYou, Detail: "permission prompt"}); o != Posted {
		t.Errorf("unknown focus: %s", o)
	}
	// Runaway alerts ignore focus.
	r.d.Focus = focus{platform.Focused}
	s := rules.Store{}
	s.Reconcile([]rules.Cond{{Rule: rules.Quiet, Scope: sid, Session: sid}}, r.clk.t, "d", false)
	s.Save(r.d.Paths.Alerts())
	id := s[rules.Key(rules.Quiet, sid)].ID
	if o, why := r.dispatch(Request{Kind: Quiet, AlertID: id, Edge: "e1", Duration: dur(6 * time.Minute)}); o != Posted {
		t.Errorf("runaway under focus: %s %s", o, why)
	}
}

func TestAlertDismissedBeforeDispatchIsDropped(t *testing.T) {
	r := newRig(t)
	s := rules.Store{}
	s.Reconcile([]rules.Cond{{Rule: rules.Quiet, Scope: sid, Session: sid}}, r.clk.t, "d", false)
	id := s[rules.Key(rules.Quiet, sid)].ID
	s.Dismiss(id, false)
	s.Save(r.d.Paths.Alerts())
	if o, _ := r.dispatch(Request{Kind: Quiet, AlertID: id, Duration: dur(time.Minute)}); o != Dropped {
		t.Errorf("dismissed alert posted: %s", o)
	}
	if o, _ := r.dispatch(Request{Kind: Quiet, AlertID: "nope0", Duration: dur(time.Minute)}); o != Dropped {
		t.Errorf("unknown alert posted: %s", o)
	}
}

// Rate limit: 10 alerts give 6 notices plus one overflow notice naming the key.
func TestRateLimitGivesSixPlusOneOverflow(t *testing.T) {
	r := newRig(t)
	s := rules.Store{}
	var ids []string
	var conds []rules.Cond
	for i := 0; i < 10; i++ {
		scope := "s" + string(rune('a'+i))
		conds = append(conds, rules.Cond{Rule: rules.Quiet, Scope: scope, Session: scope})
	}
	s.Reconcile(conds, r.clk.t, "d", false)
	for i := 0; i < 10; i++ {
		ids = append(ids, s[rules.Key(rules.Quiet, "s"+string(rune('a'+i)))].ID)
	}
	s.Save(r.d.Paths.Alerts())
	counts := map[Outcome]int{}
	for i := 0; i < 10; i++ {
		scope := "s" + string(rune('a'+i))
		req := Request{Kind: Quiet, Session: scope, AlertID: ids[i], Edge: ids[i], Duration: dur(time.Minute)}
		o, _, err := Dispatch(r.d, req)
		if err != nil {
			t.Fatal(err)
		}
		counts[o]++
		r.clk.t = r.clk.t.Add(time.Second)
	}
	if counts[Posted] != 6 || counts[Overflow] != 1 || counts[Dropped] != 3 {
		t.Errorf("outcomes = %v", counts)
	}
	if len(r.n.posts) != 7 {
		t.Fatalf("posts = %d, want 6 + 1 overflow", len(r.n.posts))
	}
	over := r.n.posts[6]
	if over.Group != OverflowGroup || over.Body != "More notices are waiting; prefix + W opens bosun" {
		t.Errorf("overflow = %+v", over)
	}
	// Two minutes on, the window has drained.
	r.clk.t = r.clk.t.Add(2 * time.Minute)
	req := Request{Kind: Quiet, Session: "sz", AlertID: ids[0], Edge: "new", Duration: dur(time.Minute)}
	if o, why, _ := Dispatch(r.d, req); o != Posted {
		t.Log(why)
		t.Errorf("after the window drained: %s", o)
	}
}

// LESSONS L-40: post in one process, retract in another. Two Dispatch calls with
// no shared memory stand in for two processes; the id travels through notify.json.
func TestPostThenRetractAcrossDispatches(t *testing.T) {
	r := newRig(t)
	r.emit(0, "SessionStart", entry("cli"))
	r.emit(1, "PermissionRequest")
	r.dispatch(Request{Kind: NeedsYou, Detail: "permission prompt"})
	id := r.n.next + 100
	// A new Deps value and a new notifier: nothing in memory carries over.
	other := &fakeNotifier{}
	d2 := r.d
	d2.Notifier = other
	o, _, err := Dispatch(d2, Request{Retract: true, Group: "bosun." + sid + ".attn"})
	if err != nil || o != Retracted || len(other.closed) != 1 || other.closed[0] != id {
		t.Fatalf("outcome %s err %v closed %v want id %d", o, err, other.closed, id)
	}
	if o, _, _ := Dispatch(d2, Request{Retract: true, Group: "bosun." + sid + ".attn"}); o != Dropped {
		t.Errorf("second retract: %s", o)
	}
}

func TestReplaceUsesTheStoredID(t *testing.T) {
	r := newRig(t)
	r.emit(0, "SessionStart", entry("cli"))
	r.emit(1, "PermissionRequest")
	r.dispatch(Request{Kind: NeedsYou, Detail: "permission prompt"})
	r.emit(2, "PostToolUse")
	r.emit(3, "PermissionRequest") // a new episode
	r.dispatch(Request{Kind: NeedsYou, Detail: "permission prompt"})
	if len(r.n.posts) != 2 || r.n.posts[1].ReplaceID != 101 {
		t.Errorf("posts = %+v", r.n.posts)
	}
}

// LESSONS L-12: a backend that hangs stalls one dispatch; the next gives up on
// the lock at its deadline and still returns.
func TestHungBackendDoesNotStallTheNextDispatch(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the 2 s lock deadline")
	}
	r := newRig(t)
	r.n.block = make(chan struct{})
	done := make(chan struct{})
	go func() { Dispatch(r.d, Request{Test: true}); close(done) }()
	defer func() { close(r.n.block); <-done }() // unblock, then let it finish before TempDir cleanup
	time.Sleep(100 * time.Millisecond)
	start := time.Now()
	o, _, err := Dispatch(r.d, Request{Test: true})
	if err != nil || o != Busy {
		t.Errorf("outcome %s err %v", o, err)
	}
	if d := time.Since(start); d > 4*time.Second {
		t.Errorf("second dispatch took %v", d)
	}
}

func TestOffWithoutBackend(t *testing.T) {
	r := newRig(t)
	r.d.Notifier = nil
	if o, _, _ := Dispatch(r.d, Request{Test: true}); o != Off {
		t.Errorf("got %s", o)
	}
}

func TestNotifyJSONIsAtomicAndSurvives(t *testing.T) {
	r := newRig(t)
	r.n.next = 0
	Dispatch(r.d, Request{Test: true})
	st := LoadState(r.d.Paths.Notify())
	if _, ok := st.Posted["bosun.test"]; !ok || len(st.Window) != 1 {
		t.Errorf("state = %+v", st)
	}
	ents, _ := os.ReadDir(filepath.Dir(r.d.Paths.Notify()))
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

// A short turn after a needs-you: the done notice is not worth posting, but the
// stale needs-you notice in the same group must go.
func TestShortDoneRetractsTheStaleNeedsYouNotice(t *testing.T) {
	r := newRig(t)
	r.emit(0, "SessionStart", entry("cli"))
	r.emit(1, "UserPromptSubmit")
	r.emit(2, "PermissionRequest")
	if o, _ := r.dispatch(Request{Kind: NeedsYou, Detail: "permission prompt"}); o != Posted {
		t.Fatal(o)
	}
	r.emit(3, "PostToolUse")
	r.emit(4, "Stop") // a 3 s turn
	if o, why := r.dispatch(Request{Kind: Done}); o != Retracted || len(r.n.closed) != 1 {
		t.Errorf("outcome %s (%s) closed %v", o, why, r.n.closed)
	}
}

// ---- Linux click (no daemon: a short-lived child with a hard deadline)

type streamer struct {
	lines   []string
	called  bool
	timeout time.Duration
	args    []string
}

func (s *streamer) Stream(timeout time.Duration, name string, args []string, onLine func(string) bool) error {
	s.called, s.timeout, s.args = true, timeout, append([]string{name}, args...)
	for _, l := range s.lines {
		if onLine(l) {
			return nil
		}
	}
	return nil
}

func TestListenJumpsOnTheDefaultActionForItsNotice(t *testing.T) {
	run := &fakeRunner{}
	st := &streamer{lines: []string{
		"/org/freedesktop/Notifications: org.freedesktop.Notifications.NotificationClosed (uint32 99, uint32 2)",
		"/org/freedesktop/Notifications: org.freedesktop.Notifications.ActionInvoked (uint32 99, 'default')",
		"/org/freedesktop/Notifications: org.freedesktop.Notifications.ActionInvoked (uint32 42, 'other')",
		"/org/freedesktop/Notifications: org.freedesktop.Notifications.ActionInvoked (uint32 42, 'default')",
		"/org/freedesktop/Notifications: org.freedesktop.Notifications.ActionInvoked (uint32 42, 'default')", // never reached
	}}
	clicked, err := Listen(ClickDeps{Stream: st, Run: run, Gdbus: "/usr/bin/gdbus", Exe: "/usr/bin/bosun", Timeout: time.Minute}, 42, "s1")
	if err != nil || !clicked {
		t.Fatalf("clicked=%v err=%v", clicked, err)
	}
	if len(run.calls) != 1 || strings.Join(run.calls[0], " ") != "/usr/bin/bosun jump --session s1" {
		t.Errorf("calls = %v", run.calls)
	}
	if st.timeout != time.Minute || strings.Join(st.args, " ") != "/usr/bin/gdbus monitor --session --dest org.freedesktop.Notifications" {
		t.Errorf("stream: %v %v", st.timeout, st.args)
	}
}

func TestListenEndsWhenTheNoticeIsClosedOrTimesOut(t *testing.T) {
	run := &fakeRunner{}
	closed := &streamer{lines: []string{
		"/org/freedesktop/Notifications: org.freedesktop.Notifications.NotificationClosed (uint32 42, uint32 1)",
		"/org/freedesktop/Notifications: org.freedesktop.Notifications.ActionInvoked (uint32 42, 'default')",
	}}
	if clicked, _ := Listen(ClickDeps{Stream: closed, Run: run, Gdbus: "g", Exe: "b"}, 42, "s1"); clicked || len(run.calls) != 0 {
		t.Error("a closed notice must end the listener without a jump")
	}
	quiet := &streamer{}
	if clicked, _ := Listen(ClickDeps{Stream: quiet, Run: run, Gdbus: "g", Exe: "b"}, 42, "s1"); clicked || quiet.timeout != ClickTimeout {
		t.Errorf("a quiet bus: clicked=%v timeout=%v, want the hard default %v", clicked, quiet.timeout, ClickTimeout)
	}
}

func TestPostOffersAClickOnlyWhenTheServerAdvertisesActions(t *testing.T) {
	r := newRig(t)
	fr := &fakeRunner{out: map[string]string{"GetCapabilities": "(['actions', 'body', 'sound'],)", "Notify": "(uint32 7,)"}}
	r.d.Notifier = GDBus{R: fr, Bin: "gdbus"}
	var spawned [][]string
	r.d.Spawn = func(args ...string) error { spawned = append(spawned, args); return nil }
	r.emit(0, "SessionStart", entry("cli"))
	r.emit(1, "PermissionRequest")
	if o, why := r.dispatch(Request{Kind: NeedsYou, Detail: "permission prompt"}); o != Posted {
		t.Fatalf("%s %s", o, why)
	}
	var notifyArgv string
	for _, c := range fr.calls {
		if strings.Contains(strings.Join(c, " "), ".Notify") {
			notifyArgv = strings.Join(c, "|")
		}
	}
	if !strings.Contains(notifyArgv, "['default', 'Open in tmux']") {
		t.Errorf("Notify argv lacks the default action:\n%s", notifyArgv)
	}
	if len(spawned) != 1 || strings.Join(spawned[0], " ") != "notify-click --id 7 --session "+sid {
		t.Errorf("spawned = %v", spawned)
	}
	// The capability probe is cached in notify.json: a second dispatch does not ask again.
	probes := func() (n int) {
		for _, c := range fr.calls {
			if strings.Contains(strings.Join(c, " "), "GetCapabilities") {
				n++
			}
		}
		return
	}
	before := probes()
	r.emit(5, "PostToolUse")
	r.emit(6, "PermissionRequest")
	r.dispatch(Request{Kind: NeedsYou, Detail: "permission prompt"})
	if probes() != before {
		t.Error("the capability probe was not cached")
	}
	if len(spawned) != 1 { // the second post replaced id 7; its listener is still live
		t.Errorf("spawned = %v", spawned)
	}
}

func TestNoClickWithoutActionsOrASession(t *testing.T) {
	r := newRig(t)
	fr := &fakeRunner{out: map[string]string{"GetCapabilities": "(['body'],)", "Notify": "(uint32 7,)"}}
	r.d.Notifier = GDBus{R: fr, Bin: "gdbus"}
	n := 0
	r.d.Spawn = func(...string) error { n++; return nil }
	r.emit(0, "SessionStart", entry("cli"))
	r.emit(1, "PermissionRequest")
	r.dispatch(Request{Kind: NeedsYou, Detail: "permission prompt"})
	for _, c := range fr.calls {
		if strings.Contains(strings.Join(c, " "), "'default'") {
			t.Error("offered an action to a server that does not advertise actions")
		}
	}
	// Account-level notices have no session to jump to.
	fr.out["GetCapabilities"] = "(['actions'],)"
	r.d.Paths.State = r.d.Paths.State + "2"
	Dispatch(r.d, Request{Test: true})
	if n != 0 {
		t.Errorf("spawned %d listeners for notices that cannot be clicked", n)
	}
}

func TestDoneBodyExcludesForkInheritanceAfterCompact(t *testing.T) {
	r := newRig(t)
	r.d.Cfg.Notify.DoneMinTurn = 0
	r.emit(0, "SessionStart", func(e *hook.Event) { e.Src = "fork"; entry("cli")(e) })
	r.emit(1, "UserPromptSubmit")
	r.emit(2, "SessionStart", func(e *hook.Event) { e.Src = "compact" })
	r.emit(3, "PreToolUse", func(e *hook.Event) { e.Tool = "Edit" })
	r.emit(30, "Stop")
	snap, _ := json.Marshal(map[string]any{"session_id": sid, "cost_usd": 12.0, "first_usd": 10.0})
	os.WriteFile(r.d.Paths.Session(sid)+"/statusline.json", snap, 0o600)
	if o, why := r.dispatch(Request{Kind: Done}); o != Posted {
		t.Fatalf("%s %s", o, why)
	}
	if got := r.n.posts[0].Line2; !strings.Contains(got, "$2.00 this session") {
		t.Errorf("body = %q; a fork's inherited $10 must not be counted", got)
	}
}

// The owner's mock (build-log/mockups/1-bar-notices.png), line by line.
func TestNoticesMatchTheMock(t *testing.T) {
	waited := 42 * time.Second
	m, _ := Render(Request{Kind: NeedsYou, Task: "refund-cancel", Repo: "payments-api", Detail: "permission prompt",
		Tool: "Bash", Target: "npm test", Where: "payments:1", Waited: &waited})
	if m.Title != "bosun · needs you" || m.Body != "refund-cancel / payments-api wants to run npm test" || m.Line2 != "payments:1 · waiting 0:42" {
		t.Errorf("needs-you: %+v", m)
	}
	m, _ = Render(Request{Kind: NeedsYou, Detail: "input needed", Question: "Keep idempotency key on retry?", Where: "payments:2"})
	if m.Body != "asks: Keep idempotency key on retry?" || m.Line2 != "payments:2" {
		t.Errorf("question: %+v", m)
	}
	m, _ = Render(Request{Kind: SubagentRuntime, Agent: "Explore", Task: "refund-cancel", AlertID: "41",
		Duration: dur(14 * time.Minute), USD: f64(3.12), LimitDur: dur(10 * time.Minute), LimitUSD: f64(2)})
	if m.Title != "bosun · runaway subagent" || m.Body != "Explore in refund-cancel: 14m00s and $3.12 (limits 10m00s / $2)" || m.Line2 != "bosun ack 41 · or interrupt from the popup" {
		t.Errorf("runaway: %+v", m)
	}
	m, _ = Render(Request{Kind: Done, Task: "cancel-flow-ui", Repo: "member-portal", Edits: num(12), OwnUSD: f64(1.84), Where: "portal:2"})
	if m.Title != "bosun · finished" || m.Icon != "emblem-ok-symbolic" || m.Body != "cancel-flow-ui / member-portal is done, ready to review" || m.Line2 != "portal:2 · $1.84 this session" {
		t.Errorf("done: %+v", m)
	}
}

func TestSecondLineIsItalicOnlyWithBodyMarkup(t *testing.T) {
	n := Notice{Body: "a < b", Line2: "x & y"}
	if got := bodyOf(n); got != "a < b\nx & y" {
		t.Errorf("plain = %q", got)
	}
	n.Markup = true
	if got := bodyOf(n); got != "a &lt; b\n<i>x &amp; y</i>" {
		t.Errorf("markup = %q", got)
	}
}

func TestSingleLineBodyIsEscapedWithBodyMarkup(t *testing.T) {
	const raw = `<a href="x">y</a> & z`
	const esc = `&lt;a href="x"&gt;y&lt;/a&gt; &amp; z`
	n := Notice{Title: "t", Subtitle: "<b>s</b>", Body: raw}
	if got := bodyOf(n); got != raw {
		t.Errorf("markup off: %q", got)
	}
	n.Markup = true
	if got := bodyOf(n); got != esc {
		t.Errorf("markup on: %q", got)
	}

	for _, markup := range []bool{false, true} {
		n.Markup = markup
		want, wantSub := raw, "<b>s</b>"
		if markup {
			want, wantSub = esc, "&lt;b&gt;s&lt;/b&gt;"
		}
		fr := &fakeRunner{out: map[string]string{"Notify": "(uint32 1,)"}}
		if _, err := (GDBus{R: fr, Bin: "gdbus"}).Post(n); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(fr.calls[0], " "); !strings.Contains(got, wantSub+`\n`+want) {
			t.Errorf("gdbus markup=%v argv = %s", markup, got)
		}
		fr = &fakeRunner{}
		if _, err := (NotifySend{R: fr, Bin: "notify-send"}).Post(n); err != nil {
			t.Fatal(err)
		}
		if got := fr.calls[0]; got[len(got)-1] != wantSub+"\n"+want {
			t.Errorf("notify-send markup=%v body = %q", markup, got[len(got)-1])
		}
	}
}

func TestNotifySendEscapesBothLinesWithoutItalics(t *testing.T) {
	fr := &fakeRunner{}
	n := Notice{Title: "t", Body: "a<b", Line2: "x&y", Markup: true}
	if _, err := (NotifySend{R: fr, Bin: "notify-send"}).Post(n); err != nil {
		t.Fatal(err)
	}
	if got := fr.calls[0]; got[len(got)-1] != "a&lt;b\nx&amp;y" {
		t.Errorf("body = %q", got[len(got)-1])
	}
}

func TestParseArgsValidatesSession(t *testing.T) {
	for _, bad := range []string{"../../etc", "a/b", strings.Repeat("a", 65)} {
		if _, err := ParseArgs([]string{"--session", bad}); err == nil {
			t.Errorf("--session %q accepted", bad)
		}
	}
	r, err := ParseArgs([]string{"--session", "3f2c9a1e-4b5d-4c6e-8f70-123456789abc"})
	if err != nil || r.Session != "3f2c9a1e-4b5d-4c6e-8f70-123456789abc" {
		t.Errorf("valid id: %+v %v", r, err)
	}
}
