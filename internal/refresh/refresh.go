// Package refresh implements `bosun refresh`, the detached job that keeps the
// agents cache, the cached account key, the ledger and today's spend current.
// Hooks and the status line never call claude; this does, off the hot path.
package refresh

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/jmvaldez/bosun/internal/atomicfile"
	"github.com/jmvaldez/bosun/internal/config"
	"github.com/jmvaldez/bosun/internal/flock"
	"github.com/jmvaldez/bosun/internal/platform"
)

const agentsTimeout = 5 * time.Second

var errNoClaude = errors.New("claude binary not found")

// ResolveClaude returns claude.bin verbatim when set, else claude's path from
// PATH.
func ResolveClaude(cfg config.Config) (string, error) {
	if cfg.Claude.Bin != "" {
		return cfg.Claude.Bin, nil
	}
	p, err := exec.LookPath("claude")
	if err != nil {
		return "", errNoClaude
	}
	return p, nil
}

// Run refreshes the agents cache, the identity when it is missing or older
// than IdentityTTL, and spend (refreshSpend). Concurrent refreshes dedupe on
// refresh.lock: a second one exits quietly. It returns the process exit code.
func Run(ad platform.Adapters, cfg config.Config, out, errw io.Writer) int {
	if ad.Paths.State == "" || ad.Paths.Cache == "" {
		fmt.Fprintln(errw, "bosun refresh: no state or cache dir (HOME unset?)")
		return 1
	}
	lock, err := flock.TryLock(ad.Paths.Locks()+"/refresh.lock", 0)
	if errors.Is(err, flock.ErrBusy) {
		return 0
	}
	if err != nil {
		fmt.Fprintf(errw, "bosun refresh: %v\n", err)
		return 1
	}
	defer lock.Unlock()

	// Spend works from files alone, so a missing claude binary only costs the
	// agents cache and identity, not today's total.
	code := 0
	if bin, err := ResolveClaude(cfg); err != nil {
		fmt.Fprintf(errw, "bosun refresh: %v\n", err)
		code = 1
	} else {
		if err := refreshAgents(ad, bin, out); err != nil {
			fmt.Fprintf(errw, "bosun refresh: %v\n", err)
			code = 1
		}
		if err := refreshIdentity(ad, out); err != nil {
			fmt.Fprintf(errw, "bosun refresh: identity: %v\n", err)
			code = 1
		}
	}
	if err := refreshSpend(ad, cfg, out); err != nil {
		fmt.Fprintf(errw, "bosun refresh: spend: %v\n", err)
		code = 1
	}
	return code
}

func refreshAgents(ad platform.Adapters, bin string, out io.Writer) error {
	b, err := ad.Runner.Run(agentsTimeout, bin, "agents", "--json")
	if err != nil {
		// Leave the old cache in place: it ages out and the next tick retries.
		return fmt.Errorf("claude agents: %w", err)
	}
	rows, err := ParseAgents(b, ad.Procs)
	if err != nil {
		return err
	}
	data, err := json.Marshal(Cache{V: 1, FetchedAt: ad.Clock.Now().UTC(), Rows: rows})
	if err != nil {
		return err
	}
	if err := atomicfile.Write(ad.Paths.AgentsCache(), data, 0o600); err != nil {
		return err
	}
	fmt.Fprintf(out, "agents: %d rows\n", len(rows))
	return nil
}

func refreshIdentity(ad platform.Adapters, out io.Writer) error {
	now := ad.Clock.Now()
	if id, ok := ReadIdentity(ad.Paths.Identity()); ok && now.Sub(id.FetchedAt) < IdentityTTL {
		return nil
	}
	if ad.Identity == nil {
		return errors.New("no identity adapter")
	}
	key, err := ad.Identity.Account()
	if err != nil {
		return err
	}
	if err := WriteIdentity(ad.Paths.Identity(), key, now); err != nil {
		return err
	}
	fmt.Fprintln(out, "identity: cached")
	return nil
}
