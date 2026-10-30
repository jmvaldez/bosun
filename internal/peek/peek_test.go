package peek

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func asst(blocks ...map[string]any) string {
	b, _ := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{"content": blocks}})
	return string(b) + "\n"
}
func result(id string) string {
	b, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": "SECRET output"}}}})
	return string(b) + "\n"
}
func use(id, name string, input map[string]any) map[string]any {
	return map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}
}
func write(t *testing.T, s string) string {
	p := filepath.Join(t.TempDir(), "t.jsonl")
	os.WriteFile(p, []byte(s), 0o644)
	return p
}

func TestPendingPerTool(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"bash command", asst(use("t1", "Bash", map[string]any{"command": "npm test -- refunds", "description": "x"})), "Bash: npm test -- refunds"},
		{"edit path", asst(use("t1", "Edit", map[string]any{"file_path": "/src/a.go", "old_string": "SECRET", "new_string": "y"})), "Edit: /src/a.go"},
		{"write path", asst(use("t1", "Write", map[string]any{"content": "zzz", "file_path": "/src/b.go"})), "Write: /src/b.go"},
		{"read path", asst(use("t1", "Read", map[string]any{"file_path": "/etc/hosts"})), "Read: /etc/hosts"},
		{"first string field of another tool", `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Grep","input":{"limit":5,"pattern":"TODO","path":"/x"}}]}}` + "\n", "Grep: TODO"},
		{"glob pattern", asst(use("t1", "Glob", map[string]any{"path": "/x", "pattern": "**/*.go"})), "Glob: **/*.go"},
		{"webfetch url", asst(use("t1", "WebFetch", map[string]any{"prompt": "summarise", "url": "https://example.com"})), "WebFetch: https://example.com"},
		{"task description", asst(use("t1", "Task", map[string]any{"prompt": "long brief", "description": "find callers"})), "Task: find callers"},
		{"agent description", asst(use("t1", "Agent", map[string]any{"prompt": "long brief", "description": "find callers"})), "Agent: find callers"},
		{"ask user question", asst(use("t1", "AskUserQuestion", map[string]any{"questions": []any{map[string]any{"question": "Which db?"}}})), "AskUserQuestion: Which db?"},
		{"control characters and newlines", asst(use("t1", "Bash", map[string]any{"command": "echo a\n\x1b[31mb\tc"})), "Bash: echo a [31mb c"},
		{"answered calls are skipped", asst(use("t1", "Bash", map[string]any{"command": "old"})) + result("t1") + asst(use("t2", "Read", map[string]any{"file_path": "/new"})), "Read: /new"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Pending(write(t, tt.body))
			if !ok || got != tt.want {
				t.Errorf("got %q (%v), want %q", got, ok, tt.want)
			}
		})
	}
}

// Pending's subject is Latest's Target, so the popup and the notice name a
// call the same way.
func TestPendingSubjectIsLatestTarget(t *testing.T) {
	p := write(t, asst(use("t1", "WebFetch", map[string]any{"prompt": "p", "url": "https://example.com"})))
	got, _ := Pending(p)
	if want := "WebFetch: " + Latest(p).Target; got != want {
		t.Errorf("Pending = %q, want %q", got, want)
	}
}

func TestNothingPending(t *testing.T) {
	if _, ok := Pending(write(t, asst(use("t1", "Bash", map[string]any{"command": "x"}))+result("t1"))); ok {
		t.Error("an answered call is not pending")
	}
	if _, ok := Pending(write(t, "")); ok {
		t.Error("empty file")
	}
	if _, ok := Pending(filepath.Join(t.TempDir(), "missing")); ok {
		t.Error("missing file")
	}
}

// A 10 MB transcript reads only the tail (SPEC M4 acceptance).
func TestOnlyTheTailOfALargeTranscriptIsRead(t *testing.T) {
	pad := strings.Repeat(`{"type":"attachment","x":"`+strings.Repeat("p", 1000)+`"}`+"\n", 10_500) // ~10.5 MB
	body := asst(use("old", "Bash", map[string]any{"command": "ancient"})) + pad + asst(use("t9", "Bash", map[string]any{"command": "make build"}))
	p := write(t, body)
	fi, _ := os.Stat(p)
	if fi.Size() < 10<<20 {
		t.Fatalf("fixture is only %d bytes", fi.Size())
	}
	f, _ := os.Open(p)
	defer f.Close()
	cr := &countingSeeker{f: f}
	got, ok := pendingFrom(cr)
	if !ok || got != "Bash: make build" {
		t.Fatalf("got %q %v", got, ok)
	}
	if cr.read > tailBytes {
		t.Errorf("read %d bytes of a %d byte file; the tail is %d", cr.read, fi.Size(), tailBytes)
	}
}

type countingSeeker struct {
	f    *os.File
	read int64
}

func (c *countingSeeker) Read(p []byte) (int, error) {
	n, err := c.f.Read(p)
	c.read += int64(n)
	return n, err
}
func (c *countingSeeker) Seek(off int64, whence int) (int64, error) { return c.f.Seek(off, whence) }

func TestPromptFirstLine(t *testing.T) {
	user := func(id string, content any) string {
		b, _ := json.Marshal(map[string]any{"type": "user", "promptId": id, "message": map[string]any{"content": content}})
		return string(b) + "\n"
	}
	p := write(t, user("p1", "fix the refund bug\nand then more lines")+user("p2", []any{map[string]any{"type": "text", "text": "second prompt\nline two"}}))
	if got := PromptFirstLine(p, "p1"); got != "fix the refund bug" {
		t.Errorf("p1 = %q", got)
	}
	if got := PromptFirstLine(p, "p2"); got != "second prompt" {
		t.Errorf("p2 = %q", got)
	}
	if got := PromptFirstLine(p, "nope"); got != "" {
		t.Errorf("unknown = %q", got)
	}
}
