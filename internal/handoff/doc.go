package handoff

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jmvaldez/bosun/internal/hook"
	"github.com/jmvaldez/bosun/internal/runner"
)

const (
	gitTimeout = 5 * time.Second
	// excludeLine keeps the doc's directory out of git without touching
	// .gitignore.
	excludeLine = ".bosun/"
	// eventsTail bounds how much of events.jsonl ReadEvents looks at.
	eventsTail = 256 << 10
)

// Locate picks the doc's path and creates its directory with mode 0o700. In a
// git repo the doc is <worktree root>/.bosun/handoff-<sid>.md, and `.bosun/` is
// added to the repo's info/exclude when missing (D-36: Claude Code treats a
// write under .git as a sensitive file). Outside a repo it is
// <dir>/handoff-<sid>.md, and it refuses when dir is empty.
func Locate(r runner.Runner, cwd, sid, dir string) (string, error) {
	name := "handoff-" + sid + ".md"
	if cwd != "" {
		if top, err := r.Run(gitTimeout, "git", "-C", cwd, "rev-parse", "--show-toplevel"); err == nil {
			root := strings.TrimSpace(string(top))
			out, err := r.Run(gitTimeout, "git", "-C", cwd, "rev-parse", "--git-path", "info/exclude")
			if err != nil {
				return "", fmt.Errorf("git: %w", err)
			}
			exclude := strings.TrimSpace(string(out))
			if !filepath.IsAbs(exclude) {
				exclude = filepath.Join(cwd, exclude)
			}
			if err := ensureExcluded(exclude); err != nil {
				return "", err
			}
			docDir := filepath.Join(root, excludeLine)
			if err := os.MkdirAll(docDir, 0o700); err != nil {
				return "", err
			}
			return filepath.Join(docDir, name), nil
		}
	}
	if dir == "" {
		return "", errors.New("not in a git repo and handoff.dir is not set")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// ensureExcluded appends excludeLine to the exclude file unless a line already
// names it. It is idempotent.
func ensureExcluded(path string) error {
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, l := range strings.Split(string(b), "\n") {
		if t := strings.TrimSpace(l); t == excludeLine || t == strings.TrimSuffix(excludeLine, "/") {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	line := excludeLine + "\n"
	if len(b) > 0 && !bytes.HasSuffix(b, []byte("\n")) {
		line = "\n" + line
	}
	_, err = f.WriteString(line)
	return err
}

// Stat is the production Deps.Stat.
func Stat(path string) (time.Time, int64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}, 0, err
	}
	return fi.ModTime(), fi.Size(), nil
}

// ReadEvents reads the events in the last 256 KiB of an events.jsonl, oldest
// first. A missing file or a bad line is skipped.
func ReadEvents(path string) []hook.Event {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil
	}
	cut := false
	if fi.Size() > eventsTail {
		if _, err := f.Seek(fi.Size()-eventsTail, io.SeekStart); err != nil {
			return nil
		}
		cut = true
	}
	r := bufio.NewReaderSize(f, 64<<10)
	if cut {
		_, _ = r.ReadBytes('\n') // the first line is probably cut
	}
	var evs []hook.Event
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return evs // a partial last line is left for the next read
		}
		var e hook.Event
		if json.Unmarshal(line, &e) == nil {
			evs = append(evs, e)
		}
	}
}
