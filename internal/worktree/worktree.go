// Package worktree adds the git worktree `bosun run --worktree` asks for. It
// only ever adds: it never removes, prunes, or moves a worktree.
package worktree

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jmvaldez/bosun/internal/runner"
)

const gitTimeout = 10 * time.Second

// Result describes the worktree that now exists.
type Result struct {
	Path      string
	Branch    string
	NewBranch bool // the branch was created from HEAD
}

// Add creates <parent>/<repo>-<branch> (slashes in branch become dashes) as a
// worktree of the repository holding dir. An empty parent means the main
// worktree's parent dir, so the new one sits beside it. It refuses a path that
// already exists (empty or not, so it can never touch someone's work), a bare
// repository, and a branch name git rejects. The branch is created from dir's
// HEAD only if it does not exist yet.
func Add(r runner.Runner, dir, branch, parent string) (Result, error) {
	git := func(args ...string) (string, error) {
		out, err := r.Run(gitTimeout, "git", append([]string{"-C", dir}, args...)...)
		return strings.TrimSpace(string(out)), err
	}
	if _, err := git("check-ref-format", "--branch", branch); err != nil {
		return Result{}, fmt.Errorf("%q is not a usable branch name", branch)
	}
	common, err := git("rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return Result{}, fmt.Errorf("%s is not inside a git repository: %w", dir, err)
	}
	if filepath.Base(common) != ".git" {
		return Result{}, fmt.Errorf("cannot place a worktree beside a bare repository (%s); set run.worktree_dir and use a normal clone", common)
	}
	mainDir := filepath.Dir(common)
	if parent == "" {
		parent = filepath.Dir(mainDir)
	}
	path := filepath.Join(parent, filepath.Base(mainDir)+"-"+strings.ReplaceAll(branch, "/", "-"))
	if _, err := os.Lstat(path); err == nil {
		return Result{}, fmt.Errorf("%s already exists; refusing to reuse it", path)
	}
	_, existsErr := git("show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	res := Result{Path: path, Branch: branch}
	if existsErr == nil {
		_, err = git("worktree", "add", path, branch)
	} else {
		res.NewBranch = true
		_, err = git("worktree", "add", "-b", branch, path)
	}
	if err != nil {
		return Result{}, fmt.Errorf("git worktree add: %w", err)
	}
	return res, nil
}
