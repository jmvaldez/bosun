package ledger

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jmvaldez/bosun/internal/atomicfile"
	"github.com/jmvaldez/bosun/internal/paths"
)

const (
	// SessionDirRetention: sessions/<id>/ is pruned this long after the session
	// ended and was ingested.
	SessionDirRetention = 7 * 24 * time.Hour
	// RequestRetention: request rows are rolled up and pruned after this long.
	RequestRetention = 400 * 24 * time.Hour
)

type Pruned struct {
	SessionDirs int
	Requests    int
}

// WriteIngestMarker stores the time of the last full ingest as a plain file.
func WriteIngestMarker(path string, t time.Time) error {
	b, err := json.Marshal(struct {
		At string `json:"at"`
	}{t.UTC().Format(time.RFC3339)})
	if err != nil {
		return err
	}
	return atomicfile.Write(path, b, 0o600)
}

// ReadIngestMarker returns the last full ingest time; ok is false if none.
func ReadIngestMarker(path string) (time.Time, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}, false
	}
	var m struct {
		At string `json:"at"`
	}
	if json.Unmarshal(b, &m) != nil {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, m.At)
	return t, err == nil
}

// Retain prunes what has aged out.
//
// Session dirs go only for sessions with ended_at set, ended at least
// SessionDirRetention ago, and ingested after they ended (so the ledger holds
// what the dir held). Request rows older than RequestRetention are folded into
// rollups (priced now, per session, UTC day and model) and deleted in one
// transaction, so a total never loses spend. Sessions, subagents and rollups
// are kept forever.
func (d *DB) Retain(p paths.Paths, now time.Time) (Pruned, error) {
	out, err := d.retain(p, now)
	if err != nil {
		return out, fmt.Errorf("ledger: retain: %w", err)
	}
	return out, nil
}

func (d *DB) retain(p paths.Paths, now time.Time) (Pruned, error) {
	var out Pruned
	lastIngest, hasIngest := d.LastIngest()

	if hasIngest {
		rows, err := d.sql.Query("SELECT session_id FROM sessions WHERE ended_at <> 0 AND ended_at <= ? AND ended_at <= ?",
			now.Add(-SessionDirRetention).UnixMilli(), lastIngest.UnixMilli())
		if err != nil {
			return out, err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return out, err
			}
			ids = append(ids, id)
		}
		rows.Close()
		for _, id := range ids {
			if !paths.ValidSessionID(id) {
				continue
			}
			dir := p.Session(id)
			fi, err := os.Lstat(dir)
			if err != nil || !fi.IsDir() { // missing, or a symlink: leave it alone
				continue
			}
			if rel, err := filepath.Rel(p.Sessions(), dir); err != nil || rel != id {
				continue
			}
			if err := os.RemoveAll(dir); err == nil {
				out.SessionDirs++
			}
		}
	}

	n, err := d.rollUp(now.Add(-RequestRetention).UnixMilli())
	out.Requests = n
	return out, err
}

func (d *DB) rollUp(cutoff int64) (int, error) {
	tx, err := d.sql.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(requestSelect+" WHERE ts < ?", cutoff)
	if err != nil {
		return 0, err
	}
	old, err := scanRequests(rows)
	rows.Close()
	if err != nil || len(old) == 0 {
		return 0, err
	}
	type key struct{ sid, day, model string }
	type agg struct {
		ts                  int64
		n                   int
		in, out, cr, c5, c1 int64
		usd                 float64
		est                 bool
	}
	groups := map[key]*agg{}
	for _, r := range old {
		k := key{r.SessionID, r.TS.UTC().Format("2006-01-02"), r.Model}
		g := groups[k]
		if g == nil {
			g = &agg{ts: r.TS.UnixMilli()}
			groups[k] = g
		}
		if t := r.TS.UnixMilli(); t < g.ts {
			g.ts = t
		}
		p := PriceRequest(r)
		g.n++
		g.in, g.out, g.cr, g.c5, g.c1 = g.in+r.Input, g.out+r.Output, g.cr+r.CacheRead, g.c5+r.Cache5m, g.c1+r.Cache1h
		g.usd += p.USD
		g.est = g.est || p.Estimated
	}
	for k, g := range groups {
		if _, err := tx.Exec(`INSERT INTO rollups(session_id,day,model,ts,requests,input,output,cache_read,cache_5m,cache_1h,usd,est)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(session_id,day,model) DO UPDATE SET
			  ts = MIN(rollups.ts, excluded.ts), requests = rollups.requests + excluded.requests,
			  input = rollups.input + excluded.input, output = rollups.output + excluded.output,
			  cache_read = rollups.cache_read + excluded.cache_read, cache_5m = rollups.cache_5m + excluded.cache_5m,
			  cache_1h = rollups.cache_1h + excluded.cache_1h, usd = rollups.usd + excluded.usd,
			  est = MAX(rollups.est, excluded.est)`,
			k.sid, k.day, k.model, g.ts, g.n, g.in, g.out, g.cr, g.c5, g.c1, g.usd, boolInt(g.est)); err != nil {
			return 0, err
		}
	}
	if _, err := tx.Exec("DELETE FROM requests WHERE ts < ?", cutoff); err != nil {
		return 0, err
	}
	return len(old), tx.Commit()
}

// Rollup is one folded group of old requests.
type Rollup struct {
	SessionID string
	Day       string
	Model     string
	TS        time.Time
	Requests  int
	Input     int64
	Output    int64
	Cache     int64 // cache_read + cache_5m + cache_1h
	USD       float64
	Est       bool
}

func (d *DB) loadRollups() ([]Rollup, error) {
	rows, err := d.sql.Query("SELECT session_id, day, model, ts, requests, input, output, cache_read + cache_5m + cache_1h, usd, est FROM rollups ORDER BY day, session_id, model")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rollup
	for rows.Next() {
		var r Rollup
		var ts int64
		var est int
		if err := rows.Scan(&r.SessionID, &r.Day, &r.Model, &ts, &r.Requests, &r.Input, &r.Output, &r.Cache, &r.USD, &est); err != nil {
			return nil, err
		}
		r.TS, r.Est = fromMS(ts), est == 1
		out = append(out, r)
	}
	return out, rows.Err()
}
