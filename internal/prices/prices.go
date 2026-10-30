// Package prices is the only place bosun turns tokens into dollars. The popup,
// `spend`, rules, and the ledger all call Cost; there is no second pricing path
// (LESSONS L-34).
//
// Rates are the official list prices fetched from
// https://platform.claude.com/docs/en/about-claude/pricing on 2026-10-23 (the
// "Model pricing" table and the prompt caching, fast mode, data residency, and
// long context sections). Every figure is an API-rate estimate, whatever plan
// the user is on. Update the table and the fetch date together.
package prices

import "strings"

// Rate is USD per million tokens.
type Rate struct {
	Input, Output, Write5m, Write1h, Read float64
}

// entry maps a model id prefix to its rates. Haiku 5.5 is the one model priced
// by prompt length: long applies when the prompt (input plus all cache tokens)
// exceeds longOver tokens.
type entry struct {
	prefix   string
	rate     Rate
	long     *Rate
	longOver int64
	// fastMult is the fast-mode multiplier on every category (fast prices stack
	// with the caching multipliers, and each fast rate is 2x standard).
	fastMult float64
}

// table is ordered most specific first: a prefix must come before any prefix
// that is a prefix of it (claude-opus-5-5 before claude-opus-5). The ordering
// test enforces this.
var table = []entry{
	{prefix: "claude-fable-5-1", rate: Rate{10, 50, 12.50, 20, 0.25}},
	{prefix: "claude-fable-5", rate: Rate{10, 50, 12.50, 20, 1}},
	{prefix: "claude-mythos-5-1", rate: Rate{10, 50, 12.50, 20, 0.25}},
	{prefix: "claude-mythos-5", rate: Rate{10, 50, 12.50, 20, 1}},

	{prefix: "claude-opus-5-5", rate: Rate{4, 20, 5, 8, 0.20}, fastMult: 2},
	{prefix: "claude-opus-5", rate: Rate{5, 25, 6.25, 10, 0.50}, fastMult: 2},
	{prefix: "claude-opus-4-8", rate: Rate{5, 25, 6.25, 10, 0.50}, fastMult: 2},
	{prefix: "claude-opus-4-7", rate: Rate{5, 25, 6.25, 10, 0.50}},
	{prefix: "claude-opus-4-6", rate: Rate{5, 25, 6.25, 10, 0.50}},
	{prefix: "claude-opus-4-5", rate: Rate{5, 25, 6.25, 10, 0.50}},
	{prefix: "claude-opus-4-1", rate: Rate{15, 75, 18.75, 30, 1.50}},
	{prefix: "claude-opus-4", rate: Rate{15, 75, 18.75, 30, 1.50}},

	{prefix: "claude-sonnet-5-5", rate: Rate{2, 10, 2.50, 4, 0.10}},
	{prefix: "claude-sonnet-5", rate: Rate{2, 10, 2.50, 4, 0.20}},
	{prefix: "claude-sonnet-4-6", rate: Rate{3, 15, 3.75, 6, 0.30}},
	{prefix: "claude-sonnet-4-5", rate: Rate{3, 15, 3.75, 6, 0.30}},
	{prefix: "claude-sonnet-4", rate: Rate{3, 15, 3.75, 6, 0.30}},

	{prefix: "claude-haiku-5-5", rate: Rate{0.10, 0.50, 0.125, 0.20, 0.01},
		long: &Rate{0.50, 2.50, 0.625, 1, 0.05}, longOver: 100_000},
	{prefix: "claude-haiku-4-5", rate: Rate{1, 5, 1.25, 2, 0.10}},
	{prefix: "claude-3-5-haiku", rate: Rate{0.80, 4, 1, 1.60, 0.08}},
	{prefix: "claude-haiku-3-5", rate: Rate{0.80, 4, 1, 1.60, 0.08}},
}

// fallback prices a model the table does not know, at Sonnet 4.x rates: a
// middle tier, so a surprise model is neither free nor wildly inflated. Rows
// priced this way are marked estimated.
var fallback = Rate{3, 15, 3.75, 6, 0.30}

// WebSearchUSD is the price of one server-side web search ($10 per 1,000).
const WebSearchUSD = 0.01

// dataResidencyMult applies whenever inference_geo is "us". The pricing page
// lists US-only inference for Claude 4.6 and later; the model is not checked.
const dataResidencyMult = 1.1

// Usage is one request's token counts, as the transcript reports them.
type Usage struct {
	Input       int64
	Output      int64
	CacheRead   int64
	CacheWrite5 int64 // 5 minute cache writes
	CacheWrite1 int64 // 1 hour cache writes
	WebSearches int64
	Speed       string // "fast" or "standard"
	Geo         string // inference_geo; see dataResidencyMult
}

type Result struct {
	USD float64
	// Estimated is true when the model is not in the table and the fallback
	// tier priced it.
	Estimated bool
}

func lookup(model string) (entry, bool) {
	for _, e := range table {
		if strings.HasPrefix(model, e.prefix) {
			return e, true
		}
	}
	return entry{}, false
}

// Known reports whether the model has its own table entry.
func Known(model string) bool { _, ok := lookup(model); return ok }

// Cost prices one request. Every caller passes the row's own model.
func Cost(model string, u Usage) Result {
	e, ok := lookup(model)
	if !ok {
		e = entry{rate: fallback}
	}
	r := e.rate
	if e.long != nil && u.Input+u.CacheRead+u.CacheWrite5+u.CacheWrite1 > e.longOver {
		r = *e.long
	}
	usd := (float64(u.Input)*r.Input +
		float64(u.Output)*r.Output +
		float64(u.CacheWrite5)*r.Write5m +
		float64(u.CacheWrite1)*r.Write1h +
		float64(u.CacheRead)*r.Read) / 1e6
	if u.Speed == "fast" && e.fastMult > 0 {
		usd *= e.fastMult
	}
	if u.Geo == "us" {
		usd *= dataResidencyMult
	}
	usd += float64(u.WebSearches) * WebSearchUSD
	return Result{USD: usd, Estimated: !ok}
}

// prefixes lists the table's prefixes in order, for tests.
func prefixes() []string {
	out := make([]string, len(table))
	for i, e := range table {
		out[i] = e.prefix
	}
	return out
}

// rateFor returns the standard rate of a known model, for tests.
func rateFor(model string) (Rate, bool) {
	e, ok := lookup(model)
	return e.rate, ok
}
