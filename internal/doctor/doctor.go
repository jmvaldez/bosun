// Package doctor is `bosun doctor`: a read-only self-check. It stats, reads and
// asks other programs questions; it never creates, writes or deletes a file
// (LESSONS L-44), which a test proves with a tree listing before and after.
package doctor

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jmvaldez/bosun/internal/config"
	"github.com/jmvaldez/bosun/internal/hook"
	"github.com/jmvaldez/bosun/internal/paths"
	"github.com/jmvaldez/bosun/internal/platform"
	"github.com/jmvaldez/bosun/internal/runner"
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

	// Each check lives in the file for its area: checks_install.go (binary,
	// plugin, settings, status line, tmux, config), checks_runtime.go
	// (heartbeat, version, sessions seen, notifier, WSL) and checks_spend.go
	// (identity, state dir, ingest).
	e.checkBinary(add)
	e.checkPlugin(add, settings)
	e.checkSettingsHooks(add, settings)
	hb := e.checkHeartbeat(add)
	e.checkVersionSkew(add, hb)
	e.checkStatusLine(add, settings)
	e.checkSessionsSeen(add)
	e.checkContextField(add)
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

type addFn func(name string, st Status, format string, a ...any)
