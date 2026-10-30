package notify

import (
	"fmt"
	"strings"
	"time"
)

// Message is a rendered notice: a title, one line saying what happened, and a
// quieter second line saying where.
type Message struct {
	Title    string
	Subtitle string
	Body     string
	Line2    string
	Icon     string
	Sound    bool
	Group    string
	// Session is who a click should jump to; empty for account-level notices.
	Session string
}

// FormatDuration renders 5m54s, 1h02m, 45s.
func FormatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

func usd(v float64) string { return fmt.Sprintf("$%.2f", v) }

// usdStep prints a step like 10 or 2.5 without trailing zeros.
func usdStep(v float64) string {
	s := fmt.Sprintf("%.2f", v)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	return "$" + s
}

func missing(kind Kind, what string) error {
	return fmt.Errorf("%w: %s needs %s", ErrMissing, kind, what)
}

// Icons are standard theme names, present on GNOME.
const (
	iconAsk  = "dialog-question"
	iconWarn = "dialog-warning"
	iconDone = "emblem-ok-symbolic"
)

// who is "<task> / <repo>", whichever parts exist.
func who(r Request) string {
	var parts []string
	if t := clean(r.Task, 40); t != "" {
		parts = append(parts, t)
	}
	if p := clean(r.Repo, 60); p != "" {
		parts = append(parts, p)
	}
	if len(parts) == 0 {
		return "a session"
	}
	return strings.Join(parts, " / ")
}

// where is the second line's prefix: the tmux session:window when known.
func where(r Request, rest string) string {
	if r.Where == "" {
		return rest
	}
	if rest == "" {
		return r.Where
	}
	return r.Where + " · " + rest
}

// wantsTo phrases a pending tool call: "wants to run npm test".
func wantsTo(tool, target string) string {
	switch tool {
	case "Bash":
		return "wants to run " + target
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
		return "wants to edit " + target
	case "Read":
		return "wants to read " + target
	}
	if tool == "" {
		return "wants to use a tool"
	}
	return "wants to use " + tool + " " + target
}

func ackLine(r Request) string {
	if r.AlertID == "" {
		return "bosun ack · or interrupt from the popup"
	}
	return "bosun ack " + r.AlertID + " · or interrupt from the popup"
}

// Render fills the fixed template for the request's kind. A required value that
// was not passed is ErrMissing: an alert banner reading "$0" or "0s" is a bug
// (LESSONS L-38), so zero values are never substituted.
func Render(r Request) (Message, error) {
	m := Message{Title: "bosun", Group: groupOf(r), Session: r.Session}
	switch r.Kind {
	case NeedsYou:
		m.Title, m.Icon, m.Sound = "bosun · needs you", iconAsk, true
		if !contains(details, r.Detail) {
			return m, missing(r.Kind, "detail (one of "+strings.Join(details, ", ")+")")
		}
		switch {
		case r.Question != "":
			m.Body = "asks: " + clean(r.Question, 160)
		case r.Target != "":
			m.Body = who(r) + " " + wantsTo(r.Tool, clean(r.Target, 160))
		default:
			m.Body = who(r) + " is waiting on you (" + r.Detail + ")"
		}
		wait := ""
		if r.Waited != nil && *r.Waited >= time.Second {
			wait = "waiting " + clockText(*r.Waited)
		}
		m.Line2 = where(r, wait)
	case Done:
		m.Title, m.Icon = "bosun · finished", iconDone
		if r.Edits == nil {
			return m, missing(r.Kind, "edits")
		}
		m.Body = who(r) + " is done, ready to review"
		// A session with no status line has no trusted figure; leave the clause out
		// rather than print a made-up one.
		rest := ""
		if r.OwnUSD != nil {
			rest = usd(*r.OwnUSD) + " this session"
		}
		m.Line2 = where(r, rest)
	case Failed:
		m.Title, m.Icon, m.Sound = "bosun · turn failed", iconWarn, true
		if r.Duration == nil {
			return m, missing(r.Kind, "duration")
		}
		m.Body = who(r) + " failed after " + FormatDuration(*r.Duration)
		m.Line2 = where(r, "")
	case SubagentRuntime, SubagentCost:
		m.Title, m.Icon = "bosun · runaway subagent", iconWarn
		if r.Agent == "" {
			return m, missing(r.Kind, "agent")
		}
		in := clean(r.Agent, 40) + " in " + clean(firstNonEmpty(r.Task, r.Repo, "a session"), 60) + ": "
		switch {
		case r.Duration != nil && r.USD != nil && r.LimitDur != nil && r.LimitUSD != nil:
			m.Body = fmt.Sprintf("%s%s and %s (limits %s / %s)", in, FormatDuration(*r.Duration), usd(*r.USD), FormatDuration(*r.LimitDur), usdStep(*r.LimitUSD))
		case r.Kind == SubagentRuntime:
			if r.Duration == nil || r.LimitDur == nil {
				return m, missing(r.Kind, "agent, duration and limit-duration")
			}
			m.Body = fmt.Sprintf("%s%s (limit %s)", in, FormatDuration(*r.Duration), FormatDuration(*r.LimitDur))
		default:
			if r.USD == nil || r.LimitUSD == nil {
				return m, missing(r.Kind, "agent, usd and limit-usd")
			}
			m.Body = fmt.Sprintf("%s%s est. (limit %s)", in, usd(*r.USD), usdStep(*r.LimitUSD))
		}
		m.Line2 = ackLine(r)
	case SessionCost:
		m.Title, m.Icon = "bosun · runaway session", iconWarn
		if r.Step == nil {
			return m, missing(r.Kind, "step")
		}
		m.Body = who(r) + " passed " + usdStep(*r.Step)
		m.Line2 = ackLine(r)
	case Quiet:
		m.Title, m.Icon = "bosun · quiet session", iconWarn
		if r.Duration == nil {
			return m, missing(r.Kind, "duration")
		}
		m.Body = who(r) + ": no activity for " + FormatDuration(*r.Duration)
		m.Line2 = ackLine(r)
	case DailyPace:
		m.Title, m.Icon = "bosun · daily pace", iconWarn
		if r.USD == nil || r.Ratio == nil {
			return m, missing(r.Kind, "usd and ratio")
		}
		m.Body = fmt.Sprintf("today %s, %.1fx typical", usd(*r.USD), *r.Ratio)
		m.Line2 = ackLine(r)
	case DailyBudget:
		m.Title, m.Icon = "bosun · daily budget", iconWarn
		if r.USD == nil || r.CapUSD == nil {
			return m, missing(r.Kind, "usd and cap")
		}
		m.Body = fmt.Sprintf("today %s of %s", usd(*r.USD), usd(*r.CapUSD))
		m.Line2 = ackLine(r)
	default:
		return m, fmt.Errorf("notify: no template for kind %q", r.Kind)
	}
	return m, nil
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

// clockText renders 0:42 or 1:02:03.
func clockText(d time.Duration) string {
	d = d.Round(time.Second)
	if d >= time.Hour {
		return fmt.Sprintf("%d:%02d:%02d", int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60)
	}
	return fmt.Sprintf("%d:%02d", int(d.Minutes()), int(d.Seconds())%60)
}

func groupOf(r Request) string {
	if r.Group != "" {
		return r.Group
	}
	return Group(r.Kind, r.Session)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// OverflowMessage is the one notice sent when the rate limit is hit. It gives
// no count: notify.json keeps no tally of the notices it held back.
func OverflowMessage(popupKey string) Message {
	return Message{
		Title: "bosun · more waiting", Group: OverflowGroup, Icon: iconAsk,
		Body: fmt.Sprintf("More notices are waiting; prefix + %s opens bosun", popupKey),
	}
}
