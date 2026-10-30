//go:build darwin

package platform

import (
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

type darwinProcs struct{}

// NewProcs returns the sysctl based implementation. Never read kinfo_proc at a
// hardcoded offset (LESSONS L-11); the typed accessor handles layout.
func NewProcs() Procs { return darwinProcs{} }

func (darwinProcs) start(pid int) (unix.Timeval, error) {
	if pid <= 0 {
		return unix.Timeval{}, ErrNoProcess
	}
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return unix.Timeval{}, err
	}
	tv := kp.Proc.P_starttime
	if tv.Sec == 0 && tv.Usec == 0 {
		return tv, ErrNoProcess
	}
	return tv, nil
}

func (d darwinProcs) StartToken(pid int) (string, error) {
	tv, err := d.start(pid)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d.%06d", tv.Sec, tv.Usec), nil
}

func (d darwinProcs) StartTime(pid int) (time.Time, error) {
	tv, err := d.start(pid)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(int64(tv.Sec), int64(tv.Usec)*1000), nil
}

func (d darwinProcs) Alive(pid int, token string) bool {
	return alive(pid, token, d.StartToken)
}

func (darwinProcs) Parent(pid int) (int, error) {
	if pid <= 0 {
		return 0, ErrNoProcess
	}
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, err
	}
	return int(kp.Eproc.Ppid), nil
}
