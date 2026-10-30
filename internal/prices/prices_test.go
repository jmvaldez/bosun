package prices

import (
	"math"
	"strings"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// Most specific prefix first: any entry whose prefix is a prefix of an earlier
// entry would shadow it, so the longer one must come first (LESSONS L-34).
func TestTableOrdering(t *testing.T) {
	ps := prefixes()
	for i, a := range ps {
		for j, b := range ps {
			if i < j && strings.HasPrefix(b, a) && a != b {
				t.Errorf("%q (index %d) shadows the more specific %q (index %d)", a, i, b, j)
			}
		}
		for j := i + 1; j < len(ps); j++ {
			if ps[j] == a {
				t.Errorf("duplicate prefix %q", a)
			}
		}
	}
}

// Each entry's rates against the pricing page, spelled out so a transcription
// error shows up as a failing line, not a silent shift.
func TestRatesMatchPricingPage(t *testing.T) {
	want := map[string]Rate{
		"claude-fable-5-1":  {10, 50, 12.50, 20, 0.25},
		"claude-fable-5":    {10, 50, 12.50, 20, 1},
		"claude-mythos-5-1": {10, 50, 12.50, 20, 0.25},
		"claude-mythos-5":   {10, 50, 12.50, 20, 1},
		"claude-opus-5-5":   {4, 20, 5, 8, 0.20},
		"claude-opus-5":     {5, 25, 6.25, 10, 0.50},
		"claude-opus-4-8":   {5, 25, 6.25, 10, 0.50},
		"claude-opus-4-7":   {5, 25, 6.25, 10, 0.50},
		"claude-opus-4-6":   {5, 25, 6.25, 10, 0.50},
		"claude-opus-4-5":   {5, 25, 6.25, 10, 0.50},
		"claude-opus-4-1":   {15, 75, 18.75, 30, 1.50},
		"claude-opus-4":     {15, 75, 18.75, 30, 1.50},
		"claude-sonnet-5-5": {2, 10, 2.50, 4, 0.10},
		"claude-sonnet-5":   {2, 10, 2.50, 4, 0.20},
		"claude-sonnet-4-6": {3, 15, 3.75, 6, 0.30},
		"claude-sonnet-4-5": {3, 15, 3.75, 6, 0.30},
		"claude-sonnet-4":   {3, 15, 3.75, 6, 0.30},
		"claude-haiku-5-5":  {0.10, 0.50, 0.125, 0.20, 0.01},
		"claude-haiku-4-5":  {1, 5, 1.25, 2, 0.10},
		"claude-3-5-haiku":  {0.80, 4, 1, 1.60, 0.08},
		"claude-haiku-3-5":  {0.80, 4, 1, 1.60, 0.08},
	}
	if len(want) != len(prefixes()) {
		t.Errorf("table has %d entries, test lists %d", len(prefixes()), len(want))
	}
	for model, w := range want {
		got, ok := rateFor(model)
		if !ok || got != w {
			t.Errorf("%s = %+v (known %v), want %+v", model, got, ok, w)
		}
	}
}

// Cache write tiers have their own rates (1 h is not the 5 m price) and the
// cache read multiple differs by model (0.05x input on Sonnet 5.5, 0.1x on
// Sonnet 5).
func TestCacheTiersPricedSeparately(t *testing.T) {
	u := Usage{CacheWrite5: 1_000_000}
	if got := Cost("claude-sonnet-5-5", u).USD; !near(got, 2.50) {
		t.Errorf("5m write = %v", got)
	}
	if got := Cost("claude-sonnet-5-5", Usage{CacheWrite1: 1_000_000}).USD; !near(got, 4) {
		t.Errorf("1h write = %v", got)
	}
	if got := Cost("claude-sonnet-5-5", Usage{CacheRead: 1_000_000}).USD; !near(got, 0.10) {
		t.Errorf("5.5 read = %v", got)
	}
	if got := Cost("claude-sonnet-5", Usage{CacheRead: 1_000_000}).USD; !near(got, 0.20) {
		t.Errorf("sonnet 5 read = %v", got)
	}
}

func TestDatedIdsMatchByPrefix(t *testing.T) {
	r := Cost("claude-haiku-4-5-20251001", Usage{Input: 1_000_000, Output: 1_000_000})
	if r.Estimated || !near(r.USD, 6) {
		t.Errorf("got %+v", r)
	}
	if got := Cost("claude-opus-4-1-20250805", Usage{Output: 1_000_000}).USD; !near(got, 75) {
		t.Errorf("opus 4.1 = %v", got)
	}
	// claude-opus-5-5 must not be priced as claude-opus-5.
	if got := Cost("claude-opus-5-5", Usage{Output: 1_000_000}).USD; !near(got, 20) {
		t.Errorf("opus 5.5 = %v", got)
	}
}

func TestHaikuLongContextTier(t *testing.T) {
	short := Cost("claude-haiku-5-5", Usage{Input: 100_000})
	if !near(short.USD, 0.01) {
		t.Errorf("at the boundary still short tier: %v", short.USD)
	}
	long := Cost("claude-haiku-5-5", Usage{Input: 1, CacheRead: 100_000, Output: 1_000_000})
	if !near(long.USD, 0.0000005+0.05*0.1+2.50) {
		t.Errorf("long tier = %v", long.USD)
	}
}

func TestFastModeAndGeoStack(t *testing.T) {
	std := Cost("claude-opus-5-5", Usage{Output: 1_000_000}).USD
	fast := Cost("claude-opus-5-5", Usage{Output: 1_000_000, Speed: "fast"}).USD
	if !near(fast, 2*std) || !near(fast, 40) {
		t.Errorf("fast = %v std = %v", fast, std)
	}
	us := Cost("claude-opus-5-5", Usage{Output: 1_000_000, Speed: "fast", Geo: "us"}).USD
	if !near(us, 44) {
		t.Errorf("fast + us = %v", us)
	}
	// Fast mode is not offered on Sonnet; the field is ignored there.
	if got := Cost("claude-sonnet-5-5", Usage{Output: 1_000_000, Speed: "fast"}).USD; !near(got, 10) {
		t.Errorf("sonnet fast = %v", got)
	}
}

func TestWebSearch(t *testing.T) {
	if got := Cost("claude-sonnet-5-5", Usage{WebSearches: 3}).USD; !near(got, 0.03) {
		t.Errorf("got %v", got)
	}
}

func TestUnknownModelUsesFallbackAndIsEstimated(t *testing.T) {
	r := Cost("claude-nonexistent-0", Usage{Input: 1_000_000})
	if !r.Estimated || !near(r.USD, 3) {
		t.Errorf("got %+v", r)
	}
	if Known("claude-nonexistent-0") || Known("<synthetic>") || Known("") {
		t.Error("Known is true for an unknown model")
	}
	if z := Cost("<synthetic>", Usage{}); z.USD != 0 {
		t.Errorf("synthetic zero-usage rows must cost nothing: %+v", z)
	}
}

// Every model id seen in the local transcripts must have its own entry (not the
// fallback) unless it is a known non-model. Opt-in: reads real transcripts.
func TestEveryLocalModelIDHasAnEntry(t *testing.T) {
	ids := localModelIDs(t)
	for id := range ids {
		switch id {
		case "<synthetic>", "claude-nonexistent-0", "opus", "sonnet", "haiku":
			continue // placeholders and aliases that never price a request
		}
		if !Known(id) {
			t.Errorf("model id %q from local transcripts has no price entry", id)
		}
	}
}
