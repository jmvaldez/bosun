//go:build darwin

package platform

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Compile-checked from Linux; the live run is the macOS acceptance row
// (LESSONS L-11: the token must be non-zero and match ps to the second).
func TestStartTimeMatchesPS(t *testing.T) {
	cmd := exec.Command("sleep", "5")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	p := NewProcs()
	tok, err := p.StartToken(cmd.Process.Pid)
	if err != nil || tok == "" || strings.HasPrefix(tok, "0.") {
		t.Fatalf("token = %q err = %v", tok, err)
	}
	got, err := p.StartTime(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(cmd.Process.Pid)).Output()
	if err != nil {
		t.Fatal(err)
	}
	want, err := time.ParseInLocation("Mon Jan _2 15:04:05 2006", strings.TrimSpace(string(out)), time.Local)
	if err != nil {
		t.Fatalf("parse %q: %v", out, err)
	}
	if !got.Truncate(time.Second).Equal(want) {
		t.Fatalf("StartTime %v != ps lstart %v", got.Truncate(time.Second), want)
	}
}
