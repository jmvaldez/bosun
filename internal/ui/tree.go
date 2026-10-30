package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jmvaldez/bosun/internal/spend"
	"github.com/jmvaldez/bosun/internal/term"
)

type nodeKind int

const (
	kGroup nodeKind = iota
	kSession
	kPrompt
	kRequest
	kSub
	kMore
	kUnattr
)

// node is one row of the Spend tree. Totals roll up from the contributions, so
// a parent always equals the sum of what it holds.
type node struct {
	kind     nodeKind
	key      string // unique path, for fold state
	label    string
	session  string
	prompt   string
	usd      float64
	est      bool
	shown    int // contributions with a dollar figure
	hidden   int // contributions live cost hides (spend.Redact)
	runaway  bool
	reqs     int
	repo     string // for groups and sessions: the repo's short name
	meta     string // dim text after the label: where and when
	num      int    // prompt number
	in, out  int64
	cache    int64
	first    time.Time
	last     time.Time
	children []*node
	depth    int
}

const maxRequestsShown = 3

func (m *Model) since() time.Time {
	t, err := m.data.Clock.Since(periods[m.period], m.data.Now)
	if err != nil {
		return time.Time{}
	}
	return t
}

func (m *Model) groupBy() string { return groups[m.group] }

// rebuildTree builds the tree for the current period and grouping.
func (m *Model) rebuildTree() {
	if m.data.Clock.Loc == nil {
		m.tree = nil
		return
	}
	cs := spend.Filter(m.data.Contribs, m.since(), "")
	by := m.groupBy()
	roots := map[string]*node{}
	sessions := map[string]*node{} // by group key + session
	prompts := map[string]*node{}
	subs := map[string]*node{}
	label := m.sessionLabels()
	m.lastPrompt = map[string]int{}

	get := func(mp map[string]*node, k string, mk func() *node) *node {
		if n, ok := mp[k]; ok {
			return n
		}
		n := mk()
		n.key = k
		mp[k] = n
		return n
	}
	add := func(n *node, c spend.Contribution) {
		n.usd += c.USD
		n.est = n.est || c.Est
		if c.Redacted {
			n.hidden++
		} else {
			n.shown++
		}
		if c.Kind == spend.RequestRow {
			n.reqs++
			n.in += c.Req.Input
			n.out += c.Req.Output
			n.cache += c.Req.CacheRead + c.Req.Cache5m + c.Req.Cache1h
			if n.first.IsZero() || c.TS.Before(n.first) {
				n.first = c.TS
			}
			if c.TS.After(n.last) {
				n.last = c.TS
			}
		}
	}

	for _, c := range cs {
		gk := spend.GroupKey(by, c, m.data.Clock)
		root := get(roots, "g:"+gk, func() *node { return &node{kind: kGroup, label: gk} })
		add(root, c)
		parent := root
		if by != "session" {
			sk := "g:" + gk + "/s:" + c.Session.ID
			sn := get(sessions, sk, func() *node {
				l, meta := label(c.Session)
				return &node{kind: kSession, session: c.Session.ID, label: l, meta: meta, repo: shortRepo(c.Session.Repo)}
			})
			if root.repo == "" {
				root.repo = sn.repo
			}
			add(sn, c)
			if sn.depth == 0 {
				root.children = appendOnce(root.children, sn)
			}
			parent = sn
		} else {
			root.session = c.Session.ID
			root.kind = kSession
			root.label, root.meta = label(c.Session)
			root.repo = shortRepo(c.Session.Repo)
		}
		if c.Kind == spend.Unattributed {
			un := get(prompts, parent.key+"/unattributed", func() *node { return &node{kind: kUnattr, label: "unattributed", session: c.Session.ID} })
			add(un, c)
			parent.children = appendOnce(parent.children, un)
			continue
		}
		pk := parent.key + "/p:" + c.Req.PromptID
		pn := get(prompts, pk, func() *node {
			l, when := "no prompt", ""
			if c.PromptN > 0 {
				l = fmt.Sprintf("prompt #%d", c.PromptN)
				if p, ok := m.data.Snap.Prompts[c.Req.PromptID]; ok && !p.StartedAt.IsZero() {
					when = p.StartedAt.In(m.data.Clock.Loc).Format("15:04")
				}
				if c.PromptN > m.lastPrompt[c.Session.ID] {
					m.lastPrompt[c.Session.ID] = c.PromptN
				}
			}
			return &node{kind: kPrompt, label: l, meta: when, num: c.PromptN, session: c.Session.ID, prompt: c.Req.PromptID}
		})
		add(pn, c)
		parent.children = appendOnce(parent.children, pn)
		if c.Req.AgentID != "" {
			sk := pk + "/a:" + c.Req.AgentID
			sub := get(subs, sk, func() *node {
				t := c.AgentType
				if t == "" {
					t = "subagent"
				}
				return &node{kind: kSub, session: c.Session.ID, label: t, runaway: m.subRunaway(c.Session.ID, c.Req.AgentID)}
			})
			add(sub, c)
			if !strings.Contains(sub.label, " ") && c.Req.Model != "" {
				sub.label += " " + modelLabel(c.Req.Model)
			}
			pn.children = appendOnce(pn.children, sub)
			continue
		}
		rn := &node{kind: kRequest, key: pk + "/r:" + c.Req.MessageID, session: c.Session.ID,
			label: modelLabel(c.Req.Model), usd: c.USD, est: c.Est, reqs: 1, shown: 1 - redactedInt(c), hidden: redactedInt(c),
			in: c.Req.Input, out: c.Req.Output, cache: c.Req.CacheRead + c.Req.Cache5m + c.Req.Cache1h, first: c.TS}
		pn.children = append(pn.children, rn)
	}

	var out []*node
	for _, r := range roots {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].usd != out[j].usd {
			return out[i].usd > out[j].usd
		}
		return out[i].label < out[j].label
	})
	for _, r := range out {
		finish(r, 0)
	}
	m.tree = out
	if n := len(m.visible()); m.spendSel >= n {
		m.spendSel = max(n-1, 0)
	}
}

func redactedInt(c spend.Contribution) int {
	if c.Redacted {
		return 1
	}
	return 0
}

func appendOnce(list []*node, n *node) []*node {
	for _, x := range list {
		if x == n {
			return list
		}
	}
	n.depth = 1 // marks "attached"; real depths are set in finish
	return append(list, n)
}

// finish orders children, sets depths, and folds requests beyond the first few
// into one "+ N more requests" line.
func finish(n *node, depth int) {
	n.depth = depth
	switch n.kind {
	case kGroup, kSession:
		sort.Slice(n.children, func(i, j int) bool {
			a, b := n.children[i], n.children[j]
			if a.kind == kUnattr || b.kind == kUnattr {
				return b.kind == kUnattr && a.kind != kUnattr
			}
			if a.usd != b.usd {
				return a.usd > b.usd
			}
			return a.label < b.label
		})
	case kPrompt:
		sort.SliceStable(n.children, func(i, j int) bool {
			a, b := n.children[i], n.children[j]
			if (a.kind == kSub) != (b.kind == kSub) {
				return a.kind == kSub
			}
			return a.first.Before(b.first)
		})
		var reqs []*node
		var rest []*node
		for _, c := range n.children {
			if c.kind != kRequest {
				rest = append(rest, c)
			} else {
				reqs = append(reqs, c)
			}
		}
		if len(reqs) > maxRequestsShown {
			more := &node{kind: kMore, key: n.key + "/more", session: n.session}
			for _, r := range reqs[maxRequestsShown:] {
				more.usd += r.usd
				more.est = more.est || r.est
				more.shown += r.shown
				more.hidden += r.hidden
				more.reqs++
			}
			more.label = fmt.Sprintf("+ %d more requests", more.reqs)
			reqs = append(reqs[:maxRequestsShown], more)
		}
		n.children = append(rest, reqs...)
	}
	for _, c := range n.children {
		finish(c, depth+1)
	}
}

// visible flattens the tree by fold state.
func (m *Model) visible() []*node {
	var out []*node
	var walk func(n *node)
	walk = func(n *node) {
		out = append(out, n)
		if m.expanded[n.key] {
			for _, c := range n.children {
				walk(c)
			}
		}
	}
	for _, r := range m.tree {
		walk(r)
	}
	return out
}

func (m *Model) subRunaway(session, agent string) bool {
	for _, s := range m.data.Sessions {
		if s.ID == session {
			for _, sub := range s.Subs {
				if sub.ID == agent {
					return sub.Alert
				}
			}
		}
	}
	return false
}

// sessionLabels names a session "session a1f3…" and, dim after it, where it
// runs and when it started.
func (m *Model) sessionLabels() func(ledgerSession) (string, string) {
	where := map[string]string{}
	for _, s := range m.data.Sessions {
		if s.Where != "" {
			where[s.ID] = tmuxCell(s, m.data.Sessions)
		}
	}
	return func(s ledgerSession) (string, string) {
		id := s.ID
		if len(id) > 4 {
			id = id[:4] + "…"
		}
		var meta []string
		if w := where[s.ID]; w != "" {
			meta = append(meta, w)
		}
		if st, ok := m.data.Snap.Sessions[s.ID]; ok && !st.StartedAt.IsZero() && m.data.Clock.Loc != nil {
			meta = append(meta, "started "+st.StartedAt.In(m.data.Clock.Loc).Format("15:04"))
		}
		return "session " + id, strings.Join(meta, " · ")
	}
}

// revealSession expands the tree down to a session and selects it.
func (m *Model) revealSession(id string) {
	m.rebuildTree()
	var find func(n *node, path []*node) []*node
	find = func(n *node, path []*node) []*node {
		path = append(path, n)
		if n.kind == kSession && n.session == id {
			return path
		}
		for _, c := range n.children {
			if c.kind == kGroup || c.kind == kSession {
				if p := find(c, path); p != nil {
					return p
				}
			}
		}
		return nil
	}
	for _, r := range m.tree {
		if p := find(r, nil); p != nil {
			for _, n := range p {
				m.expanded[n.key] = true
			}
			for i, n := range m.visible() {
				if n == p[len(p)-1] {
					m.spendSel = i
				}
			}
			return
		}
	}
	m.status = "no spend recorded for that session in this period"
}

func (m *Model) updateSpend(k term.Key) bool {
	vis := m.visible()
	switch k {
	case "j", "down":
		m.spendSel = min(m.spendSel+1, max(len(vis)-1, 0))
	case "k", "up":
		m.spendSel = max(m.spendSel-1, 0)
	case "l", "right":
		if m.spendSel < len(vis) && len(vis[m.spendSel].children) > 0 {
			m.expanded[vis[m.spendSel].key] = true
		}
	case "h", "left":
		if m.spendSel >= len(vis) {
			return false
		}
		n := vis[m.spendSel]
		if m.expanded[n.key] {
			delete(m.expanded, n.key)
			return false
		}
		// Already collapsed or a leaf: go to the parent.
		for i := m.spendSel - 1; i >= 0; i-- {
			if vis[i].depth < n.depth {
				m.spendSel = i
				break
			}
		}
	case "p", "r":
		m.period = (m.period + 1) % len(periods)
		m.rebuildTree()
	case "g", "b":
		m.group = (m.group + 1) % len(groups)
		m.expanded = map[string]bool{}
		m.rebuildTree()
	case "e":
		path, err := m.act.Export(periods[m.period], groups[m.group])
		if err != nil {
			m.status = "export failed: " + err.Error()
		} else {
			m.status = "wrote " + path
		}
	case "v":
		if !m.opts.Peek {
			m.status = "peek is off (ui.peek = false)"
			return false
		}
		if m.spendSel < len(vis) && vis[m.spendSel].kind == kPrompt {
			if m.peekPrompt == vis[m.spendSel].key {
				m.peekPrompt = ""
			} else {
				m.peekPrompt = vis[m.spendSel].key
			}
		}
	case "enter":
		if m.spendSel < len(vis) && vis[m.spendSel].session != "" {
			for _, s := range m.data.Sessions {
				if s.ID == vis[m.spendSel].session {
					return m.jump(s)
				}
			}
			m.status = "that session is not running in tmux"
		}
	case "a":
		if v, ok := m.oldestWaiting(); ok {
			return m.jump(v)
		}
		m.status = "nothing waiting"
	}
	return false
}
