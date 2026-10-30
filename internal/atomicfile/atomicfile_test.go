package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReplacesAndLeavesNoTemp(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "f.json")
	for _, body := range []string{"one", "two"} {
		if err := Write(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := os.ReadFile(p)
	if string(got) != "two" {
		t.Fatalf("got %q", got)
	}
	ents, _ := os.ReadDir(filepath.Dir(p))
	if len(ents) != 1 {
		t.Fatalf("temp file left behind: %v", ents)
	}
	fi, _ := os.Stat(p)
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("perm = %v", fi.Mode().Perm())
	}
}
