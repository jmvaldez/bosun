// Package plugstate reads Claude Code's own plugin bookkeeping, read-only, to
// tell "installed" from "enabled": a plugin can be installed and disabled, and
// a marketplace can be added with nothing installed from it. It also reads
// settings.json to tell whether live cost is on.
package plugstate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Installed reports whether id (name@marketplace) is in installed_plugins.json.
func Installed(claudeDir, id string) bool {
	b, err := os.ReadFile(filepath.Join(claudeDir, "plugins", "installed_plugins.json"))
	if err != nil {
		return false
	}
	var f struct {
		Plugins map[string]json.RawMessage `json:"plugins"`
	}
	if json.Unmarshal(b, &f) != nil {
		return false
	}
	_, ok := f.Plugins[id]
	return ok
}

// MarketplaceAdded reports whether name is in known_marketplaces.json.
func MarketplaceAdded(claudeDir, name string) bool {
	b, err := os.ReadFile(filepath.Join(claudeDir, "plugins", "known_marketplaces.json"))
	if err != nil {
		return false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return false
	}
	_, ok := m[name]
	return ok
}

// WrapMarker is what bosun's statusLine wrapper runs; a command that contains it
// is the wrap.
const WrapMarker = "bosun statusline"

// LiveCost reports whether settings.json's statusLine.command is bosun's wrap,
// which is what "live cost on" means (D-31). It is read from the file each time,
// never stored. An unreadable file or no status line is off.
func LiveCost(settings string) bool {
	b, err := os.ReadFile(settings)
	if err != nil {
		return false
	}
	var s struct {
		StatusLine struct {
			Command string `json:"command"`
		} `json:"statusLine"`
	}
	if json.Unmarshal(b, &s) != nil {
		return false
	}
	return strings.Contains(s.StatusLine.Command, WrapMarker)
}
