package flock

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestSecondLockIsBusyAfterDeadline(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.lock")
	l, err := TryLock(p, 0)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = TryLock(p, 30*time.Millisecond)
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v, want ErrBusy", err)
	}
	if d := time.Since(start); d < 25*time.Millisecond || d > time.Second {
		t.Errorf("waited %v", d)
	}
	l.Unlock()
	l2, err := TryLock(p, 0)
	if err != nil {
		t.Fatalf("lock after unlock: %v", err)
	}
	l2.Unlock()
}

func TestWaitClamped(t *testing.T) {
	if testing.Short() {
		t.Skip("waits the full clamp")
	}
	p := filepath.Join(t.TempDir(), "x.lock")
	l, _ := TryLock(p, 0)
	defer l.Unlock()
	start := time.Now()
	_, err := TryLock(p, time.Hour)
	if !errors.Is(err, ErrBusy) || time.Since(start) > MaxWait+time.Second {
		t.Fatalf("err=%v after %v", err, time.Since(start))
	}
}
