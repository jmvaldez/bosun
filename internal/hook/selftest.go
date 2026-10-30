package hook

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jmvaldez/bosun/internal/platform"
)

// selfTestSentinel is planted in every text-bearing path.
const selfTestSentinel = "BOSUN-SELFTEST-SENTINEL-5f2a"

type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }

// SelfTest pushes a payload carrying a sentinel in every text field through the
// real decoder and encoder for each of Events and fails if the sentinel
// survives into an event line. It touches no files, so `doctor` can run it.
func SelfTest() error {
	s := selfTestSentinel
	text := map[string]any{"command": s, "file_path": s, "nested": []any{map[string]any{"deep": s}}}
	for _, ev := range Events {
		payload := map[string]any{
			"session_id": "00000000-0000-0000-0000-000000000000", "hook_event_name": ev,
			"prompt": s, "message": s, "title": s, "error": s, "last_assistant_message": s,
			"tool_input": text, "tool_response": text, "tool_name": "Bash",
			"tool_calls":             []any{map[string]any{"tool_input": text, "tool_response": text}},
			"permission_suggestions": []any{map[string]any{"directories": []any{s}}},
			"background_tasks":       []any{map[string]any{"id": "a1", "type": "subagent", "status": "running", "description": s, "command": s}},
			"session_crons":          []any{map[string]any{"prompt": s}},
			"scratchpad_dir":         "/tmp/" + s, "agent_transcript_path": "/tmp/" + s, "session_name": s, "model": s,
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		p, ok := decode(raw)
		if !ok {
			return fmt.Errorf("self-test: the decoder rejected a valid %s payload", ev)
		}
		ad := platform.Adapters{Clock: fixedClock{}, Getenv: func(string) string { return "" }}
		line, err := buildEvent(ad, p, ad.Clock.Now()).encode()
		if err != nil {
			return fmt.Errorf("self-test: %s: %w", ev, err)
		}
		if strings.Contains(string(line), s) {
			return fmt.Errorf("self-test: text from a %s payload reached the event line: %s", ev, line)
		}
	}
	return nil
}
