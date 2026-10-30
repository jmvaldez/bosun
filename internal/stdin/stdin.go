// Package stdin reads a pipe without ever blocking past a deadline
// (LESSONS L-17).
package stdin

import (
	"io"
	"sync"
	"time"
)

// Read collects up to limit bytes from r and returns when r hits EOF, the
// limit is reached, or deadline passes, whichever comes first. A stalled writer
// therefore yields whatever arrived so far instead of holding the caller.
//
// os.Stdin on a pipe is blocking and cannot take a read deadline, so the read
// runs in a goroutine that is abandoned on timeout; the process is about to
// exit anyway.
func Read(r io.Reader, limit int, deadline time.Duration) []byte {
	var (
		mu   sync.Mutex
		buf  []byte
		done = make(chan struct{})
	)
	go func() {
		defer close(done)
		chunk := make([]byte, 4096)
		for {
			n, err := r.Read(chunk)
			mu.Lock()
			if room := limit - len(buf); n > room {
				n = room
			}
			buf = append(buf, chunk[:n]...)
			full := len(buf) >= limit
			mu.Unlock()
			if err != nil || full {
				return
			}
		}
	}()
	t := time.NewTimer(deadline)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
	}
	mu.Lock()
	defer mu.Unlock()
	return append([]byte(nil), buf...)
}
