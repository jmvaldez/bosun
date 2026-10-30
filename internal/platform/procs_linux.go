//go:build linux

package platform

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"time"
)

// userHZ is the clock-tick rate /proc exposes (USER_HZ). It is 100 on every
// architecture Go targets (alpha's is 1024, but Go has no alpha port), so no
// sysconf (cgo) is needed.
const userHZ = 100

type linuxProcs struct {
	procDir string
}

// NewProcs returns the /proc based implementation.
func NewProcs() Procs { return linuxProcs{procDir: "/proc"} }

func (p linuxProcs) startTicks(pid int) (uint64, error) {
	if pid <= 0 {
		return 0, ErrNoProcess
	}
	b, err := os.ReadFile(fmt.Sprintf("%s/%d/stat", p.procDir, pid))
	if err != nil {
		return 0, err
	}
	return parseStartTicks(b)
}

// parseStartTicks reads field 22 (starttime). The comm field in parentheses can
// itself contain spaces and ')', so everything is counted from after the last
// ')'. After it, field 3 (state) is index 0, which puts field 22 at index 19.
// Field numbers follow https://man7.org/linux/man-pages/man5/proc_pid_stat.5.html.
func parseStartTicks(stat []byte) (uint64, error) {
	i := bytes.LastIndexByte(stat, ')')
	if i < 0 {
		return 0, fmt.Errorf("platform: malformed stat: no ')'")
	}
	fields := bytes.Fields(stat[i+1:])
	if len(fields) < 20 {
		return 0, fmt.Errorf("platform: malformed stat: %d fields after comm", len(fields))
	}
	v, err := strconv.ParseUint(string(fields[19]), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("platform: malformed stat starttime: %w", err)
	}
	return v, nil
}

// StartToken is the raw tick count since boot. It is not derived from btime,
// which can shift when the clock is stepped, so it is stable for the process's
// whole life.
func (p linuxProcs) StartToken(pid int) (string, error) {
	t, err := p.startTicks(pid)
	if err != nil {
		return "", err
	}
	return strconv.FormatUint(t, 10), nil
}

func (p linuxProcs) StartTime(pid int) (time.Time, error) {
	ticks, err := p.startTicks(pid)
	if err != nil {
		return time.Time{}, err
	}
	b, err := os.ReadFile(p.procDir + "/stat")
	if err != nil {
		return time.Time{}, err
	}
	btime, err := parseBtime(b)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(btime, 0).Add(time.Duration(ticks) * time.Second / userHZ), nil
}

func parseBtime(procStat []byte) (int64, error) {
	for _, line := range bytes.Split(procStat, []byte("\n")) {
		if rest, ok := bytes.CutPrefix(line, []byte("btime ")); ok {
			return strconv.ParseInt(string(bytes.TrimSpace(rest)), 10, 64)
		}
	}
	return 0, fmt.Errorf("platform: no btime in /proc/stat")
}

func (p linuxProcs) Alive(pid int, token string) bool {
	return alive(pid, token, p.StartToken)
}

// Parent reads field 4 (ppid), counted after the last ')' like starttime.
func (p linuxProcs) Parent(pid int) (int, error) {
	if pid <= 0 {
		return 0, ErrNoProcess
	}
	b, err := os.ReadFile(fmt.Sprintf("%s/%d/stat", p.procDir, pid))
	if err != nil {
		return 0, err
	}
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return 0, fmt.Errorf("platform: malformed stat: no ')'")
	}
	fields := bytes.Fields(b[i+1:])
	if len(fields) < 2 {
		return 0, fmt.Errorf("platform: malformed stat")
	}
	return strconv.Atoi(string(fields[1]))
}
