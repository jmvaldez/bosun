// Package runner runs the child processes bosun waits on, each with a deadline
// (LESSONS L-12). Two callers start children outside it: statusline's
// passthrough (own deadline) and platform.Detached (never waits).
package runner

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// Runner runs a command to completion and returns its stdout.
type Runner interface {
	Run(timeout time.Duration, name string, args ...string) ([]byte, error)
}

// InputRunner is a Runner that can also feed the child's standard input.
type InputRunner interface {
	Runner
	RunInput(timeout time.Duration, stdin string, name string, args ...string) ([]byte, error)
}

// Streamer reads a long-running command's output line by line. onLine returns
// true to stop; the command is killed when it does, or when timeout elapses.
type Streamer interface {
	Stream(timeout time.Duration, name string, args []string, onLine func(string) bool) error
}

// Exec is the real Runner.
type Exec struct{}

// Run kills the child when timeout elapses. WaitDelay bounds the wait for the
// output pipes, which a grandchild holding stdout would otherwise keep open.
func (e Exec) Run(timeout time.Duration, name string, args ...string) ([]byte, error) {
	return e.RunInput(timeout, "", name, args...)
}

// RunInput is Run with stdin fed to the child; empty stdin means none.
func (Exec) RunInput(timeout time.Duration, stdin string, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &limitedWriter{w: &errb, n: 2048}
	cmd.WaitDelay = 500 * time.Millisecond
	err := cmd.Run()
	if err != nil {
		// Keep the tool's own message (git says why it refused); exit status alone is no help.
		if msg := strings.TrimSpace(errb.String()); msg != "" {
			err = fmt.Errorf("%w: %s", err, msg)
		}
	}
	return out.Bytes(), err
}

// limitedWriter keeps the first n bytes and discards the rest. It always reports
// the whole write as done: a short write would fail exec's stderr copy and with
// it the Run.
type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n > 0 {
		k := min(len(p), l.n)
		l.w.Write(p[:k])
		l.n -= k
	}
	return len(p), nil
}

// Stream implements Streamer: used only by the short-lived click listener, whose
// hard timeout is the context deadline.
func (Exec) Stream(timeout time.Duration, name string, args []string, onLine func(string) bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.WaitDelay = 500 * time.Millisecond
	if err := cmd.Start(); err != nil {
		return err
	}
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if onLine(sc.Text()) {
			break
		}
	}
	cancel() // stop reading and kill the child, whether we stopped early or timed out
	_ = cmd.Wait()
	return nil
}
