package uninstall

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmvaldez/bosun/internal/install"
	"github.com/jmvaldez/bosun/internal/paths"
	"github.com/jmvaldez/bosun/internal/platform"
)

type stubTmux map[string]string

func (s stubTmux) Run(_ time.Duration, _ string, args ...string) ([]byte, error) {
	return []byte(s[strings.Join(args, " ")]), nil
}

type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Date(2026, 10, 25, 9, 8, 7, 0, time.UTC) }

func TestGoldenUninstallPlanWithPurge(t *testing.T) {
	root := t.TempDir()
	home := root + "/home"
	p := paths.Paths{Config: home + "/.config/bosun", State: home + "/.local/state/bosun", Cache: home + "/.cache/bosun",
		Data: home + "/.local/share/bosun", Claude: home + "/.claude"}
	for _, d := range []string{p.Config, p.State, p.Data} {
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, paths.MarkerName), nil, 0o600)
	}
	os.MkdirAll(p.Claude, 0o755)
	wrapped := install.WrapCommand(`jq -r '.model.id'`)
	os.WriteFile(p.ClaudeSettings(), []byte(`{"enabledPlugins":{"bosun@bosun":true},"statusLine":{"type":"command","command":`+quote(wrapped)+"}}\n"), 0o644)
	os.MkdirAll(p.Claude+"/plugins", 0o755)
	os.WriteFile(p.Claude+"/plugins/known_marketplaces.json", []byte(`{"bosun":{}}`), 0o644)
	conf := home + "/.tmux.conf"
	os.WriteFile(conf, []byte("run '~/.tmux/plugins/tpm/tpm'\nsource-file -q "+p.TmuxConf()+"\n"), 0o644)

	ad := platform.Adapters{Paths: p, Clock: fixedClock{}, Getenv: func(k string) string {
		if k == "HOME" {
			return home
		}
		return ""
	}}
	run := stubTmux{
		"list-keys -T prefix":     "bind-key -T prefix W display-popup -E '/x/bosun ui'\nbind-key -T prefix a run-shell -b '/x/bosun jump --queue'\n",
		"show-hooks -g":           "client-attached[42] run-shell -b \"/x/bosun status --catch-up\"\n",
		"show-options -gv prefix": "C-a\n",
	}
	env := install.Env{Runner: run, ClaudeBin: "/bin/claude", TmuxBin: "/bin/tmux", Home: home, TTY: true, Stdin: strings.NewReader("n\n")}
	env.Home = home
	o := Options{Home: home, Settings: p.ClaudeSettings(), TmuxConf: conf, Executable: home + "/.local/bin/bosun", Purge: true, PurgeConfig: true,
		Explicit: map[string]bool{}, Tilde: env.Tilde}
	iopt := install.Options{Settings: p.ClaudeSettings(), TmuxConf: conf}
	iopt.Config.Tmux.PopupKey, iopt.Config.Tmux.JumpKey = "W", "a"
	o.Apply = func(out io.Writer, purge []install.Step) int {
		return install.UninstallApply(ad, iopt, env, out, purge...)
	}
	var out, errw bytes.Buffer
	Run(p, o, &out, &errw)
	got := out.String()
	want, err := os.ReadFile("testdata/uninstall_plan.golden")
	if os.Getenv("BOSUN_UPDATE_GOLDEN") != "" {
		os.WriteFile("testdata/uninstall_plan.golden", []byte(got), 0o644)
		want = []byte(got)
	}
	if err != nil && os.Getenv("BOSUN_UPDATE_GOLDEN") == "" || string(want) != got {
		t.Errorf("differs (BOSUN_UPDATE_GOLDEN=1 rewrites it)\n--- got\n%s--- want\n%s", got, want)
	}
	for _, d := range []string{p.State, p.Config, p.Data} {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("%s deleted by a declined plan", d)
		}
	}
}
