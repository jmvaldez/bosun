package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmvaldez/bosun/internal/runner"
)

// repo makes a throwaway repository with one commit under a temp dir.
func repo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "api")
	os.MkdirAll(dir, 0o755)
	run := func(args ...string) {
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644)
	run("add", ".")
	run("commit", "-q", "-m", "init")
	return dir
}

func branchOf(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func TestAddCreatesBranchAndWorktreeBesideTheRepo(t *testing.T) {
	dir := repo(t)
	res, err := Add(runner.Exec{}, dir, "feat/ABC-123", "")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(filepath.Dir(dir), "api-feat-ABC-123")
	if res.Path != want || !res.NewBranch {
		t.Fatalf("result = %+v, want path %s", res, want)
	}
	if got := branchOf(t, res.Path); got != "feat/ABC-123" {
		t.Errorf("worktree is on %q", got)
	}
	if _, err := os.Stat(filepath.Join(res.Path, "a.txt")); err != nil {
		t.Errorf("the worktree has no checkout: %v", err)
	}
	if got := branchOf(t, dir); got != "main" {
		t.Errorf("the main worktree moved to %q", got)
	}
}

func TestAddRefusesAnExistingPathEvenWhenEmpty(t *testing.T) {
	dir := repo(t)
	path := filepath.Join(filepath.Dir(dir), "api-feat-X-1")
	os.MkdirAll(path, 0o755)
	if _, err := Add(runner.Exec{}, dir, "feat/X-1", ""); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("err = %v", err)
	}
	// Nothing was created or removed: the empty dir is still there, no branch made.
	if _, err := os.Stat(path); err != nil {
		t.Error("the existing dir was removed")
	}
	if out, _ := exec.Command("git", "-C", dir, "branch", "--list", "feat/X-1").Output(); len(out) != 0 {
		t.Errorf("a branch was created for a refused worktree: %s", out)
	}
	// And the first success followed by a second request for the same branch refuses.
	if _, err := Add(runner.Exec{}, dir, "feat/Y-2", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := Add(runner.Exec{}, dir, "feat/Y-2", ""); err == nil {
		t.Error("a second worktree for the same branch was created")
	}
}

func TestAddUsesAnExistingBranchWithoutResettingIt(t *testing.T) {
	dir := repo(t)
	exec.Command("git", "-C", dir, "branch", "topic").Run()
	res, err := Add(runner.Exec{}, dir, "topic", "")
	if err != nil || res.NewBranch {
		t.Fatalf("res %+v err %v", res, err)
	}
}

func TestAddHonorsWorktreeDirAndRejectsBadInput(t *testing.T) {
	dir := repo(t)
	parent := t.TempDir()
	res, err := Add(runner.Exec{}, dir, "ABC-1", parent)
	if err != nil || filepath.Dir(res.Path) != parent {
		t.Fatalf("res %+v err %v", res, err)
	}
	for _, bad := range []string{"", "has space", "a..b", "-rf", "x.lock"} {
		if _, err := Add(runner.Exec{}, dir, bad, parent); err == nil {
			t.Errorf("branch %q accepted", bad)
		}
	}
	if _, err := Add(runner.Exec{}, t.TempDir(), "ok-1", parent); err == nil {
		t.Error("a non-repository was accepted")
	}
}

func TestAddNeverRemovesOrPrunes(t *testing.T) {
	dir := repo(t)
	first, err := Add(runner.Exec{}, dir, "feat/A-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Add(runner.Exec{}, dir, "feat/B-2", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first.Path); err != nil {
		t.Errorf("an earlier worktree disappeared: %v", err)
	}
	out, _ := exec.Command("git", "-C", dir, "worktree", "list").Output()
	if n := strings.Count(string(out), "\n"); n != 3 {
		t.Errorf("worktree list has %d entries:\n%s", n, out)
	}
}
