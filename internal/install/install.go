// Package install implements `bosun install`. It prints every edit the user
// must make themselves and writes only bosun-owned files: a commented
// config.toml (if absent), bosun's own tmux.conf, and identity.json. `bosun
// setup` (D-31) is the route that makes the edits; see setup.go.
package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jmvaldez/bosun/internal/atomicfile"
	"github.com/jmvaldez/bosun/internal/config"
	"github.com/jmvaldez/bosun/internal/platform"
	"github.com/jmvaldez/bosun/internal/plugstate"
	"github.com/jmvaldez/bosun/internal/refresh"
	"github.com/jmvaldez/bosun/internal/tmuxconf"
)

// Options carries the flags and the paths main discovered.
type Options struct {
	// Repo is the bosun checkout holding .claude-plugin/marketplace.json. Empty
	// means search upward from SearchDirs.
	Repo       string
	SearchDirs []string
	// Settings overrides the settings.json to read (tests, odd setups).
	Settings string
	// Bosun is the absolute path written into the tmux conf.
	Bosun string
	// Version stamps the generated conf header.
	Version string
	// TmuxConf is the user's tmux.conf, read-only; empty means search the
	// standard locations under home.
	TmuxConf string
	// Config supplies the key bindings and glyph set for the generated tmux conf.
	Config config.Config
	// ConfChanged is set when bosun's own tmux.conf was rewritten with new
	// content (an upgrade); setup then reloads it.
	ConfChanged bool
}

// Run returns the exit code. Failures to write bosun's own files are reported
// and exit non-zero; failures to read the user's files only change the advice.
func Run(ad platform.Adapters, opt Options, out io.Writer) int {
	code := 0
	lines := writeOwnedFiles(ad, &opt, &code)
	fmt.Fprintln(out, "bosun install")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Files bosun owns")
	for _, line := range lines {
		fmt.Fprintln(out, "  "+line)
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "Edits you make (bosun never touches these files)")
	fmt.Fprintln(out)
	printBinary(out)
	printPlugin(out, opt)
	printStatusLine(ad, opt, out)
	printTmux(ad, opt, out)
	return code
}

func writeOwnedFiles(ad platform.Adapters, opt *Options, code *int) []string {
	var lines []string
	fail := func(format string, a ...any) {
		lines = append(lines, "FAILED  "+fmt.Sprintf(format, a...))
		*code = 1
	}

	cfgPath := ad.Paths.ConfigFile()
	switch {
	case cfgPath == "":
		fail("config.toml: no config dir (HOME unset?)")
	default:
		if _, err := os.Stat(cfgPath); err == nil {
			lines = append(lines, "kept    "+cfgPath+" (already exists)")
		} else if errors.Is(err, fs.ErrNotExist) {
			if err := atomicfile.Write(cfgPath, []byte(configTemplate()), 0o600); err != nil {
				fail("%s: %v", cfgPath, err)
			} else {
				lines = append(lines, "wrote   "+cfgPath)
			}
		} else {
			fail("%s: %v", cfgPath, err)
		}
	}

	if line, ok := writeTmuxConf(ad, opt); ok {
		lines = append(lines, line)
	} else {
		fail("%s", line)
	}

	idPath := ad.Paths.Identity()
	switch {
	case idPath == "":
		fail("identity.json: no state dir (HOME unset?)")
	case ad.Identity == nil:
		lines = append(lines, "skipped identity.json (no identity adapter)")
	default:
		key, err := ad.Identity.Account()
		if err != nil {
			// Not fatal: the detached refresher retries on its next run.
			lines = append(lines, fmt.Sprintf("skipped identity.json (%v); `bosun refresh` retries it", err))
		} else if err := refresh.WriteIdentity(idPath, key, ad.Clock.Now()); err != nil {
			fail("%s: %v", idPath, err)
		} else {
			lines = append(lines, "wrote   "+idPath)
		}
	}
	return lines
}

// writeTmuxConf regenerates bosun's own tmux.conf. ok is false on failure, and
// line then says why; a skipped write (bosun's path unknown) is ok.
func writeTmuxConf(ad platform.Adapters, opt *Options) (line string, ok bool) {
	confPath := ad.Paths.TmuxConf()
	switch {
	case confPath == "":
		return "tmux.conf: no data dir (HOME unset?)", false
	case opt.Bosun == "":
		return "skipped tmux.conf (bosun path unknown)", true
	}
	text, err := tmuxconf.Generate(tmuxconf.Options{
		Version: opt.Version, Bosun: opt.Bosun, JumpKey: opt.Config.Tmux.JumpKey, PopupKey: opt.Config.Tmux.PopupKey,
		SpendKey: opt.Config.Tmux.SpendKey, ASCII: opt.Config.UI.Glyphs == "ascii",
	})
	if err == nil {
		// A changed conf (a new binding, a new bar) must be re-sourced even when
		// the user's source-file line is already in place.
		if old, rerr := os.ReadFile(confPath); rerr == nil && string(old) != text {
			opt.ConfChanged = true
		}
		err = atomicfile.Write(confPath, []byte(text), 0o644)
	}
	if err != nil {
		return fmt.Sprintf("%s: %v", confPath, err), false
	}
	return "wrote   " + confPath, true
}

// configTemplate is the effective defaults with every line commented out, so a
// fresh file changes nothing until the user opts in.
func configTemplate() string {
	var b strings.Builder
	b.WriteString("# bosun config. Every key is optional; uncomment a line to override its default.\n")
	b.WriteString("# `bosun config` prints the effective values and any unknown keys.\n\n")
	for _, line := range strings.Split(strings.TrimRight(config.Format(config.Default(), config.Result{}), "\n"), "\n") {
		if line == "" || strings.HasPrefix(line, "[") {
			b.WriteString(line + "\n")
			continue
		}
		b.WriteString("# " + line + "\n")
	}
	return b.String()
}

func printBinary(out io.Writer) {
	fmt.Fprintln(out, "1. bosun on PATH")
	if p, err := exec.LookPath("bosun"); err == nil {
		fmt.Fprintf(out, "   found %s\n", p)
		fmt.Fprintln(out, "   Hooks run with the PATH Claude Code inherited; the plugin calls plain `bosun`.")
	} else {
		fmt.Fprintln(out, "   `bosun` is not on this shell's PATH. Run `make install` (puts it in ~/.local/bin,")
		fmt.Fprintln(out, "   which Claude Code hooks have on their PATH) and check with `command -v bosun`.")
	}
	fmt.Fprintln(out)
}

func printPlugin(out io.Writer, opt Options) {
	repo := marketplaceDir(opt)
	if repo == "" {
		repo = "<path to the bosun repo>"
	}
	fmt.Fprintln(out, "2. Claude Code plugin (registers the 13 hooks)")
	fmt.Fprintf(out, "   claude plugin marketplace add %s\n", shellQuote(repo))
	fmt.Fprintln(out, "   claude plugin install bosun@bosun")
	fmt.Fprintln(out, "   Do not also register bosun hooks in settings.json; they would fire twice.")
	fmt.Fprintln(out)
}

func printStatusLine(ad platform.Adapters, opt Options, out io.Writer) {
	fmt.Fprintln(out, "3. Status line (plugins cannot set it, so you edit settings.json)")
	path := opt.Settings
	if path == "" {
		path = ad.Paths.ClaudeSettings()
	}
	cur, err := currentStatusLine(path)
	switch {
	case err != nil:
		fmt.Fprintf(out, "   Could not read %s (%v).\n", path, err)
		fmt.Fprintln(out, "   Wrap your statusLine command by hand using the form below.")
		fmt.Fprintln(out)
		printWrapped(out, "")
	case cur.Command == "":
		fmt.Fprintf(out, "   %s has no statusLine command. To capture spend without a visible status line,\n", path)
		fmt.Fprintln(out, "   set statusLine.command to:")
		fmt.Fprintln(out)
		printWrapped(out, "")
	case alreadyWrapped(cur.Command):
		fmt.Fprintf(out, "   %s already wraps its statusLine with bosun; nothing to change.\n", path)
		fmt.Fprintln(out)
	default:
		fmt.Fprintf(out, "   In %s replace statusLine.command with:\n", path)
		fmt.Fprintln(out)
		printWrapped(out, cur.Command)
	}
}

func printWrapped(out io.Writer, orig string) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // keep > and & readable for copy and paste
	_ = enc.Encode(WrapCommand(orig))
	fmt.Fprintf(out, "   \"command\": %s", buf.String())
	fmt.Fprintln(out)
}

type statusLine struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

func currentStatusLine(path string) (statusLine, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return statusLine{}, err
	}
	var s struct {
		StatusLine statusLine `json:"statusLine"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return statusLine{}, err
	}
	return s.StatusLine, nil
}

func alreadyWrapped(cmd string) bool { return strings.Contains(cmd, plugstate.WrapMarker) }

// WrapCommand builds the guarded status line command. If bosun is missing the
// original runs unchanged, so a removed binary never blanks the status line.
//
// A plain command is passed as words. Anything with shell syntax (pipes,
// quotes, substitutions) goes through `sh -c` as one quoted argument; without
// that, `exec bosun statusline -- a | b` would pipe bosun's output into b.
func WrapCommand(orig string) string {
	if strings.TrimSpace(orig) == "" {
		return "if command -v bosun >/dev/null; then exec bosun statusline; fi"
	}
	words := orig
	// `FOO=1 cmd` is plain characters but not a command word: after `exec` the
	// shell would look for a program named FOO=1.
	if !isPlain(orig) || strings.Contains(strings.Fields(orig)[0], "=") {
		words = "sh -c '" + strings.ReplaceAll(orig, "'", `'\''`) + "'"
	}
	return fmt.Sprintf("if command -v bosun >/dev/null; then exec bosun statusline -- %s; else exec %s; fi", words, words)
}

// isPlain reports whether s is only characters a shell passes through as
// literals or as word breaks between literals.
func isPlain(s string) bool {
	return isWord(strings.ReplaceAll(s, " ", "_"))
}

// isWord reports whether s is a single shell word with nothing to split or
// expand. A space is plain for WrapCommand's pass-through but not here: an
// unquoted path with a space is two arguments.
func isWord(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("_./-=:,@+", r):
		default:
			return false
		}
	}
	return true
}

// shellQuote single-quotes s for a POSIX shell unless it is already one word.
func shellQuote(s string) string {
	if s != "" && isWord(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// findRepo looks upward from each dir for the marketplace manifest that marks
// the bosun checkout.
func findRepo(dirs []string) string {
	for _, d := range dirs {
		for i := 0; d != "" && i < 12; i++ {
			if _, err := os.Stat(filepath.Join(d, ".claude-plugin", "marketplace.json")); err == nil {
				return d
			}
			parent := filepath.Dir(d)
			if parent == d {
				break
			}
			d = parent
		}
	}
	return ""
}

// printTmux reads the user's tmux.conf (never writes it) to tell them where the
// one source-file line goes.
func printTmux(ad platform.Adapters, opt Options, out io.Writer) {
	fmt.Fprintln(out, "4. tmux")
	conf := ad.Paths.TmuxConf()
	if conf == "" {
		fmt.Fprintln(out, "   No data dir resolved, so no tmux conf was generated.")
		return
	}
	line := "source-file -q " + conf
	user, text := readTmuxConf(ad, opt)
	switch {
	case user != "" && strings.Contains(text, conf):
		fmt.Fprintf(out, "   %s already sources %s; nothing to add. Reload with: tmux source-file %s\n", user, conf, user)
		return
	case user == "":
		fmt.Fprintln(out, "   Add this line to your tmux.conf, after your plugin manager and theme lines:")
	default:
		where := "at the end"
		if n := tpmRunLine(text); n > 0 {
			where = fmt.Sprintf("after the tpm `run` line (line %d), so the theme has built status-right first", n)
		}
		fmt.Fprintf(out, "   Add this line to %s %s:\n", user, where)
	}
	fmt.Fprintln(out)
	fmt.Fprintf(out, "     %s\n\n", line)
	reload := "tmux source-file ~/.tmux.conf"
	if user != "" {
		reload = "tmux source-file " + user
	}
	fmt.Fprintf(out, "   Then reload once (prefix + r if bound, or: %s).\n", reload)
	fmt.Fprintln(out, "   For a 1 s bar set `status-interval 1`; themepack's powerline themes already do.")
}

func readTmuxConf(ad platform.Adapters, opt Options) (path, text string) {
	c, exists := tmuxConfFile(ad, opt)
	if !exists {
		return "", ""
	}
	if b, err := os.ReadFile(c); err == nil {
		return c, string(b)
	}
	return "", ""
}

// tpmRunLine returns the 1-based line of the tpm `run` line, or 0.
func tpmRunLine(text string) int {
	for i, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "run") && strings.Contains(l, "tpm") && !strings.HasPrefix(l, "#") {
			return i + 1
		}
	}
	return 0
}

// UnwrapCommand recovers the original statusLine command from a string
// WrapCommand built, for `uninstall` to print. ok is false when the command is
// not bosun's wrapper.
func UnwrapCommand(wrapped string) (orig string, ok bool) {
	const head = "if command -v bosun >/dev/null; then exec bosun statusline"
	rest, found := strings.CutPrefix(strings.TrimSpace(wrapped), head)
	if !found {
		return "", false
	}
	if strings.HasPrefix(rest, "; fi") { // wrapped around nothing
		return "", true
	}
	rest, found = strings.CutPrefix(rest, " -- ")
	if !found {
		return "", false
	}
	// The same words appear again after "; else exec ".
	words, _, found := strings.Cut(rest, "; else exec ")
	if !found {
		return "", false
	}
	if q, ok := strings.CutPrefix(words, "sh -c "); ok && len(q) >= 2 && q[0] == '\'' && q[len(q)-1] == '\'' {
		return strings.ReplaceAll(q[1:len(q)-1], `'\''`, "'"), true
	}
	return words, true
}
