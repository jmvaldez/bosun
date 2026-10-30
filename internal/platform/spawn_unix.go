//go:build linux || darwin

package platform

import (
	"os"
	"syscall"
)

// Spawner starts a detached child that outlives the caller.
type Spawner interface {
	Spawn(name string, args ...string) error
}

// Detached starts children in their own session with stdio on /dev/null, and
// never waits for them. The child bounds itself (refresh holds a lock and has
// per-exec deadlines), so no context deadline applies here.
type Detached struct{}

func (Detached) Spawn(name string, args ...string) error {
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer null.Close()
	p, err := os.StartProcess(name, append([]string{name}, args...), &os.ProcAttr{
		Files: []*os.File{null, null, null},
		Env:   os.Environ(),
		Sys:   &syscall.SysProcAttr{Setsid: true},
	})
	if err != nil {
		return err
	}
	return p.Release()
}
