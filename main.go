// bosun watches Claude Code agent sessions across tmux. main wires adapters and
// dispatches commands; the work lives in internal/.
package main

import (
	_ "embed"
	"io"
	"os"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/jmvaldez/bosun/internal/paths"
	"github.com/jmvaldez/bosun/internal/platform"
	"github.com/jmvaldez/bosun/internal/runner"
)

// VERSION is the single source for the plugin manifests; a test keeps plugin/
// and .claude-plugin/ in step with it, and the release workflow checks the tag.
//
//go:embed VERSION
var versionFile string

// version is stamped by release builds (-ldflags "-X main.version=1.2.3"). A
// build without it, such as `go build` or `make build`, falls back to the
// VERSION file marked as a development build with the VCS revision when Go
// recorded one.
var version string

func init() {
	if version == "" {
		version = devVersion(strings.TrimSpace(versionFile), debug.ReadBuildInfo)
	}
}

// devVersion builds "<base>-dev+<first 7 hex of vcs.revision>", or "<base>-dev"
// when the build recorded no revision of at least 7 characters.
func devVersion(base string, read func() (*debug.BuildInfo, bool)) string {
	if bi, ok := read(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 7 {
				return base + "-dev+" + s.Value[:7]
			}
		}
	}
	return base + "-dev"
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// app is what a command gets: the adapters, its streams, and the catch-up
// evaluation main runs for user-facing commands.
type app struct {
	ad      platform.Adapters
	in      io.Reader
	out     io.Writer
	errw    io.Writer
	catchUp func()
}

func run(args []string, in io.Reader, out, errw io.Writer) int {
	a := &app{ad: newAdapters(), in: in, out: out, errw: errw}
	a.catchUp = func() { a.evaluate(false) }
	return a.dispatch(args)
}

// newAdapters builds the one Adapters value handed to every command. The hook
// path runs it too, so it stays cheap: env reads plus, on Linux, at most one
// read of /proc/sys/kernel/osrelease for WSL detection.
func newAdapters() platform.Adapters {
	return platform.Adapters{
		Paths:   paths.Resolve(os.Getenv),
		Kind:    platform.Detect(runtime.GOOS, os.Getenv, os.ReadFile),
		Procs:   platform.NewProcs(),
		Runner:  runner.Exec{},
		Clock:   platform.SystemClock{},
		Spawner: platform.Detached{},
		Getenv:  os.Getenv,
	}
}
