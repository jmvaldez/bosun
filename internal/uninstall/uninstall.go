// Package uninstall prints the steps to remove bosun, and with --purge deletes
// bosun's own dirs. It is the only code that deletes anything outside a session
// dir, so every deletion goes through Remove's checks (LESSONS L-46).
package uninstall

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jmvaldez/bosun/internal/install"
	"github.com/jmvaldez/bosun/internal/paths"
)

type Options struct {
	Purge       bool
	PurgeConfig bool
	// Executable is the running bosun, printed in the last step.
	Executable string
	// Settings is the Claude settings.json, read to print the original statusLine.
	Settings string
	// TmuxConf is the user's tmux.conf, read to name the line to remove.
	TmuxConf string
	Home     string
	// Explicit says which dirs were set through BOSUN_*_DIR (keyed state, cache,
	// data, config); an explicit dir need not be named "bosun".
	Explicit map[string]bool
	// Apply, when set, reverses the plugin, status line and tmux steps itself
	// (D-26) instead of printing them; it returns an exit code.
	Apply func(out io.Writer, purge []install.Step) int
	// Tilde shortens paths under home in the --apply output.
	Tilde func(string) string
}

// Run prints the steps in order, then purges if asked. With Apply set it
// instead runs the steps and the purge as one confirmed plan. It returns the
// exit code.
func Run(p paths.Paths, o Options, out, errw io.Writer) int {
	if o.Apply != nil {
		// Purge is part of the plan: it runs only after the same yes.
		rc := o.Apply(out, purgeSteps(p, o))
		fmt.Fprintf(out, "\nThe binary is yours to remove: rm %s\n", orDefault(o.short(o.Executable), "$(command -v bosun)"))
		return rc
	}
	fmt.Fprintln(out, "bosun uninstall: steps, in order (bosun does not edit your files)")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "1. Remove the plugin")
	fmt.Fprintln(out, "     claude plugin uninstall bosun@bosun")
	fmt.Fprintln(out, "     claude plugin marketplace remove bosun")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "2. Restore your original status line")
	printStatusLine(out, o.Settings)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "3. Remove the tmux line and re-source")
	if conf := p.TmuxConf(); conf != "" {
		fmt.Fprintf(out, "     delete this line from %s:\n       source-file -q %s\n", orDefault(o.TmuxConf, "your tmux.conf"), conf)
		fmt.Fprintf(out, "     then: tmux source-file %s   (windows keep a stale bar until tmux restarts)\n", orDefault(o.TmuxConf, "~/.tmux.conf"))
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "4. Delete the binary")
	fmt.Fprintf(out, "     rm %s\n", orDefault(o.Executable, "$(command -v bosun)"))
	fmt.Fprintln(out)

	if !o.Purge && !o.PurgeConfig {
		fmt.Fprintf(out, "Your bosun data is untouched: state %s, cache %s, data %s, config %s.\n", p.State, p.Cache, p.Data, p.Config)
		fmt.Fprintln(out, "Add --purge to delete state, cache and data, or --purge-config to delete config too.")
		return 0
	}
	return purge(p, o, out, errw, 0)
}

func purge(p paths.Paths, o Options, out, errw io.Writer, rc int) int {
	targets := []struct{ name, dir string }{{"state", p.State}, {"cache", p.Cache}, {"data", p.Data}}
	if o.PurgeConfig {
		targets = append(targets, struct{ name, dir string }{"config", p.Config})
	}
	for _, t := range targets {
		switch err := Remove(t.name, t.dir, p, o); {
		case err == nil:
			fmt.Fprintf(out, "deleted %s dir %s\n", t.name, t.dir)
		case errors.Is(err, os.ErrNotExist):
			fmt.Fprintf(out, "%s dir %s does not exist\n", t.name, t.dir)
		default:
			fmt.Fprintf(errw, "bosun uninstall: refusing to delete the %s dir: %v\n", t.name, err)
			rc = 1
		}
	}
	if !o.PurgeConfig {
		fmt.Fprintf(out, "kept config %s (--purge-config deletes it)\n", p.Config)
	}
	return rc
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func printStatusLine(out io.Writer, settings string) {
	b, err := os.ReadFile(settings)
	if err != nil {
		fmt.Fprintln(out, "     (could not read settings.json) put back whatever statusLine.command you had before bosun install;")
		fmt.Fprintln(out, "     the wrapper is `if command -v bosun ...; then exec bosun statusline -- <original>; else exec <original>; fi`")
		return
	}
	cmd := statusLineCommand(b)
	orig, ok := install.UnwrapCommand(cmd)
	switch {
	case !ok:
		fmt.Fprintln(out, "     statusLine.command is not wrapped by bosun; nothing to restore")
	case orig == "":
		fmt.Fprintln(out, "     the wrapper had no original command: remove the statusLine key")
	default:
		fmt.Fprintln(out, "     in settings.json set statusLine.command back to:")
		fmt.Fprintf(out, "       %s\n", orig)
	}
}

// Remove deletes one bosun dir after checking it. It refuses a path that is not
// exactly the resolved dir of that kind, anything that looks like a home or root
// dir, a dir not named bosun unless it was set explicitly, a symlink, a
// non-directory, and a dir without the bosun marker.
func Remove(kind, dir string, p paths.Paths, o Options) error {
	if dir == "" {
		return fmt.Errorf("no %s dir resolved", kind)
	}
	clean := filepath.Clean(dir)
	allowed := map[string]string{"state": p.State, "cache": p.Cache, "data": p.Data, "config": p.Config}
	if want, ok := allowed[kind]; !ok || filepath.Clean(want) != clean {
		return fmt.Errorf("%s is not the resolved %s dir", dir, kind)
	}
	if !filepath.IsAbs(clean) || strings.Count(clean, string(filepath.Separator)) < 2 || clean == filepath.Clean(o.Home) || clean == "/" {
		return fmt.Errorf("%s is too broad to delete", dir)
	}
	if !o.Explicit[kind] && filepath.Base(clean) != "bosun" {
		return fmt.Errorf("%s is not named bosun", dir)
	}
	fi, err := os.Lstat(clean)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink", dir)
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	// The name check is bypassed by an explicit override, and a typo in one can
	// land on a shared dir. Only a dir bosun marked is ours to empty.
	if !paths.HasMarker(clean) {
		return fmt.Errorf("%s has no %s marker, so bosun did not create it (to purge it anyway, create the file yourself)", dir, paths.MarkerName)
	}
	return os.RemoveAll(clean)
}

// purgeSteps turns --purge and --purge-config into plan steps. A dir that does
// not exist has nothing to delete; Remove still makes every safety check when
// the step runs.
func purgeSteps(p paths.Paths, o Options) []install.Step {
	if !o.Purge && !o.PurgeConfig {
		return nil
	}
	targets := []struct{ name, dir string }{{"state", p.State}, {"cache", p.Cache}, {"data", p.Data}}
	if o.PurgeConfig {
		targets = append(targets, struct{ name, dir string }{"config", p.Config})
	}
	var steps []install.Step
	for _, t := range targets {
		if _, err := os.Lstat(t.dir); t.dir == "" || err != nil {
			continue
		}
		steps = append(steps, install.Step{
			Name: "purge " + t.name, Title: "Delete " + o.short(t.dir), Result: "Deleted " + o.short(t.dir),
			Do: func() ([]string, error) {
				if err := Remove(t.name, t.dir, p, o); err != nil {
					return nil, fmt.Errorf("refusing to delete the %s dir: %w", t.name, err)
				}
				return nil, nil
			},
		})
	}
	return steps
}

func (o Options) short(p string) string {
	if o.Tilde == nil {
		return p
	}
	return o.Tilde(p)
}
