package ledger

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const requestSelect = `SELECT message_id, request_id, session_id, prompt_id, agent_id, model, ts,
	input, output, cache_read, cache_5m, cache_1h, web, speed, geo FROM requests`

func scanRequests(rows *sql.Rows) ([]Request, error) {
	var out []Request
	for rows.Next() {
		var r Request
		var ts int64
		if err := rows.Scan(&r.MessageID, &r.RequestID, &r.SessionID, &r.PromptID, &r.AgentID, &r.Model, &ts,
			&r.Input, &r.Output, &r.CacheRead, &r.Cache5m, &r.Cache1h, &r.Web, &r.Speed, &r.Geo); err != nil {
			return nil, err
		}
		r.TS = fromMS(ts)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Snapshot is a read-only copy of what the views need. Loading it once keeps
// every view working from the same rows.
type Snapshot struct {
	Sessions  map[string]Session
	Requests  []Request
	Prompts   map[string]Prompt   // by prompt id
	Subagents map[string]Subagent // by session id + "/" + agent id
	// Rollups hold request rows older than the retention horizon.
	Rollups []Rollup
}

// Load reads sessions and the requests at or after since (zero means all).
func (d *DB) Load(since time.Time) (Snapshot, error) {
	snap, err := d.load(since)
	if err != nil {
		return snap, fmt.Errorf("ledger: load: %w", err)
	}
	return snap, nil
}

func (d *DB) load(since time.Time) (Snapshot, error) {
	snap := Snapshot{Sessions: map[string]Session{}, Prompts: map[string]Prompt{}, Subagents: map[string]Subagent{}}
	rows, err := d.sql.Query(`SELECT session_id, account_key, repo, branch, task, task_source, started_at, ended_at,
		last_activity_at, total_usd, inherited_usd, banked_usd, forked_from, launch_session_id, trusted, covered_from FROM sessions`)
	if err != nil {
		return snap, err
	}
	for rows.Next() {
		var s Session
		var st, en, la, cf int64
		var trusted int
		if err := rows.Scan(&s.ID, &s.Account, &s.Repo, &s.Branch, &s.Task, &s.TaskSource, &st, &en, &la,
			&s.TotalUSD, &s.InheritedUSD, &s.BankedUSD, &s.ForkedFrom, &s.LaunchID, &trusted, &cf); err != nil {
			rows.Close()
			return snap, err
		}
		s.StartedAt, s.EndedAt, s.LastActivityAt, s.Trusted = fromMS(st), fromMS(en), fromMS(la), trusted == 1
		s.CoveredFrom = fromMS(cf)
		snap.Sessions[s.ID] = s
	}
	rows.Close()

	q := requestSelect
	var args []any
	if !since.IsZero() {
		q += " WHERE ts >= ?"
		args = append(args, since.UnixMilli())
	}
	rr, err := d.sql.Query(q+" ORDER BY ts, message_id", args...)
	if err != nil {
		return snap, err
	}
	snap.Requests, err = scanRequests(rr)
	rr.Close()
	if err != nil {
		return snap, err
	}

	pr, err := d.sql.Query("SELECT prompt_id, session_id, n, started_at FROM prompts")
	if err != nil {
		return snap, err
	}
	for pr.Next() {
		var p Prompt
		var st int64
		if err := pr.Scan(&p.ID, &p.SessionID, &p.N, &st); err != nil {
			pr.Close()
			return snap, err
		}
		p.StartedAt = fromMS(st)
		snap.Prompts[p.ID] = p
	}
	pr.Close()

	sr, err := d.sql.Query("SELECT session_id, agent_id, agent_type, first_at, last_at FROM subagents")
	if err != nil {
		return snap, err
	}
	for sr.Next() {
		var s Subagent
		var f, l int64
		if err := sr.Scan(&s.SessionID, &s.AgentID, &s.AgentType, &f, &l); err != nil {
			sr.Close()
			return snap, err
		}
		s.FirstAt, s.LastAt = fromMS(f), fromMS(l)
		snap.Subagents[s.SessionID+"/"+s.AgentID] = s
	}
	sr.Close()
	snap.Rollups, err = d.loadRollups()
	return snap, err
}

func (d *DB) Session(id string) (Session, bool, error) {
	snap, err := d.Load(time.Now().Add(100 * 365 * 24 * time.Hour)) // no request rows needed
	if err != nil {
		return Session{}, false, err
	}
	s, ok := snap.Sessions[id]
	return s, ok, nil
}

func (d *DB) SetMeta(key, value string) error {
	_, err := d.sql.Exec(`INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// Meta reads a key; ok is false when absent.
func (d *DB) Meta(key string) (string, bool, error) {
	var v string
	err := d.sql.QueryRow("SELECT value FROM meta WHERE key = ?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return v, err == nil, err
}

// RecordSample stores today's cumulative spend at a minute of the day. Samples
// feed the daily_pace baseline.
func (d *DB) RecordSample(day string, minute int, cumulative float64) error {
	_, err := d.sql.Exec(`INSERT INTO spend_samples(day,minute,cumulative_usd) VALUES(?,?,?)
		ON CONFLICT(day,minute) DO UPDATE SET cumulative_usd = excluded.cumulative_usd`, day, minute, cumulative)
	return err
}

// Sample is a day's cumulative spend in USD, Minute minutes after the spend
// day began (spend.day), not after midnight.
type Sample struct {
	Day        string
	Minute     int
	Cumulative float64
}

// Samples returns all samples for a day, by minute.
func (d *DB) Samples(day string) ([]Sample, error) {
	rows, err := d.sql.Query("SELECT day, minute, cumulative_usd FROM spend_samples WHERE day = ? ORDER BY minute", day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Sample
	for rows.Next() {
		var s Sample
		if err := rows.Scan(&s.Day, &s.Minute, &s.Cumulative); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// TranscriptPath returns the main transcript recorded for a session, or "".
func (d *DB) TranscriptPath(id string) string {
	var p string
	_ = d.sql.QueryRow("SELECT path FROM ingest_files WHERE session_id = ? AND agent_id = '' LIMIT 1", id).Scan(&p)
	return p
}
