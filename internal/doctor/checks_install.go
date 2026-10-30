package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jmvaldez/bosun/internal/live"
	"github.com/jmvaldez/bosun/internal/paths"
	"github.com/jmvaldez/bosun/internal/plugstate"
	"github.com/jmvaldez/bosun/internal/statusline"
	"github.com/jmvaldez/bosun/internal/tmuxconf"
)

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

// ---- config

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
