package segment

import (
	"os"
	"strings"
	"testing"
)

func TestEmptyWhenNothingToShow(t *testing.T) {
	if got := Tmux(Counts{}, Spend{}); got != "" {
		t.Errorf("got %q", got)
	}
	if got := Tmux(Counts{Sessions: 2, Idle: 2}, Spend{}); got != "" {
		t.Errorf("idle-only agents show nothing, got %q", got)
	}
}

func TestPillColours(t *testing.T) {
	if got := Tmux(Counts{NeedsYou: 2}, Spend{}); !strings.Contains(got, "#[fg=colour232]#[bg=colour220] ! 2 ") {
		t.Errorf("1-2 should be yellow: %q", got)
	}
	if got := Tmux(Counts{NeedsYou: 3}, Spend{}); !strings.Contains(got, "#[bg=colour208] ! 3 ") {
		t.Errorf("3+ should be orange: %q", got)
	}
}

func TestFullSegment(t *testing.T) {
	got := Tmux(Counts{NeedsYou: 1, Runaway: 1, Done: 2, Working: 3}, Spend{USD: 14.2})
	for _, want := range []string{" ! 1 ", " $! 1 ", "✓2", "●3"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "#{") || strings.Count(got, "#[") != strings.Count(got, "]") {
		t.Errorf("malformed style blocks: %q", got)
	}
	// One attribute per block: no commas anywhere in a style block.
	for _, blk := range strings.Split(got, "#[")[1:] {
		if strings.Contains(blk[:strings.Index(blk, "]")], ",") {
			t.Errorf("comma in style block: %q", blk)
		}
	}
}

func TestSpendPart(t *testing.T) {
	tests := []struct {
		name string
		sp   Spend
		want []string
		not  []string
	}{
		{"plain", Spend{USD: 14.2}, []string{"$14.20", "fg=colour245"}, []string{"/$", "est."}},
		{"estimated share", Spend{USD: 3, EstShare: 0.1}, []string{"$3.00 est."}, nil},
		{"budget", Spend{USD: 15.82, Cap: 50}, []string{"$15.82/$50", "fg=colour245"}, []string{"colour196"}},
		{"over budget is bold red", Spend{USD: 51, Cap: 50}, []string{"fg=colour196", "#[bold]"}, nil},
		{"pace amber", Spend{USD: 61.75, Level: "amber"}, []string{"fg=colour214"}, nil},
		{"pace alert", Spend{USD: 88, Level: "alert"}, []string{"fg=colour196", "#[bold]"}, nil},
	}
	for _, tt := range tests {
		got := Tmux(Counts{}, tt.sp)
		for _, w := range tt.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: %q lacks %q", tt.name, got, w)
			}
		}
		for _, n := range tt.not {
			if strings.Contains(got, n) {
				t.Errorf("%s: %q contains %q", tt.name, got, n)
			}
		}
	}
	// Spend alone keeps the bar non-empty even with no agents.
	if Tmux(Counts{}, Spend{USD: 0.01}) == "" {
		t.Error("spend with no agents should still show")
	}
}

// Text must account for every session: the buckets add up to the total.
func TestTextCountsAddUp(t *testing.T) {
	c := Counts{Sessions: 8, NeedsYou: 1, Runaway: 1, Done: 2, Working: 2, Quiet: 1, Idle: 1}
	want := "8 sessions: 1 needs-you, 1 runaway, 2 done, 2 working, 1 quiet, 1 idle"
	if got := Text(c); got != want {
		t.Errorf("Text = %q, want %q", got, want)
	}
}

// The owner's mock (build-log/mockups/1-bar-notices.png): labeled colored
// blocks, zero counts hidden, spend against the budget, then the counts.
func TestBarMatchesTheMock(t *testing.T) {
	c := Counts{Sessions: 5, Subs: 4, NeedsYou: 2, Runaway: 1}
	got := Bar(c, Spend{USD: 23.40, Cap: 50}, Opts{})
	golden(t, "bar_mock.golden", got)
	plain := stripStyles(got)
	if plain != " ⚑ 2 waiting  ▲ 1 runaway  $23.40 / $50  5 sessions · 9 agents " {
		t.Errorf("visible text = %q", plain)
	}
	if strings.Contains(plain, "done") {
		t.Error("a zero count was shown")
	}
	if p := stripStyles(Bar(Counts{Sessions: 1, Done: 1}, Spend{}, Opts{})); p != " ✓ 1 done  1 session · 1 agent " {
		t.Errorf("done only = %q", p)
	}
	if Bar(Counts{}, Spend{}, Opts{}) != "" {
		t.Error("an empty bar must be empty")
	}
	// Glyph choice and the compact style.
	if p := stripStyles(Bar(c, Spend{}, Opts{ASCII: true})); !strings.Contains(p, "! 2 waiting") || !strings.Contains(p, "^ 1 runaway") || strings.ContainsAny(p, "⚑▲✓") {
		t.Errorf("ascii = %q", p)
	}
	if got := Bar(Counts{NeedsYou: 2}, Spend{}, Opts{Compact: true}); got != Tmux(Counts{NeedsYou: 2}, Spend{}) || !strings.Contains(got, " ! 2 ") {
		t.Errorf("compact = %q", got)
	}
	// One attribute per style block, no FE0F anywhere.
	for _, blk := range strings.Split(got, "#[")[1:] {
		if strings.Contains(blk[:strings.Index(blk, "]")], ",") {
			t.Errorf("comma in style block: %q", blk)
		}
	}
	if strings.ContainsRune(got, '️') {
		t.Error("variation selector in the bar")
	}
}

func stripStyles(s string) string {
	var b strings.Builder
	for len(s) > 0 {
		i := strings.Index(s, "#[")
		if i < 0 {
			b.WriteString(s)
			break
		}
		b.WriteString(s[:i])
		j := strings.Index(s[i:], "]")
		s = s[i+j+1:]
	}
	return b.String()
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	p := "testdata/" + name
	if os.Getenv("BOSUN_UPDATE_GOLDEN") != "" {
		os.MkdirAll("testdata", 0o755)
		os.WriteFile(p, []byte(got+"\n"), 0o644)
	}
	want, err := os.ReadFile(p)
	if err != nil || strings.TrimRight(string(want), "\n") != got {
		t.Errorf("%s differs (BOSUN_UPDATE_GOLDEN=1 rewrites it)\n got %s\nwant %s", name, got, want)
	}
}
