package ledger

import (
	"testing"

	"github.com/jmvaldez/bosun/internal/flock"
)

func holdLock(t *testing.T, path string) func() {
	t.Helper()
	l, err := flock.TryLock(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	return l.Unlock
}
