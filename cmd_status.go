package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jmvaldez/bosun/internal/clean"
	"github.com/jmvaldez/bosun/internal/eval"
	"github.com/jmvaldez/bosun/internal/live"
	"github.com/jmvaldez/bosun/internal/pace"
	"github.com/jmvaldez/bosun/internal/segment"
	"github.com/jmvaldez/bosun/internal/spend"
)

// status evaluates once and prints. --catch-up prints nothing: the tmux
// client-attached hook only wants the state brought up to date.
func (a *app) status(args []string) int {
	mode := "text"
	for _, arg := range args {
		switch arg {
		case "--tmux":
			mode = "tmux"
		case "--json":
			mode = "json"
		case "--catch-up":
			mode = "catch-up"
		default:
			fmt.Fprintf(a.errw, "bosun status: unknown argument %q\n", arg)
			return 2
		}
	}
	res := a.evaluate(mode == "tmux")
	cfg := a.loadConfig()
	clock := spend.NewClock(cfg.Spend.Day)
	today := spend.ReadToday(a.ad.Paths.Today(), clock.Day(a.ad.Clock.Now()))
	switch mode {
	case "tmux":
		fmt.Fprint(a.out, segment.Bar(res.Counts, segment.Spend{USD: today.USD, EstShare: today.EstShare, Redacted: today.Redacted, Cap: cfg.Rules.DailyBudget, Level: paceLevel(res.Pace)}, segment.Opts{Compact: cfg.Bar.Style == "compact", ASCII: cfg.UI.Glyphs == "ascii"}))
	case "json":
		b, _ := json.MarshalIndent(struct {
			eval.Result
			Today spend.DaySum `json:"today"`
		}{res, today}, "", "  ")
		fmt.Fprintln(a.out, string(b))
	case "text":
		fmt.Fprintf(a.out, "%s; today %s\n", segment.Text(res.Counts), todayText(today))
		now := a.ad.Clock.Now()
		for _, s := range res.Sessions {
			line := statusLine(s)
			if s.Display == live.Working && !s.PaneGone {
				line += fmt.Sprintf(" · %s, %s", s.Activity(), now.Sub(s.Since).Round(time.Second))
			}
			fmt.Fprintln(a.out, line)
		}
	}
	return 0
}

// todayText is today's total for the text output; sessions live cost hides are
// left out of it and noted.
func todayText(t spend.DaySum) string {
	switch {
	case t.Redacted == 0:
		return fmt.Sprintf("$%.2f", t.USD)
	case t.USD <= 0:
		return spend.Dash
	}
	return fmt.Sprintf("$%.2f %s", t.USD, spend.RedactedNote(t.Redacted))
}

// statusLine is a session's row in the text output. Repo, task and where can
// hold values stored before they were filtered at the source.
func statusLine(s eval.View) string {
	return fmt.Sprintf("  %-9s %-14s %s", s.State, orDash(clean.Text(s.Where)), strings.TrimSpace(clean.Text(s.Repo)+" "+clean.Text(s.Task)))
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func paceLevel(st pace.Status) string {
	switch {
	case st.Alert:
		return "alert"
	case st.Amber:
		return "amber"
	}
	return ""
}
