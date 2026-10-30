package main

import (
	"fmt"
	"io"
)

// args walks one command's arguments and reports misuse the same way for every
// command: "bosun <cmd>: <flag> needs a value" or "unknown argument", exit 2.
// It is not flag.FlagSet, which stops at the first positional argument
// (`tag TEXT --session ID`) and does not fit the `--` passthrough in `run`.
type args struct {
	cmd  string
	list []string
	i    int // index of the current argument; -1 before the first next
	errw io.Writer
}

// args starts a cursor over one command's arguments.
func (a *app) args(cmd string, list []string) *args {
	return &args{cmd: cmd, list: list, i: -1, errw: a.errw}
}

// next advances to the following argument and returns it. It reports false at the end.
func (p *args) next() (string, bool) {
	if p.i+1 >= len(p.list) {
		return "", false
	}
	p.i++
	return p.list[p.i], true
}

// value returns the current flag's value and advances past it. A missing value
// prints "needs a value"; the caller then returns 2.
func (p *args) value() (string, bool) {
	if p.i+1 >= len(p.list) {
		fmt.Fprintf(p.errw, "bosun %s: %s needs a value\n", p.cmd, p.list[p.i])
		return "", false
	}
	p.i++
	return p.list[p.i], true
}

// rest returns everything after the current position and consumes it, for a `--` passthrough.
func (p *args) rest() []string {
	r := p.list[p.i+1:]
	p.i = len(p.list) - 1
	return r
}

// unknown reports the current argument as not understood and returns exit code 2.
func (p *args) unknown() int {
	fmt.Fprintf(p.errw, "bosun %s: unknown argument %q\n", p.cmd, p.list[p.i])
	return 2
}

// usage prints a usage line and returns exit code 2.
func (p *args) usage(line string) int {
	fmt.Fprintln(p.errw, "usage: "+line)
	return 2
}
