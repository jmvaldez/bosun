package platform

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jmvaldez/bosun/internal/runner"
)

// ClaudeIdentity asks the claude binary who is logged in. Bin comes from
// refresh.ResolveClaude; empty means claude was not found.
type ClaudeIdentity struct {
	Runner runner.Runner
	Bin    string
}

// Account returns the lowercased login email. Only the email key is decoded,
// so org names and ids never reach bosun.
func (c ClaudeIdentity) Account() (string, error) {
	if c.Bin == "" {
		return "", errors.New("claude binary not found")
	}
	out, err := c.Runner.Run(5*time.Second, c.Bin, "auth", "status", "--json")
	if err != nil {
		return "", err
	}
	var st struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(out, &st); err != nil {
		return "", err
	}
	key := strings.ToLower(strings.TrimSpace(st.Email))
	if key == "" {
		return "", errors.New("claude auth status has no email")
	}
	return key, nil
}
