// Package gitinfo reads repo and branch straight from .git files. Hooks must
// not exec git (SPEC section 2, hot path purity).
package gitinfo

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jmvaldez/bosun/internal/clean"
)

// Info is what a cwd tells us about its repository.
type Info struct {
	Repo   string // origin "owner/name", else the main worktree dir name
	Branch string // empty when detached or not a repo
}

const (
	maxWalk = 40
	maxRead = 64 << 10
)

// Read inspects dir and its parents for a .git entry.
func Read(dir string) Info {
	if dir == "" || !filepath.IsAbs(dir) {
		return Info{}
	}
	for i := 0; i < maxWalk; i++ {
		dotgit := filepath.Join(dir, ".git")
		if fi, err := os.Lstat(dotgit); err == nil {
			return inspect(dir, dotgit, fi)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return Info{}
}

func inspect(worktree, dotgit string, fi os.FileInfo) Info {
	gitDir, commonDir := dotgit, dotgit
	if !fi.IsDir() {
		// A linked worktree: .git is a file pointing at <main>/.git/worktrees/<n>.
		line, ok := firstLine(dotgit)
		target, found := strings.CutPrefix(line, "gitdir:")
		if !ok || !found {
			return Info{}
		}
		gitDir = resolve(worktree, strings.TrimSpace(target))
		commonDir = gitDir
		if rel, ok := firstLine(filepath.Join(gitDir, "commondir")); ok {
			commonDir = resolve(gitDir, rel)
		}
	}
	return Info{Repo: clean.Text(repoName(commonDir)), Branch: clean.Text(branch(gitDir))}
}

func resolve(base, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(base, p)
}

func branch(gitDir string) string {
	line, ok := firstLine(filepath.Join(gitDir, "HEAD"))
	if !ok {
		return ""
	}
	ref, ok := strings.CutPrefix(line, "ref: refs/heads/")
	if !ok {
		return ""
	}
	return ref
}

func repoName(commonDir string) string {
	if url := originURL(filepath.Join(commonDir, "config")); url != "" {
		if n := ownerName(url); n != "" {
			return n
		}
	}
	if filepath.Base(commonDir) == ".git" {
		return filepath.Base(filepath.Dir(commonDir))
	}
	return strings.TrimSuffix(filepath.Base(commonDir), ".git")
}

// originURL scans for `url =` inside [remote "origin"]. A full INI parser is
// not worth it for one key.
func originURL(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(io.LimitReader(f, maxRead))
	inOrigin := false
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			inOrigin = strings.HasPrefix(line, `[remote "origin"`)
			continue
		}
		if inOrigin {
			if k, v, ok := strings.Cut(line, "="); ok && strings.TrimSpace(k) == "url" {
				return strings.TrimSpace(v)
			}
		}
	}
	return ""
}

// ownerName keeps the last two path segments of a remote URL, which covers
// scp-style, https, and ssh:// forms.
func ownerName(url string) string {
	url = strings.TrimSuffix(strings.TrimSpace(url), "/")
	url = strings.TrimSuffix(url, ".git")
	i := strings.LastIndexAny(url, "/:")
	if i < 0 {
		return ""
	}
	name := url[i+1:]
	rest := url[:i]
	j := strings.LastIndexAny(rest, "/:")
	owner := rest[j+1:]
	if owner == "" || name == "" || strings.Contains(owner, "@") {
		return ""
	}
	return owner + "/" + name
}

func firstLine(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	sc := bufio.NewScanner(io.LimitReader(f, maxRead))
	if !sc.Scan() {
		return "", false
	}
	return strings.TrimSpace(sc.Text()), true
}
