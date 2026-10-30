package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureMarkersMarksOnlyDirsHoldingOnlyBosunFiles(t *testing.T) {
	root := t.TempDir()
	p := Paths{State: root + "/state", Cache: root + "/cache", Data: root + "/data", Config: root + "/config"}
	// State from an older build: bosun files only.
	os.MkdirAll(p.State+"/sessions", 0o700)
	os.WriteFile(p.State+"/live.json", []byte("{}"), 0o600)
	// A shared dir a mistyped override points at.
	os.MkdirAll(p.Data+"/applications", 0o700)
	// Empty dir, and a dir that does not exist.
	os.MkdirAll(p.Cache, 0o700)

	EnsureMarkers(p)
	for dir, want := range map[string]bool{p.State: true, p.Cache: true, p.Data: false} {
		if HasMarker(dir) != want {
			t.Errorf("HasMarker(%s) = %v, want %v", dir, !want, want)
		}
	}
	if _, err := os.Stat(p.Config); err == nil {
		t.Error("EnsureMarkers created a dir")
	}
	if _, err := os.Stat(filepath.Join(p.Data, MarkerName)); err == nil {
		t.Error("a foreign dir was marked")
	}
	EnsureMarkers(p) // idempotent
}
