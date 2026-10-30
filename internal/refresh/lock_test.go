package refresh

import (
	"testing"

	"github.com/jmvaldez/bosun/internal/flock"
)

func heldLock(t *testing.T, path string) (release func()) {
	t.Helper()
	l, err := flock.TryLock(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	return l.Unlock
}
