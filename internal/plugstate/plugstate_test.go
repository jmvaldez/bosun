package plugstate

import (
	"os"
	"path/filepath"
	"testing"
)

// write puts body at dir/rel, creating the dirs on the way.
func write(t *testing.T, dir, rel, body string) string {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLiveCost(t *testing.T) {
	for name, c := range map[string]struct {
		body string // empty: no file
		want bool
	}{
		"missing file":      {"", false},
		"bosun wrap":        {`{"statusLine":{"type":"command","command":"if command -v bosun >/dev/null; then exec bosun statusline -- ccline; fi"}}`, true},
		"bare wrap":         {`{"statusLine":{"command":"bosun statusline -- x"}}`, true},
		"different command": {`{"statusLine":{"type":"command","command":"ccline"}}`, false},
		"no status line":    {`{"theme":"dark"}`, false},
		"status line empty": {`{"statusLine":{}}`, false},
		"status line text":  {`{"statusLine":"bosun statusline"}`, false},
		"not json":          {`bosun statusline`, false},
		"empty file":        {` `, false},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			if c.body != "" {
				write(t, filepath.Dir(path), "settings.json", c.body)
			}
			if got := LiveCost(path); got != c.want {
				t.Errorf("LiveCost = %v, want %v", got, c.want)
			}
		})
	}
}

func TestInstalled(t *testing.T) {
	for name, c := range map[string]struct {
		body string // empty: no file
		want bool
	}{
		"missing file": {"", false},
		"present":      {`{"plugins":{"bosun@bosun":[{"scope":"user"}]}}`, true},
		"other only":   {`{"plugins":{"other@bosun":[]}}`, false},
		"no plugins":   {`{}`, false},
		"not json":     {`nope`, false},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if c.body != "" {
				write(t, dir, "plugins/installed_plugins.json", c.body)
			}
			if got := Installed(dir, "bosun@bosun"); got != c.want {
				t.Errorf("Installed = %v, want %v", got, c.want)
			}
		})
	}
}

func TestMarketplaceAdded(t *testing.T) {
	for name, c := range map[string]struct {
		body string // empty: no file
		want bool
	}{
		"missing file": {"", false},
		"present":      {`{"bosun":{"source":{"source":"directory","path":"/x"}}}`, true},
		"other only":   {`{"other":{}}`, false},
		"not json":     {`nope`, false},
		"array":        {`["bosun"]`, false},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if c.body != "" {
				write(t, dir, "plugins/known_marketplaces.json", c.body)
			}
			if got := MarketplaceAdded(dir, "bosun"); got != c.want {
				t.Errorf("MarketplaceAdded = %v, want %v", got, c.want)
			}
		})
	}
}
