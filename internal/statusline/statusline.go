// Package statusline implements `bosun statusline -- CMD...`: record the live
// session total, then run the user's real status line command with the same
// stdin and let its output through untouched.
package statusline

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"time"

	"github.com/jmvaldez/bosun/internal/atomicfile"
	"github.com/jmvaldez/bosun/internal/flock"
	"github.com/jmvaldez/bosun/internal/paths"
	"github.com/jmvaldez/bosun/internal/platform"
	"github.com/jmvaldez/bosun/internal/stdin"
)

const (
	maxStdin      = 4 << 20
	stdinDeadline = 500 * time.Millisecond
	// wrappedTimeout bounds the user's command. Claude Code redraws on a
	// debounce and abandons slow runs itself; this only stops a leak.
	wrappedTimeout = 5 * time.Second
	lockWait       = 10 * time.Millisecond
)

// Snapshot is sessions/<id>/statusline.json.
type Snapshot struct {
	V         int     `json:"v"`
	TS        string  `json:"ts"`
	SessionID string  `json:"session_id"`
	CostUSD   float64 `json:"cost_usd"`
	BankedUSD float64 `json:"banked_usd"`
	MaxUSD    float64 `json:"max_usd"`
	// FirstUSD and FirstTS are the first total bosun saw for the session. For a
	// fork that is the parent's total at fork time, before the fork spends.
	FirstUSD   float64 `json:"first_usd,omitempty"`
	FirstTS    string  `json:"first_ts,omitempty"`
	Model      string  `json:"model,omitempty"`
	Version    string  `json:"version,omitempty"`
	DurationMS int64   `json:"duration_ms,omitempty"`
	// CtxPct is the context fill (0-100) the status line reported. A pointer so
	// that absent stays absent and never reads as 0.
	CtxPct  *float64 `json:"ctx_pct,omitempty"`
	CtxSize int64    `json:"ctx_size,omitempty"`
}

// stdinView lists the only status line keys bosun decodes. Everything else,
// including session_name (derived from prompt text), is dropped by omission.
type stdinView struct {
	SessionID string `json:"session_id"`
	Version   string `json:"version"`
	Model     struct {
		ID string `json:"id"`
	} `json:"model"`
	Cost struct {
		Total      *float64 `json:"total_cost_usd"`
		DurationMS int64    `json:"total_duration_ms"`
	} `json:"cost"`
	ContextWindow struct {
		UsedPct *float64 `json:"used_percentage"`
		Size    int64    `json:"context_window_size"`
	} `json:"context_window"`
}

// Run records the snapshot and runs argv (the words after "--"), returning the
// process exit code, always 0. The wrapped command runs even if recording
// fails or panics: the user's status line must never go blank because of bosun.
func Run(ad platform.Adapters, args []string, in io.Reader, stdout, stderr io.Writer) int {
	raw := stdin.Read(in, maxStdin, stdinDeadline)
	record(ad, raw)
	if argv := command(args); len(argv) > 0 {
		passthrough(argv, raw, stdout, stderr)
	}
	return 0
}

// command returns the words after the first "--"; with no "--" it treats all
// args as the command.
func command(args []string) []string {
	for i, a := range args {
		if a == "--" {
			return args[i+1:]
		}
	}
	return args
}

func record(ad platform.Adapters, raw []byte) {
	defer func() { _ = recover() }()
	var v stdinView
	if json.Unmarshal(raw, &v) != nil || v.Cost.Total == nil || !paths.ValidSessionID(v.SessionID) || ad.Paths.State == "" {
		return
	}
	lock, err := flock.TryLock(ad.Paths.StatuslineLock(v.SessionID), lockWait)
	if err != nil {
		// Busy or unwritable: the next tick (about 300 ms later) carries the
		// same total, so skipping loses nothing.
		return
	}
	defer lock.Unlock()

	snapPath := ad.Paths.StatuslineSnap(v.SessionID)
	prev, _ := ReadSnapshot(snapPath)
	now := ad.Clock.Now().UTC().Format(time.RFC3339Nano)
	max, banked := Bank(prev.MaxUSD, prev.BankedUSD, *v.Cost.Total)
	snap := Snapshot{
		V: 1, TS: now, SessionID: v.SessionID, FirstUSD: prev.FirstUSD, FirstTS: prev.FirstTS,
		CostUSD: *v.Cost.Total, BankedUSD: banked, MaxUSD: max,
		Model: clip(v.Model.ID), Version: clip(v.Version), DurationMS: v.Cost.DurationMS,
		CtxPct: v.ContextWindow.UsedPct, CtxSize: v.ContextWindow.Size,
	}
	if snap.FirstTS == "" && prev.TS == "" {
		snap.FirstUSD, snap.FirstTS = *v.Cost.Total, now
	}
	if b, err := json.Marshal(snap); err == nil {
		_ = atomicfile.Write(snapPath, b, 0o600)
	}
}

// Bank folds a new reported total into the running max and banked spend. A real
// reset (`/login`) drops the total below half the max or under one cent; only
// then is the old max banked. A small dip (5.00 to 4.98) must not bank, or
// live cost would double (LESSONS L-35).
func Bank(prevMax, prevBanked, total float64) (max, banked float64) {
	if prevMax > 0 && total < prevMax && (total < prevMax*0.5 || total < 0.01) {
		return total, prevBanked + prevMax
	}
	if total > prevMax {
		return total, prevBanked
	}
	return prevMax, prevBanked
}

func clip(s string) string {
	if len(s) > 64 {
		return s[:64]
	}
	return s
}

// passthrough streams the wrapped command's output straight to stdout so the
// bytes are exactly what the command wrote.
func passthrough(argv []string, raw []byte, stdout, stderr io.Writer) {
	ctx, cancel := context.WithTimeout(context.Background(), wrappedTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = bytes.NewReader(raw)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.WaitDelay = 500 * time.Millisecond
	_ = cmd.Run() // a failing wrapped command is the user's to debug; bosun still exits 0
}
