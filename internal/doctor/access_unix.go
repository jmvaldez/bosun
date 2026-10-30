//go:build linux || darwin

package doctor

import "golang.org/x/sys/unix"

// accessWritable asks the kernel without creating anything (LESSONS L-44).
func accessWritable(dir string) error { return unix.Access(dir, unix.W_OK) }
