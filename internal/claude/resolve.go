package claude

import (
	"errors"
	"os/exec"
	"time"

	"github.com/jmvaldez/bosun/internal/config"
)

// AgentsTimeout bounds one `claude agents --json` call.
const AgentsTimeout = 5 * time.Second

var errNoClaude = errors.New("claude binary not found")

// Resolve returns claude.bin verbatim when set, else claude's path from PATH.
func Resolve(cfg config.Config) (string, error) {
	if cfg.Claude.Bin != "" {
		return cfg.Claude.Bin, nil
	}
	p, err := exec.LookPath("claude")
	if err != nil {
		return "", errNoClaude
	}
	return p, nil
}
