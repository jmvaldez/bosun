package main

import (
	"encoding/json"
	"fmt"
	"github.com/jmvaldez/bosun/internal/paths"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func tree(root string) string {
	var lines []string
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil {
			fi, _ := d.Info()
			lines = append(lines, fmt.Sprintf("%s %v %d %d", p, fi.Mode(), fi.Size(), fi.ModTime().UnixNano()))
		}
		return nil
	})
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// SPEC acceptance: doctor leaves the filesystem unchanged (LESSONS L-44), on a
// machine with nothing installed and on one with state.
func TestDoctorCommandIsReadOnly(t *testing.T) {
	env, root := isolated(t)
	before := tree(root)
	r := runBosun(t, env, nil, "doctor", "--json")
	if after := tree(root); after != before {
		t.Errorf("doctor changed the tree:\n--- before\n%s\n--- after\n%s", before, after)
	}
	var checks []struct{ Name, Status string }
	if err := json.Unmarshal(r.out, &checks); err != nil {
		t.Fatalf("%v\n%s", err, r.out)
	}
	if r.code == 0 {
		t.Error("an empty install should fail the doctor (exit non-zero)")
	}
	for _, d := range []string{"state", "cache", "data"} {
		if _, err := os.Stat(root + "/" + d); !os.IsNotExist(err) {
			t.Errorf("doctor created %s", d)
		}
	}
	// With a state dir that has content.
	writeTranscript(t, root, "aaaaaaaa-0000-0000-0000-00000000000a", 1, nowish())
	runBosun(t, env, nil, "refresh")
	before = tree(root)
	runBosun(t, env, nil, "doctor")
	if after := tree(root); after != before {
		t.Errorf("doctor changed a populated tree:\n--- before\n%s\n--- after\n%s", before, after)
	}
	text := runBosun(t, env, nil, "doctor")
	if !strings.Contains(string(text.out), "prompt-filter") || !strings.Contains(string(text.out), "checks,") {
		t.Errorf("text report:\n%s", text.out)
	}
}

func TestUninstallCommandPrintsAndPurgeIsSafe(t *testing.T) {
	env, root := isolated(t)
	os.MkdirAll(root+"/state", 0o755)
	os.WriteFile(root+"/state/x", []byte("x"), 0o644)
	r := runBosun(t, env, nil, "uninstall")
	if r.code != 0 || !strings.Contains(string(r.out), "1. Remove the plugin") || !strings.Contains(string(r.out), "4. Delete the binary") {
		t.Fatalf("exit %d\n%s", r.code, r.out)
	}
	if _, err := os.Stat(root + "/state/x"); err != nil {
		t.Error("uninstall deleted state without --purge")
	}
	// An explicit dir that bosun never marked is refused (review finding 9).
	if r := runBosun(t, env, nil, "uninstall", "--purge"); r.code == 0 {
		t.Error("--purge of an unmarked dir exited 0")
	}
	if _, err := os.Stat(root + "/state/x"); err != nil {
		t.Fatal("--purge deleted a dir without the bosun marker")
	}
	// BOSUN_STATE_DIR is explicit and marked, so --purge may delete it; config stays.
	os.WriteFile(root+"/state/"+paths.MarkerName, nil, 0o600)
	if r := runBosun(t, env, nil, "uninstall", "--purge"); r.code != 0 {
		t.Fatalf("--purge: %d %s", r.code, r.out)
	}
	if _, err := os.Stat(root + "/state"); !os.IsNotExist(err) {
		t.Error("--purge left the state dir")
	}
	if _, err := os.Stat(root + "/config/config.toml"); err != nil {
		t.Error("--purge deleted the config")
	}
	// A symlinked state dir is refused and its target survives.
	target := root + "/precious"
	os.MkdirAll(target, 0o755)
	os.WriteFile(target+"/keep", []byte("x"), 0o644)
	os.Symlink(target, root+"/state")
	if r := runBosun(t, env, nil, "uninstall", "--purge"); r.code == 0 {
		t.Error("--purge of a symlinked dir exited 0")
	}
	if _, err := os.Stat(target + "/keep"); err != nil {
		t.Error("deleted through a symlink")
	}
}

func nowish() time.Time { return time.Now().Add(-time.Minute) }
