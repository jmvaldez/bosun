//go:build linux || darwin

// Package term puts the terminal in raw mode and decodes keys for the popup.
package term

import "golang.org/x/sys/unix"

// State holds the terminal settings to restore.
type State struct {
	fd   int
	orig unix.Termios
}

// MakeRaw switches fd to raw mode with the typed termios calls (no hand-coded
// request numbers beyond the per-OS constants) and returns the state to restore.
// Output post-processing stays on so a stray "\n" still returns the carriage.
func MakeRaw(fd int) (*State, error) {
	t, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return nil, err
	}
	st := &State{fd: fd, orig: *t}
	raw := *t
	raw.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	raw.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	raw.Cflag &^= unix.CSIZE | unix.PARENB
	raw.Cflag |= unix.CS8
	raw.Cc[unix.VMIN] = 1
	raw.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, ioctlSetTermios, &raw); err != nil {
		return nil, err
	}
	return st, nil
}

func (s *State) Restore() error {
	return unix.IoctlSetTermios(s.fd, ioctlSetTermios, &s.orig)
}

func Size(fd int) (cols, rows int, err error) {
	ws, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ)
	if err != nil {
		return 0, 0, err
	}
	return int(ws.Col), int(ws.Row), nil
}
