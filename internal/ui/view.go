package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/jmvaldez/bosun/internal/clean"
	"github.com/jmvaldez/bosun/internal/eval"
	"github.com/jmvaldez/bosun/internal/live"
	"github.com/jmvaldez/bosun/internal/segment"
	"github.com/jmvaldez/bosun/internal/spend"
)

// ANSI styles. Frames carry them only when Options.Color is set.
const (
	reset   = "\x1b[0m"
	bold    = "\x1b[1m"
	dim     = "\x1b[2m"
	rev     = "\x1b[7m"
	cYel    = "\x1b[38;5;220m"
	cOrg    = "\x1b[38;5;208m"
	cRed    = "\x1b[38;5;203m"
	cCyan   = "\x1b[38;5;51m"
	cGrey   = "\x1b[38;5;245m"
	cAmber  = "\x1b[38;5;214m"
	cGreen  = "\x1b[38;5;114m"
	cPurple = "\x1b[38;5;141m"
	cBlue   = "\x1b[38;5;75m"
	cWhite  = "\x1b[38;5;255m"

	// Bands are background colours for the selected row and runaway rows.
	bandAmber = "\x1b[48;5;58m"
	bandRed   = "\x1b[48;5;52m"
	bandGrey  = "\x1b[48;5;237m"
)

func (m *Model) sty(code, s string) string {
	if !m.opts.Color || s == "" {
		return s
	}
	return code + s + reset
}

// band paints a whole line's background, keeping the foreground styles inside it.
func (m *Model) band(code, line string) string {
	if !m.opts.Color {
		return line
	}
	return code + strings.ReplaceAll(line, reset, reset+code) + reset
}

// glyphs are the state marks: flag, tri, check and swap come from
// segment.Glyphs; ASCII also swaps ✱ ▸ for * >.
type glyphs struct{ flag, tri, check, swap, quiet, idle string }

func (m *Model) g() glyphs {
	flag, tri, check, swap := segment.Glyphs(m.opts.ASCII)
	if m.opts.ASCII {
		return glyphs{flag, tri, check, swap, "*", ">"}
	}
	return glyphs{flag, tri, check, swap, "✱", "▸"}
}

// NarrowBelow is the width under which the Spend summary pane collapses to two
// lines above the tree.
const NarrowBelow = 110

// View renders a frame as exactly h lines of exactly w columns each; w and h
// below 40 x 8 are raised to that minimum.
func (m *Model) View(w, h int) []string {
	if w < 40 {
		w = 40
	}
	if h < 8 {
		h = 8
	}
	inner := w - 4
	var body []string
	if m.tab == TabAgents {
		body = m.agentsBody(inner, h-6)
	} else {
		body = m.spendBody(inner, h-6)
	}
	out := make([]string, 0, h)
	out = append(out, "┌"+strings.Repeat("─", w-2)+"┐")
	out = append(out, m.boxLine(m.headerLine(inner), inner))
	out = append(out, "├"+strings.Repeat("─", w-2)+"┤")
	for i := 0; i < h-6; i++ {
		l := ""
		if i < len(body) {
			l = body[i]
		}
		out = append(out, m.boxLine(l, inner))
	}
	out = append(out, "├"+strings.Repeat("─", w-2)+"┤")
	out = append(out, m.boxLine(m.footer(inner), inner))
	out = append(out, "└"+strings.Repeat("─", w-2)+"┘")
	return out
}

func (m *Model) boxLine(s string, inner int) string {
	return "│ " + Fit(s, inner) + " │"
}

// headerLine: the view's title and counts on the left; the alert counts and
// today's spend on the right (status), or the estimate note (spend).
func (m *Model) headerLine(inner int) string {
	var left, right string
	if m.tab == TabAgents {
		c := m.data.Counts
		left = m.sty(bold+cBlue, "bosun status") + m.sty(cGrey, "  "+plural(c.Sessions, "session")+" · "+plural(c.Sessions+c.Subs, "agent")+" · "+plural(m.data.Repos, "repo"))
		gl := m.g()
		var parts []string
		if c.NeedsYou > 0 {
			parts = append(parts, m.sty(cAmber, fmt.Sprintf("%s %d", gl.flag, c.NeedsYou)))
		}
		if c.Runaway > 0 {
			parts = append(parts, m.sty(cRed, fmt.Sprintf("%s %d", gl.tri, c.Runaway)))
		}
		if c.Handoff > 0 {
			parts = append(parts, m.sty(cCyan, fmt.Sprintf("%s %d", gl.swap, c.Handoff)))
		}
		if c.Done > 0 {
			parts = append(parts, m.sty(cGreen, fmt.Sprintf("%s %d", gl.check, c.Done)))
		}
		today := fmt.Sprintf("today $%.2f", m.data.Today.USD)
		if m.data.Cap > 0 {
			today += fmt.Sprintf(" / $%.0f", m.data.Cap)
		}
		if n := m.data.Today.Redacted; n > 0 {
			if m.data.Today.USD <= 0 {
				today = "today " + spend.Dash
			} else {
				today += " " + spend.RedactedNote(n)
			}
		}
		parts = append(parts, m.sty(bold, today))
		right = strings.Join(parts, "  ")
	} else {
		left = m.sty(bold+cBlue, "bosun spend") + m.sty(cGrey, "  --by "+m.byChain()+" · "+periods[m.period])
		right = m.sty(cGrey, "estimates at API rates")
	}
	gap := inner - Width(left) - Width(right)
	if gap < 2 {
		return Cut(left, inner)
	}
	return left + strings.Repeat(" ", gap) + right
}

// footer is the confirmation or status message when there is one, else the
// key hints.
func (m *Model) footer(inner int) string {
	if m.confirm != nil {
		return m.sty(bold, m.confirm.prompt)
	}
	if m.status != "" {
		return m.status
	}
	return m.hints(inner)
}

type hint struct{ key, what string }

// hints renders the key line for the active view, dropping the least important
// entries until it fits. Every entry is a real binding (see Update).
func (m *Model) hints(inner int) string {
	var items []hint
	var drop []int // indexes to give up first, least important first
	if m.tab == TabAgents {
		items = []hint{{"enter", "jump"}, {"a", "oldest waiting"}, {"x", "ack alert"}, {"!", "interrupt"}, {"d", "review diff"}, {"s", "spend for row"}, {"tab", "spend view"}, {"esc", "close"}}
		drop = []int{6, 1, 5, 4, 3, 2}
	} else {
		items = []hint{{"←→", "collapse / expand"}, {"b", "cycle group by"}, {"r", "range"}, {"e", "export CSV"}, {"enter", "jump"}, {"tab", "status view"}, {"esc", "close"}}
		drop = []int{5, 4, 1, 3, 2}
	}
	gone := map[int]bool{}
	render := func() string {
		var parts []string
		for i, it := range items {
			if gone[i] {
				continue
			}
			parts = append(parts, m.sty(bold, it.key)+" "+m.sty(cGrey, it.what))
		}
		return strings.Join(parts, "  ")
	}
	line := render()
	for _, i := range drop {
		if Width(line) <= inner {
			break
		}
		gone[i] = true
		line = render()
	}
	return line
}

// ---- status view

// agentCols are the column widths of the agents table. ctx is 0 when the CTX
// column is dropped.
type agentCols struct{ task, repo, tmux, state, wait, ctx, usd int }

// ctxWidth is the CTX column ("100%" is the widest cell).
const ctxWidth = 4

func agentWidths(inner int) agentCols {
	c := agentCols{task: 16, repo: 14, tmux: 11, wait: 6, usd: 9}
	switch {
	case inner >= 140:
		c.task, c.repo, c.tmux = 22, 18, 14
	case inner >= 110:
		c.task, c.repo, c.tmux = 18, 16, 12
	case inner < 90:
		c.task, c.repo, c.tmux = 12, 10, 9
	}
	// The CTX column is the first to go when the popup is narrow.
	if inner >= 90 {
		c.ctx = ctxWidth
	}
	// icon(2) + sep + task + sep + repo + sep + tmux + sep + state + sep + wait + sep + usd,
	// plus sep + ctx when shown
	c.state = inner - (2 + 1 + c.task + 1 + c.repo + 1 + c.tmux + 1 + 1 + c.wait + 1 + c.usd)
	if c.ctx > 0 {
		c.state -= 1 + c.ctx
	}
	if c.state < 14 {
		c.state = 14
	}
	return c
}

// arow is one selectable row: a session, or one of its running subagents.
type arow struct {
	sess int
	sub  int // index into Sessions[sess].Subs, or -1 for the session row
}

func (m *Model) agentRows() []arow {
	var rows []arow
	for i, s := range m.data.Sessions {
		rows = append(rows, arow{i, -1})
		for j := range s.Subs {
			rows = append(rows, arow{i, j})
		}
	}
	return rows
}

func (m *Model) selected() (eval.View, arow, bool) {
	rows := m.agentRows()
	if m.agentSel < 0 || m.agentSel >= len(rows) {
		return eval.View{}, arow{}, false
	}
	r := rows[m.agentSel]
	return m.data.Sessions[r.sess], r, true
}

func (m *Model) agentsBody(inner, rows int) []string {
	if len(m.data.Sessions) == 0 {
		return []string{"", m.sty(dim, "  No Claude Code agents running")}
	}
	c := agentWidths(inner)
	head := "  " + " " + Pad("TASK", c.task) + " " + Pad("REPO", c.repo) + " " + Pad("TMUX", c.tmux) + " " + Pad("STATE", c.state) + " " + Pad("TIME", c.wait) + " " + m.ctxHead(c) + PadLeft("$ TODAY", c.usd)
	lines := []string{m.sty(dim, head)}
	selRows := m.agentRows()
	var body []string
	owner := []int{} // body line -> row index
	for idx, r := range selRows {
		v := m.data.Sessions[r.sess]
		var line string
		band := ""
		if r.sub < 0 {
			line = m.sessionRow(v, c)
		} else {
			line = m.subRow(v, v.Subs[r.sub], c)
		}
		// The band covers the selected session and its subagents, or one subagent.
		if sr := selRows[min(m.agentSel, len(selRows)-1)]; m.agentSel < len(selRows) && sr.sess == r.sess && (sr.sub < 0 || sr.sub == r.sub) {
			band = bandGrey
			if v.Display == live.NeedsYou {
				band = bandAmber
			} else if v.Display == live.Runaway {
				band = bandRed
			}
		}
		if band != "" {
			line = m.band(band, Fit(line, inner))
		}
		body = append(body, line)
		owner = append(owner, idx)
		if idx == m.agentSel && r.sub < 0 && m.peek && m.opts.Peek {
			p := m.act.Peek(v)
			if p == "" {
				p = "nothing pending"
			}
			body = append(body, m.sty(dim, "     ⤷ "+p))
			owner = append(owner, idx)
		}
	}
	// Keep the selected row on screen.
	avail := rows - 1
	start := 0
	if avail > 0 {
		last := -1
		for i, o := range owner {
			if o == m.agentSel {
				last = i
			}
		}
		if last >= avail {
			start = last - avail + 1
		}
	}
	return append(lines, body[min(start, len(body)):]...)
}

func (m *Model) iconFor(v eval.View) (string, string) {
	gl := m.g()
	switch v.Display {
	case live.NeedsYou:
		return gl.flag, cAmber
	case live.Runaway:
		return gl.tri, cRed
	case live.Handoff:
		return gl.swap, cCyan
	case live.Done:
		return gl.check, cGreen
	case live.Working:
		if v.PaneGone {
			return "?", cGrey
		}
		return m.spin(), cBlue
	case live.Quiet:
		return gl.quiet, cPurple
	}
	return gl.idle, cGrey
}

// spinFrames animate the working mark, one frame per popup refresh.
var spinFrames = map[bool][]string{
	false: {"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
	true:  {"|", "/", "-", "\\"},
}

func (m *Model) spin() string {
	f := spinFrames[m.opts.ASCII]
	return f[int(m.data.Now.Unix()%int64(len(f)))]
}

func shortRepo(r string) string {
	r = clean.Text(r)
	return r[strings.LastIndexByte(r, '/')+1:]
}

// tmuxCell is session:window. The pane suffix stays only when another
// session in all shares that window, so the row still says which pane it is.
func tmuxCell(v eval.View, all []eval.View) string {
	if v.PaneGone {
		return "? pane gone"
	}
	win := windowOf(v.Where)
	for _, o := range all {
		if o.ID != v.ID && !o.PaneGone && o.Pane != v.Pane && windowOf(o.Where) == win {
			return v.Where
		}
	}
	return win
}

// windowOf trims the pane suffix from session:window.pane.
func windowOf(where string) string {
	if i := strings.LastIndexByte(where, '.'); i > strings.IndexByte(where, ':') && i > 0 {
		return where[:i]
	}
	return where
}

// waitCell is how long a needs-you or done session has been waiting, or how
// long a working one's turn has run, m:ss.
func (m *Model) waitCell(v eval.View) string {
	if v.PaneGone || v.Display != live.NeedsYou && v.Display != live.Done && v.Display != live.Working {
		return "—"
	}
	return clock(m.data.Now.Sub(v.Since))
}

// clock renders m:ss, or 1h05m past an hour.
func clock(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	if d >= time.Hour {
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%d:%02d", int(d.Minutes()), int(d.Seconds())%60)
}

func (m *Model) sessionRow(v eval.View, c agentCols) string {
	icon, col := m.iconFor(v)
	text, scol := m.stateText(v)
	usdCell := "-"
	if sc, ok := m.today[v.ID]; ok {
		usdCell = usd(sc.USD)
		if sc.Est {
			usdCell += "e"
		}
		if sc.Hidden {
			usdCell = spend.Dash
		}
	}
	wcol := cGrey
	if v.Display == live.Working {
		wcol = cBlue
	} else if v.Display == live.NeedsYou {
		wcol = cAmber
	} else if v.Display == live.Done {
		wcol = cGreen
	}
	taskCell := m.sty(bold, Fit(clean.Text(v.Task), c.task))
	if v.Task == "" {
		// A live session with no task shows Claude Code's own name, dim, so it
		// reads as a fallback and not as a task.
		if name := m.data.Names[v.ID]; name != "" && m.opts.ShowText && !v.PaneGone {
			taskCell = m.sty(dim, Fit(clean.Text(name), c.task))
		} else {
			taskCell = m.sty(bold, Fit("(no task)", c.task))
		}
	}
	return m.sty(col, icon) + "  " + taskCell + " " + Fit(shortRepo(v.Repo), c.repo) + " " +
		m.sty(cGrey, Fit(clean.Text(tmuxCell(v, m.data.Sessions)), c.tmux)) + " " + m.sty(scol, Fit(text, c.state)) + " " + m.sty(wcol, Fit(m.waitCell(v), c.wait)) + " " +
		m.ctxCell(v, c) + PadLeft(usdCell, c.usd)
}

// ctxHead is the CTX column header with its separator, or "" when dropped.
func (m *Model) ctxHead(c agentCols) string {
	if c.ctx == 0 {
		return ""
	}
	return PadLeft("CTX", c.ctx) + " "
}

// ctxCell is the context fill with its separator: `41%`, or `—` when unknown.
// Default below 50%, amber from 50%, red from 70% (the handoff threshold).
func (m *Model) ctxCell(v eval.View, c agentCols) string {
	if c.ctx == 0 {
		return ""
	}
	if v.CtxPct == nil {
		return m.sty(cGrey, PadLeft("—", c.ctx)) + " "
	}
	pct := *v.CtxPct
	cell := PadLeft(fmt.Sprintf("%.0f%%", pct), c.ctx)
	switch {
	case pct >= 70:
		cell = m.sty(cRed, cell)
	case pct >= 50:
		cell = m.sty(cAmber, cell)
	}
	return cell + " "
}

// stateText is `<state> · <detail>` and the colour it is drawn in. With
// show_text the detail names the command, question or file (read from the
// transcript now, kept nowhere); without it, it is an enum or a number.
func (m *Model) stateText(v eval.View) (string, string) {
	switch {
	case v.PaneGone:
		return "pane lost", cGrey
	case v.Display == live.NeedsYou:
		var info = m.latest(v)
		if v.WaitKind == "question" || info.Tool == "AskUserQuestion" {
			if m.opts.ShowText && info.Question != "" {
				return `question · "` + info.Question + `"`, cAmber
			}
			return "question", cAmber
		}
		if m.opts.ShowText && info.Pending && info.Target != "" {
			return "permission · " + m.rel(v, info.Target), cAmber
		}
		return "permission", cAmber
	case v.Display == live.Runaway:
		if len(v.Alerts) > 0 {
			return "runaway · " + v.Alerts[0], cRed
		}
		return "runaway", cRed
	case v.Display == live.Handoff:
		// Phase 4 adds the h key; until then the line names no key.
		if v.CtxPct != nil {
			return fmt.Sprintf("handoff · context %.0f%% · consider a handoff", *v.CtxPct), cCyan
		}
		return "handoff · consider a handoff", cCyan
	case v.Display == live.Done:
		if v.Edits > 0 {
			return fmt.Sprintf("done · ready to review (%d files)", v.Edits), cGreen
		}
		return "done · ready to review", cGreen
	case v.Display == live.Quiet:
		return "quiet · no events for " + v.Quiet + " (maybe stuck)", cPurple
	case v.Display == live.Working:
		return m.activity(v), cWhite
	}
	return "idle · prompt empty", cGrey
}

// activity is what a working session is doing right now: the open tool call
// with its subject under show_text, else the hook-only line ("thinking",
// "waiting on 2 subagents"). A tool open for a while shows its runtime.
func (m *Model) activity(v eval.View) string {
	if info := m.latest(v); info.Pending && info.Tool != "" {
		text := eval.ToolVerb(info.Tool)
		if t := m.rel(v, info.Target); t != "" {
			text += " " + t
		} else {
			text += " " + info.Tool
		}
		return text + m.toolAge(v)
	}
	if v.ToolOpen {
		return v.Activity() + m.toolAge(v)
	}
	return v.Activity()
}

// toolAge is " (0:42)" once the open tool call has run for 10 s.
func (m *Model) toolAge(v eval.View) string {
	if v.ToolSince.IsZero() {
		return ""
	}
	d := m.data.Now.Sub(v.ToolSince)
	if d < 10*time.Second {
		return ""
	}
	return " (" + clock(d) + ")"
}

// latest reads the transcript's newest tool call, only with show_text.
func (m *Model) latest(v eval.View) (info peekInfo) {
	if !m.opts.ShowText || v.Transcript == "" {
		return info
	}
	return m.act.Latest(v)
}

// rel shows a path inside the session's working directory relative to it.
func (m *Model) rel(v eval.View, target string) string {
	if v.Cwd != "" && strings.HasPrefix(target, v.Cwd+"/") {
		return clean.Text(strings.TrimPrefix(target, v.Cwd+"/"))
	}
	return clean.Text(target)
}

// durationSpaced renders 14m 08s, the way the subagent rows show runtime.
func durationSpaced(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

func (m *Model) subRow(parent eval.View, s eval.SubView, c agentCols) string {
	gl := m.g()
	name := clean.Text(s.Type)
	if name == "" {
		name = "subagent"
	}
	mark, col := m.sty(cGrey, "·"), cGrey
	if s.Alert {
		mark, col = m.sty(cRed, gl.tri), cRed
	}
	left := "└ " + mark + " " + m.sty(col, name)
	state := fmt.Sprintf("subagent · %s · %d tools", durationSpaced(s.Runtime), s.Tools)
	cost := fmt.Sprintf("$%.2f", s.EstUSD)
	// The child starts under the task column, after the icon column.
	pad := strings.Repeat(" ", 3)
	taskW := c.task
	return pad + Fit(left, taskW) + " " + m.sty(cGrey, Fit(shortRepo(parent.Repo), c.repo)) + " " + strings.Repeat(" ", c.tmux) + " " +
		m.sty(col, Fit(state, c.state)) + " " + strings.Repeat(" ", c.wait) + " " + strings.Repeat(" ", c.ctx+min(c.ctx, 1)) + m.sty(col, PadLeft(cost, c.usd))
}
