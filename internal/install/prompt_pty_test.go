//go:build linux

package install

import (
	"bytes"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// openPTY returns both ends of a new pty; the test types on master and the
// prompt reads from slave.
func openPTY(t *testing.T) (master, slave *os.File) {
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

// askOnPTY runs AskLiveCost on a real pty and types typed at it.
func askOnPTY(t *testing.T, def bool, typed string) (bool, string, error) {
	t.Helper()
	master, slave := openPTY(t)
	type result struct {
		yes bool
		err error
	}
	done := make(chan result, 1)
	var screen bytes.Buffer
	go func() {
		yes, err := AskLiveCost(slave, slave, def, false)
		done <- result{yes, err}
	}()
	// The prompt sets raw mode before drawing; wait for the screen, then type.
	read := make(chan string, 1)
	go func() {
		buf := make([]byte, 4096)
		n, _ := master.Read(buf)
		read <- string(buf[:n])
	}()
	select {
	case s := <-read:
		screen.WriteString(s)
	case <-time.After(5 * time.Second):
		t.Fatal("the prompt drew nothing")
	}
	master.WriteString(typed)
	select {
	case r := <-done:
		return r.yes, screen.String(), r.err
	case <-time.After(5 * time.Second):
		t.Fatal("the prompt did not return")
	}
	return false, "", nil
}

func TestPromptOnARealTerminal(t *testing.T) {
	yes, screen, err := askOnPTY(t, true, "\x1b[B\r") // down arrow, enter
	if yes || err != nil {
		t.Errorf("down+enter = %v, %v", yes, err)
	}
	if !strings.Contains(screen, "Show live session cost?") {
		t.Errorf("screen:\n%q", screen)
	}
	if yes, _, err := askOnPTY(t, false, "y"); !yes || err != nil {
		t.Errorf("y = %v, %v", yes, err)
	}
	if _, _, err := askOnPTY(t, true, "\x03"); !errors.Is(err, ErrAborted) {
		t.Errorf("ctrl-c: %v", err)
	}
	if _, _, err := askOnPTY(t, true, "\x1b"); !errors.Is(err, ErrAborted) {
		t.Errorf("esc: %v", err)
	}
}
