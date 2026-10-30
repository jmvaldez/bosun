// Package rules keeps alert state. The evaluator works out which conditions
// hold on each tick and Reconcile turns that into at most one alert per rule
// and scope, each with a short id, re-armed when its condition clears.
package rules

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"sort"
	"time"

	"github.com/jmvaldez/bosun/internal/atomicfile"
)

// Rule names, as in config and notices.
const (
	SubagentRuntime = "subagent_runtime"
	Quiet           = "quiet"
	SubagentCost    = "subagent_cost"
	SessionCost     = "session_cost"
	DailyPace       = "daily_pace"
	DailyBudget     = "daily_budget"
)

// Alert is one entry of alerts.json, keyed "<rule>|<scope>".
type Alert struct {
	ID      string    `json:"id"`
	Rule    string    `json:"rule"`
	Scope   string    `json:"scope"`
	Session string    `json:"session,omitempty"` // empty for account-level alerts
	Agent   string    `json:"agent,omitempty"`   // subagent id
	Since   time.Time `json:"since"`
	// Level is the multiple crossed, for session_cost.
	Level int `json:"level,omitempty"`
	// Day pins a sticky alert (pace, budget) to the day it fired.
	Day       string `json:"day,omitempty"`
	Dismissed bool   `json:"dismissed,omitempty"`
	// Detail is a short enum-and-number string for the popup, never text.
	Detail string `json:"detail,omitempty"`
}

// Store is alerts.json: alerts whose condition still holds (a sticky one, until
// its day ends), dismissed ones included. A cleared one is removed, which is
// what re-arms it.
type Store map[string]*Alert

// Key builds the dedupe key.
func Key(rule, scope string) string { return rule + "|" + scope }

// Load reads alerts.json; missing or corrupt means no alerts.
func Load(path string) Store {
	s := Store{}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	if s == nil {
		s = Store{}
	}
	return s
}

// Save writes alerts.json atomically.
func (s Store) Save(path string) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return atomicfile.Write(path, b, 0o600)
}

// Cond is a condition that holds right now.
type Cond struct {
	Rule    string
	Scope   string
	Session string
	Agent   string
	Level   int
	Detail  string
	// Sticky alerts (pace, budget) stay until the day rolls over, so a ratio
	// flapping around its threshold cannot fire twice in one day.
	Sticky bool
	Day    string
}

// Fire is an alert that fired this tick.
type Fire struct {
	Alert Alert
	// CatchUp is set when a catch-up run found the alert: it shows in the
	// segment and popup but never becomes a desktop notification.
	CatchUp bool
}

// Reconcile applies this tick's conditions. It returns alerts that fired
// (new, or session_cost crossing its next multiple) and alerts that cleared.
// A dismissed alert stays hidden until its condition clears; a new level
// is a later fire and shows again.
func (s Store) Reconcile(conds []Cond, now time.Time, today string, catchUp bool) (fires []Fire, cleared []Alert) {
	holds := map[string]bool{}
	for _, c := range conds {
		k := Key(c.Rule, c.Scope)
		holds[k] = true
		a := s[k]
		switch {
		case a == nil:
			a = &Alert{Rule: c.Rule, Scope: c.Scope, Session: c.Session, Agent: c.Agent, Since: now, Level: c.Level, Detail: c.Detail}
			if c.Sticky {
				a.Day = c.Day
			}
			a.ID = shortID(k, now)
			s[k] = a
			fires = append(fires, Fire{Alert: *a, CatchUp: catchUp})
		case c.Level > a.Level:
			a.Level, a.Since, a.Dismissed, a.Detail = c.Level, now, false, c.Detail
			a.ID = shortID(k, now)
			fires = append(fires, Fire{Alert: *a, CatchUp: catchUp})
		default:
			a.Detail = c.Detail
		}
	}
	for k, a := range s {
		if holds[k] {
			continue
		}
		if a.Day != "" && a.Day == today {
			continue // sticky: held until the day rolls over
		}
		cleared = append(cleared, *a)
		delete(s, k)
	}
	sort.Slice(cleared, func(i, j int) bool { return cleared[i].ID < cleared[j].ID })
	return fires, cleared
}

// shortID is five hex digits of the key and time: short enough to type into
// `bosun ack`, and new for every episode.
func shortID(key string, t time.Time) string {
	h := sha256.Sum256([]byte(key + t.UTC().Format(time.RFC3339Nano)))
	return hex.EncodeToString(h[:])[:5]
}

// Active returns the alerts that are not dismissed.
func (s Store) Active() []*Alert {
	var out []*Alert
	for _, a := range s {
		if !a.Dismissed {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Dismiss hides the alert with this id (or all of them) until its condition
// clears. It returns the dismissed alerts. Dismissal never changes rule state.
func (s Store) Dismiss(id string, all bool) []Alert {
	var out []Alert
	for _, a := range s {
		if (all || a.ID == id) && !a.Dismissed {
			a.Dismissed = true
			out = append(out, *a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// BySession groups the undismissed session and subagent alerts, quiet included,
// by session; account-level alerts are left out.
func (s Store) BySession() map[string][]*Alert {
	out := map[string][]*Alert{}
	for _, a := range s.Active() {
		if a.Session != "" {
			out[a.Session] = append(out[a.Session], a)
		}
	}
	return out
}

// AppendLog records a fired alert in alerts.jsonl: ids, rules and scopes only.
func AppendLog(path string, a Alert, now time.Time) error {
	line, err := json.Marshal(struct {
		TS    string `json:"ts"`
		ID    string `json:"id"`
		Rule  string `json:"rule"`
		Scope string `json:"scope"`
		Level int    `json:"level,omitempty"`
	}{now.UTC().Format(time.RFC3339Nano), a.ID, a.Rule, a.Scope, a.Level})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}
