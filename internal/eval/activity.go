package eval

import "fmt"

// ToolVerb is what a tool call is doing, as the start of an activity line.
func ToolVerb(tool string) string {
	switch tool {
	case "Bash", "BashOutput":
		return "running"
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
		return "editing"
	case "Read", "NotebookRead":
		return "reading"
	case "Grep", "Glob", "LS":
		return "searching"
	case "WebFetch", "WebSearch":
		return "browsing"
	case "Task", "Agent":
		return "delegating"
	case "TodoWrite":
		return "planning"
	}
	return "using"
}

// Activity says what a working session is doing from hook state alone: the
// open tool call, running subagents or background tasks, else thinking. It
// never carries tool input; the popup adds that under show_text.
func (v View) Activity() string {
	switch {
	case v.ToolOpen && v.LastTool != "":
		return ToolVerb(v.LastTool) + " " + v.LastTool
	case v.Subagents == 1:
		return "waiting on 1 subagent"
	case v.Subagents > 1:
		return fmt.Sprintf("waiting on %d subagents", v.Subagents)
	case v.Background:
		return "background tasks running"
	}
	return "thinking"
}
