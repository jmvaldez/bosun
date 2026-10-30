package live

import (
	"time"

	"github.com/jmvaldez/bosun/internal/hook"
)

var editTools = map[string]bool{"Edit": true, "Write": true, "MultiEdit": true, "NotebookEdit": true}

// needsNotification lists the Notification types that mean a human is needed.
// idle_prompt is deliberately absent (LESSONS L-24).
var needsNotification = map[string]bool{
	"permission_prompt":      true,
	"elicitation_dialog":     true,
	"elicitation_url_dialog": true,
	"agent_needs_input":      true,
}

// permEpisodeWindow bounds the late-Notification guard: a permission_prompt
// Notification arrives about 6 s after its PermissionRequest.
const permEpisodeWindow = 30 * time.Second

func (s *Session) setState(state string, ts time.Time) {
	if s.State == state {
		return
	}
	s.State, s.StateSince = state, ts
}

// leaveNeeds ends a wait. LastLeave lets Apply tell a late permission_prompt
// echo from a new wait.
func (s *Session) leaveNeeds(ts time.Time) {
	if s.State == NeedsYou {
		s.setState(Working, ts)
		s.NeedsAID = ""
		s.LastLeave = ts
	}
}

// releaseNeeds ends a wait only when the event comes from the agent that
// entered it. A subagent's progress says nothing about the main thread's prompt,
// and the main thread's Stop or PostToolBatch says nothing about a subagent's;
// agent_id alone tells them apart (LESSONS L-25). NeedsAID "" is the main thread.
func (s *Session) releaseNeeds(aid string, ts time.Time) {
	if s.State == NeedsYou && aid == s.NeedsAID {
		s.leaveNeeds(ts)
	}
}

// Apply folds one event into the session. It returns false (and changes
// nothing) when the timestamp is bad: a bad ts must never reset a timer
// (LESSONS L-23).
func (s *Session) Apply(ev hook.Event) bool {
	ts := parseTS(ev.TS)
	if ts.IsZero() {
		return false
	}
	s.Ended = s.Ended && ev.Ev != "SessionStart"
	s.LastEvent = ts
	if ev.Tmux != nil {
		s.Loc = Locator{Sock: ev.Tmux.Sock, SPID: ev.Tmux.SPID, Pane: ev.Tmux.Pane}
	}
	if ev.PID > 0 && ev.PID != s.PID {
		s.PID, s.StartTok = ev.PID, ""
	}
	main := ev.AID == ""

	switch ev.Ev {
	case "SessionStart":
		s.start(ev, ts)
	case "UserPromptSubmit":
		s.ToolOpen = false
		s.leaveNeeds(ts) // the user typed: whoever was waiting is answered
		// D-8: a prompt after a Stop that still had background work continues
		// that turn (see Session.TurnCarry).
		if !s.TurnCarry {
			s.Edits, s.TurnStart, s.Failed = 0, ts, false
		}
		s.TurnCarry, s.BGPending, s.BGIDs = false, false, nil
		s.NeedsAID = ""
		s.setState(Working, ts)
	case "PreToolUse":
		if main {
			s.LastTool, s.LastToolAt, s.ToolOpen = ev.Tool, ts, true
			if editTools[ev.Tool] {
				s.Edits++
			}
		} else if sub := s.Subs[ev.AID]; sub != nil {
			sub.Tools++
		}
		if s.State != NeedsYou { // a tool starting does not answer the prompt (SPEC section 5)
			s.setState(Working, ts)
		}
	case "PermissionRequest":
		s.LastPermReq = ts
		kind := "permission"
		if ev.Tool == "AskUserQuestion" {
			kind = "question"
		}
		s.enterNeeds(ev.AID, ts, kind)
	case "Notification":
		if !needsNotification[ev.NType] {
			break
		}
		// The permission_prompt Notification follows its PermissionRequest in
		// the same waiting episode; if the prompt was already answered it is a
		// late echo, not a new wait.
		late := ev.NType == "permission_prompt" && !s.LastPermReq.IsZero() &&
			!s.LastLeave.Before(s.LastPermReq) && ts.Sub(s.LastPermReq) < permEpisodeWindow
		if !late {
			kind := "permission"
			if ev.NType != "permission_prompt" {
				kind = "question"
			}
			s.enterNeeds(ev.AID, ts, kind)
		}
	case "PostToolUse", "PostToolUseFailure":
		if main {
			s.LastToolAt, s.ToolOpen = ts, false
		}
		s.releaseNeeds(ev.AID, ts)
	case "PostToolBatch":
		s.releaseNeeds(ev.AID, ts)
	case "Stop", "StopFailure":
		s.ToolOpen = false
		s.releaseNeeds("", ts)
		s.Failed = ev.Ev == "StopFailure"
		s.BGIDs = runningIDs(ev.BG)
		s.BGPending = len(s.BGIDs) > 0
		s.TurnCarry = s.BGPending
		switch {
		case s.State == NeedsYou: // a subagent still waits; the turn's end does not hide it
		case s.BGPending:
			s.setState(Working, ts)
		default:
			s.setState(Done, ts)
		}
	case "SubagentStart":
		if ev.AID != "" {
			if s.Subs == nil {
				s.Subs = map[string]*Sub{}
			}
			s.Subs[ev.AID] = &Sub{Type: ev.AType, Start: ts}
		}
	case "SubagentStop":
		if _, ok := s.Subs[ev.AID]; ok { // an unpaired stop is ignored
			delete(s.Subs, ev.AID)
			s.SubsEnded++
			s.dropBG(ev.AID)
			s.releaseNeeds(ev.AID, ts) // a subagent that is gone is not waiting
		}
	case "SessionEnd":
		s.Ended = true
	}
	return true
}

func (s *Session) start(ev hook.Event, ts time.Time) {
	s.Source, s.ToolOpen = ev.Src, false
	if ev.Src == "fork" {
		s.Forked = true
	}
	if x := ev.Start; x != nil {
		s.StartTok, s.Cwd, s.Repo, s.Branch = x.StartTok, x.Cwd, x.Repo, x.Branch
		s.Task, s.TaskSrc, s.Account = x.Task, x.TaskSrc, x.Account
		s.BaseTask, s.BaseSrc = x.Task, x.TaskSrc
		s.Entrypoint, s.Transcript = x.Entrypoint, x.Transcript
	}
	if ev.Src == "compact" {
		return // compaction happens mid-session; keep the running state
	}
	s.State, s.StateSince = Idle, ts
	s.Edits, s.TurnStart, s.Failed = 0, time.Time{}, false
	s.BGPending, s.BGIDs, s.Subs, s.SubsEnded, s.TurnCarry = false, nil, nil, 0, false
	s.NeedsAID, s.NotWaitingReads = "", 0
}

// enterNeeds starts a wait, or notes that the existing one continues. The
// agent that entered it keeps it: a permission_prompt Notification carries no
// agent_id, so letting it overwrite the owner would hand a subagent's wait to
// the main thread.
func (s *Session) enterNeeds(aid string, ts time.Time, kind string) {
	if s.State != NeedsYou {
		s.setState(NeedsYou, ts)
		s.NotWaitingReads = 0
		s.NeedsAID, s.WaitKind = aid, kind
	}
}

func (s *Session) dropBG(id string) {
	out := s.BGIDs[:0]
	for _, x := range s.BGIDs {
		if x != id {
			out = append(out, x)
		}
	}
	s.BGIDs = out
	if s.BGPending && len(out) == 0 {
		s.BGPending = false
	}
}

func runningIDs(bg []hook.BGTask) []string {
	var ids []string
	for _, t := range bg {
		if t.Status == "running" {
			ids = append(ids, t.ID)
		}
	}
	return ids
}
