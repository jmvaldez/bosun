package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jmvaldez/bosun/internal/eval"
	"github.com/jmvaldez/bosun/internal/ledger"
	"github.com/jmvaldez/bosun/internal/live"
	"github.com/jmvaldez/bosun/internal/pace"
	"github.com/jmvaldez/bosun/internal/peek"
	"github.com/jmvaldez/bosun/internal/segment"
	"github.com/jmvaldez/bosun/internal/spend"
	"github.com/jmvaldez/bosun/internal/term"
)

type Tab int

const (
	TabAgents Tab = iota
	TabSpend
)

// SpendCell is a session's spend and whether any of it is an estimate. Hidden
// is true when live cost is off and the amount has no trusted figure: the cell
// shows a dash.
type SpendCell struct {
	USD    float64
	Est    bool
	Hidden bool
}

// Data is everything a frame needs; Run's loop refills it on every tick.
type Data struct {
	Now      time.Time
	Sessions []eval.View
	Counts   segment.Counts
	Repos    int
	Today    spend.DaySum
	Cap      float64
	Pace     pace.Status
	Snap     ledger.Snapshot
	Contribs []spend.Contribution
	Clock    spend.Clock
	// Names are Claude Code's session names by session id, held in memory for
	// the frame (D-27). Set only with show_text; never stored.
	Names map[string]string
}

// Actions are the side effects the popup can cause. Production wires tmux;
// tests wire fakes.
type Actions interface {
	Jump(pane string) error
	Diff(cwd string) error
	// Interrupt re-checks the pane and sends the interrupt keys; the message
	// says what happened ("sent", or "pane changed, not sent").
	Interrupt(v eval.View) (string, error)
	Dismiss(session, agent string) (int, error)
	Export(period, group string) (string, error)
	// Peek and PromptLine read transcript text at render time; callers drop it
	// after the frame.
	Peek(v eval.View) string
	PromptLine(session, prompt string) string
	// Latest is the newest tool call in the session's transcript (the command
	// being asked about, the question, what a working session is editing). It
	// is only called when Options.ShowText is set (D-27).
	Latest(v eval.View) peek.Info
}

type Options struct {
	Peek  bool // ui.peek: whether `v` may read the transcript
	Color bool
	// ShowText is privacy.show_text (D-27): state details, questions, commands
	// and prompt lines are read from the transcript at render time. Off,
	// details are enum-only.
	ShowText bool
	// ASCII swaps the glyphs for plain characters (ui.glyphs = "ascii").
	ASCII bool
}

var (
	periods = []string{"today", "7d", "30d"}
	groups  = []string{"task", "day", "repo", "model", "session"}
)

type confirm struct {
	prompt string
	view   eval.View
	agent  string
}

type Model struct {
	opts Options
	act  Actions
	data Data

	tab      Tab
	agentSel int
	peek     bool
	status   string
	confirm  *confirm

	today         map[string]SpendCell // each session's spend today
	lastPrompt    map[string]int       // newest prompt number per session
	period, group int
	expanded      map[string]bool
	spendSel      int
	peekPrompt    string // prompt id whose first line is shown in the tree
	tree          []*node
}

func New(opts Options, act Actions) *Model {
	return &Model{opts: opts, act: act, expanded: map[string]bool{}, lastPrompt: map[string]int{}}
}

// SetData installs fresh data and keeps the selections in range.
func (m *Model) SetData(d Data) {
	m.data = d
	m.today = map[string]SpendCell{}
	if d.Clock.Loc != nil {
		day := d.Clock.Day(d.Now)
		for _, c := range d.Contribs {
			if !c.TS.IsZero() && d.Clock.Day(c.TS) == day {
				cell := m.today[c.Session.ID]
				cell.USD += c.USD
				cell.Est = cell.Est || c.Est
				cell.Hidden = cell.Hidden || c.Redacted
				m.today[c.Session.ID] = cell
			}
		}
	}
	m.rebuildTree()
	if n := len(m.agentRows()); m.agentSel >= n {
		m.agentSel = max(n-1, 0)
	}
}

// SetTab chooses the view the popup opens on (prefix + $ opens the spend view).
func (m *Model) SetTab(t Tab) { m.tab = t }

func (m *Model) Tab() Tab { return m.tab }

func (m *Model) Status() string { return m.status }

func (m *Model) Update(k term.Key) (quit bool) {
	if m.confirm != nil {
		return m.updateConfirm(k)
	}
	m.status = ""
	switch k {
	case "q", "esc", "ctrl-c":
		return true
	case "tab":
		m.tab = 1 - m.tab
		return false
	}
	if m.tab == TabAgents {
		return m.updateAgents(k)
	}
	return m.updateSpend(k)
}

func (m *Model) updateConfirm(k term.Key) bool {
	c := m.confirm
	m.confirm = nil
	if k != "y" {
		m.status = "nothing sent"
		return false
	}
	msg, err := m.act.Interrupt(c.view)
	switch {
	case err != nil:
		m.status = "interrupt failed: " + err.Error()
	default:
		m.status = msg
	}
	return false
}

// ---- status view

func (m *Model) updateAgents(k term.Key) bool {
	rows := m.agentRows()
	switch k {
	case "j", "down":
		m.agentSel = min(m.agentSel+1, max(len(rows)-1, 0))
	case "k", "up":
		m.agentSel = max(m.agentSel-1, 0)
	case "enter":
		v, _, ok := m.selected()
		if !ok {
			return false
		}
		return m.jump(v)
	case "a":
		if v, ok := m.oldestWaiting(); ok {
			return m.jump(v)
		}
		m.status = "nothing waiting"
	case "s":
		if v, _, ok := m.selected(); ok {
			m.tab = TabSpend
			m.revealSession(v.ID)
		}
	case "d":
		v, _, ok := m.selected()
		if !ok || v.Cwd == "" {
			m.status = "no working directory known for this row"
			return false
		}
		if err := m.act.Diff(v.Cwd); err != nil {
			m.status = "diff failed: " + err.Error()
			return false
		}
		return true
	case "v":
		if !m.opts.Peek {
			m.status = "peek is off (ui.peek = false)"
			return false
		}
		m.peek = !m.peek
	case "x":
		v, r, ok := m.selected()
		if !ok {
			return false
		}
		agent := ""
		if r.sub >= 0 {
			agent = v.Subs[r.sub].ID
		}
		n, err := m.act.Dismiss(v.ID, agent)
		switch {
		case err != nil:
			m.status = "dismiss failed: " + err.Error()
		case n == 0:
			m.status = "no active alert on this row"
		default:
			m.status = fmt.Sprintf("dismissed %d alert(s)", n)
		}
	case "!":
		v, r, ok := m.selected()
		if !ok {
			return false
		}
		if v.PaneGone {
			m.status = "pane gone: cannot interrupt"
			return false
		}
		c := &confirm{view: v}
		who := strings.TrimSpace(whereCell(v) + " " + taskOrUntasked(v.Task))
		if r.sub >= 0 {
			c.agent = v.Subs[r.sub].ID
			c.prompt = fmt.Sprintf("interrupt %s? Claude Code cannot stop one subagent: this interrupts the parent's turn. y/n", who)
		} else {
			c.prompt = fmt.Sprintf("interrupt %s? y/n", who)
		}
		m.confirm = c
	}
	return false
}

func (m *Model) jump(v eval.View) bool {
	if v.PaneGone {
		m.status = "pane gone: nothing to jump to"
		return false
	}
	if err := m.act.Jump(v.Pane); err != nil {
		m.status = "jump failed: " + err.Error()
		return false
	}
	return true
}

// oldestWaiting is the head of the waiting queue: needs-you first, then done,
// oldest first; runaway, working and idle are never targets.
func (m *Model) oldestWaiting() (eval.View, bool) {
	var q []eval.View
	for _, s := range m.data.Sessions {
		if !s.PaneGone && (s.Display == live.NeedsYou || s.Display == live.Done) {
			q = append(q, s)
		}
	}
	sort.SliceStable(q, func(i, j int) bool {
		if q[i].Display != q[j].Display {
			return q[i].Display == live.NeedsYou
		}
		return q[i].Since.Before(q[j].Since)
	})
	if len(q) == 0 {
		return eval.View{}, false
	}
	return q[0], true
}

func taskOrUntasked(t string) string {
	if t == "" {
		return "untasked"
	}
	return t
}

// whereCell is the WHERE column: session:window.pane and the repo's short name.
func whereCell(v eval.View) string {
	if v.PaneGone {
		return "? pane gone"
	}
	where := v.Where
	if where != "" && v.Repo != "" {
		where += " " + v.Repo[strings.LastIndexByte(v.Repo, '/')+1:]
	}
	return where
}
