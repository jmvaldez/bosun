package main

import (
	"fmt"
	"time"

	"github.com/jmvaldez/bosun/internal/ledger"
	"github.com/jmvaldez/bosun/internal/refresh"
	"github.com/jmvaldez/bosun/internal/spend"
)

// spend ingests (so the numbers are current), then prints a breakdown.
func (a *app) spend(args []string) int {
	by, since, taskKey, format := "task", "", "", "text"
	p := a.args("spend", args)
	for arg, ok := p.next(); ok; arg, ok = p.next() {
		var got bool
		switch arg {
		case "--by":
			by, got = p.value()
		case "--since":
			since, got = p.value()
		case "--task":
			taskKey, got = p.value()
		case "--json":
			format, got = "json", true
		case "--csv":
			format, got = "csv", true
		default:
			return p.unknown()
		}
		if !got {
			return 2
		}
	}
	if !spend.IsGrouping(by) {
		fmt.Fprintf(a.errw, "bosun spend: unknown --by %q (have %v)\n", by, spend.Groupings)
		return 2
	}
	cfg := a.loadConfig()
	clock := spend.NewClock(cfg.Spend.Day)
	now := a.ad.Clock.Now()
	from, err := clock.Since(since, now)
	if err != nil {
		fmt.Fprintf(a.errw, "bosun spend: %v\n", err)
		return 2
	}
	if a.ad.Paths.State == "" {
		fmt.Fprintln(a.errw, "bosun spend: no state dir (HOME unset?)")
		return 1
	}
	db, err := ledger.Open(a.ad.Paths.Ledger())
	if err != nil {
		fmt.Fprintf(a.errw, "bosun spend: %v\n", err)
		return 1
	}
	defer db.Close()
	if st, err := db.Ingest(refresh.IngestOpts(a.ad, cfg)); err != nil {
		fmt.Fprintf(a.errw, "bosun spend: ingest: %v\n", err)
		return 1
	} else if st.Skipped {
		// Another ingest is running; its rows are committed per file, so what
		// we read is consistent, just possibly a little behind.
		fmt.Fprintln(a.errw, "bosun spend: another ingest is running; showing what it has stored so far")
	}
	snap, err := db.Load(time.Time{})
	if err != nil {
		fmt.Fprintf(a.errw, "bosun spend: %v\n", err)
		return 1
	}
	res, err := spend.Group(spend.Filter(spend.ForDisplay(snap, clock, a.ad.Paths.ClaudeSettings()), from, taskKey), by, clock)
	if err != nil {
		fmt.Fprintf(a.errw, "bosun spend: %v\n", err)
		return 2
	}
	res.Since = since
	switch format {
	case "json":
		err = spend.JSON(a.out, res)
	case "csv":
		err = spend.CSV(a.out, res)
	default:
		spend.Text(a.out, res)
	}
	if err != nil {
		fmt.Fprintf(a.errw, "bosun spend: %v\n", err)
		return 1
	}
	return 0
}
