package main

import (
	"testing"

	"github.com/jmvaldez/bosun/internal/live"
)

func TestFindSession(t *testing.T) {
	pane := live.Locator{Pane: "%1"}
	f := live.File{Sessions: map[string]*live.Session{
		"aaaa1111": {ID: "aaaa1111", Task: "ABC-1", Loc: pane},
		"aaaa2222": {ID: "aaaa2222", Task: "ABC-2", Loc: pane},
		"bbbb1111": {ID: "bbbb1111", Task: "XYZ-9", Loc: pane},
		"cccc1111": {ID: "cccc1111", Task: "DUP-1", Loc: pane},
		"cccc2222": {ID: "cccc2222", Task: "DUP-1", Loc: pane},
		"dddd1111": {ID: "dddd1111", Task: "GONE-1", Loc: pane, PaneGone: true},
	}}
	for _, c := range []struct{ target, want string }{
		{"aaaa1111", "aaaa1111"}, // exact id
		{"bbbb", "bbbb1111"},     // unique prefix
		{"xyz-9", "bbbb1111"},    // task, any case
		{"ABC-2", "aaaa2222"},
		{"aaaa", ""},   // ambiguous prefix
		{"DUP-1", ""},  // ambiguous task
		{"GONE-1", ""}, // a task only counts on a live pane
		{"nope", ""},
	} {
		s, err := findSession(f, c.target)
		switch {
		case c.want == "" && err == nil:
			t.Errorf("%q: found %s, want an error", c.target, s.ID)
		case c.want != "" && (err != nil || s.ID != c.want):
			t.Errorf("%q: got %v, %v; want %s", c.target, s, err, c.want)
		}
	}
}

func TestHandoffArgumentErrors(t *testing.T) {
	env, _ := isolated(t)
	if r := runBosun(t, env, nil, "handoff"); r.code != 2 {
		t.Errorf("no target: exit %d", r.code)
	}
	if r := runBosun(t, env, nil, "handoff", "x", "--bogus"); r.code != 2 {
		t.Errorf("unknown flag: exit %d", r.code)
	}
	if r := runBosun(t, env, nil, "handoff", "nope"); r.code != 1 {
		t.Errorf("unknown session: exit %d", r.code)
	}
}
