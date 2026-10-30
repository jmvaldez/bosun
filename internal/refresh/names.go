package refresh

import (
	"encoding/json"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/jmvaldez/bosun/internal/runner"
)

// ParseNames reads the session names out of `claude agents --json`, keyed by
// session id, with control characters removed. The name is derived from prompt
// text, so the result is for render-time display only: it lives in memory and
// is never written to a file, a log, the ledger or notification state.
func ParseNames(out []byte) (map[string]string, error) {
	var raw []struct {
		SessionID string `json:"sessionId"`
		Name      string `json:"name"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}
	names := make(map[string]string, len(raw))
	for _, r := range raw {
		name := strings.Join(strings.FieldsFunc(strings.Map(func(c rune) rune {
			if unicode.IsControl(c) {
				return ' '
			}
			return c
		}, r.Name), unicode.IsSpace), " ")
		if r.SessionID != "" && name != "" {
			names[r.SessionID] = name
		}
	}
	return names, nil
}

// Names serves session names to the popup. A fetch runs in the background so a
// slow `claude agents --json` never holds up a frame; until one lands the
// caller gets the previous map, or none. Nothing is persisted.
type Names struct {
	run     runner.Runner
	bin     string
	ttl     time.Duration
	mu      sync.Mutex
	names   map[string]string
	at      time.Time
	pending bool
}

// NewNames builds a source that runs bin through run, at most once per ttl.
func NewNames(run runner.Runner, bin string, ttl time.Duration) *Names {
	return &Names{run: run, bin: bin, ttl: ttl}
}

// Get returns the cached names and starts a refetch when they are older than
// the TTL and none is in flight. A failed fetch keeps the old names and retries
// after the TTL.
func (n *Names) Get(now time.Time) map[string]string {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.pending && now.Sub(n.at) >= n.ttl {
		n.pending = true
		go n.fetch(now)
	}
	return n.names
}

func (n *Names) fetch(started time.Time) {
	var names map[string]string
	if b, err := n.run.Run(agentsTimeout, n.bin, "agents", "--json"); err == nil {
		names, _ = ParseNames(b)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.pending, n.at = false, started
	if names != nil {
		n.names = names
	}
}
