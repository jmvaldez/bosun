package install

import (
	"os"
	"path/filepath"
)

// StablePaths names the running binary by a path that survives an upgrade, and
// lists the directories to search for the share dir (marketplaceDir).
//
// os.Executable resolves symlinks. On Homebrew that lands in a versioned
// Cellar directory, which `brew cleanup` deletes after an upgrade; the tmux conf
// and the plugin marketplace would then point at nothing. The PATH location
// (/opt/homebrew/bin/bosun, a symlink into the Cellar) and the share dir beside
// it (/opt/homebrew/share/bosun) are re-pointed by brew, so they are preferred,
// but only when lookPath's answer really is the running binary: a different
// bosun earlier on PATH must not lend its share dir.
func StablePaths(exe string, lookPath func(string) (string, error)) (bosun string, searchDirs []string) {
	bosun = exe
	if p, err := lookPath("bosun"); err == nil && filepath.IsAbs(p) && sameFile(p, exe) {
		bosun = p
		searchDirs = append(searchDirs, filepath.Dir(p))
	}
	if d := filepath.Dir(exe); len(searchDirs) == 0 || searchDirs[0] != d {
		searchDirs = append(searchDirs, d)
	}
	return bosun, searchDirs
}

func sameFile(a, b string) bool {
	fa, err := os.Stat(a) // Stat follows symlinks: a link and its target are the same file
	if err != nil {
		return false
	}
	fb, err := os.Stat(b)
	return err == nil && os.SameFile(fa, fb)
}
