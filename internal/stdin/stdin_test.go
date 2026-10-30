package stdin

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

func TestReadToEOF(t *testing.T) {
	got := Read(strings.NewReader("hello"), 1024, time.Second)
	if string(got) != "hello" {
		t.Fatalf("got %q", got)
	}
}

func TestReadCapsAtLimit(t *testing.T) {
	got := Read(bytes.NewReader(bytes.Repeat([]byte("x"), 10<<20)), 64<<10, time.Second)
	if len(got) != 64<<10 {
		t.Fatalf("len = %d", len(got))
	}
}

func TestReadReturnsPartialOnStall(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	go pw.Write([]byte(`{"a":1}`))
	start := time.Now()
	got := Read(pr, 1024, 100*time.Millisecond)
	if string(got) != `{"a":1}` {
		t.Fatalf("got %q", got)
	}
	if d := time.Since(start); d > 300*time.Millisecond {
		t.Fatalf("took %v", d)
	}
}
