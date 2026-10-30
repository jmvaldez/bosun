package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jmvaldez/bosun/internal/clean"
	"github.com/jmvaldez/bosun/internal/live"
	"github.com/jmvaldez/bosun/internal/spend"
)

var bars = []rune("▁▂▃▄▅▆▇█")

func usd(v float64) string {
	if v < 0 {
		return fmt.Sprintf("-$%.2f", -v)
	}
	return fmt.Sprintf("$%.2f", v)
}

// bigFont is a 3-row block font for the headline total.
var bigFont = map[rune][3]string{
	'0': {"█▀█", "█ █", "▀▀▀"},
	'1': {"▄█ ", " █ ", "▄█▄"},
	'2': {"▀▀█", "▄▀▀", "▀▀▀"},
	'3': {"▀▀█", " ▀█", "▀▀▀"},
	'4': {"█ █", "▀▀█", "  ▀"},
	'5': {"█▀▀", "▀▀█", "▀▀▀"},
	'6': {"█▀▀", "█▀█", "▀▀▀"},
	'7': {"▀▀█", "  █", "  ▀"},
	'8': {"█▀█", "█▀█", "▀▀▀"},
	'9': {"█▀█", "▀▀█", "▀▀▀"},
	'$': {"▄█▄", "▀█▄", "▄█▀"},
	'.': {" ", " ", "▄"},
	'-': {"   ", "▀▀▀", "   "},
}

// bigNumber renders s in the block font, or nil when it will not fit w.
func bigNumber(s string, w int) []string {
	rows := [3]string{}
	for i, r := range s {
		g, ok := bigFont[r]
		if !ok {
			return nil
		}
		for k := range rows {
			if i > 0 {
				rows[k] += " "
			}
			rows[k] += g[k]
		}
	}
	if Width(rows[0]) > w {
		return nil
	}
	return rows[:]
}

// modelLabel turns claude-opus-5-5-20260101 into opus-5.5.
func modelLabel(model string) string {
	m := strings.TrimPrefix(model, "claude-")
	parts := strings.Split(m, "-")
	if n := len(parts); n > 0 && len(parts[n-1]) >= 8 {
		parts = parts[:n-1] // a date suffix
	}
	if n := len(parts); n >= 3 && isDigits(parts[n-1]) && isDigits(parts[n-2]) {
		return strings.Join(parts[:n-2], "-") + "-" + parts[n-2] + "." + parts[n-1]
	}
	return strings.Join(parts, "-")
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (m *Model) byChain() string {
	switch by := m.groupBy(); by {
	case "session":
		return "session → prompt → request"
	default:
		return by + " → session → prompt → request"
	}
}

type weekDay struct {
	letter string
	usd    float64
}

// week is the last seven days, oldest first.
func (m *Model) week() (days [7]weekDay, sum float64) {
	d := m.data
	for i := 0; i < 7; i++ {
		dt := d.Now.AddDate(0, 0, -(6 - i))
		v := spend.DayTotal(d.Contribs, d.Clock, d.Clock.Day(dt), time.Time{}).USD
		days[i] = weekDay{dt.In(d.Clock.Loc).Weekday().String()[:1], v}
		sum += v
	}
	return days, sum
}

// weekBars draws the days as vertical bars height rows tall, each bw columns
// wide, with the day letters below and today (the last) highlighted.
func (m *Model) weekBars(days [7]weekDay, height, bw int) []string {
	var mx float64
	for _, d := range days {
		mx = max(mx, d.usd)
	}
	var lines []string
	for r := 0; r < height; r++ {
		var b strings.Builder
		for i, d := range days {
			lvl := 0
			if mx > 0 {
				lvl = int(d.usd/mx*float64(height*8) + 0.5)
				if d.usd > 0 && lvl == 0 {
					lvl = 1
				}
			}
			v := lvl - (height-1-r)*8
			ch := " "
			switch {
			case v >= 8:
				ch = "█"
			case v > 0:
				ch = string(bars[v-1])
			}
			col := "\x1b[38;5;60m"
			if i == 6 {
				col = cBlue
			}
			cell := strings.Repeat(ch, bw)
			if ch != " " {
				cell = m.sty(col, cell)
			}
			if i > 0 {
				b.WriteString(" ")
			}
			b.WriteString(cell)
		}
		lines = append(lines, b.String())
	}
	var lab strings.Builder
	for i, d := range days {
		if i > 0 {
			lab.WriteString(" ")
		}
		cell := Pad(strings.Repeat(" ", (bw-1)/2)+d.letter, bw)
		if i == 6 {
			cell = m.sty(bold+cWhite, cell)
		} else {
			cell = m.sty(cGrey, cell)
		}
		lab.WriteString(cell)
	}
	return append(lines, lab.String())
}

// leftPane is the summary beside the tree: today's total against the budget,
// the week, today by model, and the subagent share.
func (m *Model) leftPane(w, rows int) []string {
	clk := m.data.Clock
	if clk.Loc == nil {
		return nil
	}
	l := m.todayLines(w)
	l = append(l, "")
	l = append(l, m.weekLines(w, rows)...)
	l = append(l, "")
	return append(l, m.modelLines(w, clk.Day(m.data.Now))...)
}

// todayLines is today's total with the budget or pace bar under it.
func (m *Model) todayLines(w int) []string {
	d := m.data
	clk := d.Clock
	now := d.Now
	t := spend.DayTotal(d.Contribs, clk, clk.Day(now), time.Time{})
	var l []string
	l = append(l, m.sty(cGrey, "TODAY · "+strings.ToUpper(now.In(clk.Loc).Format("Mon Jan 2"))))
	if big := bigNumber(usd(t.USD), w); big != nil {
		for _, r := range big {
			l = append(l, m.sty(bold+cWhite, r))
		}
	} else {
		l = append(l, m.sty(bold+cWhite, usd(t.USD)))
	}
	if t.Redacted > 0 {
		l = append(l, m.sty(cGrey, spend.RedactedNote(t.Redacted)+" "+spend.Plural(t.Redacted, "session")+", live cost off"))
	}
	if d.Cap > 0 {
		pct := t.USD / d.Cap
		l = append(l, m.progress(pct, w-2), m.sty(cGrey, fmt.Sprintf("%d%% of $%.0f daily budget", int(pct*100+0.5), d.Cap)))
	} else if d.Pace.OK && d.Pace.Typical > 0 {
		pct := d.Pace.Ratio / 2
		l = append(l, m.progress(pct, w-2), m.sty(cGrey, fmt.Sprintf("pace %.1fx of typical by %s", d.Pace.Ratio, now.In(clk.Loc).Format("15:04"))))
	} else {
		l = append(l, m.sty(cGrey, "pace: collecting days"))
	}
	return l
}

// weekLines is the last-seven-days bar chart, sized to the pane.
func (m *Model) weekLines(w, rows int) []string {
	days, sum := m.week()
	l := []string{m.sty(cGrey, "LAST 7 DAYS · "+usd(sum))}
	height, bw := 6, 3
	switch {
	case rows < 24:
		height = 3
	case rows < 30:
		height = 4
	}
	if w < 7*3+6 {
		bw = 2
	}
	return append(l, m.weekBars(days, height, bw)...)
}

// modelLines is today by model plus the subagent share; the rows plus the
// unattributed remainder add up to the total.
func (m *Model) modelLines(w int, day string) []string {
	d := m.data
	clk := d.Clock
	models := map[string]float64{}
	estModel := map[string]bool{}
	var all, sub, unattr float64
	for _, c := range d.Contribs {
		if c.TS.IsZero() || clk.Day(c.TS) != day || c.Redacted {
			continue
		}
		// An apportioned remainder carries its model (D-28): it folds in, as est.
		if c.Kind == spend.Unattributed && c.Req.Model == "" {
			unattr += c.USD
			continue
		}
		name := modelLabel(c.Req.Model)
		models[name] += c.USD
		if c.Est {
			estModel[name] = true
		}
		all += c.USD
		if c.Req.AgentID != "" {
			sub += c.USD
		}
	}
	type kv struct {
		k string
		v float64
	}
	var list []kv
	for k, v := range models {
		list = append(list, kv{k, v})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].v != list[j].v {
			return list[i].v > list[j].v
		}
		return list[i].k < list[j].k
	})
	l := []string{m.sty(cGrey, "BY MODEL · TODAY")}
	var other float64
	for i, e := range list {
		if i >= 3 {
			other += e.v
			continue
		}
		amount := usd(e.v)
		if estModel[e.k] {
			amount += " est."
		}
		l = append(l, Pad(e.k, w-14)+PadLeft(amount, 14))
	}
	if other != 0 {
		l = append(l, Pad("other", w-14)+PadLeft(usd(other), 14))
	}
	if unattr >= 0.005 || unattr <= -0.005 {
		l = append(l, m.sty(cGrey, Pad("unattributed", w-14)+PadLeft(usd(unattr), 14)))
	}
	share := 0
	if all > 0 {
		share = int(sub/all*100 + 0.5)
	}
	l = append(l, m.sty(cGrey, Pad("subagents share", w-14)+PadLeft(fmt.Sprintf("%d%%", share), 14)))
	return l
}

// progress is a bar w columns wide (at least 4); at or over 100% it is full and
// red.
func (m *Model) progress(frac float64, w int) string {
	if w < 4 {
		w = 4
	}
	filled := int(frac*float64(w) + 0.5)
	filled = min(max(filled, 0), w)
	col := cBlue
	if frac >= 1 {
		col = cRed
	}
	return m.sty(col, strings.Repeat("█", filled)) + m.sty("\x1b[38;5;238m", strings.Repeat("░", w-filled))
}

// narrowTop is the two-line summary shown instead of the side pane (see
// NarrowBelow).
func (m *Model) narrowTop() []string {
	d := m.data
	clk := d.Clock
	if clk.Loc == nil {
		return nil
	}
	t := spend.DayTotal(d.Contribs, clk, clk.Day(d.Now), time.Time{})
	today := "TODAY " + usd(t.USD)
	if t.Redacted > 0 {
		today += " " + spend.RedactedNote(t.Redacted)
	}
	if d.Cap > 0 {
		today += fmt.Sprintf(" of $%.0f %d%%", d.Cap, int(t.USD/d.Cap*100+0.5))
	}
	if d.Pace.OK && d.Pace.Typical > 0 {
		today += fmt.Sprintf("  pace %.1fx", d.Pace.Ratio)
	}
	days, sum := m.week()
	var mx float64
	for _, x := range days {
		mx = max(mx, x.usd)
	}
	var sp strings.Builder
	for _, x := range days {
		idx := 0
		if mx > 0 {
			idx = int(x.usd / mx * float64(len(bars)-1))
		}
		if x.usd == 0 {
			sp.WriteString("·")
		} else {
			sp.WriteRune(bars[idx])
		}
	}
	return []string{m.sty(bold, today), m.sty(cGrey, "LAST 7 DAYS "+usd(sum)+"  ") + m.sty(cBlue, sp.String())}
}

func (m *Model) spendBody(inner, rows int) []string {
	wide := inner+4 >= NarrowBelow
	main := rows
	var out []string
	if wide {
		leftW := 30
		rightW := inner - leftW - 3
		left := m.leftPane(leftW, main)
		right := m.treePane(rightW, main)
		for i := 0; i < main; i++ {
			l, r := "", ""
			if i < len(left) {
				l = left[i]
			}
			if i < len(right) {
				r = right[i]
			}
			out = append(out, Fit(l, leftW)+" │ "+Fit(r, rightW))
		}
		return out
	}
	top := m.narrowTop()
	out = append(out, top...)
	return append(out, m.treePane(inner, main-len(top))...)
}

// treePane renders the tree: a column header, the rows around the selection,
// then the unattributed and TOTAL rows.
func (m *Model) treePane(w, rows int) []string {
	rightW := 30
	if w < 70 {
		rightW = 12
	}
	nameW := w - rightW - 1
	head := Pad("NAME", nameW) + " " + PadLeft("TOKENS (in / out / cache) · $", rightW)
	if rightW < 30 {
		head = Pad("NAME", nameW) + " " + PadLeft("$", rightW)
	}
	lines := []string{m.sty(cGrey, Fit(head, w))}

	cs := spend.Filter(m.data.Contribs, m.since(), "")
	var un, total, est float64
	hid := map[string]bool{}
	for _, c := range cs {
		if c.Redacted {
			hid[c.Session.ID] = true
		}
		total += c.USD
		if c.Est {
			est += c.USD
		}
		if c.Kind == spend.Unattributed {
			un += c.USD
		}
	}
	tailRows := 3
	avail := rows - len(lines) - tailRows
	vis := m.visible()
	if len(vis) == 0 {
		lines = append(lines, "", m.sty(cGrey, "  no spend in this period"))
	}
	start := 0
	if m.spendSel >= avail {
		start = m.spendSel - avail + 1
	}
	shown := 0
	for i := start; i < len(vis) && shown < avail; i++ {
		n := vis[i]
		line := Fit(m.treeLine(n, nameW, rightW), w)
		switch {
		case i == m.spendSel && n.runaway:
			line = m.band(bandRed, line)
		case i == m.spendSel:
			line = m.band(bandGrey, line)
		case n.runaway:
			line = m.band(bandRed, line)
		}
		lines = append(lines, line)
		shown++
	}
	estPct := 0
	if total != 0 {
		estPct = int(est/total*100 + 0.5)
	}
	for len(lines) < rows-tailRows {
		lines = append(lines, "")
	}
	unLine := ""
	if un >= 0.005 || un <= -0.005 {
		unLine = m.sty(cGrey, Pad("  unattributed", nameW+1+rightW-11)+PadLeft(usd(un), 11))
	}
	totalLabel := fmt.Sprintf("TOTAL  %d%% est.", estPct)
	if len(hid) > 0 {
		totalLabel += "  " + spend.RedactedNote(len(hid))
	}
	lines = append(lines,
		unLine,
		m.sty("\x1b[38;5;238m", strings.Repeat("─", w)),
		Pad(m.sty(bold, totalLabel), nameW+1+rightW-11)+PadLeft(m.sty(bold, usd(total)), 11),
	)
	return lines[:min(len(lines), rows)]
}

func indent(depth int) string { return strings.Repeat("  ", depth) }

// treeLine is one tree row: the styled label on the left, tokens and spend on
// the right.
func (m *Model) treeLine(n *node, nameW, rightW int) string {
	marker := "  "
	if len(n.children) > 0 {
		marker = "▸ "
		if m.expanded[n.key] {
			marker = "▾ "
		}
	}
	// Labels carry repo, task and agent names: strip them before styling, since
	// Fit passes OSC and DCS sequences through.
	cn := *n
	cn.label, cn.meta, cn.repo = clean.Text(n.label), clean.Text(n.meta), clean.Text(n.repo)
	n = &cn
	gl := m.g()
	var label string
	switch n.kind {
	case kGroup:
		sess := countSessions(n)
		meta := n.repo
		if meta != "" {
			meta += " · "
		}
		meta += plural(sess, "session")
		if subs := countKind(n, kSub); subs > 0 {
			meta += " · " + plural(subs, "subagent")
		}
		name := n.label
		if name == "untasked" && m.groupBy() == "task" {
			name = "(no task)"
		}
		label = m.sty(bold, name) + " " + m.sty(cGrey, meta)
	case kSession:
		label = m.sty(bold, n.label)
		if n.meta != "" {
			label += " " + m.sty(cGrey, n.meta)
		}
	case kPrompt:
		label = m.sty(bold, n.label) + " " + m.promptText(n)
		if m.promptWaiting(n) {
			label += " " + m.sty(cAmber, "(waiting)")
		}
	case kSub:
		runtime := ""
		if !n.first.IsZero() && !n.last.IsZero() {
			runtime = " · " + shortDuration(n.last.Sub(n.first))
		}
		mark := m.sty(cGrey, "·")
		if n.runaway {
			mark = m.sty(cRed, gl.tri)
		}
		label = fmt.Sprintf("%s subagent %s · %d req%s", mark, n.label, n.reqs, runtime)
		if n.runaway {
			label = m.sty(cRed, label)
		}
	case kRequest:
		label = "req " + n.label
		if !n.first.IsZero() {
			label += " " + m.sty(cGrey, n.first.In(m.data.Clock.Loc).Format("15:04:05"))
		}
	case kMore:
		label = m.sty(cGrey, n.label)
	case kUnattr:
		label = m.sty(cGrey, n.label)
	default:
		label = n.label
	}
	spendCell := usd(n.usd)
	if n.est {
		spendCell += " est"
	}
	switch {
	case n.hidden > 0 && n.shown == 0:
		spendCell = spend.Dash
	case n.hidden > 0:
		spendCell += " +" + spend.Dash
	}
	right := spendCell
	switch {
	case n.kind == kRequest && rightW >= 30:
		right = fmt.Sprintf("%s / %s / %s · %s", spend.Tok(n.in), spend.Tok(n.out), spend.Tok(n.cache), spendCell)
	case n.kind == kMore, n.kind == kUnattr:
	case n.kind == kGroup, n.kind == kSession:
		right = m.sty(bold, spendCell)
	}
	if n.kind == kRequest || n.kind == kMore || n.kind == kUnattr {
		right = m.sty(cWhite, right)
	}
	return Fit(indent(n.depth)+marker+label, nameW) + " " + PadLeft(right, rightW)
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func shortDuration(d time.Duration) string {
	d = d.Round(time.Minute)
	if d >= time.Hour {
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}

// promptText is the prompt's first line when show_text is on or the row is
// peeked with v (read now from the transcript, dropped after the frame), else
// the time it started.
func (m *Model) promptText(n *node) string {
	if m.opts.ShowText || (m.opts.Peek && m.peekPrompt == n.key) {
		if p := m.act.PromptLine(n.session, n.prompt); p != "" {
			return p
		}
	}
	return m.sty(cGrey, clean.Text(n.meta))
}

// promptWaiting marks the newest prompt of a session that is waiting on you.
func (m *Model) promptWaiting(n *node) bool {
	if n.num == 0 || n.num != m.lastPrompt[n.session] {
		return false
	}
	for _, s := range m.data.Sessions {
		if s.ID == n.session {
			return s.Display == live.NeedsYou
		}
	}
	return false
}

func countKind(n *node, k nodeKind) int {
	c := 0
	for _, ch := range n.children {
		if ch.kind == k {
			c++
		}
		c += countKind(ch, k)
	}
	return c
}

func countSessions(n *node) int {
	seen := map[string]bool{}
	var walk func(x *node)
	walk = func(x *node) {
		if x.session != "" && x.kind == kSession {
			seen[x.session] = true
		}
		for _, c := range x.children {
			walk(c)
		}
	}
	walk(n)
	if len(seen) == 0 && n.session != "" {
		return 1
	}
	return len(seen)
}
