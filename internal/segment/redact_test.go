package segment

import (
	"strings"
	"testing"
)

// D-31: sessions live cost hides leave the figure alone and add a short note;
// with nothing else known the bar shows a lone dash.
func TestSpendPartWithHiddenSessions(t *testing.T) {
	for _, tt := range []struct {
		name string
		sp   Spend
		want string
	}{
		{"note after the figure", Spend{USD: 14.2, Redacted: 2}, "$14.20 + 2 —"},
		{"note keeps est and cap", Spend{USD: 3, EstShare: 0.1, Cap: 50, Redacted: 1}, "$3.00/$50 est. + 1 —"},
		{"nothing known is a dash", Spend{Redacted: 3}, "—#[nobold]"},
	} {
		if got := Tmux(Counts{}, tt.sp); !strings.Contains(got, tt.want) {
			t.Errorf("%s: compact %q lacks %q", tt.name, got, tt.want)
		}
	}
	bar := Bar(Counts{Sessions: 1}, Spend{USD: 14.2, Cap: 50, Redacted: 2}, Opts{})
	if !strings.Contains(bar, "$14.20 / $50 + 2 —") {
		t.Errorf("bar %q", bar)
	}
	if got := Bar(Counts{}, Spend{Redacted: 2}, Opts{}); !strings.Contains(got, " — ") || strings.Contains(got, "$") {
		t.Errorf("a lone dash expected: %q", got)
	}
}
