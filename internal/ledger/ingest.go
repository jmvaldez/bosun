package ledger

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jmvaldez/bosun/internal/clean"
	"github.com/jmvaldez/bosun/internal/flock"
	"github.com/jmvaldez/bosun/internal/paths"
	"github.com/jmvaldez/bosun/internal/task"
)

type IngestOpts struct {
	Paths paths.Paths
	// Budget bounds a run; zero means unlimited. The detached refresher passes
	// 10 s so a first-run ingest of a big history resumes on the next tick
	// instead of running for minutes (LESSONS L-16).
	Budget time.Duration
	Now    func() time.Time
	// Parser derives task keys from branch names with the configured regex (D-16).
	Parser *task.Parser
	// IsLive says whether a session is still running, for ended_at.
	IsLive func(id string) bool
	// Account is the cached account key, the fallback for sessions whose
	// SessionStart carried none.
	Account string
	// Since limits a run to transcripts modified at or after it (backfill).
	Since time.Time
}

type Stats struct {
	Skipped   bool // another ingest held the lock
	BudgetHit bool
	Files     int
	Requests  int // request lines written; each repeat line of a request counts
}

// Ingest folds transcripts and per-session state into the ledger. It holds
// ingest.lock non-blocking and skips if another ingest is running, so
// concurrent `spend` and `refresh` never interleave writes (LESSONS L-14).
// Re-running it over unchanged transcripts changes no session, request or
// prompt row.
func (d *DB) Ingest(o IngestOpts) (Stats, error) {
	var st Stats
	lock, err := flock.TryLock(o.Paths.Locks()+"/ingest.lock", 0)
	if errors.Is(err, flock.ErrBusy) {
		st.Skipped = true
		return st, nil
	}
	if err != nil {
		return st, err
	}
	defer lock.Unlock()
	if o.Now == nil {
		o.Now = time.Now
	}
	start := o.Now()

	var pending []transcript
	for _, f := range listTranscripts(o.Paths.Claude) {
		if !o.Since.IsZero() && f.mtime.Before(o.Since) {
			continue
		}
		need, err := d.needsIngest(f)
		if err != nil {
			return st, err
		}
		if need { // unchanged files cost nothing and must not eat the budget
			pending = append(pending, f)
		}
	}
	orderForIngest(pending, func(id string) bool { return startedAsFork(o.Paths.Session(id) + "/events.jsonl") })
	touched := map[string]bool{}
	for _, f := range pending {
		if o.Budget > 0 && o.Now().Sub(start) > o.Budget {
			st.BudgetHit = true
			break
		}
		n, err := d.ingestFile(f, o.Now().Add(-RequestRetention).UnixMilli())
		if err != nil {
			return st, err
		}
		if n.progressed {
			st.Files++
			st.Requests += n.requests
			touched[f.session] = true
		}
	}
	if st.BudgetHit {
		// Finalizing a half-read session would store a low total as final;
		// the next run finishes the files and finalizes then.
		return st, nil
	}
	for id := range sessionDirs(o.Paths) {
		touched[id] = true
	}
	if err := d.finalizeAll(touched, o); err != nil {
		return st, err
	}
	if err := d.SetMeta("last_ingest", o.Now().UTC().Format(time.RFC3339)); err != nil {
		return st, err
	}
	// A plain file as well, so `doctor` can read it without opening (and so
	// possibly creating) the database.
	return st, WriteIngestMarker(o.Paths.IngestMarker(), o.Now())
}

type transcript struct {
	path    string
	session string
	agent   string // empty for a main transcript
	size    int64
	mtime   time.Time
	first   int64 // earliest timestamp, ms; ingest order key
}

// listTranscripts finds main and subagent transcripts. Set-aside copies are
// skipped: parsing them would double count.
func listTranscripts(claudeDir string) []transcript {
	if claudeDir == "" {
		return nil
	}
	root := filepath.Join(claudeDir, "projects")
	var out []transcript
	_ = filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() || !strings.HasSuffix(p, ".jsonl") {
			return nil
		}
		if strings.Contains(p, ".orphaned-") || strings.Contains(p, ".superseded-") {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		parts := strings.Split(rel, string(filepath.Separator))
		t := transcript{path: p}
		switch {
		case len(parts) == 2:
			t.session = strings.TrimSuffix(parts[1], ".jsonl")
		case len(parts) == 4 && parts[2] == "subagents" && strings.HasPrefix(parts[3], "agent-"):
			t.session = parts[1]
			t.agent = strings.TrimSuffix(strings.TrimPrefix(parts[3], "agent-"), ".jsonl")
		default:
			return nil
		}
		if !paths.ValidSessionID(t.session) {
			return nil
		}
		fi, err := e.Info()
		if err != nil {
			return nil
		}
		t.size, t.mtime = fi.Size(), fi.ModTime()
		out = append(out, t)
		return nil
	})
	return out
}

// orderForIngest puts a fork's predecessor before the fork (LESSONS L-31,
// DECISIONS D-19): the session that first writes a request owns it, and the
// fork repeats its parent's rows. Modification time cannot decide this, because a parent that
// keeps going after the fork is modified later than the fork. A fork copies the
// parent's history, so its earliest timestamp is never earlier than the
// parent's; that is the key. When two files tie on it, the hook's record that
// one session started as a fork decides, then which file has lines of its own
// ahead of the shared prefix (the parent), and only then mtime and path.
func orderForIngest(ts []transcript, isFork func(session string) bool) {
	for i := range ts {
		ts[i].first = firstTimestamp(ts[i].path)
	}
	sort.SliceStable(ts, func(i, j int) bool { return ts[i].first < ts[j].first })
	for lo := 0; lo < len(ts); {
		hi := lo + 1
		for hi < len(ts) && ts[hi].first == ts[lo].first {
			hi++
		}
		if hi-lo > 1 {
			breakTies(ts[lo:hi], isFork)
		}
		lo = hi
	}
}

func breakTies(ts []transcript, isFork func(session string) bool) {
	forks := make([]bool, len(ts))
	keys := make([][]string, len(ts))
	for i, t := range ts {
		forks[i] = t.agent == "" && isFork != nil && isFork(t.session)
		keys[i] = lineKeys(t.path)
	}
	idx := make([]int, len(ts))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(x, y int) bool {
		i, j := idx[x], idx[y]
		if forks[i] != forks[j] {
			return forks[j]
		}
		if pi, pj := lead(keys[i], keys[j]), lead(keys[j], keys[i]); pi != pj {
			return pi > pj
		}
		a, b := ts[i], ts[j]
		if !a.mtime.Equal(b.mtime) {
			return a.mtime.Before(b.mtime)
		}
		return a.path < b.path
	})
	out := make([]transcript, len(ts))
	for n, i := range idx {
		out[n] = ts[i]
	}
	copy(ts, out)
}

// lead is how many of a's lines come before the first one b also has: lines
// of its own ahead of the shared prefix. Zero when nothing is shared.
func lead(a, b []string) int {
	in := make(map[string]bool, len(b))
	for _, k := range b {
		in[k] = true
	}
	for i, k := range a {
		if in[k] {
			return i
		}
	}
	return 0
}

var uuidMarker = []byte(`"uuid":"`)

// lineKeys identifies each of a transcript's first lines: a copied line keeps
// its uuid, and a line without one is compared whole.
func lineKeys(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	r := newLineReader(io.LimitReader(f, 2<<20))
	var out []string
	for i := 0; i < 200; i++ {
		line, err := r.next()
		if err != nil {
			break
		}
		key := string(line)
		if j := bytes.Index(line, uuidMarker); j >= 0 {
			rest := line[j+len(uuidMarker):]
			if k := bytes.IndexByte(rest, '"'); k > 0 {
				key = string(rest[:k])
			}
		}
		out = append(out, key)
	}
	return out
}

// startedAsFork reads the hook's events for a SessionStart with source fork.
// Event lines hold ids and enums only.
func startedAsFork(events string) bool {
	f, err := os.Open(events)
	if err != nil {
		return false
	}
	defer f.Close()
	r := newLineReader(io.LimitReader(f, 1<<20))
	for i := 0; i < 50; i++ {
		line, err := r.next()
		if err != nil {
			break
		}
		if bytes.Contains(line, []byte(`"ev":"SessionStart"`)) && bytes.Contains(line, []byte(`"src":"fork"`)) {
			return true
		}
	}
	return false
}

var tsMarker = []byte(`"timestamp":"`)

// firstTimestamp returns the earliest timestamp among the first lines of a
// transcript, in ms; a file with none sorts last.
func firstTimestamp(path string) int64 {
	f, err := os.Open(path)
	if err != nil {
		return math.MaxInt64
	}
	defer f.Close()
	r := newLineReader(io.LimitReader(f, 2<<20))
	best := int64(math.MaxInt64)
	for i := 0; i < 200; i++ {
		line, err := r.next()
		if err != nil {
			break
		}
		if j := bytes.Index(line, tsMarker); j >= 0 {
			rest := line[j+len(tsMarker):]
			if k := bytes.IndexByte(rest, '"'); k > 0 {
				if ms := parseMS(string(rest[:k])); ms != 0 && ms < best {
					best = ms
				}
			}
		}
	}
	return best
}

func sessionDirs(p paths.Paths) map[string]bool {
	out := map[string]bool{}
	ents, err := os.ReadDir(p.Sessions())
	if err != nil {
		return out
	}
	for _, e := range ents {
		if e.IsDir() && paths.ValidSessionID(e.Name()) {
			out[e.Name()] = true
		}
	}
	return out
}

// transcript line types worth parsing. Everything else (api-request*,
// atis-latch, mode, permission-mode, ai-title, last-prompt, queue-operation,
// attachment, system, file-history-snapshot) is skipped without decoding.
var (
	markUser      = []byte(`"type":"user"`)
	markAssistant = []byte(`"type":"assistant"`)
	markCost      = []byte(`"type":"cost-state"`)
)

type usageJSON struct {
	Input         int64 `json:"input_tokens"`
	Output        int64 `json:"output_tokens"`
	CacheRead     int64 `json:"cache_read_input_tokens"`
	CacheCreate   int64 `json:"cache_creation_input_tokens"`
	CacheCreation struct {
		T5m int64 `json:"ephemeral_5m_input_tokens"`
		T1h int64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
	ServerTool struct {
		Web int64 `json:"web_search_requests"`
	} `json:"server_tool_use"`
	Speed string `json:"speed"`
	Geo   string `json:"inference_geo"`
}

// lineJSON lists only the keys ingest reads. Message content (prompt text,
// assistant text, tool input and output) has no field here, so it is parsed
// past and discarded, never held.
type lineJSON struct {
	Type         string `json:"type"`
	UUID         string `json:"uuid"`
	LaunchID     string `json:"session_id"`
	Timestamp    string `json:"timestamp"`
	IsSidechain  bool   `json:"isSidechain"`
	PromptID     string `json:"promptId"`
	RequestID    string `json:"requestId"`
	TurnOrigin   string `json:"turnOrigin"`
	PromptSource string `json:"promptSource"`
	GitBranch    string `json:"gitBranch"`
	Message      struct {
		ID    string    `json:"id"`
		Model string    `json:"model"`
		Usage usageJSON `json:"usage"`
	} `json:"message"`
	TotalCostUSD float64 `json:"totalCostUSD"`
	StartTime    int64   `json:"startTime"`
}

type fileResult struct {
	progressed bool
	requests   int
}

func (d *DB) needsIngest(t transcript) (bool, error) {
	var size, mtimeNS int64
	err := d.sql.QueryRow("SELECT size, mtime_ns FROM ingest_files WHERE path = ?", t.path).Scan(&size, &mtimeNS)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return size != t.size || mtimeNS != t.mtime.UnixNano(), nil
}

// ingestFile reads the new complete lines of one transcript in one
// transaction. The stored offset moves only past whole lines.
func (d *DB) ingestFile(t transcript, cutoff int64) (fileResult, error) {
	var res fileResult
	var size, mtimeNS, off int64
	err := d.sql.QueryRow("SELECT size, mtime_ns, off FROM ingest_files WHERE path = ?", t.path).Scan(&size, &mtimeNS, &off)
	known := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return res, err
	}
	if known && size == t.size && mtimeNS == t.mtime.UnixNano() {
		return res, nil // unchanged: re-ingest is a no-op
	}
	if known && t.size < off {
		off = 0 // truncated or replaced
	}
	f, err := os.Open(t.path)
	if err != nil {
		return res, nil // vanished between listing and open
	}
	defer f.Close()
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return res, err
	}

	tx, err := d.sql.Begin()
	if err != nil {
		return res, err
	}
	defer tx.Rollback()

	w := &fileWriter{tx: tx, t: t, copiedFrom: map[string]int{}, cutoff: cutoff}
	if err := w.prepare(); err != nil {
		return res, err
	}
	if t.agent != "" {
		w.agentType = readAgentType(strings.TrimSuffix(t.path, ".jsonl") + ".meta.json")
	}

	// lineReader grows its buffer to fit any line; transcripts hold lines of
	// 100 KB and up.
	r := newLineReader(f)
	consumed := off
	for {
		line, err := r.next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break // a partial last line stays unread for the next run
			}
			return res, err
		}
		consumed += int64(len(line))
		if err := w.line(line); err != nil {
			return res, err
		}
	}
	if err := w.flush(); err != nil {
		return res, err
	}
	if _, err := tx.Exec(`INSERT INTO ingest_files(path,size,mtime_ns,off,session_id,agent_id) VALUES(?,?,?,?,?,?)
		ON CONFLICT(path) DO UPDATE SET size=excluded.size, mtime_ns=excluded.mtime_ns, off=excluded.off`,
		t.path, t.size, t.mtime.UnixNano(), consumed, t.session, t.agent); err != nil {
		return res, err
	}
	if err := tx.Commit(); err != nil {
		return res, err
	}
	res.progressed = true
	res.requests = w.requests
	return res, nil
}

type lineReader struct {
	r   io.Reader
	buf []byte
	pos int
	end int
}

func newLineReader(r io.Reader) *lineReader { return &lineReader{r: r, buf: make([]byte, 256<<10)} }

// next returns the next complete line including its newline, or io.EOF when
// only a partial line (or nothing) remains.
func (l *lineReader) next() ([]byte, error) {
	for {
		if i := bytes.IndexByte(l.buf[l.pos:l.end], '\n'); i >= 0 {
			line := l.buf[l.pos : l.pos+i+1]
			l.pos += i + 1
			return line, nil
		}
		if l.pos > 0 { // compact
			copy(l.buf, l.buf[l.pos:l.end])
			l.end -= l.pos
			l.pos = 0
		}
		if l.end == len(l.buf) { // a line longer than the buffer: grow
			nb := make([]byte, len(l.buf)*2)
			copy(nb, l.buf[:l.end])
			l.buf = nb
		}
		n, err := l.r.Read(l.buf[l.end:])
		l.end += n
		if err != nil && n == 0 {
			return nil, err
		}
	}
}

func readAgentType(metaPath string) string {
	b, err := os.ReadFile(metaPath)
	if err != nil {
		return ""
	}
	// agentType only: the same file carries a description derived from the prompt.
	var m struct {
		AgentType string `json:"agentType"`
	}
	if json.Unmarshal(b, &m) != nil {
		return ""
	}
	return clean.Ident(m.AgentType, 64)
}

// fileWriter accumulates a file's session-level facts and writes rows.
type fileWriter struct {
	tx        *sql.Tx
	t         transcript
	agentType string
	requests  int

	curPrompt string
	// session facts gathered over the file
	minTS, maxTS int64
	launchID     string
	branch       string
	// cutoff is the retention horizon: older requests are never (re)inserted.
	cutoff int64
	// copiedFrom counts rows this file repeats from other sessions, by owner.
	copiedFrom map[string]int

	insReq, insPrompt, insCost *sql.Stmt
}

func (w *fileWriter) prepare() error {
	var err error
	// A request's repeat lines carry a growing output_tokens; keep the largest
	// (LESSONS L-33). The WHERE leaves a row another session owns untouched,
	// which the RowsAffected check in line relies on.
	w.insReq, err = w.tx.Prepare(`INSERT INTO requests
		(message_id,request_id,session_id,prompt_id,agent_id,model,ts,input,output,cache_read,cache_5m,cache_1h,web,speed,geo)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(message_id,request_id) DO UPDATE SET output = MAX(requests.output, excluded.output)
		WHERE requests.session_id = excluded.session_id`)
	if err != nil {
		return err
	}
	w.insPrompt, err = w.tx.Prepare(`INSERT OR IGNORE INTO prompts(prompt_id,session_id,n,started_at)
		VALUES(?,?,(SELECT COUNT(*) FROM prompts WHERE session_id = ?) + 1,?)`)
	if err != nil {
		return err
	}
	w.insCost, err = w.tx.Prepare(`INSERT INTO cost_states(session_id,start_time,total_usd) VALUES(?,?,?)
		ON CONFLICT(session_id,start_time) DO UPDATE SET total_usd = excluded.total_usd`)
	if err != nil {
		return err
	}
	// Resume the "nearest preceding prompt" across an incremental read.
	if w.t.agent == "" {
		_ = w.tx.QueryRow("SELECT prompt_id FROM prompts WHERE session_id = ? ORDER BY n DESC LIMIT 1", w.t.session).Scan(&w.curPrompt)
	} else {
		_ = w.tx.QueryRow("SELECT prompt_id FROM requests WHERE session_id = ? AND agent_id = ? ORDER BY ts DESC LIMIT 1",
			w.t.session, w.t.agent).Scan(&w.curPrompt)
	}
	return nil
}

func (w *fileWriter) line(b []byte) error {
	isUser := bytes.Contains(b, markUser)
	isAsst := bytes.Contains(b, markAssistant)
	isCost := bytes.Contains(b, markCost)
	if !isUser && !isAsst && !isCost {
		return nil
	}
	var l lineJSON
	if json.Unmarshal(b, &l) != nil {
		return nil // one bad line is skipped, never fatal
	}
	ts := parseMS(l.Timestamp) // zero on failure; never time.Now()
	switch {
	case l.Type == "user" && isUser:
		w.touch(ts, &l)
		if w.t.agent == "" && l.IsSidechain {
			return nil
		}
		synthetic := l.TurnOrigin == "task_notification" || l.PromptSource == "system"
		if l.PromptID != "" && !synthetic {
			w.curPrompt = l.PromptID
			if w.t.agent == "" && ts != 0 {
				if _, err := w.insPrompt.Exec(l.PromptID, w.t.session, w.t.session, ts); err != nil {
					return err
				}
			}
		}
	case l.Type == "assistant" && isAsst:
		w.touch(ts, &l)
		if w.t.agent == "" && l.IsSidechain {
			return nil // main files skip sidechain lines; subagent files are all sidechain (LESSONS L-32)
		}
		m := l.Message
		if m.ID == "" || l.RequestID == "" || m.Model == "" || m.Model == "<synthetic>" || ts == 0 {
			return nil
		}
		u := m.Usage
		c5, c1 := u.CacheCreation.T5m, u.CacheCreation.T1h
		if rest := u.CacheCreate - c5 - c1; rest > 0 {
			c5 += rest // writes with no tier breakdown are priced as 5 minute writes
		}
		if ts < w.cutoff {
			return nil
		}
		res, err := w.insReq.Exec(m.ID, l.RequestID, w.t.session, w.curPrompt, w.t.agent, m.Model, ts,
			u.Input, u.Output, u.CacheRead, c5, c1, u.ServerTool.Web, u.Speed, u.Geo)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			// Another session owns this request already: this is the verbatim
			// prefix of a fork. The first writer keeps the row (never move it),
			// and that writer is the fork's parent.
			var owner string
			if err := w.tx.QueryRow("SELECT session_id FROM requests WHERE message_id = ? AND request_id = ?", m.ID, l.RequestID).Scan(&owner); err == nil {
				w.copiedFrom[owner]++
			}
			return nil
		}
		w.requests++
	case l.Type == "cost-state" && isCost:
		if l.StartTime == 0 {
			return nil
		}
		if _, err := w.insCost.Exec(w.t.session, l.StartTime, l.TotalCostUSD); err != nil {
			return err
		}
	}
	return nil
}

func (w *fileWriter) touch(ts int64, l *lineJSON) {
	if ts != 0 {
		if w.minTS == 0 || ts < w.minTS {
			w.minTS = ts
		}
		if ts > w.maxTS {
			w.maxTS = ts
		}
	}
	if w.launchID == "" && l.LaunchID != "" {
		w.launchID = l.LaunchID
	}
	if w.branch == "" && l.GitBranch != "" && l.GitBranch != "HEAD" {
		w.branch = l.GitBranch
	}
}

// flush writes the session and subagent rows. Every column is merged with the
// existing row in plain SQL (min, max, keep-first), so interleaved writers
// cannot undo each other.
func (w *fileWriter) flush() error {
	if _, err := w.tx.Exec(`INSERT INTO sessions(session_id,started_at,last_activity_at,launch_session_id,transcript_branch,forked_from)
		VALUES(?,?,?,?,?,?)
		ON CONFLICT(session_id) DO UPDATE SET
		  started_at = CASE WHEN excluded.started_at = 0 THEN sessions.started_at
		                    WHEN sessions.started_at = 0 OR excluded.started_at < sessions.started_at THEN excluded.started_at
		                    ELSE sessions.started_at END,
		  last_activity_at = MAX(sessions.last_activity_at, excluded.last_activity_at),
		  launch_session_id = CASE WHEN sessions.launch_session_id = '' THEN excluded.launch_session_id ELSE sessions.launch_session_id END,
		  transcript_branch = CASE WHEN sessions.transcript_branch = '' THEN excluded.transcript_branch ELSE sessions.transcript_branch END,
		  forked_from = CASE WHEN sessions.forked_from = '' THEN excluded.forked_from ELSE sessions.forked_from END`,
		w.t.session, w.minTS, w.maxTS, w.launchID, w.branch, w.parent()); err != nil {
		return err
	}
	if w.t.agent != "" {
		if _, err := w.tx.Exec(`INSERT INTO subagents(session_id,agent_id,agent_type,first_at,last_at) VALUES(?,?,?,?,?)
			ON CONFLICT(session_id,agent_id) DO UPDATE SET
			  agent_type = CASE WHEN subagents.agent_type = '' THEN excluded.agent_type ELSE subagents.agent_type END,
			  first_at = CASE WHEN subagents.first_at = 0 OR (excluded.first_at <> 0 AND excluded.first_at < subagents.first_at) THEN excluded.first_at ELSE subagents.first_at END,
			  last_at = MAX(subagents.last_at, excluded.last_at)`,
			w.t.session, w.t.agent, w.agentType, w.minTS, w.maxTS); err != nil {
			return err
		}
	}
	return nil
}

func parseMS(s string) int64 {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

// parent is the session that owns most of the rows this file copies, or "".
func (w *fileWriter) parent() string {
	best, n := "", 0
	for owner, c := range w.copiedFrom {
		if owner != w.t.session && (c > n || (c == n && owner < best)) {
			best, n = owner, c
		}
	}
	return best
}

// FinalizeLive refreshes the rows of sessions that are running right now from
// their state dirs (status line totals, task, banked spend), without walking
// transcripts. The refresher calls it every tick so today's spend moves with
// the status line between full ingests.
func (d *DB) FinalizeLive(o IngestOpts) error {
	lock, err := flock.TryLock(o.Paths.Locks()+"/ingest.lock", 0)
	if errors.Is(err, flock.ErrBusy) {
		return nil
	}
	if err != nil {
		return err
	}
	defer lock.Unlock()
	if o.Now == nil {
		o.Now = time.Now
	}
	ids := map[string]bool{}
	for id := range sessionDirs(o.Paths) {
		if o.IsLive != nil && o.IsLive(id) {
			ids[id] = true
		}
	}
	return d.finalizeAll(ids, o)
}

// LastIngest returns when the last full ingest finished; ok is false if none.
func (d *DB) LastIngest() (time.Time, bool) {
	v, ok, err := d.Meta("last_ingest")
	if err != nil || !ok {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, v)
	return t, err == nil
}
