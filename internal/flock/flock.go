// Package flock takes advisory locks that never wait forever (LESSONS L-12).
package flock

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// MaxWait is the hard ceiling on any lock wait.
const MaxWait = 2 * time.Second

// ErrBusy means another process held the lock for the whole wait.
var ErrBusy = errors.New("flock: busy")

// Lock is a held lock. Release it with Unlock.
type Lock struct{ f *os.File }

// TryLock locks path with LOCK_NB, polling until wait elapses. wait of zero
// tries once; anything above MaxWait is clamped.
func TryLock(path string, wait time.Duration) (*Lock, error) {
	if wait > MaxWait {
		wait = MaxWait
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return &Lock{f: f}, nil
		}
		if err != unix.EWOULDBLOCK && err != unix.EINTR {
			f.Close()
			return nil, err
		}
		if !time.Now().Before(deadline) {
			f.Close()
			return nil, ErrBusy
		}
		time.Sleep(time.Millisecond)
	}
}

// Unlock closes the file, which drops the flock.
func (l *Lock) Unlock() { l.f.Close() }
