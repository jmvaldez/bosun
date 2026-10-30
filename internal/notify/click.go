package notify

import (
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/jmvaldez/bosun/internal/runner"
)

// ClickTimeout is how long a listener child waits for a click. It is a hard
// deadline: when it passes the child exits, so no listener outlives its
// notice's usefulness and bosun still runs no daemon. A click after that does
// nothing (the notice stays in the tray, but the jump needs the listener).
const ClickTimeout = 2 * time.Minute

var (
	actionRE = regexp.MustCompile(`org\.freedesktop\.Notifications\.ActionInvoked \(uint32 (\d+), '([^']*)'\)`)
	closedRE = regexp.MustCompile(`org\.freedesktop\.Notifications\.NotificationClosed \(uint32 (\d+), uint32 \d+\)`)
)

type ClickDeps struct {
	Stream  runner.Streamer
	Run     runner.Runner
	Gdbus   string // gdbus binary
	Exe     string // bosun, to run `jump`
	Timeout time.Duration
}

// Listen watches the session bus for a click on notification id and, on the
// default action, runs `bosun jump --session`. It returns true if it jumped. It
// ends on a click, when the notice is closed, or at the deadline.
func Listen(d ClickDeps, id uint32, session string) (bool, error) {
	if d.Timeout <= 0 {
		d.Timeout = ClickTimeout
	}
	clicked := false
	err := d.Stream.Stream(d.Timeout, d.Gdbus, []string{"monitor", "--session", "--dest", dbusDest}, func(line string) bool {
		if m := actionRE.FindStringSubmatch(line); m != nil && m[1] == strconv.FormatUint(uint64(id), 10) && m[2] == "default" {
			clicked = true
			return true
		}
		if m := closedRE.FindStringSubmatch(line); m != nil && m[1] == strconv.FormatUint(uint64(id), 10) {
			return true // dismissed, expired or retracted: nothing left to click
		}
		return false
	})
	if err != nil {
		return false, fmt.Errorf("notify-click: %w", err)
	}
	if !clicked {
		return false, nil
	}
	if _, err := d.Run.Run(5*time.Second, d.Exe, "jump", "--session", session); err != nil {
		return true, fmt.Errorf("notify-click: jump: %w", err)
	}
	return true, nil
}
