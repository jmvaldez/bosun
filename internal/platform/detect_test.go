package platform

import (
	"errors"
	"testing"
)

func TestDetect(t *testing.T) {
	read := func(s string) func(string) ([]byte, error) {
		return func(p string) ([]byte, error) {
			if p != "/proc/sys/kernel/osrelease" {
				t.Errorf("read %q", p)
			}
			if s == "" {
				return nil, errors.New("absent")
			}
			return []byte(s), nil
		}
	}
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	tests := []struct {
		name string
		goos string
		env  map[string]string
		rel  string
		want Kind
	}{
		{"darwin", "darwin", nil, "", Darwin},
		{"plain linux", "linux", nil, "7.1.1-generic\n", Linux},
		{"wsl by env", "linux", map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}, "", WSL},
		{"wsl by osrelease", "linux", nil, "5.15.90.1-microsoft-standard-WSL2\n", WSL},
		{"wsl case", "linux", nil, "5.15-Microsoft\n", WSL},
		{"linux no proc", "linux", nil, "", Linux},
		{"windows", "windows", nil, "", Other},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Detect(tt.goos, env(tt.env), read(tt.rel)); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}
