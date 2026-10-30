// Package testguard keeps tests out of the real home (LESSONS L-03). A package's
// TestMain calls Main: it points every BOSUN_*_DIR at a temp dir for the whole
// test process, fails the run if the paths bosun would then resolve still fall
// under the real HOME, and fails it again if new paths appeared in the real bosun dirs
// while the tests ran. Edits to files that already existed are not judged: see
// created.
package testguard

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/jmvaldez/bosun/internal/paths"
)

var overrides = []string{"BOSUN_CONFIG_DIR", "BOSUN_STATE_DIR", "BOSUN_CACHE_DIR", "BOSUN_DATA_DIR"}

var realHome, _ = os.UserHomeDir()

// Main runs a package's tests under the guard and exits with their status.
func Main(m *testing.M) {
	os.Exit(Run(m))
}

// Run is Main for a TestMain that has cleanup of its own to do after the tests.
func Run(m *testing.M) int {
	// What bosun would use with no override: these must be left untouched.
	orig := os.Getenv
	clean := func(k string) string {
		for _, o := range overrides {
			if k == o {
				return ""
			}
		}
		return orig(k)
	}
	watched := dirs(paths.Resolve(clean))
	before := snapshot(watched)

	sandbox, err := os.MkdirTemp("", "bosun-guard")
	if err != nil {
		fmt.Fprintln(os.Stderr, "testguard:", err)
		return 1
	}
	defer os.RemoveAll(sandbox)
	for _, o := range overrides {
		os.Setenv(o, filepath.Join(sandbox, strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(o, "BOSUN_"), "_DIR"))))
	}
	if err := CheckPaths(paths.Resolve(os.Getenv), realHome, os.TempDir()); err != nil {
		fmt.Fprintln(os.Stderr, "testguard:", err)
		return 1
	}

	code := m.Run()
	if made := created(before, snapshot(watched)); len(made) > 0 {
		fmt.Fprintf(os.Stderr, "testguard: tests created paths in the real bosun dirs:\n%s\n", strings.Join(made, "\n"))
		return 1
	}
	return code
}

// CheckPaths fails when a resolved bosun dir sits under home. Dirs under tmp are
// fine even if tmp itself is under home: they are the tests' own.
func CheckPaths(p paths.Paths, home, tmp string) error {
	if home == "" {
		return nil
	}
	for name, dir := range map[string]string{"config": p.Config, "state": p.State, "cache": p.Cache, "data": p.Data} {
		if dir != "" && under(dir, home) && !under(dir, tmp) {
			return fmt.Errorf("the %s dir %s is under the real HOME %s", name, dir, home)
		}
	}
	return nil
}

func under(dir, root string) bool {
	if root == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(dir))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func dirs(p paths.Paths) []string {
	var out []string
	for _, d := range []string{p.Config, p.State, p.Cache, p.Data} {
		if d != "" {
			out = append(out, d)
		}
	}
	return out
}

// snapshot lists every path under the dirs that exists.
func snapshot(dirs []string) map[string]bool {
	seen := map[string]bool{}
	for _, d := range dirs {
		_ = filepath.WalkDir(d, func(p string, _ fs.DirEntry, err error) error {
			if err == nil {
				seen[p] = true
			}
			return nil
		})
	}
	return seen
}

var uuid = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// created returns the paths in after but not before that a test could have
// made. A bosun install running on the same machine keeps writing to the real
// dirs while the tests run, and no portable check tells its writes from a
// test's. So changes to existing files are not judged, and neither is what a
// live install creates on its own: per-session dirs (real session ids are
// UUIDs; tests use short ids), atomicfile temp files, lock files and sqlite
// sidecars.
func created(before, after map[string]bool) []string {
	var out []string
	for p := range after {
		if !before[p] && !live(p) {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func live(p string) bool {
	for _, part := range strings.Split(p, string(filepath.Separator)) {
		if uuid.MatchString(part) {
			return true
		}
	}
	b := filepath.Base(p)
	return strings.Contains(b, ".tmp") || strings.HasSuffix(b, ".lock") ||
		strings.HasSuffix(b, "-wal") || strings.HasSuffix(b, "-shm") || strings.HasSuffix(b, "-journal")
}
