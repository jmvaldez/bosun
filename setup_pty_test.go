//go:build linux

package main

import (
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// ptyPair returns both ends of a new pty; the test types on master and the
// command under test gets slave as its terminal.
func ptyPair(t *testing.T) (master, slave *os.File) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no /dev/ptmx: %v", err)
	}
	t.Cleanup(func() { m.Close() })
	if err := unix.IoctlSetPointerInt(int(m.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Skip(err)
	}
	n, err := unix.IoctlGetInt(int(m.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Skip(err)
	}
	s, err := os.OpenFile("/dev/pts/"+strconv.Itoa(n), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { s.Close() })
	return m, s
}

// setupOnPTY runs `bosun setup` on a pty, types typed once the question is on
// screen, and returns the exit code and what the command printed after it
// erased the question.
func setupOnPTY(t *testing.T, env []string, typed string) (code int, after string) {
	t.Helper()
	master, slave := ptyPair(t)
	cmd := exec.Command(binPath, "setup")
	cmd.Env = env
	cmd.Stdin, cmd.Stdout = slave, slave
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	got := make(chan string, 64)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				got <- string(buf[:n])
			}
			if err != nil {
				close(got)
				return
			}
		}
	}()
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	deadline := time.After(20 * time.Second)
	for !strings.Contains(all.String(), "enter confirm") {
		select {
		case s := <-got:
			all.WriteString(s)
		case <-deadline:
			t.Fatalf("the question never appeared:\n%q", all.String())
		}
	}
	io.WriteString(master, typed)
	for {
		select {
		case s := <-got:
			all.WriteString(s)
		case err := <-waited:
			// Take what the command wrote just before it exited.
			for drained := false; !drained; {
				select {
				case s := <-got:
					all.WriteString(s)
				case <-time.After(200 * time.Millisecond):
					drained = true
				}
			}
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			}
			// Everything after the erase sequence is what the user is left with.
			_, after, _ = strings.Cut(all.String(), "\x1b[J\x1b[?25h")
			return code, strings.ReplaceAll(after, "\r\n", "\n")
		case <-deadline:
			t.Fatalf("setup did not exit:\n%q", all.String())
		}
	}
}

// D-31: cancelling the question ends setup with "Nothing changed." and no
// doctor output; an answer leaves a record of the question.
func TestSetupOnATerminalRecordsTheAnswerAndStopsOnCancel(t *testing.T) {
	env, root := isolated(t)
	shims(t, env, root)
	os.MkdirAll(root+"/home/.claude", 0o755)

	code, after := setupOnPTY(t, env, "\x1b")
	if code != 1 || strings.TrimSpace(after) != "Nothing changed." {
		t.Errorf("cancel: exit %d, output %q; want exit 1 and only Nothing changed.", code, after)
	}

	code, after = setupOnPTY(t, env, "n")
	if !strings.HasPrefix(after, "Show live session cost? \x1b[1mNo\x1b[0m\n") {
		t.Errorf("answered: exit %d, output %q; want the record line first", code, after)
	}
}
