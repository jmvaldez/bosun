//go:build linux

package term

import (
	"os"
	"strconv"
	"testing"

	"golang.org/x/sys/unix"
)

// openPTY returns the slave end of a new pty; the master stays open until the
// test ends.
func openPTY(t *testing.T) (slave *os.File) {
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
	return s
}

func TestMakeRawAndRestore(t *testing.T) {
	s := openPTY(t)
	fd := int(s.Fd())
	before, _ := unix.IoctlGetTermios(fd, ioctlGetTermios)
	st, err := MakeRaw(fd)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if raw.Lflag&(unix.ECHO|unix.ICANON|unix.ISIG) != 0 {
		t.Errorf("not raw: lflag %#x", raw.Lflag)
	}
	if raw.Cc[unix.VMIN] != 1 || raw.Cc[unix.VTIME] != 0 {
		t.Errorf("cc = %v %v", raw.Cc[unix.VMIN], raw.Cc[unix.VTIME])
	}
	if err := st.Restore(); err != nil {
		t.Fatal(err)
	}
	after, _ := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if *after != *before {
		t.Errorf("termios not restored:\n%+v\n%+v", before, after)
	}
}

func TestSize(t *testing.T) {
	s := openPTY(t)
	if err := unix.IoctlSetWinsize(int(s.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Col: 97, Row: 31}); err != nil {
		t.Skip(err)
	}
	c, r, err := Size(int(s.Fd()))
	if err != nil || c != 97 || r != 31 {
		t.Errorf("size = %d x %d, %v", c, r, err)
	}
}
