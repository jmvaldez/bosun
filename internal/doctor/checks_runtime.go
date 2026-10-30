package doctor

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jmvaldez/bosun/internal/claude"
	"github.com/jmvaldez/bosun/internal/heartbeat"
	"github.com/jmvaldez/bosun/internal/live"
	"github.com/jmvaldez/bosun/internal/notify"
	"github.com/jmvaldez/bosun/internal/paths"
	"github.com/jmvaldez/bosun/internal/statusline"
)

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
