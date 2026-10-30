package testguard

import (
	"os"
	"strings"
	"testing"

	"github.com/jmvaldez/bosun/internal/paths"
)

func TestCheckPathsFailsUnderTheRealHome(t *testing.T) {
	home := "/home/me"
	bad := paths.Resolve(func(k string) string {
		if k == "HOME" {
			return home
		}
		return ""
	})
	err := CheckPaths(bad, home, "/tmp")
	if err == nil || !strings.Contains(err.Error(), "under the real HOME") {
		t.Fatalf("err = %v; default dirs are under the home", err)
	}
	ok := paths.Paths{Config: "/tmp/g/config", State: "/tmp/g/state", Cache: "/tmp/g/cache", Data: "/tmp/g/data"}
	if err := CheckPaths(ok, home, "/tmp"); err != nil {
		t.Errorf("temp dirs refused: %v", err)
	}
	// A temp dir that is itself under home is still the tests' own.
	inHome := paths.Paths{State: home + "/tmp/g/state"}
	if err := CheckPaths(inHome, home, home+"/tmp"); err != nil {
		t.Errorf("tmp under home refused: %v", err)
	}
	if err := CheckPaths(paths.Paths{State: home + "/.local/state/bosun"}, home, "/tmp"); err == nil {
		t.Error("the real state dir was accepted")
	}
}

func TestSnapshotSeesACreatedDir(t *testing.T) {
	d := t.TempDir() + "/bosun"
	before := snapshot([]string{d})
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := created(before, snapshot([]string{d})); len(got) != 1 || got[0] != d {
		t.Errorf("created = %v; a dir created under a watched path went unnoticed", got)
	}
	mid := snapshot([]string{d})
	os.WriteFile(d+"/x", []byte("x"), 0o600)
	if got := created(mid, snapshot([]string{d})); len(got) != 1 || got[0] != d+"/x" {
		t.Errorf("created = %v; a file written into a watched dir went unnoticed", got)
	}
}

// A live bosun install writes to the real dirs while a package's tests run;
// flagging that made `go test ./...` fail on any machine running bosun.
func TestCreatedIgnoresALiveInstall(t *testing.T) {
	d := t.TempDir() + "/bosun"
	sess := d + "/sessions/e75929e2-11f1-4984-b073-8fb5ee32a930"
	os.MkdirAll(d+"/sessions", 0o700)
	if err := os.MkdirAll(d+"/locks", 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(d+"/live.json", []byte("{}"), 0o600)
	before := snapshot([]string{d})

	os.WriteFile(d+"/live.json", []byte(`{"a":1}`), 0o600)
	os.MkdirAll(sess, 0o700)
	os.WriteFile(sess+"/events.jsonl", nil, 0o600)
	os.WriteFile(d+"/.live.json.tmp123", nil, 0o600)
	os.WriteFile(d+"/locks/ingest.lock", nil, 0o600)
	os.WriteFile(d+"/bosun.db-wal", nil, 0o600)
	if got := created(before, snapshot([]string{d})); len(got) != 0 {
		t.Errorf("live install writes flagged: %v", got)
	}

	os.MkdirAll(d+"/sessions/s1", 0o700)
	if got := created(before, snapshot([]string{d})); len(got) != 1 {
		t.Errorf("created = %v; a test session dir went unnoticed", got)
	}
}
