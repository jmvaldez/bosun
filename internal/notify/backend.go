package notify

import (
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jmvaldez/bosun/internal/platform"
	"github.com/jmvaldez/bosun/internal/runner"
)

// backendTimeout bounds every backend exec, so a hung notification server
// stalls one dispatch for at most this long (LESSONS L-12).
const backendTimeout = 2 * time.Second

var idRE = regexp.MustCompile(`uint32 (\d+)`)

// GDBus posts through org.freedesktop.Notifications directly (D-6). Unlike the
// libnotify 0.7 notify-send on Ubuntu 22.04 it returns the id, supports
// replace, and can close.
type GDBus struct {
	R   runner.Runner
	Bin string
}

const (
	dbusDest = "org.freedesktop.Notifications"
	dbusPath = "/org/freedesktop/Notifications"
)

func (GDBus) Name() string { return "gdbus" }

func (GDBus) Capabilities() platform.Capabilities {
	// A click is a signal the short-lived `notify-click` child listens for.
	return platform.Capabilities{Sound: true, Replace: true, Retract: true, Click: true}
}

// ServerCaps reports whether the notification server advertises "actions" (a
// click needs it) and "body-markup".
func (g GDBus) ServerCaps() (actions, markup bool) {
	out, err := g.R.Run(backendTimeout, g.Bin, "call", "--session", "--dest", dbusDest, "--object-path", dbusPath,
		"--method", dbusDest+".GetCapabilities")
	if err != nil {
		return false, false
	}
	return strings.Contains(string(out), "'actions'"), strings.Contains(string(out), "'body-markup'")
}

func (g GDBus) ServerActions() bool {
	a, _ := g.ServerCaps()
	return a
}

// markupEscaper escapes the three characters a freedesktop server parses as
// body markup.
var markupEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// escapeIf escapes s for a server that renders body markup. Some, GNOME among
// them, render it however the sender asked, so a prompt-injected
// `<a href=...>` must not reach one unescaped (L-1).
func escapeIf(markup bool, s string) string {
	if markup {
		return markupEscaper.Replace(s)
	}
	return s
}

// bodyOf joins the two lines. With markup the second is italic (GNOME Shell and
// the notification spec allow <i>, not <span>); the text of either is escaped,
// the body too when there is no second line.
func bodyOf(n platform.Notice) string {
	if n.Line2 == "" {
		return escapeIf(n.Markup, n.Body)
	}
	if !n.Markup {
		return n.Body + "\n" + n.Line2
	}
	return markupEscaper.Replace(n.Body) + "\n<i>" + markupEscaper.Replace(n.Line2) + "</i>"
}

// variantString quotes s as a GVariant text-format string literal
// (https://docs.gtk.org/glib/gvariant-text-format.html). A carriage return is
// dropped, not escaped.
func variantString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `'`, `\'`, "\n", `\n`, "\r", ``)
	return "'" + r.Replace(s) + "'"
}

func (g GDBus) Post(n platform.Notice) (uint32, error) {
	hints, actions := "{}", "[]"
	if n.Action != "" {
		actions = "['default', " + variantString(n.Action) + "]"
	}
	if n.Sound {
		hints = "{'sound-name': <'message-new-instant'>}"
	}
	body := bodyOf(n)
	if n.Subtitle != "" {
		body = escapeIf(n.Markup, n.Subtitle) + "\n" + body
	}
	out, err := g.R.Run(backendTimeout, g.Bin, "call", "--session", "--dest", dbusDest, "--object-path", dbusPath,
		"--method", dbusDest+".Notify", "--", // "--": the -1 timeout would parse as an option
		variantString("bosun"), strconv.FormatUint(uint64(n.ReplaceID), 10), variantString(n.Icon),
		variantString(n.Title), variantString(body), actions, hints, "-1")
	if err != nil {
		return 0, fmt.Errorf("gdbus Notify: %w", err)
	}
	return parseID(out)
}

func parseID(out []byte) (uint32, error) {
	m := idRE.FindSubmatch(out)
	if m == nil {
		return 0, fmt.Errorf("notify: no notification id in %q", strings.TrimSpace(string(out)))
	}
	id, err := strconv.ParseUint(string(m[1]), 10, 32)
	return uint32(id), err
}

func (g GDBus) Retract(id uint32, _ string) error {
	if id == 0 {
		return nil
	}
	_, err := g.R.Run(backendTimeout, g.Bin, "call", "--session", "--dest", dbusDest, "--object-path", dbusPath,
		"--method", dbusDest+".CloseNotification", "--", strconv.FormatUint(uint64(id), 10))
	return err
}

// NotifySend is the fallback. libnotify before 0.8 has no --print-id, so with
// PrintID false it posts without an id and can neither replace nor retract.
type NotifySend struct {
	R       runner.Runner
	Bin     string
	PrintID bool
	// Closer retracts by id; notify-send itself cannot.
	Closer *GDBus
}

func (NotifySend) Name() string { return "notify-send" }

func (n NotifySend) Capabilities() platform.Capabilities {
	return platform.Capabilities{Sound: true, Replace: n.PrintID, Retract: n.PrintID && n.Closer != nil}
}

func (n NotifySend) Post(m platform.Notice) (uint32, error) {
	args := []string{"-a", "bosun"}
	if n.PrintID {
		args = append(args, "--print-id")
		if m.ReplaceID != 0 {
			args = append(args, "--replace-id", strconv.FormatUint(uint64(m.ReplaceID), 10))
		}
	}
	if m.Sound {
		args = append(args, "-h", "string:sound-name:message-new-instant")
	}
	// Escape by the server's body-markup capability (m.Markup) before forcing
	// it off: notify-send takes no <i>, so the lines are joined as plain text,
	// but the server may still parse what it is given.
	m.Subtitle = escapeIf(m.Markup, m.Subtitle)
	m.Body = escapeIf(m.Markup, m.Body)
	m.Line2 = escapeIf(m.Markup, m.Line2)
	m.Markup = false
	body := bodyOf(m)
	if m.Subtitle != "" {
		body = m.Subtitle + "\n" + body
	}
	if m.Icon != "" {
		args = append(args, "-i", m.Icon)
	}
	out, err := n.R.Run(backendTimeout, n.Bin, append(args, "--", m.Title, body)...)
	if err != nil {
		return 0, fmt.Errorf("notify-send: %w", err)
	}
	if !n.PrintID {
		return 0, nil
	}
	id, err := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 32)
	if err != nil {
		return 0, fmt.Errorf("notify-send: bad id %q", strings.TrimSpace(string(out)))
	}
	return uint32(id), nil
}

func (n NotifySend) Retract(id uint32, group string) error {
	if n.Closer == nil {
		return nil
	}
	return n.Closer.Retract(id, group)
}

// notifySendVersion parses `notify-send --version` ("notify-send 0.7.9").
func notifySendVersion(out string) (major, minor int, ok bool) {
	f := strings.Fields(out)
	if len(f) == 0 {
		return 0, 0, false
	}
	parts := strings.Split(f[len(f)-1], ".")
	if len(parts) < 2 {
		return 0, 0, false
	}
	a, e1 := strconv.Atoi(parts[0])
	b, e2 := strconv.Atoi(parts[1])
	return a, b, e1 == nil && e2 == nil
}

// TerminalNotifier is the macOS backend.
//
// TODO(D-1): click-through (-activate and -execute, SPEC §6) is missing;
// Capabilities reports no Click until the macOS live checks run.
type TerminalNotifier struct {
	R   runner.Runner
	Bin string
}

func (TerminalNotifier) Name() string { return "terminal-notifier" }
func (TerminalNotifier) Capabilities() platform.Capabilities {
	return platform.Capabilities{Sound: true, Replace: true, Retract: true}
}

func (t TerminalNotifier) Post(n platform.Notice) (uint32, error) {
	n.Markup = false // terminal-notifier does not parse markup, so nothing to escape
	args := []string{"-title", n.Title, "-message", bodyOf(n), "-group", n.Group}
	if n.Subtitle != "" {
		args = append(args, "-subtitle", n.Subtitle)
	}
	if n.Sound {
		args = append(args, "-sound", "default")
	}
	_, err := t.R.Run(backendTimeout, t.Bin, args...)
	return 0, err
}

func (t TerminalNotifier) Retract(_ uint32, group string) error {
	_, err := t.R.Run(backendTimeout, t.Bin, "-remove", group)
	return err
}

// Osascript is opt-in only: macOS attributes its banners to Script Editor,
// which is often set to show none (LESSONS L-42).
type Osascript struct {
	R   runner.Runner
	Bin string
}

func (Osascript) Name() string                        { return "osascript" }
func (Osascript) Capabilities() platform.Capabilities { return platform.Capabilities{Sound: true} }

func (o Osascript) Post(n platform.Notice) (uint32, error) {
	q := func(s string) string { return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"` }
	n.Markup = false // osascript does not parse markup, so nothing to escape
	script := "display notification " + q(bodyOf(n)) + " with title " + q(n.Title) + " subtitle " + q(n.Subtitle)
	_, err := o.R.Run(backendTimeout, o.Bin, "-e", script)
	return 0, err
}

func (Osascript) Retract(uint32, string) error { return nil }

// Resolve picks the backend for notify.backend. A nil Notifier with a reason
// means notifications are off and says how to turn them on; every other surface
// keeps working. "auto" never picks osascript.
func Resolve(backend string, kind platform.Kind, look func(string) (string, error), r runner.Runner) (platform.Notifier, string) {
	if look == nil {
		look = exec.LookPath
	}
	find := func(names ...string) string {
		for _, n := range names {
			if p, err := look(n); err == nil {
				return p
			}
		}
		return ""
	}
	switch backend {
	case "off":
		return nil, "notifications are off (notify.backend = \"off\")"
	case "osascript":
		if p := find("osascript"); p != "" {
			return Osascript{R: r, Bin: p}, ""
		}
		return nil, "osascript not found"
	case "terminal-notifier":
		if p := find("terminal-notifier", "/opt/homebrew/bin/terminal-notifier", "/usr/local/bin/terminal-notifier"); p != "" {
			return TerminalNotifier{R: r, Bin: p}, ""
		}
		return nil, "terminal-notifier not found; brew install terminal-notifier"
	case "notify-send":
		return linuxNotifier(find, r, true)
	// TODO: WSL backend. "wsl" is a valid notify.backend value with nothing behind it yet.
	case "wsl":
		return nil, "the WSL backend is not implemented yet"
	}
	// auto
	switch kind {
	case platform.Darwin:
		if p := find("terminal-notifier", "/opt/homebrew/bin/terminal-notifier", "/usr/local/bin/terminal-notifier"); p != "" {
			return TerminalNotifier{R: r, Bin: p}, ""
		}
		return nil, "no notifier found; brew install terminal-notifier"
	case platform.Linux:
		return linuxNotifier(find, r, false)
	}
	return nil, "no notification backend for this platform"
}

func linuxNotifier(find func(...string) string, r runner.Runner, preferNotifySend bool) (platform.Notifier, string) {
	gdbus, send := find("gdbus"), find("notify-send")
	if gdbus != "" && !preferNotifySend {
		return GDBus{R: r, Bin: gdbus}, ""
	}
	if send != "" {
		ns := NotifySend{R: r, Bin: send}
		if out, err := r.Run(backendTimeout, send, "--version"); err == nil {
			if maj, min, ok := notifySendVersion(string(out)); ok && (maj > 0 || min >= 8) {
				ns.PrintID = true
			}
		}
		if gdbus != "" {
			ns.Closer = &GDBus{R: r, Bin: gdbus}
		}
		return ns, ""
	}
	if gdbus != "" {
		return GDBus{R: r, Bin: gdbus}, ""
	}
	return nil, "no notifier found; install libnotify-bin (notify-send) or gdbus"
}
