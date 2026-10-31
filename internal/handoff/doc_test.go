package handoff

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmvaldez/bosun/internal/runner"
)

func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	return dir
}

func TestLocateInRepo(t *testing.T) {
	repo := gitRepo(t)
	sub := filepath.Join(repo, "pkg")
	os.MkdirAll(sub, 0o755)
	for i := 0; i < 2; i++ { // the second pass proves it is idempotent
		got, err := Locate(runner.Exec{}, sub, "abc", "")
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(repo, ".bosun", "handoff-abc.md"); got != want {
			t.Errorf("doc = %s, want %s", got, want)
		}
	}
	b, _ := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	if n := strings.Count(string(b), ".bosun/"); n != 1 {
		t.Errorf(".bosun/ appears %d times in info/exclude:\n%s", n, b)
	}
	if fi, _ := os.Stat(filepath.Join(repo, ".bosun")); fi.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %v", fi.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(repo, ".gitignore")); err == nil {
		t.Error(".gitignore was created")
	}
}

func TestLocateOutsideRepo(t *testing.T) {
	plain := t.TempDir()
	if _, err := Locate(runner.Exec{}, plain, "abc", ""); err == nil {
		t.Error("no repo and no handoff.dir should refuse")
	}
	dir := filepath.Join(t.TempDir(), "docs")
	got, err := Locate(runner.Exec{}, plain, "abc", dir)
	if err != nil || got != filepath.Join(dir, "handoff-abc.md") {
		t.Errorf("Locate = %q, %v", got, err)
	}
}

func TestReadEventsTail(t *testing.T) {
	p := filepath.Join(t.TempDir(), "events.jsonl")
	var b strings.Builder
	for i := 0; i < 6000; i++ {
		b.WriteString(`{"v":1,"ts":"2026-10-31T12:00:00Z","ev":"PostToolUse","sid":"x"}` + "\n")
	}
	b.WriteString(`{"v":1,"ts":"2026-10-31T12:00:01Z","ev":"Stop","sid":"x"}` + "\n")
	os.WriteFile(p, []byte(b.String()), 0o600)
	evs := ReadEvents(p)
	if len(evs) == 0 || len(evs) >= 6001 || evs[len(evs)-1].Ev != "Stop" {
		t.Errorf("read %d events, last %q", len(evs), evs[len(evs)-1].Ev)
	}
}
