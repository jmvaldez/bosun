package main

import (
	"bytes"
	"reflect"
	"testing"
)

func TestArgsCursor(t *testing.T) {
	cases := []struct {
		name     string
		in       []string
		wantSeen []string // each argument next returned, flag values folded in as "flag=value"
		wantErr  string
		wantRest []string
	}{
		{name: "flag with value", in: []string{"--by", "task"}, wantSeen: []string{"--by=task"}},
		{name: "missing value", in: []string{"--since"}, wantErr: "bosun t: --since needs a value\n"},
		{name: "positional between flags", in: []string{"--clear", "word", "--session", "x"}, wantSeen: []string{"--clear", "word", "--session=x"}},
		{name: "double dash", in: []string{"--task", "a", "--", "-p", "hi"}, wantSeen: []string{"--task=a", "--"}, wantRest: []string{"-p", "hi"}},
		{name: "unknown", in: []string{"--bogus"}, wantErr: "bosun t: unknown argument \"--bogus\"\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var errw bytes.Buffer
			p := (&app{errw: &errw}).args("t", c.in)
			var seen, rest []string
		loop:
			for {
				arg, ok := p.next()
				if !ok {
					break
				}
				switch arg {
				case "--by", "--since", "--session", "--task":
					v, ok := p.value()
					if !ok {
						break loop
					}
					seen = append(seen, arg+"="+v)
				case "--":
					seen = append(seen, arg)
					rest = p.rest()
				case "--clear":
					seen = append(seen, arg)
				default:
					if arg[0] == '-' {
						if rc := p.unknown(); rc != 2 {
							t.Errorf("unknown returned %d, want 2", rc)
						}
						break loop
					}
					seen = append(seen, arg)
				}
			}
			if !reflect.DeepEqual(seen, c.wantSeen) {
				t.Errorf("seen = %q, want %q", seen, c.wantSeen)
			}
			if errw.String() != c.wantErr {
				t.Errorf("stderr = %q, want %q", errw.String(), c.wantErr)
			}
			if !reflect.DeepEqual(rest, c.wantRest) {
				t.Errorf("rest = %q, want %q", rest, c.wantRest)
			}
		})
	}
}

func TestArgsUsage(t *testing.T) {
	var errw bytes.Buffer
	p := (&app{errw: &errw}).args("t", nil)
	if rc := p.usage("bosun t X"); rc != 2 || errw.String() != "usage: bosun t X\n" {
		t.Errorf("usage = %d %q", rc, errw.String())
	}
}

func TestCommandsRejectAFlagWithoutAValue(t *testing.T) {
	for _, tt := range []struct {
		name string
		run  func(a *app, args []string) int
		args []string
		want string
	}{
		{"jump", (*app).jump, []string{"--client"}, "bosun jump: --client needs a value\n"},
		{"install", (*app).install, []string{"--repo"}, "bosun install: --repo needs a value\n"},
		{"setup", (*app).setup, []string{"--repo"}, "bosun setup: --repo needs a value\n"},
		{"backfill", (*app).backfill, []string{"--since"}, "bosun backfill: --since needs a value\n"},
		{"run", (*app).runTask, []string{"--task"}, "bosun run: --task needs a value\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var errw bytes.Buffer
			if code := tt.run(&app{errw: &errw}, tt.args); code != 2 || errw.String() != tt.want {
				t.Errorf("got %d %q, want 2 %q", code, errw.String(), tt.want)
			}
		})
	}
}
