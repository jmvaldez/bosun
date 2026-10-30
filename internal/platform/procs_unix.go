//go:build linux || darwin

package platform

import (
	"errors"

	"golang.org/x/sys/unix"
)

// ErrNoProcess is returned when a pid does not exist or is not valid.
var ErrNoProcess = errors.New("platform: no such process")

// pidExists treats EPERM as alive: the process is there, just not ours.
// Non-positive pids are rejected: kill takes 0 as the caller's process group,
// -1 as every process it may signal, and -n as group n.
func pidExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := unix.Kill(pid, 0)
	return err == nil || err == unix.EPERM
}

// alive layers the start-token check over pidExists to reject pid reuse.
func alive(pid int, token string, startToken func(int) (string, error)) bool {
	if !pidExists(pid) {
		return false
	}
	if token == "" {
		return true
	}
	cur, err := startToken(pid)
	return err == nil && cur == token
}
