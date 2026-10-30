package spend

import (
	"fmt"
	"sort"
	"strings"
)

// Groupings, as `spend --by` accepts them.
var Groupings = []string{"day", "task", "repo", "session", "prompt", "request", "model", "subagent"}

// Row is one line of a view. Sessions, requests and tokens count what the row
// holds; USD is the dollars, Est marks an estimate.
type Row struct {
	Key      string  `json:"key"`
	Sessions int     `json:"sessions"`
	Requests int     `json:"requests"`
	Input    int64   `json:"input_tokens"`
	Output   int64   `json:"output_tokens"`
	Cache    int64   `json:"cache_tokens"`
	USD      float64 `json:"usd"`
	EstUSD   float64 `json:"est_usd"`
	Est      bool    `json:"est"`
	// Redacted counts the sessions in the row whose amounts are hidden because
	// live cost is off (Redact). USD leaves them out.
	Redacted int `json:"redacted_sessions,omitempty"`
}

// Result is a whole view.
type Result struct {
	By    string `json:"by"`
	Since string `json:"since,omitempty"`
	Rows  []Row  `json:"rows"`
	Total Row    `json:"total"`
}

// IsGrouping reports whether by is implemented. An unknown grouping is an
// error, never an alias of another (LESSONS L-47).
func IsGrouping(by string) bool {
	for _, g := range Groupings {
		if g == by {
			return true
		}
	}
	return false
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// key names the group a contribution falls into.
func key(by string, c Contribution, clock Clock) string {
	s := c.Session
	switch by {
	case "day":
		if c.TS.IsZero() {
			return "unknown"
		}
		return clock.Day(c.TS)
	case "task":
		if s.Task == "" {
			return "untasked"
		}
		return s.Task
	case "repo":
		if s.Repo == "" {
			return "unknown"
		}
		return s.Repo
	case "session":
		return s.ID
	case "model":
		if c.Kind == Unattributed && c.Req.Model == "" {
			return "unattributed"
		}
		return c.Req.Model
	case "prompt":
		switch {
		case c.Kind == Unattributed:
			return short(s.ID) + " unattributed"
		case c.PromptN > 0:
			return fmt.Sprintf("%s prompt #%d", short(s.ID), c.PromptN)
		}
		return short(s.ID) + " no prompt"
	case "request":
		if c.Kind == Unattributed {
			return short(s.ID) + " unattributed"
		}
		return c.Req.MessageID + "/" + c.Req.RequestID
	case "subagent":
		switch {
		case c.Kind == Unattributed && c.Req.AgentID == "":
			return short(s.ID) + " unattributed"
		case c.Req.AgentID == "":
			return short(s.ID) + " main"
		case c.AgentType != "":
			return fmt.Sprintf("%s %s#%s", short(s.ID), c.AgentType, c.Req.AgentID)
		}
		return fmt.Sprintf("%s subagent#%s", short(s.ID), c.Req.AgentID)
	}
	return ""
}

// Group builds the view. Every grouping sums the same contributions, so
// Total is identical across them.
func Group(cs []Contribution, by string, clock Clock) (Result, error) {
	if !IsGrouping(by) {
		return Result{}, fmt.Errorf("spend: unknown grouping %q (have %s)", by, strings.Join(Groupings, ", "))
	}
	rows := map[string]*Row{}
	seen := map[string]map[string]bool{}
	hid := map[string]map[string]bool{}
	var total Row
	totalSessions, totalHid := map[string]bool{}, map[string]bool{}
	for _, c := range cs {
		k := key(by, c, clock)
		r := rows[k]
		if r == nil {
			r = &Row{Key: k}
			rows[k] = r
			seen[k], hid[k] = map[string]bool{}, map[string]bool{}
		}
		add := func(r *Row) {
			r.USD += c.USD
			if c.Est {
				r.EstUSD += c.USD
				r.Est = true
			}
			if c.Kind == RequestRow {
				r.Requests++
				r.Input += c.Req.Input
				r.Output += c.Req.Output
				r.Cache += c.Req.CacheRead + c.Req.Cache5m + c.Req.Cache1h
			}
		}
		add(r)
		add(&total)
		seen[k][c.Session.ID] = true
		totalSessions[c.Session.ID] = true
		if c.Redacted {
			hid[k][c.Session.ID] = true
			totalHid[c.Session.ID] = true
		}
	}
	res := Result{By: by}
	for k, r := range rows {
		r.Sessions, r.Redacted = len(seen[k]), len(hid[k])
		res.Rows = append(res.Rows, *r)
	}
	total.Key, total.Sessions, total.Redacted = "TOTAL", len(totalSessions), len(totalHid)
	res.Total = total
	sort.Slice(res.Rows, func(i, j int) bool {
		a, b := res.Rows[i], res.Rows[j]
		if by == "day" {
			return a.Key < b.Key
		}
		if a.USD != b.USD {
			return a.USD > b.USD
		}
		return a.Key < b.Key
	})
	return res, nil
}

// GroupKey exports key for views (the popup's tree) that need the same keys as
// `spend --by`.
func GroupKey(by string, c Contribution, clock Clock) string { return key(by, c, clock) }
