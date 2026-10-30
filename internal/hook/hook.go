// Package hook records one Claude Code hook event. It is the hottest path in
// bosun: it must finish in well under 50 ms, never block, and always exit 0
// (see Run).
package hook

import (
	"encoding/json"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jmvaldez/bosun/internal/clean"
	"github.com/jmvaldez/bosun/internal/gitinfo"
	"github.com/jmvaldez/bosun/internal/heartbeat"
	"github.com/jmvaldez/bosun/internal/paths"
	"github.com/jmvaldez/bosun/internal/platform"
	"github.com/jmvaldez/bosun/internal/stdin"
	"github.com/jmvaldez/bosun/internal/task"
)

const (
	// maxStdin bounds a huge payload (LESSONS L-17); decode keeps the keys that
	// arrived before the cut.
	maxStdin = 64 << 10
	// stdinDeadline stops a stalled writer from holding the hook.
	stdinDeadline = 200 * time.Millisecond
	// eventVersion is the events.jsonl schema version.
	eventVersion = 1
)

// The only environment variables whose values land in an event. Hooks inherit
// whatever secrets the user's launcher injects, so nothing else is read.
const (
	envTmux       = "TMUX"
	envTmuxPane   = "TMUX_PANE"
	envClaudePID  = "CLAUDE_PID"
	envSessionID  = "CLAUDE_CODE_SESSION_ID"
	envTask       = "BOSUN_TASK"
	envEntrypoint = "CLAUDE_CODE_ENTRYPOINT"
)

// Run handles one hook invocation and returns the process exit code, which is
// always 0. The recover is load bearing: an uncaught Go panic exits 2, which on
// PreToolUse blocks the tool and feeds the stack trace to Claude (LESSONS L-18).
func Run(ad platform.Adapters, args []string, in io.Reader) (code int) {
	defer func() { _ = recover() }()
	record(ad, pluginVersion(args), in)
	return 0
}

func record(ad platform.Adapters, version string, in io.Reader) {
	start := ad.Clock.Now()
	raw := stdin.Read(in, maxStdin, stdinDeadline)
	p, ok := decode(raw)
	if !ok || !isKnownEvent(p.event) || !paths.ValidSessionID(p.sessionID) {
		return
	}
	if ad.Paths.State == "" {
		return
	}
	line, err := buildEvent(ad, p, start).encode()
	if err != nil {
		return
	}
	if err := appendLine(ad.Paths.Session(p.sessionID), line); err != nil {
		return
	}
	spawnDispatch(ad, p)
	spawnTmuxSync(ad, p)
	// The sample excludes the heartbeat write itself, which cannot time itself.
	_ = heartbeat.Record(ad.Paths, ad.Clock.Now(), p.event, version, ad.Clock.Now().Sub(start))
}

func buildEvent(ad platform.Adapters, p payload, now time.Time) Event {
	e := Event{
		V:        eventVersion,
		TS:       now.UTC().Format(time.RFC3339Nano),
		Ev:       p.event,
		SID:      p.sessionID,
		AID:      clean.Ident(p.agentID, 64),
		AType:    clean.Ident(p.agentType, 64),
		PromptID: clean.Ident(p.promptID, 64),
		Tool:     clean.Ident(p.toolName, 64),
		TUID:     clean.Ident(p.toolUseID, 64),
		NType:    clean.Ident(p.notifType, 40),
		BG:       cleanBG(p.bg),
		Src:      source(p.source),
		Reason:   endReason(p.reason),
	}
	pid := claudePID(ad.Getenv(envClaudePID))
	e.PID = pid
	e.Tmux = tmuxLoc(ad.Getenv(envTmux), ad.Getenv(envTmuxPane))
	if p.event == "SessionStart" {
		e.Start = startExtra(ad, p, pid)
	}
	return e
}

func startExtra(ad platform.Adapters, p payload, pid int) *StartExtra {
	x := &StartExtra{
		Cwd:        p.cwd,
		Transcript: p.transcriptPath,
		Entrypoint: entrypoint(ad.Getenv(envEntrypoint)),
	}
	info := gitinfo.Read(p.cwd)
	x.Repo, x.Branch = info.Repo, info.Branch
	// Hooks never read config, so the task key uses the default pattern. The
	// env task may be free text; only the branch is held to the key pattern.
	parser := task.Default()
	fromEnv, envErr := parser.Tag(ad.Getenv(envTask))
	switch {
	case envErr == nil:
		x.Task, x.TaskSrc = fromEnv, task.SourceEnv
	case parser.Key(info.Branch) != "":
		x.Task, x.TaskSrc = parser.Key(info.Branch), task.SourceBranch
	}
	x.Account = cachedAccount(ad.Paths.Identity())
	if pid > 0 && ad.Procs != nil {
		x.StartTok, _ = ad.Procs.StartToken(pid)
	}
	return x
}

// cachedAccount reads the key `bosun refresh` cached. A hook never asks claude.
func cachedAccount(path string) string {
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var id struct {
		AccountKey string `json:"account_key"`
	}
	if json.Unmarshal(b, &id) != nil {
		return ""
	}
	return id.AccountKey
}

// entrypoint keeps the CLAUDE_CODE_ENTRYPOINT value (cli, sdk-cli, ...) when it
// looks like an enum (D-10).
func entrypoint(s string) string {
	if len(s) == 0 || len(s) > 32 {
		return ""
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return ""
		}
	}
	return s
}

func claudePID(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// tmuxLoc parses $TMUX ("socket,serverpid,session"). The socket path is
// everything before the last two commas, since a path may contain commas.
func tmuxLoc(tmux, pane string) *TmuxLoc {
	if tmux == "" {
		return nil
	}
	parts := strings.Split(tmux, ",")
	if len(parts) < 3 {
		return nil
	}
	spid, err := strconv.Atoi(parts[len(parts)-2])
	if err != nil || spid <= 0 {
		return nil
	}
	loc := &TmuxLoc{Sock: strings.Join(parts[:len(parts)-2], ","), SPID: spid}
	if validPane(pane) {
		loc.Pane = pane
	}
	return loc
}

func validPane(p string) bool {
	if len(p) < 2 || len(p) > 12 || p[0] != '%' {
		return false
	}
	for i := 1; i < len(p); i++ {
		if p[i] < '0' || p[i] > '9' {
			return false
		}
	}
	return true
}

// pluginVersion extracts --plugin-version. Unknown flags are ignored so a newer
// plugin never breaks an older binary.
func pluginVersion(args []string) string {
	for i, a := range args {
		v := ""
		switch {
		case a == "--plugin-version" && i+1 < len(args):
			v = args[i+1]
		case strings.HasPrefix(a, "--plugin-version="):
			v = strings.TrimPrefix(a, "--plugin-version=")
		default:
			continue
		}
		return clean.Ident(v, 32)
	}
	return ""
}

// notifyingEdge maps an event to the notice kind and detail it may start, or "".
// This is only a hint: the dispatcher re-checks the edge against the session's
// real state before posting anything.
func notifyingEdge(p payload) (kind, detail string) {
	switch p.event {
	case "PermissionRequest":
		return "needs-you", "permission prompt"
	case "Notification":
		switch p.notifType {
		case "permission_prompt":
			return "needs-you", "permission prompt"
		case "elicitation_dialog", "elicitation_url_dialog":
			return "needs-you", "dialog"
		case "agent_needs_input":
			return "needs-you", "input needed"
		}
	case "Stop":
		for _, t := range cleanBG(p.bg) {
			if t.Status == "running" {
				return "", "" // background work pending: the turn continues
			}
		}
		return "done", ""
	case "StopFailure":
		return "failed", ""
	}
	return "", ""
}

// spawnDispatch forks `bosun notify-dispatch` on a notifying edge and moves on.
// A session started by the SDK (`claude -p`, entrypoint sdk-*) never notifies,
// so no child is started for it (D-10).
func spawnDispatch(ad platform.Adapters, p payload) {
	if ad.Spawner == nil {
		return
	}
	kind, detail := notifyingEdge(p)
	if kind == "" || strings.HasPrefix(entrypoint(ad.Getenv(envEntrypoint)), "sdk-") {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	args := []string{"notify-dispatch", "--kind", kind, "--session", p.sessionID}
	if detail != "" {
		args = append(args, "--detail", detail)
	}
	_ = ad.Spawner.Spawn(exe, args...)
}

// spawnTmuxSync forks `bosun tmux-sync` on SessionStart inside tmux and moves
// on. bosun never edits the user's tmux config (D-31), so this is how the
// running server comes to have bosun's conf: the child checks the version the
// server holds and sources the conf only when it is behind. Nothing is waited
// for and nothing here can fail the hook. An SDK session (`claude -p`) is not
// one the user watches in a pane, so it skips this as it skips notices.
func spawnTmuxSync(ad platform.Adapters, p payload) {
	if ad.Spawner == nil || p.event != "SessionStart" || ad.Getenv(envTmux) == "" ||
		strings.HasPrefix(entrypoint(ad.Getenv(envEntrypoint)), "sdk-") {
		return
	}
	if exe, err := os.Executable(); err == nil {
		_ = ad.Spawner.Spawn(exe, "tmux-sync")
	}
}
