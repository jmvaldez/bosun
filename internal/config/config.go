// Package config loads config.toml. Every key is optional; a bad value falls
// back to its default and is reported, and unknown keys are reported too
// (LESSONS L-43). Hooks never read config.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Config is the effective configuration.
type Config struct {
	Rules   Rules
	Pace    Pace
	Spend   Spend
	Task    Task
	Tmux    Tmux
	Claude  Claude
	Notify  Notify
	Focus   Focus
	UI      UI
	Bar     Bar
	Privacy Privacy
	Run     Run
	Handoff Handoff
}

type Rules struct {
	SubagentRuntime time.Duration
	Quiet           time.Duration
	SubagentCost    float64
	SessionCost     float64
	DailyBudget     float64
}

type Pace struct {
	Enabled    bool
	Amber      float64
	Alert      float64
	MinOver    float64
	MinDays    int
	WindowDays int
}

type Spend struct{ Day string }

type Task struct {
	KeyRegex string
	Prefixes []string
}

type Tmux struct{ PopupKey, JumpKey, SpendKey string }

// Bar is the tmux status segment: "labeled" colored blocks or "compact".
type Bar struct{ Style string }

// Privacy.ShowText gates showing prompt, question and command text read from
// the transcript at render or notify time (D-27). bosun never stores it.
type Privacy struct{ ShowText bool }

type Claude struct{ Bin, Launcher string }

type Notify struct {
	Backend      string
	DoneMinTurn  time.Duration
	Sound        string
	MaxPerMinute int
	ActivateApp  string
}

type Focus struct{ WSL string }

type UI struct {
	DiffCmd       string
	Peek          bool
	InterruptKeys string
	// Glyphs is "unicode" or "ascii"; tmuxconf.WinMark holds the glyph sets.
	Glyphs string
}

type Run struct{ WorktreeDir string }

// Handoff configures the context handoff. SuggestAt is the context fill, as a
// share of the window, at which a done or idle session is offered a handoff;
// 0 turns the suggestion off. Prompt overrides the embedded template inline,
// Dir is where the doc goes outside a git repo, and Deadline bounds the wait
// for the doc. Auto is reserved for the evaluator starting the handoff itself.
type Handoff struct {
	SuggestAt   float64
	Prompt, Dir string
	Deadline    time.Duration
	Auto        bool
}

// DefaultKeyRegex is the issue key pattern when task.key_regex is unset.
const DefaultKeyRegex = `[A-Z][A-Z0-9]+-\d+`

// Default mirrors the key table in kit/SPEC.md section 8, plus the keys D-27
// added.
func Default() Config {
	return Config{
		Rules: Rules{
			SubagentRuntime: 10 * time.Minute,
			Quiet:           5 * time.Minute,
			SubagentCost:    2.00,
			SessionCost:     10.00,
		},
		Pace:   Pace{Enabled: true, Amber: 1.25, Alert: 1.5, MinOver: 20.00, MinDays: 7, WindowDays: 30},
		Spend:  Spend{Day: "local"},
		Task:   Task{KeyRegex: DefaultKeyRegex, Prefixes: []string{}},
		Tmux:   Tmux{PopupKey: "W", JumpKey: "a", SpendKey: "$"},
		Claude: Claude{Launcher: "claude"},
		Notify: Notify{
			Backend:      "auto",
			DoneMinTurn:  20 * time.Second,
			Sound:        "default",
			MaxPerMinute: 6,
			ActivateApp:  "auto",
		},
		Focus:   Focus{WSL: "tmux"},
		UI:      UI{DiffCmd: "git diff", Peek: true, InterruptKeys: "Escape", Glyphs: "unicode"},
		Bar:     Bar{Style: "labeled"},
		Privacy: Privacy{ShowText: true},
		Handoff: Handoff{SuggestAt: 0.70, Deadline: 5 * time.Minute},
	}
}

// Result is what Load found besides the config itself.
type Result struct {
	// Unknown lists dotted keys that no field consumes.
	Unknown []string
	// Problems lists keys whose value was invalid and fell back to the default.
	Problems []string
}

// Load reads path. A missing file yields the defaults and no error. A file that
// does not parse yields the defaults and an error.
func Load(path string) (Config, Result, error) {
	c := Default()
	if path == "" {
		return c, Result{}, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, Result{}, nil
	}
	if err != nil {
		return c, Result{}, err
	}
	return Parse(data)
}

// Parse decodes TOML text. The document is decoded generically and walked
// against the field table so each bad value can fall back on its own; decoding
// straight into the struct would abort at the first type mismatch.
func Parse(data []byte) (Config, Result, error) {
	c := Default()
	var raw map[string]any
	if _, err := toml.Decode(string(data), &raw); err != nil {
		return Default(), Result{}, err
	}
	known := map[string]*entry{}
	for _, e := range c.entries() {
		known[e.key] = e
	}
	var res Result
	var walk func(prefix string, m map[string]any)
	walk = func(prefix string, m map[string]any) {
		for k, v := range m {
			key := prefix + k
			if sub, ok := v.(map[string]any); ok {
				walk(key+".", sub)
				continue
			}
			e, ok := known[key]
			if !ok {
				res.Unknown = append(res.Unknown, key)
				continue
			}
			if err := e.set(v); err != nil {
				res.Problems = append(res.Problems, fmt.Sprintf("%s: %v (using default %s)", key, err, e.format()))
			}
		}
	}
	walk("", raw)
	// The set functions write straight into c's fields via pointers captured
	// from c.entries(), so c already holds the result.
	sort.Strings(res.Unknown)
	sort.Strings(res.Problems)
	return c, res, nil
}

// entry binds one dotted key to a field.
type entry struct {
	key    string
	set    func(any) error
	format func() string
}

func (c *Config) entries() []*entry {
	return []*entry{
		dur("rules.subagent_runtime", &c.Rules.SubagentRuntime),
		dur("rules.quiet", &c.Rules.Quiet),
		money("rules.subagent_cost", &c.Rules.SubagentCost),
		money("rules.session_cost", &c.Rules.SessionCost),
		money("rules.daily_budget", &c.Rules.DailyBudget),
		boolean("pace.enabled", &c.Pace.Enabled),
		ratio("pace.amber", &c.Pace.Amber),
		ratio("pace.alert", &c.Pace.Alert),
		money("pace.min_over", &c.Pace.MinOver),
		count("pace.min_days", &c.Pace.MinDays),
		count("pace.window_days", &c.Pace.WindowDays),
		str("spend.day", &c.Spend.Day, "local", "utc"),
		regex("task.key_regex", &c.Task.KeyRegex),
		strs("task.prefixes", &c.Task.Prefixes),
		str("tmux.popup_key", &c.Tmux.PopupKey),
		str("tmux.jump_key", &c.Tmux.JumpKey),
		str("tmux.spend_key", &c.Tmux.SpendKey),
		str("claude.bin", &c.Claude.Bin),
		str("claude.launcher", &c.Claude.Launcher),
		str("notify.backend", &c.Notify.Backend, "auto", "terminal-notifier", "osascript", "notify-send", "wsl", "off"),
		dur("notify.done_min_turn", &c.Notify.DoneMinTurn),
		str("notify.sound", &c.Notify.Sound),
		count("notify.max_per_minute", &c.Notify.MaxPerMinute),
		str("notify.activate_app", &c.Notify.ActivateApp),
		str("focus.wsl", &c.Focus.WSL, "tmux", "never"),
		str("ui.diff_cmd", &c.UI.DiffCmd),
		boolean("ui.peek", &c.UI.Peek),
		str("ui.interrupt_keys", &c.UI.InterruptKeys),
		str("ui.glyphs", &c.UI.Glyphs, "unicode", "ascii"),
		str("bar.style", &c.Bar.Style, "labeled", "compact"),
		boolean("privacy.show_text", &c.Privacy.ShowText),
		str("run.worktree_dir", &c.Run.WorktreeDir),
		share("handoff.suggest_at", &c.Handoff.SuggestAt),
		str("handoff.prompt", &c.Handoff.Prompt),
		str("handoff.dir", &c.Handoff.Dir),
		dur("handoff.deadline", &c.Handoff.Deadline),
		boolean("handoff.auto", &c.Handoff.Auto),
	}
}

// Keys lists every known dotted key; the key-count test pins it.
func Keys() []string {
	c := Default()
	var ks []string
	for _, e := range c.entries() {
		ks = append(ks, e.key)
	}
	return ks
}

// Format renders the effective config as TOML, one section per prefix, with
// leading comment lines for anything Load reported.
func Format(c Config, res Result) string {
	var b strings.Builder
	for _, u := range res.Unknown {
		fmt.Fprintf(&b, "# warning: unknown key %s\n", u)
	}
	for _, p := range res.Problems {
		fmt.Fprintf(&b, "# warning: %s\n", p)
	}
	if len(res.Unknown)+len(res.Problems) > 0 {
		b.WriteByte('\n')
	}
	section := ""
	for _, e := range c.entries() {
		sec, name, _ := strings.Cut(e.key, ".")
		if sec != section {
			if section != "" {
				b.WriteByte('\n')
			}
			fmt.Fprintf(&b, "[%s]\n", sec)
			section = sec
		}
		fmt.Fprintf(&b, "%s = %s\n", name, e.format())
	}
	return b.String()
}

func dur(key string, dst *time.Duration) *entry {
	return &entry{
		key: key,
		set: func(v any) error {
			s, ok := v.(string)
			if !ok {
				return fmt.Errorf("want a duration string like \"5m\"")
			}
			d, err := time.ParseDuration(s)
			if err != nil || d <= 0 {
				return fmt.Errorf("invalid duration %q", s)
			}
			*dst = d
			return nil
		},
		format: func() string { return strconv.Quote(dst.String()) },
	}
}

func num(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int64:
		return float64(n), true
	}
	return 0, false
}

func money(key string, dst *float64) *entry {
	return &entry{
		key: key,
		set: func(v any) error {
			n, ok := num(v)
			if !ok || n < 0 {
				return fmt.Errorf("want a number >= 0")
			}
			*dst = n
			return nil
		},
		format: func() string { return strconv.FormatFloat(*dst, 'f', 2, 64) },
	}
}

// ratio is a positive multiplier such as 1.25.
func ratio(key string, dst *float64) *entry {
	e := money(key, dst)
	set := e.set
	e.set = func(v any) error {
		if n, ok := num(v); ok && n <= 0 {
			return fmt.Errorf("want a number > 0")
		}
		return set(v)
	}
	e.format = func() string { return strconv.FormatFloat(*dst, 'f', -1, 64) }
	return e
}

// share is a fraction of a whole between 0 and 1; 0 turns the rule off.
func share(key string, dst *float64) *entry {
	e := money(key, dst)
	set := e.set
	e.set = func(v any) error {
		if n, ok := num(v); ok && n > 1 {
			return fmt.Errorf("want a number between 0 and 1")
		}
		return set(v)
	}
	e.format = func() string { return strconv.FormatFloat(*dst, 'f', -1, 64) }
	return e
}

func count(key string, dst *int) *entry {
	return &entry{
		key: key,
		set: func(v any) error {
			n, ok := v.(int64)
			if !ok || n < 1 {
				return fmt.Errorf("want an integer >= 1")
			}
			*dst = int(n)
			return nil
		},
		format: func() string { return strconv.Itoa(*dst) },
	}
}

func boolean(key string, dst *bool) *entry {
	return &entry{
		key: key,
		set: func(v any) error {
			b, ok := v.(bool)
			if !ok {
				return fmt.Errorf("want true or false")
			}
			*dst = b
			return nil
		},
		format: func() string { return strconv.FormatBool(*dst) },
	}
}

func str(key string, dst *string, allowed ...string) *entry {
	return &entry{
		key: key,
		set: func(v any) error {
			s, ok := v.(string)
			if !ok {
				return fmt.Errorf("want a string")
			}
			if len(allowed) > 0 && !contains(allowed, s) {
				return fmt.Errorf("want one of %s", strings.Join(allowed, ", "))
			}
			*dst = s
			return nil
		},
		format: func() string { return strconv.Quote(*dst) },
	}
}

func strs(key string, dst *[]string) *entry {
	return &entry{
		key: key,
		set: func(v any) error {
			arr, ok := v.([]any)
			if !ok {
				return fmt.Errorf("want an array of strings")
			}
			out := make([]string, 0, len(arr))
			for _, x := range arr {
				s, ok := x.(string)
				if !ok {
					return fmt.Errorf("want an array of strings")
				}
				out = append(out, s)
			}
			*dst = out
			return nil
		},
		format: func() string {
			q := make([]string, len(*dst))
			for i, s := range *dst {
				q[i] = strconv.Quote(s)
			}
			return "[" + strings.Join(q, ", ") + "]"
		},
	}
}

func regex(key string, dst *string) *entry {
	e := str(key, dst)
	set := e.set
	e.set = func(v any) error {
		if s, ok := v.(string); ok {
			if _, err := regexp.Compile(s); err != nil {
				return fmt.Errorf("invalid regexp: %v", err)
			}
		}
		return set(v)
	}
	return e
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
