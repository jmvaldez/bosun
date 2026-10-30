package doctor

import (
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/jmvaldez/bosun/internal/claude"
	"github.com/jmvaldez/bosun/internal/ledger"
)

func (e Env) checkIdentity(add addFn) {
	if e.ClaudeBin == "" {
		add("claude", Fail, "claude does not resolve on PATH (set claude.bin)")
		return
	}
	add("claude", OK, "claude resolves at %s", e.ClaudeBin)
	id, ok := claude.ReadIdentity(e.Paths.Identity())
	if !ok {
		add("identity", Warn, "no cached account key (identity.json): run bosun install or bosun refresh")
		return
	}
	out, err := e.Runner.Run(5*time.Second, e.ClaudeBin, "auth", "status", "--json")
	if err != nil {
		add("identity", Warn, "cached account key found, but claude auth status failed: %v", err)
		return
	}
	var st struct {
		Email string `json:"email"`
	}
	_ = json.Unmarshal(out, &st)
	if cur := strings.ToLower(strings.TrimSpace(st.Email)); cur != "" && cur != id.AccountKey {
		add("identity", Fail, "the cached account key is for a different login than claude reports now: run bosun refresh")
		return
	}
	add("identity", OK, "cached account key matches the current login (cached %v ago)", e.Now.Sub(id.FetchedAt).Round(time.Minute))
	if c, _ := claude.ReadAgents(e.Paths.AgentsCache(), e.Now, claude.AgentsTTL); !c.FetchedAt.IsZero() {
		add("agents-cache", OK, "agents.json is %v old", e.Now.Sub(c.FetchedAt).Round(time.Second))
	}
}

func (e Env) checkState(add addFn) {
	dir := e.Paths.State
	if dir == "" {
		add("state-dir", Fail, "no state dir could be resolved (HOME unset?)")
		return
	}
	fi, err := os.Stat(dir)
	switch {
	case os.IsNotExist(err):
		add("state-dir", Warn, "%s does not exist yet; the first hook creates it", dir)
		return
	case err != nil || !fi.IsDir():
		add("state-dir", Fail, "%s is not a usable directory: %v", dir, err)
		return
	}
	if err := accessWritable(dir); err != nil {
		add("state-dir", Fail, "%s is not writable: %v", dir, err)
	} else {
		add("state-dir", OK, "%s exists and is writable", dir)
	}
	switch t, ok := ledger.ReadIngestMarker(e.Paths.IngestMarker()); {
	case !ok:
		add("ingest", Warn, "no ingest has completed yet (the refresher runs one within minutes of use)")
	case e.Now.Sub(t) > ingestMaxAge:
		add("ingest", Fail, "the last ingest was %v ago (over 20 days); Claude Code prunes transcripts at 30", e.Now.Sub(t).Round(time.Hour))
	default:
		add("ingest", OK, "last ingest %v ago", e.Now.Sub(t).Round(time.Minute))
	}
}
