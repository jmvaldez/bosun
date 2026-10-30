package hook

import (
	"bytes"
	"encoding/json"
	"errors"
)

// Events lists the 13 hooks bosun registers. Anything else is ignored.
var Events = []string{
	"SessionStart", "UserPromptSubmit", "PreToolUse", "PermissionRequest",
	"PostToolUse", "PostToolUseFailure", "PostToolBatch", "Notification",
	"SubagentStart", "SubagentStop", "Stop", "StopFailure", "SessionEnd",
}

// BGTask is a background task reduced to enums and ids. Text fields such as
// description and command are never decoded (LESSONS L-21).
type BGTask struct {
	ID     string `json:"id,omitempty"`
	Type   string `json:"type,omitempty"`
	Status string `json:"status,omitempty"`
	Count  int    `json:"count,omitempty"`
}

// payload is the allowlisted view of a hook's stdin. It has no field that could
// hold prompt text, tool input, tool output, assistant text, or error text, so
// those cannot reach disk even by mistake.
type payload struct {
	event          string
	sessionID      string
	agentID        string
	agentType      string
	promptID       string
	toolName       string
	toolUseID      string
	notifType      string
	source         string
	reason         string
	cwd            string
	transcriptPath string
	bg             []BGTask
}

// decode walks the top-level object token by token, keeping allowlisted keys
// and skipping everything else. Walking (rather than unmarshaling a whole
// struct) means a payload cut off at maxStdin still yields the keys that
// arrived before the cut.
func decode(raw []byte) (payload, bool) {
	var p payload
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return p, false
	}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			break
		}
		key, ok := kt.(string)
		if !ok {
			break
		}
		if err := p.field(dec, key); err != nil {
			break
		}
	}
	return p, p.event != "" && p.sessionID != ""
}

// field decodes one value. It returns an error only when the stream is broken;
// a value of the wrong type is consumed and dropped.
func (p *payload) field(dec *json.Decoder, key string) error {
	var dst *string
	switch key {
	case "hook_event_name":
		dst = &p.event
	case "session_id":
		dst = &p.sessionID
	case "agent_id":
		dst = &p.agentID
	case "agent_type":
		dst = &p.agentType
	case "prompt_id":
		dst = &p.promptID
	case "tool_name":
		dst = &p.toolName
	case "tool_use_id":
		dst = &p.toolUseID
	case "notification_type":
		dst = &p.notifType
	case "source":
		dst = &p.source
	case "reason":
		dst = &p.reason
	case "cwd":
		dst = &p.cwd
	case "transcript_path":
		dst = &p.transcriptPath
	case "background_tasks":
		var tasks []BGTask
		return tolerate(dec.Decode(&tasks), func() { p.bg = tasks })
	default:
		var skip json.RawMessage
		return dec.Decode(&skip)
	}
	var s string
	return tolerate(dec.Decode(&s), func() { *dst = s })
}

// tolerate applies ok on success and swallows type mismatches, which leave the
// stream positioned after the value. Syntax errors and EOF still propagate.
func tolerate(err error, ok func()) error {
	if err == nil {
		ok()
		return nil
	}
	var ute *json.UnmarshalTypeError
	if errors.As(err, &ute) {
		return nil
	}
	return err
}
