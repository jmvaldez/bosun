package gitinfo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPlainRepoWithOrigin(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "proj/.git/HEAD"), "ref: refs/heads/feat/ABC-1-x\n")
	write(t, filepath.Join(root, "proj/.git/config"), "[core]\n\tbare = false\n[remote \"origin\"]\n\turl = git@github.com:acme/api.git\n")
	os.MkdirAll(filepath.Join(root, "proj/sub/deeper"), 0o755)
	got := Read(filepath.Join(root, "proj/sub/deeper"))
	if got.Repo != "acme/api" || got.Branch != "feat/ABC-1-x" {
		t.Fatalf("got %+v", got)
	}
}

func TestNoOriginUsesDirName(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "dotfiles/.git/HEAD"), "ref: refs/heads/main\n")
	got := Read(filepath.Join(root, "dotfiles"))
	if got.Repo != "dotfiles" || got.Branch != "main" {
		t.Fatalf("got %+v", got)
	}
}

func TestLinkedWorktree(t *testing.T) {
	root := t.TempDir()
	main := filepath.Join(root, "api")
	write(t, filepath.Join(main, ".git/config"), "[remote \"origin\"]\n\turl = https://github.com/acme/api.git\n")
	wt := filepath.Join(main, ".git/worktrees/wt1")
	write(t, filepath.Join(wt, "HEAD"), "ref: refs/heads/OPS-7\n")
	write(t, filepath.Join(wt, "commondir"), "../..\n")
	linked := filepath.Join(root, "api-OPS-7")
	write(t, filepath.Join(linked, ".git"), "gitdir: "+wt+"\n")
	got := Read(linked)
	if got.Repo != "acme/api" || got.Branch != "OPS-7" {
		t.Fatalf("got %+v", got)
	}
}

func TestLinkedWorktreeNoOriginUsesMainDirName(t *testing.T) {
	root := t.TempDir()
	main := filepath.Join(root, "api")
	write(t, filepath.Join(main, ".git/HEAD"), "ref: refs/heads/main\n")
	wt := filepath.Join(main, ".git/worktrees/wt1")
	write(t, filepath.Join(wt, "HEAD"), "ref: refs/heads/x\n")
	write(t, filepath.Join(wt, "commondir"), "../..\n")
	linked := filepath.Join(root, "elsewhere")
	write(t, filepath.Join(linked, ".git"), "gitdir: "+wt+"\n")
	if got := Read(linked); got.Repo != "api" {
		t.Fatalf("got %+v", got)
	}
}

func TestDetachedHeadAndNotARepo(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "r/.git/HEAD"), "0123456789abcdef0123456789abcdef01234567\n")
	if got := Read(filepath.Join(root, "r")); got.Branch != "" || got.Repo != "r" {
		t.Fatalf("detached: %+v", got)
	}
	if got := Read(t.TempDir()); got != (Info{}) {
		t.Fatalf("non-repo: %+v", got)
	}
	if got := Read("relative/path"); got != (Info{}) {
		t.Fatalf("relative: %+v", got)
	}
}

func TestOwnerName(t *testing.T) {
	for in, want := range map[string]string{
		"git@github.com:acme/api.git":         "acme/api",
		"https://github.com/acme/api":         "acme/api",
		"ssh://git@host.example/team/svc.git": "team/svc",
		"https://gitlab.com/g/sub/proj.git":   "sub/proj",
		"/srv/git/thing.git":                  "git/thing",
		"nonsense":                            "",
	} {
		if got := ownerName(in); got != want {
			t.Errorf("ownerName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestControlCharactersAreDropped(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "proj/.git/HEAD"), "ref: refs/heads/a\x1b]0;x\x07b\n")
	write(t, filepath.Join(root, "proj/.git/config"), "[remote \"origin\"]\n\turl = git@github.com:acme/ap\x1b]0;x\x07i.git\n")
	got := Read(filepath.Join(root, "proj"))
	if strings.ContainsAny(got.Repo+got.Branch, "\x1b\x07") || got.Repo == "" || got.Branch == "" {
		t.Fatalf("control characters survived or fields empty: %+v", got)
	}
}
