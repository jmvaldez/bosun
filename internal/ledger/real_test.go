package ledger

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jmvaldez/bosun/internal/paths"
	"github.com/jmvaldez/bosun/internal/task"
)

// TestRealSessionsOwnUSDMatchesCostState ingests the user's real transcripts
// (read only) into a throwaway ledger and checks, for every ended session that
// has a cost-state, that OwnUSD equals Claude Code's own total to the cent. It
// also logs how far the priced rows are from cost-state's per-model costUSD,
// which is the check on the price table. Opt-in: BOSUN_TEST_REAL_SESSIONS=1.
func TestRealSessionsOwnUSDMatchesCostState(t *testing.T) {
	if os.Getenv("BOSUN_TEST_REAL_SESSIONS") == "" {
		t.Skip("set BOSUN_TEST_REAL_SESSIONS=1 to read real transcripts (read-only)")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home")
	}
	state := t.TempDir()
	p := paths.Paths{State: state, Claude: filepath.Join(home, ".claude")}
	db, err := Open(state + "/bosun.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st, err := db.Ingest(IngestOpts{Paths: p, Parser: task.Default(), Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := db.Load(time.Time{})
	if err != nil {
		t.Fatal(err)
	}

	type muse struct {
		in, out, cr, cw int64
		cost            float64
	}
	type csInfo struct {
		sum      float64
		modelSum float64
		groups   int
		models   map[string]muse // from the last cost-state of each group, summed
		earliest int64
	}
	truth := map[string]*csInfo{}
	filepath.WalkDir(filepath.Join(p.Claude, "projects"), func(path string, d fs.DirEntry, err error) error {
		rel, rerr := filepath.Rel(filepath.Join(p.Claude, "projects"), path)
		if err != nil || rerr != nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl") || len(strings.Split(rel, "/")) != 2 ||
			strings.Contains(path, ".orphaned-") || strings.Contains(path, ".superseded-") {
			return nil
		}
		sid := strings.TrimSuffix(filepath.Base(path), ".jsonl")
		f, _ := os.Open(path)
		defer f.Close()
		r := bufio.NewReaderSize(f, 1<<20)
		last := map[int64]struct{ total, models float64 }{}
		lastModels := map[int64]map[string]muse{}
		for {
			line, err := r.ReadBytes('\n')
			if bytes.Contains(line, []byte(`"type":"cost-state"`)) && bytes.HasSuffix(line, []byte("\n")) {
				var c struct {
					Start int64   `json:"startTime"`
					Total float64 `json:"totalCostUSD"`
					Usage map[string]struct {
						In   int64   `json:"inputTokens"`
						Out  int64   `json:"outputTokens"`
						CR   int64   `json:"cacheReadInputTokens"`
						CW   int64   `json:"cacheCreationInputTokens"`
						Cost float64 `json:"costUSD"`
					} `json:"modelUsage"`
				}
				if json.Unmarshal(line, &c) == nil && c.Start != 0 {
					var m float64
					mm := map[string]muse{}
					for name, u := range c.Usage {
						m += u.Cost
						mm[name] = muse{u.In, u.Out, u.CR, u.CW, u.Cost}
					}
					last[c.Start] = struct{ total, models float64 }{c.Total, m}
					lastModels[c.Start] = mm
				}
			}
			if err != nil {
				break
			}
		}
		if len(last) > 0 {
			ci := &csInfo{groups: len(last), models: map[string]muse{}}
			for start, v := range last {
				if ci.earliest == 0 || start < ci.earliest {
					ci.earliest = start
				}
				ci.sum += v.total
				ci.modelSum += v.models
				for name, u := range lastModels[start] {
					m := ci.models[name]
					m.in, m.out, m.cr, m.cw, m.cost = m.in+u.in, m.out+u.out, m.cr+u.cr, m.cw+u.cw, m.cost+u.cost
					ci.models[name] = m
				}
			}
			truth[sid] = ci
		}
		return nil
	})

	checked, forks, addedSessions := 0, 0, 0
	var absBefore, absAfter float64
	var worstPriceGap float64
	var ids []string
	for id := range truth {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		s, ok := snap.Sessions[id]
		if !ok {
			t.Errorf("session %s has a cost-state but no ledger row", id)
			continue
		}
		want := truth[id].sum
		if s.ForkedFrom != "" {
			forks++
			if math.Abs(s.TotalUSD-want) > 0.005 {
				t.Errorf("fork %s: gross total %.4f, cost-state %.4f", id, s.TotalUSD, want)
			}
			continue
		}
		checked++
		if !s.Trusted || s.EndedAt.IsZero() {
			t.Errorf("%s: trusted=%v ended=%v", id, s.Trusted, s.EndedAt)
		}
		var rows, added float64
		for _, r := range snap.Requests {
			if r.SessionID == id {
				usd := PriceRequest(r).USD
				rows += usd
				if r.TS.UnixMilli() < truth[id].earliest {
					added += usd // D-21: spend before the earliest cost-state startTime
				}
			}
		}
		if math.Abs(OwnUSD(s)-(want+added)) > 0.005 {
			t.Errorf("%s: OwnUSD %.4f vs cost-state %.4f + added back %.4f", id, OwnUSD(s), want, added)
		}
		if added > 0 {
			addedSessions++
			absBefore += math.Abs(rows - want)
			absAfter += math.Abs(rows - want - added)
			t.Logf("D-21 %s: cost-state %.2f, rows %.2f, added back %.2f -> unattributed %+.2f (was %+.2f)", id[:8], want, rows, added, want+added-rows, want-rows)
		}
		gap := rows - truth[id].modelSum
		if math.Abs(gap) > math.Abs(worstPriceGap) {
			worstPriceGap = gap
		}
		if math.Abs(gap) > 0.5 {
			per := map[string]muse{}
			for _, r := range snap.Requests {
				if r.SessionID != id {
					continue
				}
				m := per[r.Model]
				m.in += r.Input
				m.out += r.Output
				m.cr += r.CacheRead
				m.cw += r.Cache5m + r.Cache1h
				m.cost += PriceRequest(r).USD
				per[r.Model] = m
			}
			t.Logf("session %s: rows %.2f vs cost-state models %.2f (groups %d)", id[:8], rows, truth[id].modelSum, truth[id].groups)
			for name, m := range per {
				c := truth[id].models[name]
				t.Logf("  %-24s rows in/out/cr/cw %d/%d/%d/%d $%.2f | cost-state %d/%d/%d/%d $%.2f", name, m.in, m.out, m.cr, m.cw, m.cost, c.in, c.out, c.cr, c.cw, c.cost)
			}
		}
	}
	t.Logf("D-21 added rows back in %d sessions; sum |unattributed| went %.2f -> %.2f USD", addedSessions, absBefore, absAfter)
	t.Logf("ingested %d files, %d requests; checked %d non-fork sessions and %d forks against cost-state", st.Files, st.Requests, checked, forks)
	t.Logf("largest (priced rows - cost-state per-model costUSD) over checked sessions: %+.4f USD (negative means transcripts undercount)", worstPriceGap)
	if checked == 0 {
		t.Skip("no ended sessions with a cost-state found")
	}
}
