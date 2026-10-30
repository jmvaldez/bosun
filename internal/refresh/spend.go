package refresh

import (
	"fmt"
	"io"
	"time"

	"github.com/jmvaldez/bosun/internal/claude"
	"github.com/jmvaldez/bosun/internal/config"
	"github.com/jmvaldez/bosun/internal/ledger"
	"github.com/jmvaldez/bosun/internal/live"
	"github.com/jmvaldez/bosun/internal/pace"
	"github.com/jmvaldez/bosun/internal/platform"
	"github.com/jmvaldez/bosun/internal/spend"
	"github.com/jmvaldez/bosun/internal/task"
)

const (
	// fullIngestEvery is how often the refresher walks every transcript.
	fullIngestEvery = 10 * time.Minute
	// ingestBudget bounds a refresher ingest so a first run over a long history
	// finishes across several ticks (LESSONS L-16).
	ingestBudget = 10 * time.Second
)

// IngestOpts is the one place ingest options are built, so `spend`, `backfill`
// and the refresher agree on task parsing, liveness and the fallback account.
// It is exported because those callers in main share it; it stays here rather
// than in ledger, which cannot import live.
func IngestOpts(ad platform.Adapters, cfg config.Config) ledger.IngestOpts {
	acct := ""
	if id, ok := claude.ReadIdentity(ad.Paths.Identity()); ok {
		acct = id.AccountKey
	}
	liveSessions := live.Read(ad.Paths.Live()).Sessions
	return ledger.IngestOpts{
		Paths:   ad.Paths,
		Now:     ad.Clock.Now,
		Parser:  task.FromConfig(cfg.Task),
		Account: acct,
		IsLive: func(id string) bool {
			s := liveSessions[id]
			return s != nil && !s.Ended
		},
	}
}

// refreshSpend keeps the ledger, today.json and pace.json current: a full
// ingest (then retention) once per fullIngestEvery, FinalizeLive on the ticks
// between, then today's total and one pace sample.
func refreshSpend(ad platform.Adapters, cfg config.Config, out io.Writer) error {
	if ad.Paths.State == "" {
		return nil
	}
	db, err := ledger.Open(ad.Paths.Ledger())
	if err != nil {
		return err
	}
	defer db.Close()
	now := ad.Clock.Now()
	opts := IngestOpts(ad, cfg)
	opts.Budget = ingestBudget
	if last, ok := db.LastIngest(); !ok || now.Sub(last) >= fullIngestEvery {
		st, err := db.Ingest(opts)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "ingest: %d files, %d requests, budget hit %v, skipped %v\n", st.Files, st.Requests, st.BudgetHit, st.Skipped)
		if !st.BudgetHit && !st.Skipped {
			if r, err := db.Retain(ad.Paths, now); err != nil {
				return err
			} else if r.SessionDirs+r.Requests > 0 {
				fmt.Fprintf(out, "retention: %d session dirs, %d requests rolled up\n", r.SessionDirs, r.Requests)
			}
		}
	} else if err := db.FinalizeLive(opts); err != nil {
		return err
	}

	snap, err := db.Load(time.Time{})
	if err != nil {
		return err
	}
	clock := spend.NewClock(cfg.Spend.Day)
	day := clock.Day(now)
	cs := spend.ForDisplay(snap, clock, ad.Paths.ClaudeSettings())
	today := spend.DayTotal(cs, clock, day, time.Time{})
	// The pace baseline is rebuilt once a day. A failure is dropped, not
	// returned: pace.json keeps its old BuiltDay, so the next tick retries.
	if pace.Read(ad.Paths.Pace()).BuiltDay != day {
		if pf, err := buildPace(db, cs, clock, now, cfg.Pace); err == nil {
			_ = pace.Write(ad.Paths.Pace(), pf)
		}
	}
	if err := spend.WriteToday(ad.Paths.Today(), today, now); err != nil {
		return err
	}
	start, err := clock.DayStart(day)
	if err != nil {
		return err
	}
	return db.RecordSample(day, int(now.Sub(start)/time.Minute), today.USD)
}
