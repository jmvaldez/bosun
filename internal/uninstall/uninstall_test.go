package uninstall

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jmvaldez/bosun/internal/install"
	"github.com/jmvaldez/bosun/internal/paths"
)

func layout(t *testing.T) (paths.Paths, Options, string) {
	root := t.TempDir()
	p := paths.Paths{
		Config: root + "/config/bosun", State: root + "/state/bosun", Cache: root + "/cache/bosun", Data: root + "/data/bosun",
		Claude: root + "/claude",
	}
	for _, d := range []string{p.Config, p.State, p.Cache, p.Data} {
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "file"), []byte("x"), 0o644)
		os.WriteFile(filepath.Join(d, paths.MarkerName), nil, 0o600)
	}
	os.MkdirAll(p.Claude, 0o755)
	os.WriteFile(p.ClaudeSettings(), []byte(`{"statusLine":{"type":"command","command":`+quote(install.WrapCommand(`jq -r '.model.id'`))+`}}`), 0o644)
	return p, Options{Home: root, Settings: p.ClaudeSettings(), Executable: "/home/me/.local/bin/bosun", TmuxConf: "/home/me/.tmux.conf"}, root
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

func TestPrintsTheStepsInOrderAndTouchesNothing(t *testing.T) {
	p, o, _ := layout(t)
	var out, errw bytes.Buffer
	if rc := Run(p, o, &out, &errw); rc != 0 {
		t.Fatalf("rc %d %s", rc, errw.String())
	}
	text := out.String()
	last := -1
	for _, step := range []string{"1. Remove the plugin", "claude plugin uninstall bosun@bosun", "2. Restore your original status line", "jq -r '.model.id'",
		"3. Remove the tmux line", "source-file -q " + p.Data + "/tmux.conf", "4. Delete the binary", "rm /home/me/.local/bin/bosun"} {
		i := strings.Index(text, step)
		if i < 0 || i < last {
			t.Fatalf("step %q missing or out of order in:\n%s", step, text)
		}
		last = i
	}
	for _, d := range []string{p.Config, p.State, p.Cache, p.Data} {
		if !exists(d) {
			t.Errorf("%s was deleted without --purge", d)
		}
	}
}

func TestPurgeDeletesStateCacheDataAndKeepsConfig(t *testing.T) {
	p, o, _ := layout(t)
	o.Purge = true
	var out, errw bytes.Buffer
	if rc := Run(p, o, &out, &errw); rc != 0 {
		t.Fatalf("rc %d %s", rc, errw.String())
	}
	for _, d := range []string{p.State, p.Cache, p.Data} {
		if exists(d) {
			t.Errorf("%s survived --purge", d)
		}
	}
	if !exists(p.Config) || !strings.Contains(out.String(), "kept config") {
		t.Error("--purge must keep the config")
	}
	p2, o2, _ := layout(t)
	o2.PurgeConfig = true
	Run(p2, o2, &out, &errw)
	if exists(p2.Config) {
		t.Error("--purge-config left the config")
	}
}

// LESSONS L-46.
func TestPurgeRefusesASymlinkedDir(t *testing.T) {
	p, o, root := layout(t)
	target := filepath.Join(root, "precious")
	os.MkdirAll(target, 0o755)
	os.WriteFile(filepath.Join(target, "keep"), []byte("x"), 0o644)
	os.RemoveAll(p.State)
	os.Symlink(target, p.State)
	o.Purge = true
	var out, errw bytes.Buffer
	rc := Run(p, o, &out, &errw)
	if rc == 0 || !strings.Contains(errw.String(), "symlink") {
		t.Errorf("rc %d err %q", rc, errw.String())
	}
	if !exists(filepath.Join(target, "keep")) {
		t.Fatal("deleted through a symlink")
	}
	if exists(p.Cache) || exists(p.Data) {
		t.Error("the other dirs should still be purged")
	}
}

func TestRemoveRefusesAnythingOutsideTheResolvedDirs(t *testing.T) {
	p, o, root := layout(t)
	other := filepath.Join(root, "elsewhere")
	os.MkdirAll(other, 0o755)
	if err := Remove("state", other, p, o); err == nil {
		t.Error("removed a dir that is not the resolved state dir")
	}
	if err := Remove("state", p.Cache, p, o); err == nil {
		t.Error("removed the cache dir as the state dir")
	}
	if err := Remove("bogus", p.State, p, o); err == nil {
		t.Error("an unknown kind was accepted")
	}
	// Overrides that point somewhere dangerous or oddly named.
	for _, dir := range []string{"/", o.Home, "/tmp"} {
		q := p
		q.State = dir
		if err := Remove("state", dir, q, Options{Home: o.Home, Explicit: map[string]bool{"state": true}}); err == nil {
			t.Errorf("removed %q", dir)
		}
	}
	// A dir not named bosun is deleted only when the user set it explicitly.
	q := p
	q.State = filepath.Join(root, "mystate")
	os.MkdirAll(q.State, 0o755)
	os.WriteFile(filepath.Join(q.State, paths.MarkerName), nil, 0o600)
	if err := Remove("state", q.State, q, Options{Home: o.Home}); err == nil {
		t.Error("deleted a dir not named bosun without an explicit override")
	}
	if err := Remove("state", q.State, q, Options{Home: o.Home, Explicit: map[string]bool{"state": true}}); err != nil {
		t.Errorf("explicit override refused: %v", err)
	}
	// A regular file in place of the dir.
	f := filepath.Join(root, "x/bosun")
	os.MkdirAll(filepath.Dir(f), 0o755)
	os.WriteFile(f, []byte("x"), 0o644)
	q.Cache = f
	if err := Remove("cache", f, q, o); err == nil {
		t.Error("deleted a file")
	}
}

// Review finding 9: a mistyped BOSUN_DATA_DIR=~/.local/share is explicit, so
// the name check lets it through; only the marker stops it.
func TestPurgeRefusesADirBosunNeverMarked(t *testing.T) {
	p, o, root := layout(t)
	shared := filepath.Join(root, "local/share")
	os.MkdirAll(filepath.Join(shared, "applications"), 0o755)
	os.WriteFile(filepath.Join(shared, "applications/keep"), []byte("x"), 0o644)
	p.Data = shared
	o.Purge = true
	o.Explicit = map[string]bool{"data": true}
	var out, errw bytes.Buffer
	rc := Run(p, o, &out, &errw)
	if rc == 0 || !strings.Contains(errw.String(), "marker") {
		t.Errorf("rc %d err %q", rc, errw.String())
	}
	if !exists(filepath.Join(shared, "applications/keep")) {
		t.Fatal("purged a dir bosun never created")
	}
	if exists(p.State) || exists(p.Cache) {
		t.Error("the marked dirs should still be purged")
	}
}

func TestRestoreLineWhenNotWrappedOrUnreadable(t *testing.T) {
	p, o, _ := layout(t)
	os.WriteFile(p.ClaudeSettings(), []byte(`{"statusLine":{"command":"jq ."}}`), 0o644)
	var out, errw bytes.Buffer
	Run(p, o, &out, &errw)
	if !strings.Contains(out.String(), "not wrapped by bosun; nothing to restore") {
		t.Errorf("output:\n%s", out.String())
	}
	o.Settings = "/nonexistent/settings.json"
	out.Reset()
	Run(p, o, &out, &errw)
	if !strings.Contains(out.String(), "could not read settings.json") {
		t.Errorf("output:\n%s", out.String())
	}
}
