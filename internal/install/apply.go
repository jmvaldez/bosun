package install

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jmvaldez/bosun/internal/atomicfile"
	"github.com/jmvaldez/bosun/internal/platform"
	"github.com/jmvaldez/bosun/internal/plugstate"
	"github.com/jmvaldez/bosun/internal/runner"
	"github.com/jmvaldez/bosun/internal/shell"
	"github.com/jmvaldez/bosun/internal/tmuxconf"
)

// D-26: with --apply, and only then, uninstall edits the user's settings.json
// and tmux.conf and runs `claude plugin`; setup (D-31) shares the status line
// and plugin steps. Plain install only prints.

const (
	pluginName     = "bosun@bosun"
	pluginBare     = "bosun"
	marketplace    = "bosun"
	claudeTimeout  = 90 * time.Second
	tmuxTimeout    = 5 * time.Second
	backupInfix    = ".bak-bosun-"
	backupStamp    = "20060102T150405"
	confirmRefused = "refusing to change your files: stdin is not a terminal (pass --yes to confirm)"
)

// Env is what the apply and setup steps need beyond the adapters.
type Env struct {
	Runner     runner.Runner
	ClaudeBin  string
	TmuxBin    string
	TmuxSocket string // from $TMUX; empty means the default socket
	// InTmux is true when $TMUX is set.
	InTmux bool
	Stdin  io.Reader
	TTY    bool
	Yes    bool
	Now    func() time.Time
	// Home shortens paths to ~/...; ShowDiff prints the full before and after
	// of each edit; Color bolds headings (a TTY with NO_COLOR unset).
	Home     string
	ShowDiff bool
	Color    bool
}

// tilde shortens a path under home to ~/....
func tilde(home, p string) string {
	if home != "" && (p == home || strings.HasPrefix(p, home+"/")) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

// Tilde is tilde for other packages' plan lines.
func (e Env) Tilde(p string) string { return tilde(e.Home, p) }

func (e Env) bold(s string) string {
	if e.Color {
		return "\x1b[1m" + s + "\x1b[0m"
	}
	return s
}

// Step is one change setup or uninstall --apply can make. Done steps change
// nothing. A step with a non-empty Refuse is skipped with that reason.
type Step struct {
	Name   string // short, for the "already done" line and failures
	Title  string // the numbered heading in the plan
	Detail []string
	Diff   []string // full before/after, shown with --show-diff
	Result string   // the line after a successful run
	Backup string   // what gets backed up, e.g. "settings.json"
	Done   bool
	Refuse string
	Do     func() ([]string, error)
}

func settingsPath(ad platform.Adapters, opt Options) string {
	if opt.Settings != "" {
		return opt.Settings
	}
	return ad.Paths.ClaudeSettings()
}

// marketplaceDir returns the directory holding .claude-plugin/marketplace.json:
// --repo, else a checkout above a search dir, else a package's share dir next
// to the binary, else /usr/share/bosun. "" when none has it.
func marketplaceDir(opt Options) string {
	if opt.Repo != "" {
		return opt.Repo
	}
	if r := findRepo(opt.SearchDirs); r != "" {
		return r
	}
	var cands []string
	for _, d := range opt.SearchDirs {
		cands = append(cands, filepath.Join(d, "..", "share", "bosun"))
	}
	cands = append(cands, "/usr/share/bosun")
	for _, c := range cands {
		if _, err := os.Stat(filepath.Join(c, ".claude-plugin", "marketplace.json")); err == nil {
			return filepath.Clean(c)
		}
	}
	return ""
}

// UninstallApply reverses the steps setup and the old install --apply made
// (the tmux.conf source-file line included), also unbinds what the generated
// conf bound in a running tmux, and runs extra (the purge steps) under the same
// confirmation.
func UninstallApply(ad platform.Adapters, opt Options, env Env, out io.Writer, extra ...Step) int {
	steps := []Step{statusLineRestoreStep(ad, opt, env), tmuxRemoveStep(ad, opt, env), tmuxBindingsStep(opt, env), pluginRemoveStep(ad, opt, env)}
	return runSteps(env, "bosun uninstall --apply", "Nothing to remove.", append(steps, extra...), out)
}

func runSteps(env Env, head, nothing string, steps []Step, out io.Writer) int {
	rc := 0
	fmt.Fprintln(out, env.bold(head))
	fmt.Fprintln(out)
	var pending []Step
	var done, skipped []string
	for _, s := range steps {
		switch {
		case s.Refuse != "":
			skipped = append(skipped, "! "+s.Name+": "+s.Refuse)
			rc = 1
		case s.Done:
			done = append(done, s.Name)
		default:
			pending = append(pending, s)
		}
	}
	if len(pending) == 0 {
		fmt.Fprintln(out, nothing)
		for _, l := range skipped {
			fmt.Fprintln(out, l)
		}
		return rc
	}
	plural := "s"
	if len(pending) == 1 {
		plural = ""
	}
	fmt.Fprintln(out, env.bold(fmt.Sprintf("bosun will make %d change%s:", len(pending), plural)))
	var backs []string
	for i, s := range pending {
		fmt.Fprintf(out, "\n  %d. %s\n", i+1, s.Title)
		for _, l := range s.Detail {
			fmt.Fprintf(out, "       %s\n", l)
		}
		if env.ShowDiff {
			for _, l := range s.Diff {
				fmt.Fprintf(out, "       %s\n", l)
			}
		}
		if s.Backup != "" {
			backs = append(backs, s.Backup)
		}
	}
	fmt.Fprintln(out)
	if len(backs) > 0 {
		fmt.Fprintf(out, "Backups of %s are saved before writing.\n", joinAnd(backs))
	}
	for _, l := range skipped {
		fmt.Fprintln(out, l)
	}
	if len(done) > 0 {
		fmt.Fprintf(out, "Already done: %s.\n", strings.Join(done, ", "))
	}
	if !env.ShowDiff && hasDiff(pending) {
		fmt.Fprintln(out, "(--show-diff prints the full before and after.)")
	}
	fmt.Fprintln(out)
	if !env.Yes {
		if !env.TTY {
			fmt.Fprintln(out, confirmRefused)
			return 2
		}
		fmt.Fprint(out, "Apply? [y/N] ")
		line, _ := bufio.NewReader(env.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			fmt.Fprintln(out, "Nothing changed.")
			return 1
		}
	}
	if env.Yes {
		fmt.Fprintln(out)
	}
	var backups []string
	for _, s := range pending {
		lines, err := s.Do()
		var notes []string
		for _, l := range lines {
			if b, ok := strings.CutPrefix(l, "backup "); ok {
				backups = append(backups, b)
			} else {
				notes = append(notes, l)
			}
		}
		if err != nil {
			fmt.Fprintf(out, "! %s: %v\n", s.Name, err)
			rc = 1
			continue
		}
		fmt.Fprintf(out, "\u2713 %s\n", s.Result)
		for _, n := range notes {
			fmt.Fprintf(out, "    %s\n", n)
		}
	}
	for _, b := range backups {
		fmt.Fprintf(out, "backup: %s\n", b)
	}
	return rc
}

func hasDiff(steps []Step) bool {
	for _, s := range steps {
		if len(s.Diff) > 0 {
			return true
		}
	}
	return false
}

func joinAnd(a []string) string {
	if len(a) < 2 {
		return strings.Join(a, "")
	}
	return strings.Join(a[:len(a)-1], ", ") + " and " + a[len(a)-1]
}

func (e Env) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// backup copies path to path.bak-bosun-<timestamp> before an edit, never
// overwriting an earlier backup, and returns the new path.
func backup(path string, now time.Time) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	base := path + backupInfix + now.Format(backupStamp)
	dst := base
	for i := 1; ; i++ {
		if _, err := os.Lstat(dst); errors.Is(err, fs.ErrNotExist) {
			break
		}
		dst = fmt.Sprintf("%s-%d", base, i)
	}
	return dst, atomicfile.Write(dst, b, fi.Mode().Perm())
}

// statusCommand is statusLine.command in a settings document.
func statusCommand(b []byte) string {
	var s struct {
		StatusLine statusLine `json:"statusLine"`
	}
	_ = json.Unmarshal(b, &s)
	return s.StatusLine.Command
}

// freshSettings re-reads settings.json for a step about to write it. Another
// process (claude plugin install) may have changed other keys since the plan,
// and those must survive; but if the status line is no longer what the plan
// described, nothing is written.
func freshSettings(path, plannedCommand string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err == nil && !json.Valid(b) {
		return nil, fmt.Errorf("%s is not valid JSON; change nothing", filepath.Base(path))
	}
	if statusCommand(b) != plannedCommand {
		return nil, fmt.Errorf("%s changed since the plan; run again", filepath.Base(path))
	}
	return b, nil
}

// writeKeepingMode atomically replaces path with data, keeping its permissions.
func writeKeepingMode(path string, data []byte) error {
	perm := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		perm = fi.Mode().Perm()
	}
	return atomicfile.Write(path, data, perm)
}

// ---- plugin ----

func enabledCount(settings []byte) int {
	var s struct {
		EnabledPlugins json.RawMessage `json:"enabledPlugins"`
	}
	if json.Unmarshal(settings, &s) != nil || len(s.EnabledPlugins) == 0 {
		return 0
	}
	n := 0
	var obj map[string]bool
	if json.Unmarshal(s.EnabledPlugins, &obj) == nil {
		for k, on := range obj {
			if on && k == pluginName {
				n++
			}
		}
		return n
	}
	var arr []string
	if json.Unmarshal(s.EnabledPlugins, &arr) == nil {
		for _, k := range arr {
			if k == pluginName {
				n++
			}
		}
	}
	return n
}

func pluginEnabled(ad platform.Adapters, opt Options) int {
	b, err := os.ReadFile(settingsPath(ad, opt))
	if err != nil {
		return 0
	}
	return enabledCount(b)
}

func claudeDir(ad platform.Adapters, opt Options) string { return filepath.Dir(settingsPath(ad, opt)) }

// pluginInstalled is true when Claude's own record lists the plugin or
// settings.json has an enable entry for it (the record may be unreadable).
func pluginInstalled(ad platform.Adapters, opt Options) bool {
	return plugstate.Installed(claudeDir(ad, opt), pluginName) || pluginEnabled(ad, opt) > 0
}

func pluginInstallStep(ad platform.Adapters, opt Options, env Env) Step {
	s := Step{Name: "plugin", Title: "Install the Claude Code plugin", Result: "Plugin installed (" + pluginName + ")"}
	enabled, installed := pluginEnabled(ad, opt), pluginInstalled(ad, opt)
	if enabled == 1 {
		s.Done = true
		return s
	}
	if env.ClaudeBin == "" {
		s.Refuse = "`claude` is not on PATH"
		return s
	}
	if installed && enabled == 0 {
		// Installed but disabled: enabling is the whole fix.
		s.Title, s.Result = "Enable the Claude Code plugin (installed but disabled)", "Plugin enabled ("+pluginName+")"
		s.Detail = []string{"claude plugin enable " + pluginName}
		s.Do = func() ([]string, error) {
			if _, err := env.Runner.Run(claudeTimeout, env.ClaudeBin, "plugin", "enable", pluginName); err != nil {
				return nil, fmt.Errorf("plugin enable: %w", err)
			}
			return nil, nil
		}
		return s
	}
	if installed {
		s.Done = true // enabled more than once is doctor's to report
		return s
	}
	dir := marketplaceDir(opt)
	if dir == "" {
		s.Refuse = "no .claude-plugin/marketplace.json found (pass --repo)"
		return s
	}
	shown := env.Tilde(dir)
	if shown == dir || !shell.IsWord(strings.TrimPrefix(shown, "~")) {
		shown = shell.QuoteWord(dir)
	}
	// One command: claude adds the marketplace from the path itself (D-31).
	s.Detail = append(s.Detail, "claude plugin install "+pluginBare+" --marketplace "+shown)
	s.Do = func() ([]string, error) {
		if _, err := env.Runner.Run(claudeTimeout, env.ClaudeBin, "plugin", "install", pluginBare, "--marketplace", dir); err != nil {
			return nil, fmt.Errorf("plugin install: %w", err)
		}
		return nil, nil
	}
	return s
}

func pluginRemoveStep(ad platform.Adapters, opt Options, env Env) Step {
	s := Step{Name: "plugin", Title: "Uninstall the Claude Code plugin", Result: "Plugin removed"}
	installed := pluginInstalled(ad, opt)
	market := plugstate.MarketplaceAdded(claudeDir(ad, opt), marketplace)
	if !installed && !market {
		s.Done = true
		return s
	}
	if env.ClaudeBin == "" {
		s.Refuse = "`claude` is not on PATH"
		return s
	}
	if installed {
		s.Detail = append(s.Detail, "claude plugin uninstall "+pluginName)
	}
	if market {
		s.Detail = append(s.Detail, "claude plugin marketplace remove "+marketplace)
	}
	if !installed {
		s.Title = "Remove the bosun plugin marketplace"
	}
	s.Do = func() ([]string, error) {
		var first error
		if installed {
			if _, err := env.Runner.Run(claudeTimeout, env.ClaudeBin, "plugin", "uninstall", pluginName); err != nil {
				first = fmt.Errorf("plugin uninstall: %w", err)
			}
		}
		if market {
			if _, err := env.Runner.Run(claudeTimeout, env.ClaudeBin, "plugin", "marketplace", "remove", marketplace); err != nil && first == nil {
				first = fmt.Errorf("marketplace remove: %w", err)
			}
		}
		return nil, first
	}
	return s
}

// ---- status line ----

func statusLineStep(ad platform.Adapters, opt Options, env Env) Step {
	s := Step{Name: "status line", Backup: "settings.json", Result: "Status line wrapped"}
	path := settingsPath(ad, opt)
	s.Title = "Wrap your status line in " + env.Tilde(path)
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		b = nil
	case err != nil:
		s.Refuse = err.Error()
		return s
	case !json.Valid(b):
		s.Refuse = path + " is not valid JSON; change nothing (fix it, then re-run)"
		return s
	}
	cur, _ := currentStatusLine(path)
	if b != nil && alreadyWrapped(cur.Command) {
		s.Done = true
		return s
	}
	wrapped := WrapCommand(cur.Command)
	_, err = setStatusLineCommand(b, wrapped) // validates only; Do edits a fresh read
	if err != nil {
		s.Refuse = path + ": " + err.Error()
		return s
	}
	if cur.Command == "" {
		s.Detail = []string{"No status line is set; bosun adds one that records spend", "and shows nothing."}
		s.Diff = []string{"after:", "  " + wrapped}
	} else {
		s.Detail = []string{"Your command keeps running as-is, inside `bosun statusline`.", "If bosun is ever removed, the original runs unchanged."}
		s.Diff = []string{"before:", "  " + cur.Command, "after:", "  " + wrapped}
	}
	s.Do = func() ([]string, error) {
		var lines []string
		fresh, err := freshSettings(path, cur.Command)
		if err != nil {
			return nil, err
		}
		out, err := setStatusLineCommand(fresh, wrapped)
		if err != nil {
			return nil, err
		}
		if fresh != nil {
			bak, err := backup(path, env.now())
			if err != nil {
				return nil, fmt.Errorf("backup: %w", err)
			}
			lines = append(lines, "backup "+env.Tilde(bak))
		}
		if err := writeKeepingMode(path, out); err != nil {
			return lines, err
		}
		return lines, nil
	}
	return s
}

func statusLineRestoreStep(ad platform.Adapters, opt Options, env Env) Step {
	s := Step{Name: "status line", Backup: "settings.json", Result: "Status line restored"}
	path := settingsPath(ad, opt)
	s.Title = "Restore your original status line in " + env.Tilde(path)
	b, err := os.ReadFile(path)
	switch {
	case err != nil:
		s.Done = true
		return s
	case !json.Valid(b):
		s.Refuse = path + " is not valid JSON; change nothing"
		return s
	}
	cur, _ := currentStatusLine(path)
	orig, ok := UnwrapCommand(cur.Command)
	if !ok {
		s.Done = true
		return s
	}
	if orig == "" {
		_, err = removeStatusLine(b)
	} else {
		_, err = setStatusLineCommand(b, orig)
	}
	if err != nil {
		s.Refuse = path + ": " + err.Error()
		return s
	}
	if orig == "" {
		s.Title = "Remove the status line bosun added to " + env.Tilde(path)
		s.Result = "Status line removed"
		s.Diff = []string{"before:", "  " + cur.Command, "after: (no statusLine)"}
	} else {
		s.Detail = []string{"Removes the bosun wrapper; your original command is put back."}
		s.Diff = []string{"before:", "  " + cur.Command, "after:", "  " + orig}
	}
	s.Do = func() ([]string, error) {
		fresh, err := freshSettings(path, cur.Command)
		if err != nil {
			return nil, err
		}
		var out []byte
		if orig == "" {
			out, err = removeStatusLine(fresh)
		} else {
			out, err = setStatusLineCommand(fresh, orig)
		}
		if err != nil {
			return nil, err
		}
		bak, err := backup(path, env.now())
		if err != nil {
			return nil, fmt.Errorf("backup: %w", err)
		}
		if err := writeKeepingMode(path, out); err != nil {
			return []string{"backup " + env.Tilde(bak)}, err
		}
		return []string{"backup " + env.Tilde(bak)}, nil
	}
	return s
}

// ---- tmux ----

// tmuxConfFile picks the file tmux reads: opt.TmuxConf when set, else
// ~/.tmux.conf, then $XDG_CONFIG_HOME/tmux/tmux.conf, then
// ~/.config/tmux/tmux.conf. When none exists it names ~/.tmux.conf, which the
// step creates; with HOME unset it names nothing.
func tmuxConfFile(ad platform.Adapters, opt Options) (path string, exists bool) {
	if opt.TmuxConf != "" {
		_, err := os.Stat(opt.TmuxConf)
		return opt.TmuxConf, err == nil
	}
	getenv := ad.Getenv
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	home := getenv("HOME")
	if home == "" {
		return "", false
	}
	cands := []string{filepath.Join(home, ".tmux.conf")}
	if x := getenv("XDG_CONFIG_HOME"); filepath.IsAbs(x) {
		cands = append(cands, filepath.Join(x, "tmux", "tmux.conf"))
	}
	cands = append(cands, filepath.Join(home, ".config", "tmux", "tmux.conf"))
	for _, c := range cands {
		if _, err := os.Stat(c); err == nil {
			return c, true
		}
	}
	return cands[0], false
}

func sourceLine(ad platform.Adapters) string { return "source-file -q " + ad.Paths.TmuxConf() }

func tmuxRemoveStep(ad platform.Adapters, opt Options, env Env) Step {
	s := Step{Name: "tmux config", Result: "tmux config line removed"}
	path, exists := tmuxConfFile(ad, opt)
	if !exists {
		s.Done = true
		return s
	}
	b, err := os.ReadFile(path)
	if err != nil {
		s.Refuse = err.Error()
		return s
	}
	line := sourceLine(ad)
	_, n := removeSourceLines(string(b), line)
	if n == 0 {
		s.Done = true
		return s
	}
	s.Title = "Remove bosun's tmux config line from " + env.Tilde(path)
	s.Backup = filepath.Base(path)
	s.Detail = []string{"- source-file -q " + env.Tilde(ad.Paths.TmuxConf())}
	s.Do = func() ([]string, error) {
		fb, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		newText, n := removeSourceLines(string(fb), line)
		if n == 0 {
			return nil, nil
		}
		bak, err := backup(path, env.now())
		if err != nil {
			return nil, fmt.Errorf("backup: %w", err)
		}
		if err := writeKeepingMode(path, []byte(newText)); err != nil {
			return []string{"backup " + env.Tilde(bak)}, err
		}
		return append([]string{"backup " + env.Tilde(bak)}, reloadTmux(env, path)...), nil
	}
	return s
}

// removeSourceLines drops each line that is exactly the bosun source-file line.
func removeSourceLines(text, line string) (string, int) {
	n := 0
	var keep []string
	for _, l := range strings.SplitAfter(text, "\n") {
		if strings.TrimSpace(l) == line {
			n++
			continue
		}
		keep = append(keep, l)
	}
	return strings.Join(keep, ""), n
}

// reloadTmux sources the conf in the server the caller runs under, or the
// default one if it answers. It never starts a server.
func reloadTmux(env Env, conf string) []string {
	_, note := sourceConf(env, conf)
	return []string{note}
}

// sourceConf is reloadTmux with the outcome: loaded is true when a running
// server sourced the conf, and note says what happened either way.
func sourceConf(env Env, conf string) (loaded bool, note string) {
	if env.TmuxBin == "" || env.Runner == nil {
		return false, "tmux not found; run: tmux source-file " + env.Tilde(conf)
	}
	if !env.InTmux {
		if _, err := env.Runner.Run(tmuxTimeout, env.TmuxBin, tmuxArgs(env, "list-sessions")...); err != nil {
			return false, noServerNote
		}
	}
	if _, err := env.Runner.Run(tmuxTimeout, env.TmuxBin, tmuxArgs(env, "source-file", conf)...); err != nil {
		return false, fmt.Sprintf("tmux source-file failed (%v); run: tmux source-file %s", err, env.Tilde(conf))
	}
	return true, "reloaded in the running tmux"
}

// ---- live tmux server: what re-sourcing cannot remove ----

// tmuxArgs prefixes the caller's socket, if any.
func tmuxArgs(env Env, args ...string) []string {
	if env.TmuxSocket != "" {
		return append([]string{"-S", env.TmuxSocket}, args...)
	}
	return args
}

// bosunKeyCommand reports whether a `list-keys` line is a prefix-table binding
// of key whose command is a bosun one. list-keys backslash-escapes keys tmux
// would otherwise parse (it prints the spend key $ as \$).
func bosunKeyCommand(line, key string) bool {
	f := strings.Fields(line)
	for i := 0; i+2 < len(f); i++ {
		if f[i] == "-T" {
			return f[i+1] == "prefix" && (f[i+2] == key || f[i+2] == `\`+key) && (strings.Contains(line, "bosun ui") || strings.Contains(line, "bosun jump"))
		}
	}
	return false
}

// tmuxBindingsStep unbinds the keys the generated conf bound and unsets its
// client-attached hook, on the server that answers. A key the user rebound to
// something else, or a hook index holding another command, is left alone.
func tmuxBindingsStep(opt Options, env Env) Step {
	s := Step{Name: "tmux keys"}
	if env.TmuxBin == "" || env.Runner == nil {
		s.Done = true
		return s
	}
	keys, err := env.Runner.Run(tmuxTimeout, env.TmuxBin, tmuxArgs(env, "list-keys", "-T", "prefix")...)
	if err != nil {
		s.Done = true
		return s
	}
	pfx := "prefix"
	if b, err := env.Runner.Run(tmuxTimeout, env.TmuxBin, tmuxArgs(env, "show-options", "-gv", "prefix")...); err == nil && strings.TrimSpace(string(b)) != "" {
		pfx = strings.TrimSpace(string(b))
	}
	var unbind, named []string
	var plan []string
	for _, key := range []string{opt.Config.Tmux.PopupKey, opt.Config.Tmux.JumpKey, opt.Config.Tmux.SpendKey} {
		for _, l := range strings.Split(string(keys), "\n") {
			if key != "" && bosunKeyCommand(l, key) {
				unbind = append(unbind, key)
				named = append(named, pfx+" "+key)
				plan = append(plan, "unbind-key -T prefix "+key)
				break
			}
		}
	}
	hook := false
	if hooks, err := env.Runner.Run(tmuxTimeout, env.TmuxBin, tmuxArgs(env, "show-hooks", "-g")...); err == nil {
		for _, l := range strings.Split(string(hooks), "\n") {
			if strings.HasPrefix(l, tmuxconf.AttachHook) && strings.Contains(l, "bosun status") {
				hook = true
				named = append(named, "bosun's attach hook")
				plan = append(plan, "set-hook -gu "+tmuxconf.AttachHook)
			}
		}
	}
	if len(plan) == 0 {
		s.Done = true
		return s
	}
	s.Title = "Unbind " + joinAnd(named) + " in the running tmux"
	s.Result = "tmux keys and hook removed"
	s.Diff = plan
	s.Do = func() ([]string, error) {
		var lines []string
		for _, key := range unbind {
			if _, err := env.Runner.Run(tmuxTimeout, env.TmuxBin, tmuxArgs(env, "unbind-key", "-T", "prefix", key)...); err != nil {
				return lines, fmt.Errorf("unbind %s: %w", key, err)
			}
		}
		if hook {
			if _, err := env.Runner.Run(tmuxTimeout, env.TmuxBin, tmuxArgs(env, "set-hook", "-gu", tmuxconf.AttachHook)...); err != nil {
				return lines, fmt.Errorf("unset hook: %w", err)
			}
		}
		return lines, nil
	}
	return s
}
