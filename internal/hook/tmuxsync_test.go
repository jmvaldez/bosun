package hook

import (
	"strings"
	"testing"
)

// D-31: SessionStart inside tmux forks the conf sync and nothing else does.
func TestSessionStartInTmuxForksTheConfSync(t *testing.T) {
	const start = `"hook_event_name":"SessionStart","source":"startup"`
	tests := []struct {
		name    string
		payload string
		env     map[string]string
		want    string
	}{
		{"session start in tmux", start, map[string]string{"TMUX": "/tmp/tmux-1/default,123,0"}, "tmux-sync"},
		{"session start outside tmux", start, nil, ""},
		{"another event in tmux", `"hook_event_name":"PreToolUse"`, map[string]string{"TMUX": "/tmp/tmux-1/default,123,0"}, ""},
		{"sdk session in tmux", start, map[string]string{"TMUX": "/tmp/tmux-1/default,123,0", "CLAUDE_CODE_ENTRYPOINT": "sdk-cli"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			for k, v := range tt.env {
				e.vars[k] = v
			}
			e.rebuild()
			spy := &spawnSpy{}
			e.ad.Spawner = spy
			if rc := e.run(`{"session_id":"` + sid + `",` + tt.payload + `}`); rc != 0 {
				t.Fatalf("hook exited %d", rc)
			}
			got := ""
			if len(spy.calls) > 0 {
				got = strings.Join(spy.calls[0], " ")
			}
			if got != tt.want || len(spy.calls) > 1 {
				t.Errorf("spawned %q (%d calls), want %q", got, len(spy.calls), tt.want)
			}
		})
	}
}
