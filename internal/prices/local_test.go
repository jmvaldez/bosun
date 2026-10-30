package prices

import (
	"bufio"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// localModelIDs collects assistant message.model values from real transcripts,
// read-only. It skips the calling test unless BOSUN_TEST_REAL_SESSIONS is set.
func localModelIDs(t *testing.T) map[string]bool {
	t.Helper()
	if os.Getenv("BOSUN_TEST_REAL_SESSIONS") == "" {
		t.Skip("set BOSUN_TEST_REAL_SESSIONS=1 to read real transcripts (read-only)")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home")
	}
	root := filepath.Join(home, ".claude", "projects")
	ids := map[string]bool{}
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".jsonl") {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		defer f.Close()
		r := bufio.NewReaderSize(f, 1<<20)
		for {
			line, err := r.ReadBytes('\n')
			if len(line) > 0 && strings.Contains(string(line), `"type":"assistant"`) {
				var l struct {
					Type    string `json:"type"`
					Message struct {
						Model string `json:"model"`
					} `json:"message"`
				}
				if json.Unmarshal(line, &l) == nil && l.Type == "assistant" && l.Message.Model != "" {
					ids[l.Message.Model] = true
				}
			}
			if err != nil {
				break
			}
		}
		return nil
	})
	return ids
}
