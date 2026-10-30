package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The .deb and .rpm list plugin files one by one (a tree entry would break
// directory modes), so a new file under plugin/ must be added to the nfpm list
// or the package would silently ship an incomplete plugin.
func TestGoreleaserShipsEveryPluginFile(t *testing.T) {
	cfg, err := os.ReadFile(".goreleaser.yaml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(cfg)
	n := 0
	filepath.WalkDir("plugin", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		n++
		if !strings.Contains(text, "src: "+p) || !strings.Contains(text, "dst: /usr/share/bosun/"+p) {
			t.Errorf("%s is not in the nfpm contents of .goreleaser.yaml", p)
		}
		return nil
	})
	if n == 0 {
		t.Fatal("no plugin files found")
	}
	if !strings.Contains(text, "src: .claude-plugin/marketplace.json") {
		t.Error("the marketplace manifest is not packaged")
	}
	// Release builds are static and stamped, packages run the print-only
	// postinstall, and the formula goes to the jmvaldez tap.
	for _, want := range []string{"CGO_ENABLED=0", "-X main.version={{ .Version }}", "postinstall: packaging/postinstall.sh", "jmvaldez", "homebrew-tap"} {
		if !strings.Contains(text, want) {
			t.Errorf(".goreleaser.yaml lacks %q", want)
		}
	}
}

// The post-install script prints; it must never run the plugin, status line or
// tmux steps itself.
func TestPostinstallOnlyPrints(t *testing.T) {
	b, err := os.ReadFile("packaging/postinstall.sh")
	if err != nil {
		t.Fatal(err)
	}
	var cmds []string
	inHeredoc := false
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(l)
		switch {
		case inHeredoc:
			if l == "MSG" {
				inHeredoc = false
			}
		case l == "" || strings.HasPrefix(l, "#"):
		case strings.HasSuffix(l, "<<'MSG'"):
			inHeredoc = true
			cmds = append(cmds, l)
		default:
			cmds = append(cmds, l)
		}
	}
	if len(cmds) != 2 || cmds[0] != "cat <<'MSG'" || cmds[1] != "exit 0" {
		t.Errorf("the script runs more than it prints: %q", cmds)
	}
	if !strings.Contains(string(b), "bosun setup") {
		t.Error("postinstall does not tell the user to run bosun setup")
	}
}
