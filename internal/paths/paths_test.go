package paths

import "testing"

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestResolveDefaults(t *testing.T) {
	p := Resolve(env(map[string]string{"HOME": "/h"}))
	want := Paths{
		Config: "/h/.config/bosun",
		State:  "/h/.local/state/bosun",
		Cache:  "/h/.cache/bosun",
		Data:   "/h/.local/share/bosun",
		Claude: "/h/.claude",
	}
	if p != want {
		t.Fatalf("got %+v, want %+v", p, want)
	}
}

func TestResolveOverrideBeatsXDG(t *testing.T) {
	p := Resolve(env(map[string]string{
		"HOME":              "/h",
		"XDG_STATE_HOME":    "/xdg/state",
		"BOSUN_STATE_DIR":   "/tmp/s",
		"XDG_CONFIG_HOME":   "/xdg/config",
		"CLAUDE_CONFIG_DIR": "/c",
	}))
	if p.State != "/tmp/s" {
		t.Errorf("state = %q", p.State)
	}
	if p.Config != "/xdg/config/bosun" {
		t.Errorf("config = %q", p.Config)
	}
	if p.Claude != "/c" {
		t.Errorf("claude = %q", p.Claude)
	}
}

func TestResolveIgnoresRelativeXDG(t *testing.T) {
	p := Resolve(env(map[string]string{"HOME": "/h", "XDG_STATE_HOME": "rel"}))
	if p.State != "/h/.local/state/bosun" {
		t.Errorf("state = %q", p.State)
	}
}

func TestResolveNoHome(t *testing.T) {
	p := Resolve(env(nil))
	if p != (Paths{}) {
		t.Fatalf("got %+v", p)
	}
	if p.Session("abc") != "" || p.Heartbeat() != "" {
		t.Error("derived paths must be empty when the dir is unresolved")
	}
}

func TestValidSessionID(t *testing.T) {
	for id, want := range map[string]bool{
		"11111111-2222-3333-4444-555555555555": true,
		"a":                                    true,
		"":                                     false, "../x": false, "a/b": false, "a b": false, "a.b": false, "a\x00": false,
	} {
		if got := ValidSessionID(id); got != want {
			t.Errorf("ValidSessionID(%q) = %v", id, got)
		}
	}
	long := make([]byte, 65)
	for i := range long {
		long[i] = 'a'
	}
	if ValidSessionID(string(long)) || !ValidSessionID(string(long[:64])) {
		t.Error("length bound is 64")
	}
}
