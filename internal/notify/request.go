// Package notify renders and posts desktop notifications. Every body comes
// from a fixed template filled with numbers, durations and enums that arrive as
// flags (LESSONS L-38). With privacy.show_text (D-27) the dispatcher also reads
// the pending command or question from the transcript at notify time and the
// template shows it once; it is never stored. Assistant text and error text
// never reach this package.
package notify

import (
	"errors"
	"fmt"

	"github.com/jmvaldez/bosun/internal/paths"
	"strconv"
	"strings"
	"time"
)

// Kind is what a notice is about.
type Kind string

const (
	NeedsYou        Kind = "needs-you"
	Done            Kind = "done"
	Failed          Kind = "failed"
	SubagentRuntime Kind = "subagent_runtime"
	SubagentCost    Kind = "subagent_cost"
	SessionCost     Kind = "session_cost"
	Quiet           Kind = "quiet"
	DailyPace       Kind = "daily_pace"
	DailyBudget     Kind = "daily_budget"
)

// Kinds must hold every Kind: ParseArgs rejects any kind not listed, and the
// render test requires a case for each.
var Kinds = []Kind{NeedsYou, Done, Failed, SubagentRuntime, SubagentCost, SessionCost, Quiet, DailyPace, DailyBudget}

// Detail values for a needs-you notice.
var details = []string{"permission prompt", "input needed", "dialog"}

// Request is one dispatch. Pointer fields are values a template requires: nil
// means "not provided", which is an error, never a rendered zero.
type Request struct {
	Kind    Kind
	Session string
	// Edge identifies the episode or alert so a repeat is dropped. Empty means
	// the dispatcher derives it.
	Edge    string
	AlertID string
	Repo    string
	Task    string

	Detail string // needs-you: one of details
	Agent  string // subagent kinds: agent_type

	Edits     *int
	OwnUSD    *float64
	Duration  *time.Duration // failed, quiet, subagent kinds
	LimitDur  *time.Duration // subagent kinds
	USD       *float64       // subagent kinds, daily_pace, daily_budget
	LimitUSD  *float64       // subagent kinds
	Step      *float64       // session_cost
	Ratio     *float64       // daily_pace
	CapUSD    *float64       // daily_budget
	Estimated bool

	// Where, Waited, Tool, Target and Question are filled by the dispatcher at
	// notify time and rendered once (D-27). They are not flags: Args never
	// emits them, so no text ever sits in a command line or a file.
	Where    string // tmux session:window
	Waited   *time.Duration
	Tool     string
	Target   string
	Question string

	// Retract removes the notice of the group instead of posting.
	Retract bool
	Group   string
	// Test posts a plain test notice.
	Test bool
}

// ErrMissing is returned when a template's value was not provided.
var ErrMissing = errors.New("notify: missing template value")

// Group returns the replace and retract group for a kind and session.
func Group(kind Kind, session string) string {
	switch kind {
	case NeedsYou, Done, Failed:
		return "bosun." + session + ".attn"
	case DailyPace:
		return "bosun.pace"
	case DailyBudget:
		return "bosun.budget"
	}
	return "bosun." + session + "." + string(kind)
}

// OverflowGroup is the single self-replacing overflow notice.
const OverflowGroup = "bosun.overflow"

// Args renders the request as the flags `bosun notify-dispatch` parses.
func (r Request) Args() []string {
	var a []string
	add := func(k, v string) {
		if v != "" {
			a = append(a, k, v)
		}
	}
	add("--kind", string(r.Kind))
	add("--session", r.Session)
	add("--edge", r.Edge)
	add("--alert-id", r.AlertID)
	add("--repo", r.Repo)
	add("--task", r.Task)
	add("--detail", r.Detail)
	add("--agent", r.Agent)
	if r.Edits != nil {
		add("--edits", strconv.Itoa(*r.Edits))
	}
	f := func(k string, v *float64) {
		if v != nil {
			add(k, strconv.FormatFloat(*v, 'f', -1, 64))
		}
	}
	f("--own-usd", r.OwnUSD)
	f("--usd", r.USD)
	f("--limit-usd", r.LimitUSD)
	f("--step", r.Step)
	f("--ratio", r.Ratio)
	f("--cap", r.CapUSD)
	d := func(k string, v *time.Duration) {
		if v != nil {
			add(k, v.String())
		}
	}
	d("--duration", r.Duration)
	d("--limit-duration", r.LimitDur)
	if r.Estimated {
		a = append(a, "--est")
	}
	if r.Retract {
		a = append(a, "--retract")
	}
	add("--group", r.Group)
	if r.Test {
		a = append(a, "--test")
	}
	return a
}

// ParseArgs parses the flags Args writes. Unknown flags are an error: a typo
// must not silently drop a value.
func ParseArgs(args []string) (Request, error) {
	var r Request
	for i := 0; i < len(args); i++ {
		flag := args[i]
		switch flag {
		case "--est":
			r.Estimated = true
			continue
		case "--retract":
			r.Retract = true
			continue
		case "--test":
			r.Test = true
			continue
		}
		if i+1 >= len(args) {
			return r, fmt.Errorf("notify: %s needs a value", flag)
		}
		i++
		v := args[i]
		var err error
		switch flag {
		case "--kind":
			r.Kind = Kind(v)
			if !validKind(r.Kind) {
				return r, fmt.Errorf("notify: unknown kind %q", v)
			}
		case "--session":
			if v != "" && !paths.ValidSessionID(v) {
				return r, fmt.Errorf("notify: invalid --session %q", v)
			}
			r.Session = v
		case "--edge":
			r.Edge = v
		case "--alert-id":
			r.AlertID = v
		case "--repo":
			r.Repo = v
		case "--task":
			r.Task = v
		case "--detail":
			r.Detail = v
		case "--agent":
			r.Agent = v
		case "--group":
			r.Group = v
		case "--edits":
			var n int
			n, err = strconv.Atoi(v)
			r.Edits = &n
		case "--own-usd":
			r.OwnUSD, err = parseF(v)
		case "--usd":
			r.USD, err = parseF(v)
		case "--limit-usd":
			r.LimitUSD, err = parseF(v)
		case "--step":
			r.Step, err = parseF(v)
		case "--ratio":
			r.Ratio, err = parseF(v)
		case "--cap":
			r.CapUSD, err = parseF(v)
		case "--duration":
			r.Duration, err = parseD(v)
		case "--limit-duration":
			r.LimitDur, err = parseD(v)
		default:
			return r, fmt.Errorf("notify: unknown flag %s", flag)
		}
		if err != nil {
			return r, fmt.Errorf("notify: %s: %w", flag, err)
		}
	}
	return r, nil
}

func validKind(k Kind) bool {
	for _, x := range Kinds {
		if x == k {
			return true
		}
	}
	return false
}

func parseF(v string) (*float64, error) {
	f, err := strconv.ParseFloat(v, 64)
	return &f, err
}

func parseD(v string) (*time.Duration, error) {
	d, err := time.ParseDuration(v)
	return &d, err
}

// clean strips C0 and C1 control characters and DEL and stops at about limit
// bytes. C1 matters because U+009B is the 8-bit CSI, which some terminals
// treat as ESC [. Repo and task come from local state, but a notice body is no
// place for terminal escapes.
func clean(s string, limit int) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 0x20 && r != 0x7f && (r < 0x80 || r > 0x9f) {
			b.WriteRune(r)
		}
		if b.Len() >= limit {
			break
		}
	}
	return strings.TrimSpace(b.String())
}
