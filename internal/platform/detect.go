// Package platform holds the OS seams: platform detection, the Adapters bundle
// main hands down, and per-OS process inspection.
package platform

import "strings"

// Kind is the runtime platform. WSL is a Linux build detected at runtime.
type Kind string

const (
	Darwin Kind = "darwin"
	Linux  Kind = "linux"
	WSL    Kind = "wsl"
	Other  Kind = "other"
)

// Detect is the only place that decides which platform bosun is on.
func Detect(goos string, getenv func(string) string, readFile func(string) ([]byte, error)) Kind {
	switch goos {
	case "darwin":
		return Darwin
	case "linux":
		if getenv("WSL_DISTRO_NAME") != "" {
			return WSL
		}
		if b, err := readFile("/proc/sys/kernel/osrelease"); err == nil &&
			strings.Contains(strings.ToLower(string(b)), "microsoft") {
			return WSL
		}
		return Linux
	}
	return Other
}
