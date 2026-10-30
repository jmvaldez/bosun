//go:build linux

package platform

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseStartTicksAwkwardComm(t *testing.T) {
	// comm "a b) c" defeats a naive split on spaces.
	stat := "4242 (a b) c) S 1 4242 4242 0 -1 4194304 100 0 0 0 1 2 0 0 20 0 1 0 987654 1000 10 18446744073709551615"
	got, err := parseStartTicks([]byte(stat))
	if err != nil {
		t.Fatal(err)
	}
	if got != 987654 {
		t.Fatalf("got %d, want 987654", got)
	}
}

func TestParseStartTicksMalformed(t *testing.T) {
	for _, s := range []string{"", "no parens here", "1 (x) S 1 2"} {
		if _, err := parseStartTicks([]byte(s)); err == nil {
			t.Errorf("%q: want error", s)
		}
	}
}

func TestStartTimeMatchesPS(t *testing.T) {
	cmd := exec.Command("sleep", "5")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	got, err := NewProcs().StartTime(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(cmd.Process.Pid)).Output()
	if err != nil {
		t.Skipf("ps unavailable: %v", err)
	}
	want, err := time.ParseInLocation("Mon Jan _2 15:04:05 2006", strings.TrimSpace(string(out)), time.Local)
	if err != nil {
		t.Fatalf("parse %q: %v", out, err)
	}
	if !got.Truncate(time.Second).Equal(want) {
		t.Fatalf("StartTime %v != ps lstart %v", got.Truncate(time.Second), want)
	}
}

func TestParseParentAwkwardComm(t *testing.T) {
	// ppid is the second field after the last ')'; here comm is "a b) c".
	dir := t.TempDir()
	os.MkdirAll(dir+"/77", 0o755)
	os.WriteFile(dir+"/77/stat", []byte("77 (a b) c) S 31337 77 77 0 -1 4194304"), 0o644)
	got, err := linuxProcs{procDir: dir}.Parent(77)
	if err != nil || got != 31337 {
		t.Errorf("got %d, %v", got, err)
	}
}
