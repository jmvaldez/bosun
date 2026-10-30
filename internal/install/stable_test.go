package install

import (
	"os"
	"path/filepath"
	"testing"
)

// brewLayout builds a Homebrew-like tree: the real binary in a versioned Cellar
// dir with its share dir, and a PATH symlink with a share dir beside it.
func brewLayout(t *testing.T) (root, cellarBin, linkBin string) {
	t.Helper()
	root = t.TempDir()
	cellarBin = filepath.Join(root, "Cellar", "bosun", "1.0.0", "bin")
	linkBin = filepath.Join(root, "prefix", "bin")
	for _, d := range []string{cellarBin, linkBin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(cellarBin, "bosun"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(cellarBin, "bosun"), filepath.Join(linkBin, "bosun")); err != nil {
		t.Fatal(err)
	}
	return root, cellarBin, linkBin
}

func addShare(t *testing.T, binDir string) string {
	t.Helper()
	share := filepath.Join(binDir, "..", "share", "bosun")
	if err := os.MkdirAll(filepath.Join(share, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(share, ".claude-plugin", "marketplace.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(share)
}

func lookPathAt(p string) func(string) (string, error) {
	return func(string) (string, error) { return p, nil }
}

// D-31: the marketplace and the tmux conf must name the PATH location, not the
// Cellar dir `brew cleanup` deletes.
func TestStablePathsPreferThePathLocationOverTheResolvedOne(t *testing.T) {
	_, cellarBin, linkBin := brewLayout(t)
	linkShare := addShare(t, linkBin)
	addShare(t, cellarBin) // the versioned copy exists too, and must lose
	exe := filepath.Join(cellarBin, "bosun")

	bosun, dirs := StablePaths(exe, lookPathAt(filepath.Join(linkBin, "bosun")))
	if bosun != filepath.Join(linkBin, "bosun") {
		t.Errorf("bosun = %s, want the PATH location", bosun)
	}
	if got := marketplaceDir(Options{SearchDirs: dirs}); got != linkShare {
		t.Errorf("share dir = %s, want %s", got, linkShare)
	}
}

// When the PATH location has no share dir, the resolved binary's is the
// fallback, as before.
func TestStablePathsFallBackToTheResolvedShareDir(t *testing.T) {
	_, cellarBin, linkBin := brewLayout(t)
	cellarShare := addShare(t, cellarBin)
	exe := filepath.Join(cellarBin, "bosun")

	_, dirs := StablePaths(exe, lookPathAt(filepath.Join(linkBin, "bosun")))
	if got := marketplaceDir(Options{SearchDirs: dirs}); got != cellarShare {
		t.Errorf("share dir = %s, want %s", got, cellarShare)
	}
}

// Another bosun on PATH, or none, is not this binary and lends nothing.
func TestStablePathsIgnoreAnotherBosun(t *testing.T) {
	root, cellarBin, _ := brewLayout(t)
	exe := filepath.Join(cellarBin, "bosun")
	other := filepath.Join(root, "other", "bin")
	os.MkdirAll(other, 0o755)
	os.WriteFile(filepath.Join(other, "bosun"), []byte("y"), 0o755)
	addShare(t, other)

	bosun, dirs := StablePaths(exe, lookPathAt(filepath.Join(other, "bosun")))
	if bosun != exe || len(dirs) != 1 || dirs[0] != cellarBin {
		t.Errorf("another bosun on PATH was used: %s %v", bosun, dirs)
	}
	notFound := func(string) (string, error) { return "", os.ErrNotExist }
	if bosun, dirs := StablePaths(exe, notFound); bosun != exe || len(dirs) != 1 {
		t.Errorf("no bosun on PATH: %s %v", bosun, dirs)
	}
}
