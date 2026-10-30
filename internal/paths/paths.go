// Package paths resolves bosun's directories. It never uses the stdlib's user-config helper,
// which returns ~/Library/Application Support on macOS.
package paths

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// Paths holds each resolved directory. An empty string means the directory
// could not be resolved (no override, no XDG var, no HOME); callers treat that
// as "nothing to write".
type Paths struct {
	Config string
	State  string
	Cache  string
	Data   string
	// Claude is Claude Code's own data dir. See ClaudeSettings for the one file
	// bosun writes there.
	Claude string
}

// Resolve picks each dir from its BOSUN_*_DIR var, then XDG, then the default
// under home. getenv is injected so tests never depend on the real environment.
func Resolve(getenv func(string) string) Paths {
	home := getenv("HOME")
	pick := func(override, xdg, def string) string {
		if v := getenv(override); v != "" {
			return v
		}
		// The XDG spec says relative values are invalid and must be ignored:
		// https://specifications.freedesktop.org/basedir-spec/latest/
		if v := getenv(xdg); filepath.IsAbs(v) {
			return filepath.Join(v, "bosun")
		}
		if home == "" {
			return ""
		}
		return filepath.Join(home, def, "bosun")
	}
	p := Paths{
		Config: pick("BOSUN_CONFIG_DIR", "XDG_CONFIG_HOME", ".config"),
		State:  pick("BOSUN_STATE_DIR", "XDG_STATE_HOME", filepath.Join(".local", "state")),
		Cache:  pick("BOSUN_CACHE_DIR", "XDG_CACHE_HOME", ".cache"),
		Data:   pick("BOSUN_DATA_DIR", "XDG_DATA_HOME", filepath.Join(".local", "share")),
	}
	switch {
	case getenv("CLAUDE_CONFIG_DIR") != "":
		p.Claude = getenv("CLAUDE_CONFIG_DIR")
	case home != "":
		p.Claude = filepath.Join(home, ".claude")
	}
	return p
}

func (p Paths) ConfigFile() string { return join(p.Config, "config.toml") }

// Sessions is the parent of every per-session dir.
func (p Paths) Sessions() string { return join(p.State, "sessions") }

// Session returns one session's dir. The id must already be validated.
func (p Paths) Session(id string) string { return join(p.State, "sessions", id) }

// Tag is the task set by `bosun tag`.
func (p Paths) Tag(id string) string { return join(p.State, "sessions", id, "tag") }

// Events is one session's append-only hook event log.
func (p Paths) Events(id string) string { return join(p.State, "sessions", id, "events.jsonl") }

// StatuslineSnap is one session's status line snapshot.
func (p Paths) StatuslineSnap(id string) string {
	return join(p.State, "sessions", id, "statusline.json")
}

// StatuslineLock serialises writers of one session's status line snapshot.
func (p Paths) StatuslineLock(id string) string {
	return join(p.State, "sessions", id, "statusline.lock")
}

// Locks is the dir holding every lock file.
func (p Paths) Locks() string { return join(p.State, "locks") }

// Lock is the lock file called name, under Locks.
func (p Paths) Lock(name string) string { return join(p.State, "locks", name+".lock") }

// Identity is the cached account key.
func (p Paths) Identity() string { return join(p.State, "identity.json") }

// Heartbeat is the hook heartbeat file.
func (p Paths) Heartbeat() string { return join(p.State, "heartbeat.json") }

// TmuxConf is the generated tmux conf the user sources.
func (p Paths) TmuxConf() string { return join(p.Data, "tmux.conf") }

// Jump holds each client's position in the waiting queue.
func (p Paths) Jump() string { return join(p.State, "jump.json") }

// Live is the evaluator's per-session state.
func (p Paths) Live() string { return join(p.State, "live.json") }

// Ledger is the SQLite database.
func (p Paths) Ledger() string { return join(p.State, "bosun.db") }

// Today holds today's spend for the segment.
func (p Paths) Today() string { return join(p.State, "today.json") }

// Alerts holds the active alert per rule and scope.
func (p Paths) Alerts() string { return join(p.State, "alerts.json") }

// AlertsLog is the append-only log of fired alerts.
func (p Paths) AlertsLog() string { return join(p.State, "alerts.jsonl") }

// Notify holds posted notice groups and the rate-limit window.
func (p Paths) Notify() string { return join(p.State, "notify.json") }

// Pace is the daily_pace baseline.
func (p Paths) Pace() string { return join(p.State, "pace.json") }

// IngestMarker records when the last full ingest finished, readable without the database.
func (p Paths) IngestMarker() string { return join(p.State, "ingest.json") }

// Marks records the last window marker written per window.
func (p Paths) Marks() string { return join(p.State, "marks.json") }

// AgentsCache is the cached `claude agents --json`.
func (p Paths) AgentsCache() string { return join(p.Cache, "agents.json") }

// ClaudeSettings is the user's settings.json. Only `setup` and
// `uninstall --apply` write it, after a backup (D-26).
func (p Paths) ClaudeSettings() string { return join(p.Claude, "settings.json") }

func join(dir string, elem ...string) string {
	if dir == "" {
		return ""
	}
	return filepath.Join(append([]string{dir}, elem...)...)
}

// ValidSessionID reports whether id matches ^[A-Za-z0-9-]{1,64}$. It must hold
// before the id touches a path (LESSONS L-19).
func ValidSessionID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for i := 0; i < len(id); i++ {
		if !isAlnum(id[i]) && id[i] != '-' {
			return false
		}
	}
	return true
}

// ShortID is the first 8 characters of a session id, for display.
func ShortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func isAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// MarkerName is the file bosun leaves in each of its dirs. `uninstall --purge`
// deletes a dir only if it holds one, so a mistyped BOSUN_*_DIR that names a
// shared dir (~/.local/share) is never emptied.
const MarkerName = ".bosun"

// HasMarker accepts only a regular file: a symlink or dir named .bosun does
// not mark a dir.
func HasMarker(dir string) bool {
	fi, err := os.Lstat(filepath.Join(dir, MarkerName))
	return err == nil && fi.Mode().IsRegular()
}

// owned names everything bosun itself creates directly inside its dirs.
// Dirs of every kind share one list so a user who points two overrides at one
// dir still gets it marked.
var owned = map[string]bool{
	// state
	"sessions": true, "locks": true, "identity.json": true, "heartbeat.json": true, "jump.json": true,
	"live.json": true, "bosun.db": true, "bosun.db-wal": true, "bosun.db-shm": true, "bosun.db-journal": true,
	"today.json": true, "alerts.json": true, "alerts.jsonl": true, "notify.json": true, "pace.json": true,
	"ingest.json": true, "marks.json": true,
	// cache, data, config
	"agents.json": true, "tmux.conf": true, "exports": true, "config.toml": true,
}

// EnsureMarkers writes the marker into each existing dir that has none, and
// only when everything in the dir is something bosun creates: a directory a
// user pointed an override at by mistake holds other files and stays unmarked.
// Dirs from builds before the marker get it on the next normal write. It never
// creates a dir, and read-only commands do not call it.
func EnsureMarkers(p Paths) {
	for _, dir := range []string{p.State, p.Cache, p.Data, p.Config} {
		if dir == "" || HasMarker(dir) {
			continue
		}
		fi, err := os.Lstat(dir)
		if err != nil || !fi.IsDir() {
			continue
		}
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		ok := true
		for _, e := range ents {
			if !owned[e.Name()] {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		f, err := os.OpenFile(filepath.Join(dir, MarkerName), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil && !errors.Is(err, fs.ErrExist) {
			continue
		}
		if f != nil {
			f.Close()
		}
	}
}
