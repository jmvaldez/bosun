package doctor

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jmvaldez/bosun/internal/claude"
	"github.com/jmvaldez/bosun/internal/config"
	"github.com/jmvaldez/bosun/internal/heartbeat"
	"github.com/jmvaldez/bosun/internal/paths"
	"github.com/jmvaldez/bosun/internal/platform"
	"github.com/jmvaldez/bosun/internal/tmuxconf"
)

var now = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)

// script answers fake exec calls by a substring of the joined argv.
type script struct {
	answers map[string]string
	errs    map[string]error
	calls   []string
}

func (s *script) Run(_ time.Duration, name string, args ...string) ([]byte, error) {
	line := name + " " + strings.Join(args, " ")
	s.calls = append(s.calls, line)
	for k, err := range s.errs {
		if strings.Contains(line, k) {
			return nil, err
		}
	}
	for k, v := range s.answers {
		if strings.Contains(line, k) {
			return []byte(v), nil
		}
	}
	return nil, nil
}

type rig struct {
	t    *testing.T
	root string
	env  Env
	run  *script
	bin  string
}

const goodStatusRight = "##(/usr/bin/bosun status --tmux)#[fg=colour235] %H:%M"

// newRig builds a fully installed machine: plugin enabled once, wrapped status
// line, a fresh heartbeat, current tmux conf, a running tmux with the segment.
func newRig(t *testing.T) *rig {
	root := t.TempDir()
	p := paths.Paths{Config: root + "/config", State: root + "/state", Cache: root + "/cache", Data: root + "/data", Claude: root + "/claude"}
	binDir := root + "/bin"
	os.MkdirAll(binDir, 0o755)
	os.WriteFile(binDir+"/bosun", []byte("#!/bin/sh\n"), 0o755)
	run := &script{answers: map[string]string{
		"command -v bosun":                       binDir + "/bosun\n",
		"-V":                                     "tmux 3.4\n",
		"show-environment -g PATH":               "PATH=" + binDir + ":/usr/bin\n",
		"show -gv status-right":                  goodStatusRight,
		"show -gwv window-status-format":         "#{E:@bosun-win-mark}#I:#W",
		"show -gwv window-status-current-format": "#{E:@bosun-win-mark}#I:#W*",
		"list-keys -T prefix":                    "bind-key -T prefix W display-popup -E \"/usr/bin/bosun ui\"\nbind-key -T prefix '$' display-popup -E \"/usr/bin/bosun ui --spend\"\nbind-key -T prefix a run-shell -b \"/usr/bin/bosun jump --queue\"\n",
		"show -gv status-interval":               "1\n",
		"show -gv allow-passthrough":             "off\n",
		"auth status --json":                     `{"email":"Me@Example.com"}`,
	}}
	e := Env{
		Paths: p, Version: "0.1.0", Kind: platform.Linux, Now: now, Getenv: func(string) string { return "" },
		Runner: run, Executable: binDir + "/bosun", TmuxBin: "tmux", ClaudeBin: "/usr/bin/claude",
		LookPath: func(n string) (string, error) {
			if n == "bosun" || n == "gdbus" || n == "claude" {
				return "/usr/bin/" + n, nil
			}
			return "", errors.New("not found")
		},
		Config: config.Default(),
	}
	r := &rig{t: t, root: root, env: e, run: run, bin: binDir}
	r.writeSettings(`{"enabledPlugins":{"bosun@bosun":true},"statusLine":{"type":"command","command":"if command -v bosun >/dev/null; then exec bosun statusline -- jq .; else exec jq .; fi"}}`)
	for _, d := range []string{p.State, p.Cache, p.Data, p.Config} {
		os.MkdirAll(d, 0o755)
	}
	if err := heartbeat.Record(p, now.Add(-time.Minute), "Stop", "0.1.0", 200*time.Microsecond); err != nil {
		t.Fatal(err)
	}
	conf, _ := tmuxconf.Generate(tmuxconf.Options{Version: "0.1.0", Bosun: "/usr/bin/bosun", JumpKey: "a", PopupKey: "W"})
	os.WriteFile(p.TmuxConf(), []byte(conf), 0o644)
	claude.WriteIdentity(p.Identity(), "me@example.com", now.Add(-time.Hour))
	os.WriteFile(p.IngestMarker(), []byte(`{"at":"`+now.Add(-time.Hour).Format(time.RFC3339)+`"}`), 0o644)
	return r
}

func (r *rig) writeSettings(body string) {
	os.MkdirAll(r.env.Paths.Claude, 0o755)
	os.WriteFile(r.env.Paths.ClaudeSettings(), []byte(body), 0o644)
}

func (r *rig) checks() map[string]Check {
	m := map[string]Check{}
	for _, c := range Run(r.env) {
		m[c.Name] = c
	}
	return m
}

func (r *rig) status(name string) Status {
	c, ok := r.checks()[name]
	if !ok {
		r.t.Fatalf("no check named %q", name)
	}
	return c.Status
}

func TestFullInstallPasses(t *testing.T) {
	r := newRig(t)
	cs := Run(r.env)
	for _, c := range cs {
		if c.Status == Fail {
			t.Errorf("FAIL %s: %s", c.Name, c.Detail)
		}
	}
	if Failed(cs) {
		t.Fatal("a full install must pass")
	}
	var buf strings.Builder
	Text(&buf, cs)
	if !strings.Contains(buf.String(), "0 failed") {
		t.Errorf("summary:\n%s", buf.String())
	}
	t.Log("\n" + buf.String())
}

func TestFailsOnADisabledPlugin(t *testing.T) {
	r := newRig(t)
	r.writeSettings(`{"enabledPlugins":{"bosun@bosun":false},"statusLine":{"command":"bosun statusline -- jq ."}}`)
	if r.status("plugin") != Fail {
		t.Error("a disabled plugin must fail")
	}
	r.writeSettings(`{"statusLine":{"command":"bosun statusline -- jq ."}}`)
	if r.status("plugin") != Fail {
		t.Error("a missing plugin must fail")
	}
	// Installed but disabled: the message names the one-command fix.
	dir := filepath.Dir(r.env.Paths.ClaudeSettings()) + "/plugins"
	os.MkdirAll(dir, 0o755)
	os.WriteFile(dir+"/installed_plugins.json", []byte(`{"version":2,"plugins":{"bosun@bosun":[]}}`), 0o644)
	if c := r.checks()["plugin"]; c.Status != Fail || !strings.Contains(c.Detail, "claude plugin enable bosun@bosun") {
		t.Errorf("plugin check = %+v", c)
	}
	os.Remove(dir + "/installed_plugins.json")
	if !Failed(Run(r.env)) {
		t.Error("Failed() must report it (exit non-zero)")
	}
}

func TestEnabledPluginsArrayShapeAndDoubleEnable(t *testing.T) {
	r := newRig(t)
	r.writeSettings(`{"enabledPlugins":["bosun@bosun"],"statusLine":{"command":"bosun statusline -- x"}}`)
	if r.status("plugin") != OK {
		t.Error("the array shape must be accepted")
	}
	r.writeSettings(`{"enabledPlugins":{"bosun@bosun":true,"bosun@other":true},"statusLine":{"command":"bosun statusline -- x"}}`)
	if c := r.checks()["plugin"]; c.Status != Fail || !strings.Contains(c.Detail, "2 times") {
		t.Errorf("double enable: %+v", c)
	}
}

func TestFailsOnDoubleRegistrationAndDisableAllHooks(t *testing.T) {
	r := newRig(t)
	r.writeSettings(`{"enabledPlugins":{"bosun@bosun":true},"hooks":{"Stop":[{"hooks":[{"type":"command","command":"bosun hook --plugin-version 0.1.0"}]}]},"statusLine":{"command":"bosun statusline -- x"}}`)
	if c := r.checks()["settings-hooks"]; c.Status != Fail || !strings.Contains(c.Detail, "twice") {
		t.Errorf("double registration: %+v", c)
	}
	r.writeSettings(`{"enabledPlugins":{"bosun@bosun":true},"disableAllHooks":true,"statusLine":{"command":"bosun statusline -- x"}}`)
	if r.status("settings-hooks") != Fail {
		t.Error("disableAllHooks must fail")
	}
	r2 := newRig(t)
	pol := r2.root + "/managed-settings.json"
	os.WriteFile(pol, []byte(`{"allowManagedHooksOnly":true}`), 0o644)
	r2.env.ManagedSettings = []string{pol, r2.root + "/absent.json"}
	if r2.status("managed-policy") != Fail {
		t.Error("allowManagedHooksOnly must fail")
	}
}

// D-31: live cost is opt-in, so an unwrapped status line is a choice, not a
// failure, and no snapshots are expected from it.
func TestAnUnwrappedStatusLineIsLiveCostOff(t *testing.T) {
	r := newRig(t)
	r.writeSettings(`{"enabledPlugins":{"bosun@bosun":true},"statusLine":{"type":"command","command":"jq -r .model.id"}}`)
	if r.status("statusline") != OK || r.status("snapshots") != OK {
		t.Error("an unwrapped statusLine is live cost off, which is fine")
	}
}

func TestStaleSnapshotsForLiveSessionsWarn(t *testing.T) {
	r := newRig(t)
	r.writeSettings(`{"enabledPlugins":{"bosun@bosun":true},"statusLine":{"command":"bosun statusline -- jq ."}}`)
	id := "aaaaaaaa-0000-0000-0000-00000000000a"
	f := map[string]any{"sessions": map[string]any{id: map[string]any{"id": id, "entrypoint": "cli"}}}
	b, _ := json.Marshal(f)
	os.WriteFile(r.env.Paths.Live(), b, 0o644)
	if r.status("snapshots") != Warn {
		t.Error("a live session with no snapshot should warn")
	}
	os.MkdirAll(r.env.Paths.Session(id), 0o755)
	snap, _ := json.Marshal(map[string]any{"ts": now.Add(-time.Minute).Format(time.RFC3339Nano)})
	os.WriteFile(r.env.Paths.Session(id)+"/statusline.json", snap, 0o644)
	if r.status("snapshots") != OK {
		t.Error("a fresh snapshot should pass")
	}
}

func TestHeartbeatChecks(t *testing.T) {
	r := newRig(t)
	os.Remove(r.env.Paths.Heartbeat())
	if r.status("heartbeat") != Fail {
		t.Error("no heartbeat must fail")
	}
	r = newRig(t)
	for i := 0; i < 20; i++ {
		heartbeat.Record(r.env.Paths, now.Add(-time.Minute), "Stop", "0.1.0", 80*time.Millisecond)
	}
	if r.status("heartbeat") != Fail {
		t.Error("p95 over 50 ms must fail")
	}
	r = newRig(t)
	heartbeat.Record(r.env.Paths, now.Add(-3*24*time.Hour), "Stop", "0.1.0", time.Millisecond)
	os.WriteFile(r.env.Paths.Heartbeat(), []byte(fmt.Sprintf(`{"last":%q,"plugin_version":"0.1.0","latency_us":{"Stop":[300]}}`, now.Add(-3*24*time.Hour).Format(time.RFC3339))), 0o644)
	if r.status("heartbeat") != Warn {
		t.Error("a heartbeat older than a day should warn")
	}
}

func TestVersionSkew(t *testing.T) {
	for plugin, want := range map[string]Status{"0.1.5": OK, "0.2.0": Warn, "1.0.0": Fail, "junk": Warn} {
		r := newRig(t)
		os.WriteFile(r.env.Paths.Heartbeat(), []byte(fmt.Sprintf(`{"last":%q,"plugin_version":%q,"latency_us":{"Stop":[300]}}`, now.Add(-time.Minute).Format(time.RFC3339), plugin)), 0o644)
		if got := r.status("version"); got != want {
			t.Errorf("plugin %s vs binary 0.1.0: %s, want %s", plugin, got, want)
		}
	}
	r := newRig(t)
	r.env.Version = "0.1.0-dev+abc1234" // a development build compares by major.minor
	if r.status("version") != OK {
		t.Error("a dev version suffix must not count as skew")
	}
}

// D-3: 3.2 has popups but no popup title; below 3.2 there is no popup.
func TestTmuxVersions(t *testing.T) {
	for out, want := range map[string]Status{"tmux 3.2a\n": Warn, "tmux 3.5a\n": OK, "tmux 3.1c\n": Fail, "tmux 2.9\n": Fail, "tmux next-3.4\n": OK} {
		r := newRig(t)
		r.run.answers["-V"] = out
		if got := r.status("tmux"); got != want {
			t.Errorf("%q: %s, want %s", out, got, want)
		}
	}
	r := newRig(t)
	r.run.answers["-V"] = "tmux 3.2a\n"
	if c := r.checks()["tmux"]; !strings.Contains(c.Detail, "popup yes") || !strings.Contains(c.Detail, "title no") {
		t.Errorf("3.2 detail = %q", c.Detail)
	}
}

func TestTmuxLiveChecks(t *testing.T) {
	r := newRig(t)
	r.run.answers["show -gv status-right"] = goodStatusRight + goodStatusRight
	if c := r.checks()["tmux-segment"]; c.Status != Fail || !strings.Contains(c.Detail, "2 times") {
		t.Errorf("a doubled segment: %+v", c)
	}
	r = newRig(t)
	r.run.answers["show -gv status-right"] = "%H:%M"
	if r.status("tmux-segment") != Fail {
		t.Error("a missing segment must fail")
	}
	r = newRig(t)
	r.run.answers["show -gv status-interval"] = "15\n"
	if r.status("tmux-interval") != Warn {
		t.Error("status-interval 15 should warn")
	}
	r = newRig(t)
	r.run.answers["list-keys -T prefix"] = "bind-key -T prefix x kill-pane\n"
	if c := r.checks()["tmux-keys"]; c.Status != Fail {
		t.Errorf("unbound keys: %+v", c)
	}
	r = newRig(t)
	r.run.answers["show -gv allow-passthrough"] = "on\n"
	if r.status("tmux-passthrough") != Warn {
		t.Error("passthrough plus a notifier should warn")
	}
	r = newRig(t)
	r.run.errs = map[string]error{"has-session": errors.New("no server")}
	if r.status("tmux-server") != Warn {
		t.Error("no tmux server should warn, not fail")
	}
	r = newRig(t)
	conf, _ := tmuxconf.Generate(tmuxconf.Options{Version: "0.0.9", Bosun: "/usr/bin/bosun", JumpKey: "a", PopupKey: "W"})
	os.WriteFile(r.env.Paths.TmuxConf(), []byte(conf), 0o644)
	if r.status("tmux-conf") != Warn {
		t.Error("a stale conf header should warn")
	}
}

func TestBinaryChecks(t *testing.T) {
	r := newRig(t)
	r.env.LookPath = func(string) (string, error) { return "", errors.New("no") }
	if r.status("binary") != Fail {
		t.Error("bosun off PATH must fail")
	}
	r = newRig(t)
	r.run.answers["command -v bosun"] = ""
	if r.status("binary") != Fail {
		t.Error("a non-login sh that cannot find bosun must fail")
	}
	r = newRig(t)
	r.run.answers["show-environment -g PATH"] = "PATH=/usr/bin:/bin\n"
	if c := r.checks()["binary"]; c.Status != Fail || !strings.Contains(c.Detail, "tmux's global PATH") {
		t.Errorf("tmux PATH without bosun: %+v", c)
	}
	// D-4: any of the install locations is accepted.
	for _, dir := range []string{"/home/me/.local/bin", "/usr/bin", "/home/linuxbrew/.linuxbrew/bin"} {
		r = newRig(t)
		r.run.answers["command -v bosun"] = dir + "/bosun\n"
		if got := r.status("binary"); got != OK {
			t.Errorf("bosun at %s: %s", dir, got)
		}
	}
}

func TestConfigIdentityStateAndNotifier(t *testing.T) {
	r := newRig(t)
	r.env.ConfigRes = config.Result{Unknown: []string{"rules.quite"}}
	if c := r.checks()["config"]; c.Status != Warn || !strings.Contains(c.Detail, "rules.quite") {
		t.Errorf("unknown key: %+v", c)
	}
	r.env.ConfigErr = errors.New("line 3: bad")
	if r.status("config") != Fail {
		t.Error("a config that does not parse must fail")
	}
	r = newRig(t)
	r.run.answers["auth status --json"] = `{"email":"someone-else@example.com"}`
	if r.status("identity") != Fail {
		t.Error("an account key for another login must fail")
	}
	r = newRig(t)
	r.env.ClaudeBin = ""
	if r.status("claude") != Fail {
		t.Error("claude missing must fail")
	}
	r = newRig(t)
	os.Remove(r.env.Paths.Identity())
	if r.status("identity") != Warn {
		t.Error("no identity should warn")
	}
	r = newRig(t)
	os.WriteFile(r.env.Paths.IngestMarker(), []byte(`{"at":"`+now.Add(-21*24*time.Hour).Format(time.RFC3339)+`"}`), 0o644)
	if r.status("ingest") != Fail {
		t.Error("an ingest older than 20 days must fail")
	}
	r = newRig(t)
	os.Remove(r.env.Paths.IngestMarker())
	if r.status("ingest") != Warn {
		t.Error("no ingest yet should warn")
	}
	r = newRig(t)
	r.env.LookPath = func(n string) (string, error) {
		if n == "bosun" {
			return "/usr/bin/bosun", nil
		}
		return "", errors.New("no")
	}
	if c := r.checks()["notifier"]; c.Status != Warn || !strings.Contains(c.Detail, "install") {
		t.Errorf("no notifier must say how to add one: %+v", c)
	}
	r = newRig(t)
	r.env.Config.Notify.Backend = "off"
	if c := r.checks()["notifier"]; c.Status != OK || !strings.Contains(c.Detail, "off") {
		t.Errorf("off: %+v", c)
	}
	r = newRig(t)
	r.run.errs = map[string]error{"GetCapabilities": errors.New("no bus")}
	if r.status("notifier") != Warn {
		t.Error("a silent notification server should warn")
	}
}

func TestStateDirNotWritableFails(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores modes")
	}
	r := newRig(t)
	os.Chmod(r.env.Paths.State, 0o500)
	defer os.Chmod(r.env.Paths.State, 0o700)
	if r.status("state-dir") != Fail {
		t.Error("a read-only state dir must fail")
	}
}

func TestWSLChecks(t *testing.T) {
	r := newRig(t)
	r.env.Kind = platform.WSL
	r.env.Getenv = func(k string) string {
		if k == "WSL_INTEROP" {
			return "/run/WSL/1234_interop"
		}
		return ""
	}
	r.env.Paths.State = "/mnt/c/Users/me/bosun-state"
	cs := r.checks()
	if cs["wsl-interop"].Status != Fail || cs["wsl-state"].Status != Fail {
		t.Errorf("wsl: %+v %+v", cs["wsl-interop"], cs["wsl-state"])
	}
	if _, ok := newRig(t).checks()["wsl-state"]; ok {
		t.Error("WSL checks must run only on WSL")
	}
}

// LESSONS L-44: doctor never creates or changes a file. A listing of every dir
// it might touch, with sizes and times, is identical before and after, and
// absent dirs stay absent.
func TestDoctorLeavesTheFilesystemUnchanged(t *testing.T) {
	list := func(root string) string {
		var lines []string
		filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			fi, _ := d.Info()
			lines = append(lines, fmt.Sprintf("%s %v %d %d", p, fi.Mode(), fi.Size(), fi.ModTime().UnixNano()))
			return nil
		})
		sort.Strings(lines)
		return strings.Join(lines, "\n")
	}
	r := newRig(t)
	before := list(r.root)
	Run(r.env)
	if after := list(r.root); after != before {
		t.Errorf("doctor changed the tree:\n--- before\n%s\n--- after\n%s", before, after)
	}
	// On a machine with nothing installed, no directory appears either.
	empty := t.TempDir()
	e := r.env
	e.Paths = paths.Paths{Config: empty + "/c/bosun", State: empty + "/s/bosun", Cache: empty + "/ca/bosun", Data: empty + "/d/bosun", Claude: empty + "/cl"}
	before = list(empty)
	cs := Run(e)
	if after := list(empty); after != before {
		t.Errorf("doctor created files on an empty machine:\n%s\n--- after\n%s", before, after)
	}
	if !Failed(cs) {
		t.Error("an empty machine should fail the doctor")
	}
	// And it never asks tmux to change anything.
	for _, c := range r.run.calls {
		for _, verb := range []string{"set-option", "set ", "source-file", "bind", "kill", "new-", "send-keys"} {
			if strings.Contains(c, " "+verb) && !strings.Contains(c, "show") {
				t.Errorf("a mutating command: %s", c)
			}
		}
	}
}

func TestJSONOutput(t *testing.T) {
	var b strings.Builder
	if err := JSON(&b, Run(newRig(t).env)); err != nil {
		t.Fatal(err)
	}
	var back []Check
	if err := json.Unmarshal([]byte(b.String()), &back); err != nil || len(back) < 10 {
		t.Fatalf("%v %d", err, len(back))
	}
	for _, c := range back {
		if c.Name == "" || (c.Status != OK && c.Status != Warn && c.Status != Fail) {
			t.Errorf("bad check %+v", c)
		}
	}
}

type aliveProcs struct{ platform.Procs }

func (aliveProcs) Alive(int, string) bool { return true }

func (r *rig) warnings(name string) []string {
	var out []string
	for _, c := range Run(r.env) {
		if c.Name == name && c.Status == Warn {
			out = append(out, c.Detail)
		}
	}
	return out
}

func TestWarnsOnASessionThatStartedBeforeThePluginLoaded(t *testing.T) {
	r := newRig(t)
	id := "abcdef12-0000-0000-0000-000000000001"
	dir := r.env.Paths.Session(id)
	os.MkdirAll(dir, 0o755)
	os.WriteFile(dir+"/statusline.json", []byte(`{"ts":"`+now.Add(-time.Minute).Format(time.RFC3339Nano)+`","session_id":"`+id+`"}`), 0o644)
	if w := r.warnings("sessions-seen"); len(w) != 1 || w[0] != "session abcdef12 started before the plugin loaded; restart it" {
		t.Fatalf("warnings = %v", w)
	}
	// Once its hooks have fired there is nothing to say; an old snapshot is not "fresh".
	os.WriteFile(dir+"/events.jsonl", []byte("{}\n"), 0o644)
	if w := r.warnings("sessions-seen"); len(w) != 0 {
		t.Errorf("warned about a session with events: %v", w)
	}
	os.Remove(dir + "/events.jsonl")
	os.WriteFile(dir+"/statusline.json", []byte(`{"ts":"`+now.Add(-2*time.Hour).Format(time.RFC3339Nano)+`"}`), 0o644)
	if w := r.warnings("sessions-seen"); len(w) != 0 {
		t.Errorf("warned about a stale snapshot: %v", w)
	}
}

func TestWarnsOnAClaudeProcessOutsideTmux(t *testing.T) {
	r := newRig(t)
	r.env.Procs = aliveProcs{}
	cache := claude.AgentsCache{V: 1, FetchedAt: now.Add(-time.Second), Rows: []claude.Agent{
		{SessionID: "11111111-0000-0000-0000-000000000001", PID: 4242, Kind: "interactive"},
		{SessionID: "22222222-0000-0000-0000-000000000002", PID: 4343, Kind: "interactive"},
		{SessionID: "33333333-0000-0000-0000-000000000003", PID: 4444, Kind: "background"},
	}}
	b, _ := json.Marshal(cache)
	os.MkdirAll(r.env.Paths.Cache, 0o755)
	os.WriteFile(r.env.Paths.AgentsCache(), b, 0o644)
	// 4242: hooks fired but no TMUX_PANE; 4343: in tmux but bosun never heard from it.
	for _, id := range []string{"11111111-0000-0000-0000-000000000001"} {
		os.MkdirAll(r.env.Paths.Session(id), 0o755)
		os.WriteFile(r.env.Paths.Session(id)+"/events.jsonl", []byte("{}\n"), 0o644)
	}
	r.env.ProcEnv = func(pid int) (map[string]string, error) {
		if pid == 4242 {
			return map[string]string{}, nil
		}
		return map[string]string{"TMUX_PANE": "%3"}, nil
	}
	w := r.warnings("sessions-seen")
	want := []string{"claude pid 4242 runs outside tmux; bosun can't show or jump to it", "claude pid 4343 runs in tmux but bosun has never heard from it; check that the plugin is installed and its hooks fire for it"}
	if len(w) != 2 || w[0] != want[0] || w[1] != want[1] {
		t.Fatalf("warnings = %v", w)
	}
	// An unreadable environment says nothing, and a warning never fails doctor.
	r.env.ProcEnv = func(int) (map[string]string, error) { return nil, errors.New("permission denied") }
	if w := r.warnings("sessions-seen"); len(w) != 0 {
		t.Errorf("warned without being able to tell: %v", w)
	}
	if Failed(Run(r.env)) {
		t.Error("these are warnings, not failures")
	}
}

// An empty tmux.spend_key means the spend key is never checked, so the OK line
// must not claim it is bound.
func TestTmuxKeysMessageNamesWhatWasChecked(t *testing.T) {
	r := newRig(t)
	r.env.Config.Tmux.SpendKey = "$"
	if c := r.checks()["tmux-keys"]; c.Status != OK || !strings.Contains(c.Detail, "spend") {
		t.Errorf("spend key set: %+v", c)
	}
	r = newRig(t)
	r.env.Config.Tmux.SpendKey = ""
	c := r.checks()["tmux-keys"]
	if c.Status != OK || !strings.Contains(c.Detail, "spend key was not checked") {
		t.Errorf("spend key empty: %+v", c)
	}
}

func TestContextFieldCheck(t *testing.T) {
	write := func(r *rig, id, version string, ctx *float64, age time.Duration) {
		os.MkdirAll(r.env.Paths.Session(id), 0o755)
		snap := map[string]any{"v": 1, "session_id": id, "version": version, "ts": now.Format(time.RFC3339Nano)}
		if ctx != nil {
			snap["ctx_pct"] = *ctx
		}
		b, _ := json.Marshal(snap)
		p := r.env.Paths.Session(id) + "/statusline.json"
		os.WriteFile(p, b, 0o644)
		os.Chtimes(p, now.Add(-age), now.Add(-age))
	}
	pct := 41.0
	const a, b = "aaaaaaaa-0000-0000-0000-00000000000a", "bbbbbbbb-0000-0000-0000-00000000000b"

	r := newRig(t)
	if c := r.checks()["context-field"]; c.Status != OK || c.Detail != "no status line data yet" {
		t.Errorf("no snapshots: %+v", c)
	}

	r = newRig(t)
	write(r, a, "2.1.309", &pct, time.Minute)
	write(r, b, "2.1.309", nil, 2*time.Minute)
	if c := r.checks()["context-field"]; c.Status != OK || c.Detail != "context fill seen (Claude Code 2.1.309)" {
		t.Errorf("one snapshot has it: %+v", c)
	}

	r = newRig(t)
	write(r, a, "2.1.309", nil, time.Minute)
	write(r, b, "2.2.0", nil, 2*time.Minute)
	if c := r.checks()["context-field"]; c.Status != Warn || c.Detail != "Claude Code 2.1.309 sends no context_window; handoff suggestions are off" {
		t.Errorf("none has it: %+v", c)
	}

	r = newRig(t)
	write(r, a, "2.1.100", nil, time.Minute)
	if c := r.checks()["context-field"]; c.Status != OK {
		t.Errorf("a version before the field existed should not warn: %+v", c)
	}
}

func TestHandoffsCheck(t *testing.T) {
	logs := func(r *rig, outcomes ...string) {
		var b strings.Builder
		for _, o := range outcomes {
			if o == "ok" {
				b.WriteString(`{"ts":"2026-10-31T12:00:00Z","session":"s","outcome":"ok","phase_reached":"launching"}` + "\n")
			} else {
				b.WriteString(`{"ts":"2026-10-31T12:00:00Z","session":"s","outcome":"failed","why":"` + o + `","phase_reached":"writing"}` + "\n")
			}
		}
		os.MkdirAll(r.env.Paths.State, 0o755)
		os.WriteFile(r.env.Paths.HandoffLog(), []byte(b.String()), 0o644)
	}
	r := newRig(t)
	if _, ok := r.checks()["handoff"]; ok {
		t.Error("a check appeared with no log")
	}

	r = newRig(t)
	logs(r, "ok", "ok", "ok", "deadline", "ok", "ok", "not-submitted", "ok", "ok", "deadline")
	if c := r.checks()["handoff"]; c.Status != OK || c.Detail != "7 ok, 2 deadline, 1 not-submitted (last 10)" {
		t.Errorf("mixed: %+v", c)
	}

	r = newRig(t)
	logs(r, "ok", "ok", "ok", "ok", "ok", "ok", "deadline", "ok", "deadline", "deadline")
	if c := r.checks()["handoff"]; c.Status != Warn || !strings.Contains(c.Detail, "3 of the last 5 failed with deadline") {
		t.Errorf("repeat failure: %+v", c)
	}

	r = newRig(t)
	logs(r, "ok", "ok", "ok", "ok", "ok", "deadline", "not-submitted", "deadline", "pane-gone", "ok")
	if c := r.checks()["handoff"]; c.Status != OK {
		t.Errorf("three failures with different reasons should not warn: %+v", c)
	}

	// Only the last 10 lines count.
	r = newRig(t)
	logs(r, "deadline", "deadline", "deadline", "ok", "ok", "ok", "ok", "ok", "ok", "ok", "ok", "ok", "ok")
	if c := r.checks()["handoff"]; c.Detail != "10 ok (last 10)" {
		t.Errorf("window: %+v", c)
	}
}
