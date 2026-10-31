package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMissingFileGivesDefaults(t *testing.T) {
	c, res, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil || len(res.Unknown)+len(res.Problems) != 0 {
		t.Fatalf("err=%v res=%+v", err, res)
	}
	if !reflect.DeepEqual(c, Default()) {
		t.Error("not the defaults")
	}
}

func TestEmptyFileGivesDefaults(t *testing.T) {
	c, res, err := Parse(nil)
	if err != nil || len(res.Unknown) != 0 || !reflect.DeepEqual(c, Default()) {
		t.Fatalf("c=%+v res=%+v err=%v", c, res, err)
	}
}

func TestOverridesApply(t *testing.T) {
	c, res, err := Parse([]byte(`
[rules]
quiet = "90s"
daily_budget = 50
session_cost = 7.5

[task]
prefixes = ["ABC", "XYZ"]

[ui]
peek = false
`))
	if err != nil || len(res.Problems)+len(res.Unknown) != 0 {
		t.Fatalf("err=%v res=%+v", err, res)
	}
	if c.Rules.Quiet != 90*time.Second || c.Rules.DailyBudget != 50 || c.Rules.SessionCost != 7.5 {
		t.Errorf("rules = %+v", c.Rules)
	}
	if !reflect.DeepEqual(c.Task.Prefixes, []string{"ABC", "XYZ"}) || c.UI.Peek {
		t.Errorf("task=%+v ui=%+v", c.Task, c.UI)
	}
}

// LESSONS L-43: a typo must be reported, not ignored.
func TestUnknownKeyReported(t *testing.T) {
	c, res, err := Parse([]byte("[rules]\nquite = \"1m\"\n[rulez]\nx = 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"rules.quite", "rulez.x"}; !reflect.DeepEqual(res.Unknown, want) {
		t.Errorf("unknown = %v, want %v", res.Unknown, want)
	}
	if c.Rules.Quiet != Default().Rules.Quiet {
		t.Error("typo key must not change the real one")
	}
}

func TestBadValuesFallBackIndividually(t *testing.T) {
	c, res, err := Parse([]byte(`
[rules]
quiet = "soon"
session_cost = "ten"
subagent_runtime = "3m"
[spend]
day = "gmt"
[task]
key_regex = "(["
[pace]
min_days = 0
`))
	if err != nil {
		t.Fatal(err)
	}
	d := Default()
	if c.Rules.Quiet != d.Rules.Quiet || c.Rules.SessionCost != d.Rules.SessionCost ||
		c.Spend.Day != d.Spend.Day || c.Task.KeyRegex != d.Task.KeyRegex || c.Pace.MinDays != d.Pace.MinDays {
		t.Errorf("bad values leaked: %+v", c)
	}
	if c.Rules.SubagentRuntime != 3*time.Minute {
		t.Error("good sibling value was dropped")
	}
	if len(res.Problems) != 5 {
		t.Errorf("problems = %v", res.Problems)
	}
}

func TestSyntaxErrorReturnsDefaults(t *testing.T) {
	c, _, err := Parse([]byte("[rules\n"))
	if err == nil {
		t.Fatal("want error")
	}
	if !reflect.DeepEqual(c, Default()) {
		t.Error("want defaults on syntax error")
	}
}

func TestFormatRoundTrips(t *testing.T) {
	out := Format(Default(), Result{})
	c, res, err := Parse([]byte(out))
	if err != nil || len(res.Unknown)+len(res.Problems) != 0 {
		t.Fatalf("err=%v res=%+v\n%s", err, res, out)
	}
	if !reflect.DeepEqual(c, Default()) {
		t.Errorf("round trip changed the config:\n%s", out)
	}
}

func TestFormatCoversEveryDocumentedKey(t *testing.T) {
	out := Format(Default(), Result{})
	for _, k := range Keys() {
		_, name, _ := strings.Cut(k, ".")
		if !strings.Contains(out, name+" = ") {
			t.Errorf("missing %s", k)
		}
	}
	if n := len(Keys()); n != 37 { // SPEC lists 28; D-27 and the visual overhaul add tmux.spend_key, ui.glyphs, bar.style, privacy.show_text; D-34 adds the five handoff keys
		t.Errorf("have %d keys, want 37", n)
	}
}

func TestLoadReadsFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(p, []byte("[tmux]\npopup_key = \"P\"\n"), 0o600)
	c, _, err := Load(p)
	if err != nil || c.Tmux.PopupKey != "P" {
		t.Fatalf("c=%+v err=%v", c.Tmux, err)
	}
}

func TestHandoffKeys(t *testing.T) {
	c, res, err := Parse([]byte("[handoff]\nsuggest_at = 0\nprompt = \"write it\"\ndir = \"/tmp/h\"\ndeadline = \"2m\"\nauto = true\n"))
	if err != nil || len(res.Unknown)+len(res.Problems) != 0 {
		t.Fatalf("err=%v res=%+v", err, res)
	}
	want := Handoff{SuggestAt: 0, Prompt: "write it", Dir: "/tmp/h", Deadline: 2 * time.Minute, Auto: true}
	if c.Handoff != want {
		t.Errorf("handoff = %+v, want %+v", c.Handoff, want)
	}
	d := Default().Handoff
	if d.SuggestAt != 0.70 || d.Deadline != 5*time.Minute || d.Auto {
		t.Errorf("defaults = %+v", d)
	}
	_, res, _ = Parse([]byte("[handoff]\nsuggest_at = 1.5\n"))
	if len(res.Problems) != 1 {
		t.Errorf("suggest_at above 1 should be a problem: %+v", res)
	}
}
