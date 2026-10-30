package main

import "testing"

func TestLaunchCommand(t *testing.T) {
	for _, tt := range []struct {
		name       string
		key, shell string
		args       []string
		want       string
	}{
		{"default shell", "ABC-1", "", nil,
			`'/bin/sh' -ic 'export PATH='\''/opt/bin'\'':$PATH; export BOSUN_TASK='\''ABC-1'\''; claude'`},
		{"quote in key", "it's", "/bin/zsh", nil,
			`'/bin/zsh' -ic 'export PATH='\''/opt/bin'\'':$PATH; export BOSUN_TASK='\''it'\''\'\'''\''s'\''; claude'`},
		{"args after double dash", "ABC-1", "/bin/zsh", []string{"-p", "hi there"},
			`'/bin/zsh' -ic 'export PATH='\''/opt/bin'\'':$PATH; export BOSUN_TASK='\''ABC-1'\''; claude '\''-p'\'' '\''hi there'\'''`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := launchCommand("/opt/bin/bosun", "claude", tt.key, tt.shell, tt.args); got != tt.want {
				t.Errorf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}
