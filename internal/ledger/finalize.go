package ledger

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
	"time"

	"github.com/jmvaldez/bosun/internal/hook"
	"github.com/jmvaldez/bosun/internal/paths"
	"github.com/jmvaldez/bosun/internal/prices"
	"github.com/jmvaldez/bosun/internal/statusline"
	"github.com/jmvaldez/bosun/internal/task"
)

// stateFacts is what the per-session state dir adds to the transcript view.
type stateFacts struct {
	started    time.Time
	lastEvent  time.Time
	endedAt    time.Time // SessionEnd after the last SessionStart
	startSrc   string    // source of the first SessionStart
	account    string
	hookTask   string
	hookSrc    string
	hookBranch string
	repo       string
	tag        string
	snap       *statusline.Snapshot
}

func readStateFacts(p paths.Paths, id string) stateFacts {
	var f stateFacts
	f.tag = task.ReadTag(p.Tag(id))
	if s, ok := statusline.ReadSnapshot(p.StatuslineSnap(id)); ok && s.SessionID == id {
		f.snap = &s
	}
	fh, err := os.Open(p.Events(id))
	if err != nil {
		return f
	}
	defer fh.Close()
	r := bufio.NewReaderSize(io.LimitReader(fh, 8<<20), 64<<10)
	var lastStart, end time.Time
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 && bytes.HasSuffix(line, []byte("\n")) {
			var e hook.Event
			if json.Unmarshal(line, &e) == nil {
				ts, perr := time.Parse(time.RFC3339Nano, e.TS)
				if perr == nil {
					if ts.After(f.lastEvent) {
						f.lastEvent = ts
					}
					switch e.Ev {
					case "SessionStart":
						if f.started.IsZero() {
							f.started, f.startSrc = ts, e.Src
						}
						lastStart = ts
						if x := e.Start; x != nil {
							f.account, f.hookTask, f.hookSrc, f.hookBranch, f.repo = x.Account, x.Task, x.TaskSrc, x.Branch, x.Repo
						}
					case "SessionEnd":
						end = ts
					}
				}
			}
		}
		if err != nil {
			break
		}
	}
	if end.After(lastStart) {
		f.endedAt = end
	}
	return f
}

// finalizeAll finalizes sessions parents first: a fork's inheritance reads its
// parent's finalized total, and map order would make that a coin flip.
func (d *DB) finalizeAll(ids map[string]bool, o IngestOpts) error {
	depth := map[string]int{}
	var chain func(id string, seen int) int
	chain = func(id string, seen int) int {
		if v, ok := depth[id]; ok {
			return v
		}
		var parent string
		_ = d.sql.QueryRow("SELECT forked_from FROM sessions WHERE session_id = ?", id).Scan(&parent)
		n := 0
		if parent != "" && seen < 32 {
			n = chain(parent, seen+1) + 1
		}
		depth[id] = n
		return n
	}
	order := make([]string, 0, len(ids))
	for id := range ids {
		chain(id, 0)
		order = append(order, id)
	}
	sort.Slice(order, func(i, j int) bool {
		if depth[order[i]] != depth[order[j]] {
			return depth[order[i]] < depth[order[j]]
		}
		return order[i] < order[j]
	})
	for _, id := range order {
		if err := d.finalizeSession(id, o); err != nil {
			return err
		}
	}
	return nil
}

type sessionRow struct {
	forkedFrom, trBranch string
	started, last        int64
}

func (d *DB) finalizeSession(id string, o IngestOpts) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var cur sessionRow
	err = tx.QueryRow(`SELECT forked_from, transcript_branch, started_at, last_activity_at
		FROM sessions WHERE session_id = ?`, id).
		Scan(&cur.forkedFrom, &cur.trBranch, &cur.started, &cur.last)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	facts := readStateFacts(o.Paths, id)
	if !exists && facts.lastEvent.IsZero() && facts.snap == nil {
		return nil // nothing is known about this id
	}

	taskKey, taskSrc := resolveTask(facts, cur.trBranch, o.Parser)

	account := facts.account
	if account == "" {
		account = o.Account
	}
	if account != "" {
		if _, err := tx.Exec("INSERT OR IGNORE INTO accounts(account_key) VALUES(?)", account); err != nil {
			return err
		}
	}

	started := minNonZero(cur.started, ms(facts.started))
	last := max(cur.last, ms(facts.lastEvent))

	live := o.IsLive != nil && o.IsLive(id)
	ended := ms(facts.endedAt)
	if ended == 0 && !live && last != 0 {
		ended = last
	}

	total, banked, coveredFrom, trusted, err := sessionTotals(tx, id, facts, live)
	if err != nil {
		return err
	}

	forkedFrom, inherited := cur.forkedFrom, 0.0
	if trusted {
		if forkedFrom != "" {
			inherited, err = inheritedUSD(tx, id, forkedFrom, facts.snap, total+banked)
			if err != nil {
				return err
			}
		}
	}

	_, err = tx.Exec(`INSERT INTO sessions(session_id,account_key,repo,branch,task,task_source,started_at,ended_at,last_activity_at,
			total_usd,inherited_usd,banked_usd,forked_from,trusted,covered_from)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(session_id) DO UPDATE SET
		  account_key = CASE WHEN sessions.account_key = '' THEN excluded.account_key ELSE sessions.account_key END,
		  repo = CASE WHEN excluded.repo <> '' THEN excluded.repo ELSE sessions.repo END,
		  branch = CASE WHEN excluded.branch <> '' THEN excluded.branch ELSE sessions.branch END,
		  task = CASE WHEN excluded.task_source <> '' AND `+rankSQL("excluded.task_source")+` >= `+rankSQL("sessions.task_source")+`
		              THEN excluded.task ELSE sessions.task END,
		  task_source = CASE WHEN excluded.task_source <> '' AND `+rankSQL("excluded.task_source")+` >= `+rankSQL("sessions.task_source")+`
		              THEN excluded.task_source ELSE sessions.task_source END,
		  started_at = excluded.started_at,
		  ended_at = excluded.ended_at,
		  last_activity_at = MAX(sessions.last_activity_at, excluded.last_activity_at),
		  total_usd = excluded.total_usd,
		  inherited_usd = excluded.inherited_usd,
		  banked_usd = excluded.banked_usd,
		  forked_from = CASE WHEN sessions.forked_from = '' THEN excluded.forked_from ELSE sessions.forked_from END,
		  trusted = excluded.trusted,
		  covered_from = excluded.covered_from`,
		id, account, facts.repo, facts.hookBranch, taskKey, taskSrc, started, ended, last,
		total, inherited, banked, forkedFrom, boolInt(trusted), coveredFrom)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// resolveTask picks the session's task by precedence.
func resolveTask(facts stateFacts, trBranch string, parser *task.Parser) (taskKey, taskSrc string) {
	// Task: tag > env > branch > transcript gitBranch. The hook derived its key
	// with the default pattern; a branch-sourced task is re-derived with the
	// configured one, env and tag are never re-derived (D-16).
	switch {
	case facts.tag != "":
		taskKey, taskSrc = facts.tag, task.SourceTag
	case facts.hookSrc == task.SourceEnv && facts.hookTask != "":
		taskKey, taskSrc = facts.hookTask, task.SourceEnv
	}
	if taskKey == "" && parser != nil {
		if k := parser.Key(facts.hookBranch); k != "" {
			taskKey, taskSrc = k, task.SourceBranch
		} else if k := parser.Key(trBranch); k != "" {
			taskKey, taskSrc = k, task.SourceTranscript
		}
	}
	return taskKey, taskSrc
}

// sessionTotals prices the session: the total and banked spend, where
// cost-state coverage starts, and whether the figure is Claude's own (trusted)
// or a transcript estimate.
func sessionTotals(tx *sql.Tx, id string, facts stateFacts, live bool) (total, banked float64, coveredFrom int64, trusted bool, err error) {
	rows, err := loadSessionRequests(tx, id)
	if err != nil {
		return 0, 0, 0, false, err
	}
	var est float64
	for _, r := range rows {
		est += PriceRequest(r).USD
	}
	var rolled sql.NullFloat64 // rows already folded into rollups still belong to the estimate
	if err := tx.QueryRow("SELECT SUM(usd) FROM rollups WHERE session_id = ?", id).Scan(&rolled); err != nil {
		return 0, 0, 0, false, err
	}
	est += rolled.Float64
	cs, err := costStateSum(tx, id)
	if err != nil {
		return 0, 0, 0, false, err
	}

	trusted = true
	switch {
	case facts.snap != nil && live:
		total, banked = facts.snap.CostUSD, facts.snap.BankedUSD
	case cs.found:
		total = cs.sum
		// D-21: spend before the earliest cost-state group belongs to a process
		// that never wrote one (a crash, a resume that restored nothing).
		// Claude's figure does not cover it, so add its transcript estimate.
		coveredFrom = cs.earliest
		for _, r := range rows {
			if r.TS.UnixMilli() < coveredFrom {
				total += PriceRequest(r).USD
			}
		}
		if facts.snap != nil {
			// A /login reset may be missing from cost-state; keep whatever the
			// status line banked beyond it, never both (that would double count).
			if extra := facts.snap.CostUSD + facts.snap.BankedUSD - cs.sum; extra > 0 {
				banked = extra
			}
		}
	case facts.snap != nil:
		total, banked = facts.snap.CostUSD, facts.snap.BankedUSD
	default:
		total, trusted = est, false
	}

	return total, banked, coveredFrom, trusted, nil
}

// rankSQL is the task-source precedence as plain SQL: no Go functions are
// registered with the driver, so upserts stay atomic (LESSONS L-14).
func rankSQL(col string) string {
	return "(CASE " + col + " WHEN 'tag' THEN 4 WHEN 'env' THEN 3 WHEN 'branch' THEN 2 WHEN 'transcript' THEN 1 ELSE 0 END)"
}

type costStates struct {
	sum      float64
	found    bool
	earliest int64 // earliest startTime, ms
}

// costStateSum adds the last cost-state total of each startTime group: every
// writer process (a /login reset, a restart) keeps its own startTime.
func costStateSum(tx *sql.Tx, id string) (costStates, error) {
	var c costStates
	var sum sql.NullFloat64
	var earliest sql.NullInt64
	var n int
	if err := tx.QueryRow("SELECT SUM(total_usd), COUNT(*), MIN(start_time) FROM cost_states WHERE session_id = ?", id).Scan(&sum, &n, &earliest); err != nil {
		return c, err
	}
	return costStates{sum: sum.Float64, found: n > 0, earliest: earliest.Int64}, nil
}

func loadSessionRequests(q interface {
	Query(string, ...any) (*sql.Rows, error)
}, id string) ([]Request, error) {
	rows, err := q.Query(requestSelect+" WHERE session_id = ? ORDER BY ts", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRequests(rows)
}

// PriceRequest is the one path from a stored request to a price, shared by
// finalize, rollups and the spend views (see package prices).
func PriceRequest(r Request) prices.Result {
	return prices.Cost(r.Model, prices.Usage{
		Input: r.Input, Output: r.Output, CacheRead: r.CacheRead,
		CacheWrite5: r.Cache5m, CacheWrite1: r.Cache1h, WebSearches: r.Web,
		Speed: r.Speed, Geo: r.Geo,
	})
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func minNonZero(a, b int64) int64 {
	switch {
	case a == 0:
		return b
	case b == 0:
		return a
	case a < b:
		return a
	}
	return b
}
