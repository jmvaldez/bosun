package main

import (
	"fmt"
	"time"

	"github.com/jmvaldez/bosun/internal/ledger"
	"github.com/jmvaldez/bosun/internal/refresh"
	"github.com/jmvaldez/bosun/internal/spend"
)

// backfill ingests transcripts back to --since (default 30d) through the same
// idempotent path as the refresher. Running it twice leaves the ledger as it
// was after the first run.
func (a *app) backfill(args []string) int {
	since := "30d"
	p := a.args("backfill", args)
	for arg, ok := p.next(); ok; arg, ok = p.next() {
		if arg != "--since" {
			return p.unknown()
		}
		if since, ok = p.value(); !ok {
			return 2
		}
	}
	cfg := a.loadConfig()
	clock := spend.NewClock(cfg.Spend.Day)
	from, err := clock.Since(since, a.ad.Clock.Now())
	if err != nil {
		fmt.Fprintf(a.errw, "bosun backfill: %v\n", err)
		return 2
	}
	if a.ad.Paths.State == "" || a.ad.Paths.Claude == "" {
		fmt.Fprintln(a.errw, "bosun backfill: no state or Claude dir (HOME unset?)")
		return 1
	}
	db, err := ledger.Open(a.ad.Paths.Ledger())
	if err != nil {
		fmt.Fprintf(a.errw, "bosun backfill: %v\n", err)
		return 1
	}
	defer db.Close()
	opts := refresh.IngestOpts(a.ad, cfg)
	opts.Since = from
	start := time.Now()
	st, err := db.Ingest(opts)
	if err != nil {
		fmt.Fprintf(a.errw, "bosun backfill: %v\n", err)
		return 1
	}
	if st.Skipped {
		fmt.Fprintln(a.errw, "bosun backfill: another ingest is running; try again in a moment")
		return 1
	}
	fmt.Fprintf(a.out, "backfill since %s: %d transcripts read, %d requests (%s)\n", since, st.Files, st.Requests, time.Since(start).Round(time.Millisecond))
	return 0
}
