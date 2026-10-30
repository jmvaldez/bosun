// Package doctor is `bosun doctor`: a read-only self-check. It stats, reads and
// asks other programs questions; it never creates, writes or deletes a file
// (LESSONS L-44), which a test proves with a tree listing before and after.
package doctor

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jmvaldez/bosun/internal/claude"
	"github.com/jmvaldez/bosun/internal/config"
	"github.com/jmvaldez/bosun/internal/heartbeat"
	"github.com/jmvaldez/bosun/internal/hook"
	"github.com/jmvaldez/bosun/internal/ledger"
	"github.com/jmvaldez/bosun/internal/live"
	"github.com/jmvaldez/bosun/internal/notify"
	"github.com/jmvaldez/bosun/internal/paths"
	"github.com/jmvaldez/bosun/internal/platform"
	"github.com/jmvaldez/bosun/internal/plugstate"
	"github.com/jmvaldez/bosun/internal/runner"
	"github.com/jmvaldez/bosun/internal/statusline"
	"github.com/jmvaldez/bosun/internal/tmuxconf"
)

type Status string

const (
	OK   Status = "ok"
	Warn Status = "warn"
	Fail Status = "fail"
)

// Check is one line of the report.
type Check struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail"`
}

// Env is everything doctor looks at, injected so tests need no real machine.
type Env struct {
	Paths   paths.Paths
	Version string // the running binary's version
	Kind    platform.Kind
	Now     time.Time
	Getenv  func(string) string
	Runner  runner.Runner
	Procs   platform.Procs

	// Executable is the running bosun's path.
	Executable string
	LookPath   func(string) (string, error)
	// TmuxBin is the tmux binary ("" if none was found).
	TmuxBin string
	// TmuxSocket is the server socket to ask (from $TMUX); "" means the default server.
	TmuxSocket string
	// ManagedSettings are the managed-policy files to read (all read-only).
	ManagedSettings []string
	// ClaudeBin is the resolved claude binary ("" if none).
	ClaudeBin string
	// ProcEnv reads a process's environment; only TMUX_PANE is consulted. nil
	// means readProcEnv. An error skips that process.
	ProcEnv func(pid int) (map[string]string, error)
	// Config result, loaded by the caller.
	Config    config.Config
	ConfigRes config.Result
	ConfigErr error
}

const (
	hookLatencyBudget = 50 * time.Millisecond
	snapshotFresh     = 15 * time.Minute
	ingestMaxAge      = 20 * 24 * time.Hour
	cmdTimeout        = 2 * time.Second
	knownBinDirs      = "~/.local/bin, /usr/bin, /usr/local/bin, /opt/homebrew/bin or a Homebrew prefix"
	heartbeatStale    = 24 * time.Hour
)

func Run(e Env) []Check {
	var cs []Check
	add := func(name string, st Status, format string, a ...any) {
		cs = append(cs, Check{Name: name, Status: st, Detail: fmt.Sprintf(format, a...)})
	}
	settings := readSettings(e.Paths.ClaudeSettings())

	e.checkBinary(add)
	e.checkPlugin(add, settings)
	e.checkSettingsHooks(add, settings)
	hb := e.checkHeartbeat(add)
	e.checkVersionSkew(add, hb)
	e.checkStatusLine(add, settings)
	e.checkSessionsSeen(add)
	e.checkTmux(add)
	e.checkConfig(add)
	e.checkIdentity(add)
	e.checkState(add)
	e.checkNotifier(add)
	if err := hook.SelfTest(); err != nil {
		add("prompt-filter", Fail, "%v", err)
	} else {
		add("prompt-filter", OK, "a sentinel in every text field of all 13 events never reaches an event line")
	}
	if e.Kind == platform.WSL {
		e.checkWSL(add)
	}
	return cs
}

func Failed(cs []Check) bool {
	for _, c := range cs {
		if c.Status == Fail {
			return true
		}
	}
	return false
}

func Text(w io.Writer, cs []Check) {
	width := 0
	for _, c := range cs {
		width = max(width, len(c.Name))
	}
	for _, c := range cs {
		fmt.Fprintf(w, "%-5s %-*s  %s\n", strings.ToUpper(string(c.Status)), width, c.Name, c.Detail)
	}
	var f, wn int
	for _, c := range cs {
		switch c.Status {
		case Fail:
			f++
		case Warn:
			wn++
		}
	}
	fmt.Fprintf(w, "\n%d checks, %d failed, %d warnings\n", len(cs), f, wn)
}

func JSON(w io.Writer, cs []Check) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(cs)
}

// ---- settings

type claudeSettings struct {
	ok               bool
	enabledPlugins   json.RawMessage
	hooks            json.RawMessage
	statusLineCmd    string
	disableAllHooks  bool
	allowManagedOnly bool
}

func readSettings(path string) claudeSettings {
	var s claudeSettings
	if path == "" {
		return s
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	var raw struct {
		EnabledPlugins   json.RawMessage `json:"enabledPlugins"`
		Hooks            json.RawMessage `json:"hooks"`
		DisableAllHooks  bool            `json:"disableAllHooks"`
		AllowManagedOnly bool            `json:"allowManagedHooksOnly"`
		StatusLine       struct {
			Command string `json:"command"`
		} `json:"statusLine"`
	}
	if json.Unmarshal(b, &raw) != nil {
		return s
	}
	return claudeSettings{ok: true, enabledPlugins: raw.EnabledPlugins, hooks: raw.Hooks, statusLineCmd: raw.StatusLine.Command,
		disableAllHooks: raw.DisableAllHooks, allowManagedOnly: raw.AllowManagedOnly}
}

// enabledBosun counts enabled bosun plugin entries. enabledPlugins is an object
// ({"bosun@bosun": true}) in some settings and an array in the docs; both are
// accepted (LESSONS L-45).
func enabledBosun(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	n := 0
	var obj map[string]bool
	if json.Unmarshal(raw, &obj) == nil {
		for k, on := range obj {
			if on && strings.HasPrefix(k, "bosun@") {
				n++
			}
		}
		return n
	}
	var arr []string
	if json.Unmarshal(raw, &arr) == nil {
		for _, k := range arr {
			if strings.HasPrefix(k, "bosun@") {
				n++
			}
		}
	}
	return n
}

type addFn func(name string, st Status, format string, a ...any)

func (e Env) checkPlugin(add addFn, s claudeSettings) {
	if !s.ok {
		add("plugin", Warn, "could not read %s, so the plugin was not verified", e.Paths.ClaudeSettings())
		return
	}
	switch n := enabledBosun(s.enabledPlugins); {
	case n == 1:
		add("plugin", OK, "bosun@bosun is enabled exactly once")
	case n == 0:
		if plugstate.Installed(filepath.Dir(e.Paths.ClaudeSettings()), "bosun@bosun") {
			add("plugin", Fail, "the bosun plugin is installed but disabled (claude plugin enable bosun@bosun)")
		} else {
			add("plugin", Fail, "the bosun plugin is not enabled (run bosun setup)")
		}
	default:
		add("plugin", Fail, "the bosun plugin is enabled %d times", n)
	}
}

func (e Env) checkSettingsHooks(add addFn, s claudeSettings) {
	bad := false
	if s.ok && strings.Contains(string(s.hooks), "bosun hook") {
		add("settings-hooks", Fail, "settings.json also registers bosun hooks; with the plugin every event would fire twice")
		bad = true
	}
	if s.disableAllHooks {
		add("settings-hooks", Fail, "settings.json sets disableAllHooks: no hook, so no bosun data")
		bad = true
	}
	for _, p := range e.ManagedSettings {
		m := readSettings(p)
		if m.ok && (m.allowManagedOnly || m.disableAllHooks) {
			add("managed-policy", Fail, "%s sets allowManagedHooksOnly or disableAllHooks: plugin hooks cannot run", p)
			bad = true
		}
	}
	if !bad {
		add("settings-hooks", OK, "no bosun hooks in settings, and no managed policy blocking plugin hooks")
	}
}

func (e Env) checkStatusLine(add addFn, s claudeSettings) {
	switch {
	case !s.ok:
		add("statusline", Warn, "could not read %s, so the status line was not verified", e.Paths.ClaudeSettings())
	case strings.Contains(s.statusLineCmd, "bosun statusline"):
		add("statusline", OK, "statusLine.command is wrapped with bosun statusline (live cost on)")
	default:
		// Live cost is opt-in (D-31): an unwrapped status line is a choice, and
		// there are no snapshots to expect.
		add("statusline", OK, "statusLine is not wrapped: live cost is off (bosun setup turns it on)")
		add("snapshots", OK, "live cost is off, so no status line snapshots are expected")
		return
	}
	// Every live session should have a recent snapshot.
	f := live.Read(e.Paths.Live())
	var stale []string
	checked := 0
	for id, sess := range f.Sessions {
		// An unknown entrypoint may be a non-interactive run with no status line (D-10).
		if sess.Ended || !sess.Interactive(false) {
			continue
		}
		checked++
		snap, ok := statusline.ReadSnapshot(e.Paths.StatuslineSnap(id))
		if !ok {
			stale = append(stale, paths.ShortID(id)+" (none)")
			continue
		}
		if t, err := time.Parse(time.RFC3339Nano, snap.TS); err != nil || e.Now.Sub(t) > snapshotFresh {
			stale = append(stale, paths.ShortID(id)+" (old)")
		}
	}
	sort.Strings(stale)
	switch {
	case checked == 0:
		add("snapshots", OK, "no live sessions to check")
	case len(stale) > 0:
		add("snapshots", Warn, "no fresh status line snapshot for: %s", strings.Join(stale, ", "))
	default:
		add("snapshots", OK, "every live session has a fresh status line snapshot")
	}
}

// ---- binary

func (e Env) checkBinary(add addFn) {
	found, err := e.LookPath("bosun")
	if err != nil {
		add("binary", Fail, "bosun is not on this process's PATH (only PATH was searched; install it into a PATH directory such as %s)", knownBinDirs)
		return
	}
	// A non-login shell, like the one a tmux run-shell or a hook's parent uses.
	out, err := e.Runner.Run(cmdTimeout, "sh", "-c", "command -v bosun")
	shPath := strings.TrimSpace(string(out))
	if err != nil || shPath == "" {
		add("binary", Fail, "found %s here but `sh -c 'command -v bosun'` does not resolve it: hooks will not find bosun", found)
		return
	}
	detail := "bosun resolves at " + shPath
	if st, msg := e.tmuxPathHas("bosun"); st != OK {
		add("binary", st, "%s; %s", detail, msg)
		return
	}
	add("binary", OK, "%s (non-login sh and tmux's global PATH agree)", detail)
}

// tmuxPathHas checks PATH in tmux's global environment, not this process's.
// It returns Warn when there is no tmux or no server to ask; the message is
// empty on OK.
func (e Env) tmuxPathHas(name string) (Status, string) {
	if e.TmuxBin == "" {
		return Warn, "tmux not found, so tmux's PATH was not checked"
	}
	out, err := e.tmux("show-environment", "-g", "PATH")
	if err != nil {
		return Warn, "no tmux server running, so tmux's global PATH was not checked"
	}
	line := strings.TrimSpace(string(out))
	_, pathVal, _ := strings.Cut(line, "=")
	for _, dir := range filepath.SplitList(pathVal) {
		if fi, err := os.Stat(filepath.Join(dir, name)); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return OK, ""
		}
	}
	return Fail, "tmux's global PATH has no " + name + ": run-shell bindings will fail"
}

func (e Env) tmux(args ...string) ([]byte, error) {
	full := args
	if e.TmuxSocket != "" {
		full = append([]string{"-S", e.TmuxSocket}, args...)
	}
	return e.Runner.Run(cmdTimeout, e.TmuxBin, full...)
}

// ---- heartbeat and version

func (e Env) checkHeartbeat(add addFn) heartbeat.Heartbeat {
	hb, err := heartbeat.Read(e.Paths.Heartbeat())
	if err != nil || hb.Last.IsZero() {
		add("heartbeat", Fail, "no hook has run yet (no heartbeat.json): is the plugin enabled and bosun on the hooks' PATH?")
		return heartbeat.Heartbeat{}
	}
	p50, p95, n := hb.Quantiles()
	age := e.Now.Sub(hb.Last).Round(time.Second)
	switch {
	case p95 >= hookLatencyBudget || p50 >= hookLatencyBudget:
		add("heartbeat", Fail, "hook latency over budget: p50 %v p95 %v over %d samples (limit %v)", p50, p95, n, hookLatencyBudget)
	case age > heartbeatStale:
		add("heartbeat", Warn, "last hook ran %v ago; p50 %v p95 %v over %d samples", age, p50, p95, n)
	default:
		add("heartbeat", OK, "last hook %v ago; p50 %v p95 %v over %d samples", age, p50, p95, n)
	}
	return hb
}

var verRE = regexp.MustCompile(`^v?(\d+)\.(\d+)`)

func majorMinor(v string) (int, int, bool) {
	m := verRE.FindStringSubmatch(v)
	if m == nil {
		return 0, 0, false
	}
	a, _ := strconv.Atoi(m[1])
	b, _ := strconv.Atoi(m[2])
	return a, b, true
}

func (e Env) checkVersionSkew(add addFn, hb heartbeat.Heartbeat) {
	if hb.PluginVersion == "" {
		add("version", Warn, "plugin version unknown (no heartbeat yet); binary is %s", e.Version)
		return
	}
	pm, pn, ok1 := majorMinor(hb.PluginVersion)
	bm, bn, ok2 := majorMinor(e.Version)
	switch {
	case !ok1 || !ok2:
		add("version", Warn, "cannot compare plugin %q with binary %q", hb.PluginVersion, e.Version)
	case pm != bm:
		add("version", Fail, "major version skew: plugin %s, binary %s", hb.PluginVersion, e.Version)
	case pn != bn:
		add("version", Warn, "minor version skew: plugin %s, binary %s", hb.PluginVersion, e.Version)
	default:
		add("version", OK, "plugin %s and binary %s agree", hb.PluginVersion, e.Version)
	}
}

// ---- tmux

var tmuxVerRE = regexp.MustCompile(`(\d+)\.(\d+)`)

func (e Env) checkTmux(add addFn) {
	if e.TmuxBin == "" {
		add("tmux", Fail, "tmux not found")
		return
	}
	out, err := e.Runner.Run(cmdTimeout, e.TmuxBin, "-V")
	if err != nil {
		add("tmux", Fail, "tmux -V failed: %v", err)
		return
	}
	m := tmuxVerRE.FindStringSubmatch(string(out))
	if m == nil {
		add("tmux", Warn, "cannot read the tmux version from %q", strings.TrimSpace(string(out)))
		return
	}
	maj, _ := strconv.Atoi(m[1])
	min, _ := strconv.Atoi(m[2])
	ver := strings.TrimSpace(string(out))
	switch {
	case maj < 3 || maj == 3 && min < 2:
		add("tmux", Fail, "%s is below the supported floor of 3.2 (no display-popup)", ver)
		return
	case maj == 3 && min == 2:
		add("tmux", Warn, "%s: popup yes, popup title no (needs 3.3)", ver)
	default:
		add("tmux", OK, "%s: popup and popup title supported", ver)
	}

	// The generated conf's header must match this binary.
	conf := e.Paths.TmuxConf()
	if b, err := os.ReadFile(conf); err != nil {
		add("tmux-conf", Warn, "%s is missing: run bosun setup", conf)
	} else if first, _, _ := strings.Cut(string(b), "\n"); first != tmuxconf.Header(e.Version) {
		add("tmux-conf", Warn, "%s has header %q, this binary is %s: run bosun setup", conf, first, e.Version)
	} else {
		add("tmux-conf", OK, "%s is current (%s)", conf, e.Version)
	}

	if _, err := e.tmux("has-session"); err != nil {
		add("tmux-server", Warn, "no tmux server running: segment, keys and status-interval were not checked")
		return
	}
	get := func(args ...string) string {
		o, err := e.tmux(args...)
		if err != nil {
			return ""
		}
		return string(o)
	}
	right := get("show", "-gv", "status-right")
	segs := strings.Count(right, "bosun status")
	switch {
	case segs == 1:
		add("tmux-segment", OK, "the bosun segment is in status-right exactly once")
	case segs == 0:
		add("tmux-segment", Fail, "the bosun segment is not in status-right: run bosun setup (or start a Claude session in tmux)")
	default:
		add("tmux-segment", Fail, "the bosun segment appears %d times in status-right", segs)
	}
	for _, opt := range []string{"window-status-format", "window-status-current-format"} {
		if n := strings.Count(get("show", "-gwv", opt), "@bosun-win-mark"); n != 1 {
			add("tmux-"+opt, Fail, "%s has the bosun marker %d times (want 1)", opt, n)
		}
	}
	keys := get("list-keys", "-T", "prefix")
	var missing []string
	if !strings.Contains(keys, "bosun") || !strings.Contains(keys, " ui") {
		missing = append(missing, "popup")
	}
	if !strings.Contains(keys, "jump --queue") {
		missing = append(missing, "jump")
	}
	if e.Config.Tmux.SpendKey != "" && !strings.Contains(keys, "ui --spend") {
		missing = append(missing, "spend")
	}
	if len(missing) > 0 {
		add("tmux-keys", Fail, "not bound: %s (run bosun install, then re-source your tmux.conf)", strings.Join(missing, ", "))
	} else {
		if e.Config.Tmux.SpendKey != "" {
			add("tmux-keys", OK, "popup, spend and jump keys are bound")
		} else {
			add("tmux-keys", OK, "popup and jump keys are bound (tmux.spend_key is empty, so the spend key was not checked)")
		}
	}
	if iv, err := strconv.Atoi(strings.TrimSpace(get("show", "-gv", "status-interval"))); err != nil || iv > 5 || iv < 1 {
		add("tmux-interval", Warn, "status-interval is %q; at most 5 keeps the bar current", strings.TrimSpace(get("show", "-gv", "status-interval")))
	} else {
		add("tmux-interval", OK, "status-interval is %d", iv)
	}
	if strings.TrimSpace(get("show", "-gv", "allow-passthrough")) == "on" {
		add("tmux-passthrough", Warn, "allow-passthrough is on: a notifier plus passthrough can duplicate notices")
	}
}

// ---- config, identity, state, notifier, wsl, unseen sessions

func (e Env) checkConfig(add addFn) {
	switch {
	case e.ConfigErr != nil:
		add("config", Fail, "%s does not parse: %v", e.Paths.ConfigFile(), e.ConfigErr)
	case len(e.ConfigRes.Problems)+len(e.ConfigRes.Unknown) > 0:
		var parts []string
		for _, u := range e.ConfigRes.Unknown {
			parts = append(parts, "unknown key "+u)
		}
		parts = append(parts, e.ConfigRes.Problems...)
		add("config", Warn, "%s", strings.Join(parts, "; "))
	default:
		add("config", OK, "config parses (every key known and valid)")
	}
}

func (e Env) checkIdentity(add addFn) {
	if e.ClaudeBin == "" {
		add("claude", Fail, "claude does not resolve on PATH (set claude.bin)")
		return
	}
	add("claude", OK, "claude resolves at %s", e.ClaudeBin)
	id, ok := claude.ReadIdentity(e.Paths.Identity())
	if !ok {
		add("identity", Warn, "no cached account key (identity.json): run bosun install or bosun refresh")
		return
	}
	out, err := e.Runner.Run(5*time.Second, e.ClaudeBin, "auth", "status", "--json")
	if err != nil {
		add("identity", Warn, "cached account key found, but claude auth status failed: %v", err)
		return
	}
	var st struct {
		Email string `json:"email"`
	}
	_ = json.Unmarshal(out, &st)
	if cur := strings.ToLower(strings.TrimSpace(st.Email)); cur != "" && cur != id.AccountKey {
		add("identity", Fail, "the cached account key is for a different login than claude reports now: run bosun refresh")
		return
	}
	add("identity", OK, "cached account key matches the current login (cached %v ago)", e.Now.Sub(id.FetchedAt).Round(time.Minute))
	if c, _ := claude.ReadAgents(e.Paths.AgentsCache(), e.Now, claude.AgentsTTL); !c.FetchedAt.IsZero() {
		add("agents-cache", OK, "agents.json is %v old", e.Now.Sub(c.FetchedAt).Round(time.Second))
	}
}

func (e Env) checkState(add addFn) {
	dir := e.Paths.State
	if dir == "" {
		add("state-dir", Fail, "no state dir could be resolved (HOME unset?)")
		return
	}
	fi, err := os.Stat(dir)
	switch {
	case os.IsNotExist(err):
		add("state-dir", Warn, "%s does not exist yet; the first hook creates it", dir)
		return
	case err != nil || !fi.IsDir():
		add("state-dir", Fail, "%s is not a usable directory: %v", dir, err)
		return
	}
	if err := accessWritable(dir); err != nil {
		add("state-dir", Fail, "%s is not writable: %v", dir, err)
	} else {
		add("state-dir", OK, "%s exists and is writable", dir)
	}
	switch t, ok := ledger.ReadIngestMarker(e.Paths.IngestMarker()); {
	case !ok:
		add("ingest", Warn, "no ingest has completed yet (the refresher runs one within minutes of use)")
	case e.Now.Sub(t) > ingestMaxAge:
		add("ingest", Fail, "the last ingest was %v ago (over 20 days); Claude Code prunes transcripts at 30", e.Now.Sub(t).Round(time.Hour))
	default:
		add("ingest", OK, "last ingest %v ago", e.Now.Sub(t).Round(time.Minute))
	}
}

func (e Env) checkNotifier(add addFn) {
	n, why := notify.Resolve(e.Config.Notify.Backend, e.Kind, e.LookPath, e.Runner)
	switch {
	case e.Config.Notify.Backend == "off":
		add("notifier", OK, "notifications are off by config")
	case n == nil:
		add("notifier", Warn, "no notifier: %s", why)
	default:
		if n.Name() == "gdbus" {
			// Ask the server what it supports: proves the session bus answers.
			if _, err := e.Runner.Run(cmdTimeout, "gdbus", "call", "--session", "--dest", "org.freedesktop.Notifications",
				"--object-path", "/org/freedesktop/Notifications", "--method", "org.freedesktop.Notifications.GetCapabilities"); err != nil {
				add("notifier", Warn, "gdbus found but the notification server did not answer: %v", err)
				return
			}
		}
		add("notifier", OK, "%s is available (replace=%v retract=%v)", n.Name(), n.Capabilities().Replace, n.Capabilities().Retract)
	}
}

func (e Env) checkWSL(add addFn) {
	if p := e.Getenv("WSL_INTEROP"); p != "" {
		if _, err := os.Stat(p); err != nil {
			add("wsl-interop", Fail, "WSL_INTEROP points at %s, which no longer exists (a stale tmux server env)", p)
		}
	}
	if strings.HasPrefix(e.Paths.State, "/mnt/") {
		add("wsl-state", Fail, "the state dir %s is on a Windows mount: flock and rename are unreliable there", e.Paths.State)
	}
}

// checkSessionsSeen warns about Claude Code sessions bosun cannot see: one whose
// status line is reporting but whose hooks never fired (it started before the
// plugin loaded), and one running outside tmux.
func (e Env) checkSessionsSeen(add addFn) {
	var notes []string
	ents, _ := os.ReadDir(e.Paths.Sessions())
	for _, de := range ents {
		id := de.Name()
		if !de.IsDir() || !paths.ValidSessionID(id) {
			continue
		}
		snap, ok := statusline.ReadSnapshot(e.Paths.StatuslineSnap(id))
		if !ok {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, snap.TS)
		if err != nil || e.Now.Sub(t) > snapshotFresh {
			continue
		}
		if _, err := os.Stat(e.Paths.Events(id)); err != nil && errors.Is(err, fs.ErrNotExist) {
			notes = append(notes, "session "+paths.ShortID(id)+" started before the plugin loaded; restart it")
		}
	}
	lf := live.Read(e.Paths.Live())
	known := map[int]bool{}
	for _, s := range lf.Sessions {
		if s.PID > 0 {
			known[s.PID] = true
		}
	}
	cache, _ := claude.ReadAgents(e.Paths.AgentsCache(), e.Now, claude.AgentsTTL)
	procEnv := e.ProcEnv
	if procEnv == nil {
		procEnv = readProcEnv
	}
	for _, row := range cache.Rows {
		if row.Kind != "interactive" || row.PID <= 0 || e.Procs == nil || !e.Procs.Alive(row.PID, row.StartTok) {
			continue
		}
		env, err := procEnv(row.PID)
		if err != nil {
			continue // cannot tell: say nothing
		}
		if env["TMUX_PANE"] == "" {
			notes = append(notes, fmt.Sprintf("claude pid %d runs outside tmux; bosun can't show or jump to it", row.PID))
			continue
		}
		// In tmux but never heard from: not "outside tmux", so the fix is
		// the plugin or hooks, not where the session was started.
		heard := known[row.PID]
		if _, err := os.Stat(e.Paths.Events(row.SessionID)); err == nil {
			heard = true
		}
		if !heard {
			notes = append(notes, fmt.Sprintf("claude pid %d runs in tmux but bosun has never heard from it; check that the plugin is installed and its hooks fire for it", row.PID))
		}
	}
	sort.Strings(notes)
	if len(notes) == 0 {
		add("sessions-seen", OK, "every running session is visible to bosun")
		return
	}
	for _, n := range notes {
		add("sessions-seen", Warn, "%s", n)
	}
}

// readProcEnv is the Linux default for Env.ProcEnv. It gives up after a second
// so a stuck /proc read cannot hold doctor.
func readProcEnv(pid int) (map[string]string, error) {
	type res struct {
		m   map[string]string
		err error
	}
	ch := make(chan res, 1)
	go func() {
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
		if err != nil {
			ch <- res{nil, err}
			return
		}
		m := map[string]string{}
		for _, kv := range strings.Split(string(b), "\x00") {
			if k, v, ok := strings.Cut(kv, "="); ok && (k == "TMUX" || k == "TMUX_PANE") {
				m[k] = v
			}
		}
		ch <- res{m, nil}
	}()
	select {
	case r := <-ch:
		return r.m, r.err
	case <-time.After(time.Second):
		return nil, errors.New("timed out reading the process environment")
	}
}
