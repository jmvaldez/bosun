// Package segment renders the tmux status segment and the text summary from
// session counts.
package segment

import (
	"fmt"
	"strings"

	"github.com/jmvaldez/bosun/internal/spend"
)

// Counts are the numbers the segment shows.
type Counts struct {
	// Sessions keeps the "Agents" JSON key that status --json has always emitted.
	Sessions int `json:"Agents"`
	NeedsYou int
	Runaway  int
	Done     int
	Working  int
	Quiet    int
	Idle     int
	// Subs is the number of running subagents; Sessions+Subs is the agent count.
	Subs int
}

// Spend is today's spend as the segment shows it.
type Spend struct {
	USD      float64
	EstShare float64 // fraction of USD that is an estimate (spend.DaySum.EstShare)
	// Redacted is how many sessions' amounts live cost hides (spend.DaySum.Redacted);
	// USD leaves them out.
	Redacted int
	// Cap is rules.daily_budget; zero means no budget, so no "/cap" suffix.
	Cap float64
	// Level is "", "amber" (pace at its amber ratio) or "alert" (pace alert).
	Level string
}

// Colours (SPEC section 7). Style blocks carry one attribute each so the same
// pieces are safe inside #{?...} conditionals elsewhere (LESSONS L-05).
const (
	bg     = "colour233"
	yellow = "colour220"
	orange = "colour208"
	red    = "colour160"
	cyan   = "colour51"
	grey   = "colour245"
	black  = "colour232"
	white  = "colour255"
)

func style(fg, bgc string) string { return "#[fg=" + fg + "]#[bg=" + bgc + "]" }

type Opts struct {
	// Compact is the terse pre-overhaul form (bar.style = "compact").
	Compact bool
	// ASCII swaps ⚑ ▲ ✓ for ! ^ + (ui.glyphs = "ascii").
	ASCII bool
}

// Glyphs returns the flag, runaway and done marks.
func Glyphs(ascii bool) (flag, tri, check string) {
	if ascii {
		return "!", "^", "+"
	}
	return "⚑", "▲", "✓"
}

// Bar renders the status-right segment: one labeled block per non-zero
// count, then spend, then the session and agent counts.
func Bar(c Counts, sp Spend, o Opts) string {
	if o.Compact {
		return Tmux(c, sp)
	}
	flag, tri, check := Glyphs(o.ASCII)
	var b strings.Builder
	block := func(fg, bgc, text string) {
		fmt.Fprintf(&b, "%s %s #[default]", style(fg, bgc), text)
	}
	if c.NeedsYou > 0 {
		fg := "colour214"
		if c.NeedsYou >= 3 {
			fg = "colour208"
		}
		block(fg, "colour58", fmt.Sprintf("%s %d waiting", flag, c.NeedsYou))
	}
	if c.Runaway > 0 {
		block("colour203", "colour52", fmt.Sprintf("%s %d runaway", tri, c.Runaway))
	}
	if c.Done > 0 {
		block("colour114", "colour22", fmt.Sprintf("%s %d done", check, c.Done))
	}
	if sp.USD > 0 || sp.Redacted > 0 {
		fg, bold := "colour255", ""
		switch {
		case sp.Level == "amber":
			fg = "colour214"
		case sp.Level == "alert", sp.Cap > 0 && sp.USD >= sp.Cap:
			fg, bold = "colour203", "#[bold]"
		}
		text := fmt.Sprintf("$%.2f", sp.USD)
		if sp.Cap > 0 {
			text += fmt.Sprintf(" / $%.0f", sp.Cap)
		}
		if sp.EstShare > 0 {
			text += " est."
		}
		text = redactedText(sp, text)
		fmt.Fprintf(&b, "%s%s %s #[nobold]#[default]", style(fg, "colour236"), bold, text)
	}
	if c.Sessions > 0 {
		block("colour245", bg, noun(c.Sessions, "session")+" · "+noun(c.Sessions+c.Subs, "agent"))
	}
	return b.String()
}

// Tmux renders the compact segment for a status-right #() job. It is empty when there
// is nothing to show, so a quiet bar stays quiet.
func Tmux(c Counts, sp Spend) string {
	var b strings.Builder
	if c.NeedsYou > 0 {
		colour := yellow
		if c.NeedsYou >= 3 {
			colour = orange
		}
		fmt.Fprintf(&b, "%s ! %d %s ", style(black, colour), c.NeedsYou, style(grey, bg))
	}
	if c.Runaway > 0 {
		fmt.Fprintf(&b, "%s $! %d %s ", style(white, red), c.Runaway, style(grey, bg))
	}
	if c.Done > 0 {
		fmt.Fprintf(&b, "%s✓%d ", style(cyan, bg), c.Done)
	}
	if c.Working > 0 {
		fmt.Fprintf(&b, "%s●%d ", style(grey, bg), c.Working)
	}
	if sp.USD > 0 || sp.Redacted > 0 {
		b.WriteString(spendPart(sp))
	}
	if b.Len() == 0 {
		return ""
	}
	return b.String() + "#[default]"
}

func spendPart(sp Spend) string {
	colour, bold := grey, ""
	switch {
	case sp.Level == "amber":
		colour = "colour214"
	case sp.Level == "alert", sp.Cap > 0 && sp.USD >= sp.Cap:
		colour, bold = "colour196", "#[bold]"
	}
	text := fmt.Sprintf("$%.2f", sp.USD)
	if sp.Cap > 0 {
		text += fmt.Sprintf("/$%.0f", sp.Cap)
	}
	if sp.EstShare > 0 {
		text += " est."
	}
	text = redactedText(sp, text)
	return style(colour, bg) + bold + text + "#[nobold] "
}

// redactedText adds what live cost hides to the spend text: a lone dash when
// nothing else is known, else a short "+ N —" note after the figure.
func redactedText(sp Spend, text string) string {
	switch {
	case sp.Redacted == 0:
		return text
	case sp.USD <= 0:
		return spend.Dash
	}
	return text + " " + spend.RedactedNote(sp.Redacted)
}

// Text renders the counts for a terminal.
func Text(c Counts) string {
	return fmt.Sprintf("%d sessions: %d needs-you, %d runaway, %d done, %d working, %d quiet, %d idle",
		c.Sessions, c.NeedsYou, c.Runaway, c.Done, c.Working, c.Quiet, c.Idle)
}

func noun(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
