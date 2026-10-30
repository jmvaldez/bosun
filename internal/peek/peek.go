// Package peek reads one line of text from a session's transcript for the
// popup, the spend view and notices. It is the single place bosun shows tool
// input, question or prompt text (D-27): read at render or notify time,
// returned to the caller, never stored, logged or exported.
package peek

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"unicode"
)

const tailBytes = 64 << 10

type block struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Text      string          `json:"text"`
}

type line struct {
	Type    string `json:"type"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

func blocks(raw json.RawMessage) []block {
	var bs []block
	if json.Unmarshal(raw, &bs) != nil {
		return nil
	}
	return bs
}

// Pending returns the newest unanswered tool call as "Tool: subject", where
// subject is Info.Target: the same one-line subject Latest reports.
// ok is false when no tool call is pending.
func Pending(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	return pendingFrom(f)
}

// Info describes the newest tool call in the transcript's tail.
type Info struct {
	Tool string
	// Target is the one-line subject: command, file path, pattern, url,
	// description or question, else the input's first string field.
	Target string
	// Question is the first question of an AskUserQuestion call.
	Question string
	// Pending is true while the call has no result yet.
	Pending bool
}

// Latest returns the newest tool call: what a working session is doing, or what
// a waiting one is asking. The zero Info means nothing was found.
func Latest(path string) Info {
	f, err := os.Open(path)
	if err != nil {
		return Info{}
	}
	defer f.Close()
	uses, answered := tailUses(f)
	if len(uses) == 0 {
		return Info{}
	}
	b := uses[len(uses)-1]
	in := Info{Tool: b.Name, Target: target(b), Pending: !answered[b.ID]}
	if b.Name == "AskUserQuestion" {
		in.Question = question(b.Input)
	}
	return in
}

func question(raw json.RawMessage) string {
	var in struct {
		Questions []struct {
			Question string `json:"question"`
		} `json:"questions"`
	}
	if json.Unmarshal(raw, &in) != nil || len(in.Questions) == 0 {
		return ""
	}
	return Line(in.Questions[0].Question)
}

func target(b block) string {
	field := ""
	switch b.Name {
	case "Bash":
		field = "command"
	case "Edit", "Write", "MultiEdit", "NotebookEdit", "Read":
		field = "file_path"
	case "Glob", "Grep":
		field = "pattern"
	case "WebFetch":
		field = "url"
	case "Task", "Agent":
		field = "description"
	case "AskUserQuestion":
		return question(b.Input)
	}
	var val string
	if field != "" {
		var in map[string]json.RawMessage
		if json.Unmarshal(b.Input, &in) == nil {
			_ = json.Unmarshal(in[field], &val)
		}
	}
	if val == "" {
		val = firstString(b.Input)
	}
	return Line(val)
}

func pendingFrom(f io.ReadSeeker) (string, bool) {
	uses, answered := tailUses(f)
	for i := len(uses) - 1; i >= 0; i-- {
		if !answered[uses[i].ID] {
			return describe(uses[i]), true
		}
	}
	return "", false
}

// tailUses reads the transcript's tail and returns its tool calls in order and
// the ids that have a result.
func tailUses(f io.ReadSeeker) ([]block, map[string]bool) {
	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, nil
	}
	start := int64(0)
	if size > tailBytes {
		start = size - tailBytes
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, nil
	}
	data, err := io.ReadAll(io.LimitReader(f, tailBytes))
	if err != nil {
		return nil, nil
	}
	if start > 0 { // the first line is cut mid-way
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		} else {
			return nil, nil
		}
	}
	answered := map[string]bool{}
	var uses []block
	for _, ln := range bytes.Split(data, []byte("\n")) {
		switch {
		case bytes.Contains(ln, []byte(`"tool_use"`)) || bytes.Contains(ln, []byte(`"tool_result"`)):
		default:
			continue
		}
		var l line
		if json.Unmarshal(ln, &l) != nil {
			continue
		}
		for _, b := range blocks(l.Message.Content) {
			switch {
			case l.Type == "assistant" && b.Type == "tool_use":
				uses = append(uses, b)
			case l.Type == "user" && b.Type == "tool_result":
				answered[b.ToolUseID] = true
			}
		}
	}
	return uses, answered
}

func describe(b block) string {
	return Line(b.Name + ": " + target(b))
}

// firstString returns the first string-valued field of a JSON object in
// document order (a Go map would lose it).
func firstString(raw json.RawMessage) string {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return ""
	}
	for dec.More() {
		if _, err := dec.Token(); err != nil { // key
			return ""
		}
		var v json.RawMessage
		if dec.Decode(&v) != nil {
			return ""
		}
		var s string
		if json.Unmarshal(v, &s) == nil {
			return s
		}
	}
	return ""
}

// Line makes s one printable line: control characters and newlines become
// single spaces.
func Line(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			if !space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = true
			continue
		}
		space = false
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// PromptFirstLine returns the first line of the user prompt with this id, for
// the Spend tree's `v`. Unlike the tail reads above it scans the whole
// transcript, which is fine because it runs only on a key press.
func PromptFirstLine(path, promptID string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	marker := []byte(`"promptId":"` + promptID + `"`)
	r := bufio.NewReaderSize(f, 256<<10)
	for {
		ln, err := r.ReadBytes('\n')
		if bytes.Contains(ln, marker) && bytes.Contains(ln, []byte(`"type":"user"`)) {
			var l line
			if json.Unmarshal(ln, &l) == nil && l.Type == "user" {
				var text string
				if json.Unmarshal(l.Message.Content, &text) == nil {
					return first(text)
				}
				for _, b := range blocks(l.Message.Content) {
					if b.Type == "text" {
						return first(b.Text)
					}
				}
			}
		}
		if err != nil {
			return ""
		}
	}
}

func first(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return Line(s)
}
