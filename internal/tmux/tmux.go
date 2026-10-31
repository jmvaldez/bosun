// Package tmux wraps the few tmux commands bosun runs. Every call carries Timeout
// so a hung server can never freeze the evaluator (LESSONS L-09).
package tmux

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jmvaldez/bosun/internal/platform"
	"github.com/jmvaldez/bosun/internal/runner"
	"github.com/jmvaldez/bosun/internal/shell"
)

const Timeout = 500 * time.Millisecond

// Client talks to one tmux server.
type Client struct {
	Runner runner.Runner
	// Bin is the tmux binary; empty means "tmux" on PATH.
	Bin string
	// Socket is the server socket path (-S); empty means the default server.
	Socket string
}

// SocketFromEnv extracts the socket path from $TMUX ("socket,serverpid,session").
// The path may itself contain commas, so only the last two fields are dropped.
func SocketFromEnv(tmuxEnv string) string {
	parts := strings.Split(tmuxEnv, ",")
	if len(parts) < 3 {
		return ""
	}
	return strings.Join(parts[:len(parts)-2], ",")
}

func (c Client) run(args ...string) ([]byte, error) {
	bin := c.Bin
	if bin == "" {
		bin = "tmux"
	}
	var full []string
	if c.Socket != "" {
		full = append(full, "-S", c.Socket)
	}
	return c.Runner.Run(Timeout, bin, append(full, args...)...)
}

// PasteBuffer is the tmux buffer PasteText loads and then deletes.
const PasteBuffer = "bosun-handoff"

// runIn is run with stdin; the Runner must be able to feed it.
func (c Client) runIn(stdin string, args ...string) ([]byte, error) {
	ir, ok := c.Runner.(runner.InputRunner)
	if !ok {
		return nil, fmt.Errorf("tmux runner cannot feed stdin")
	}
	bin := c.Bin
	if bin == "" {
		bin = "tmux"
	}
	var full []string
	if c.Socket != "" {
		full = append(full, "-S", c.Socket)
	}
	return ir.RunInput(Timeout, stdin, bin, append(full, args...)...)
}

// Pane is one row of list-panes -a.
type Pane struct {
	ServerPID int
	ID        string // %12
	WindowID  string // @3
	PanePID   int
	Session   string
	Window    int
	Index     int
}

// Where renders the pane as session:window.pane.
func (p Pane) Where() string { return fmt.Sprintf("%s:%d.%d", p.Session, p.Window, p.Index) }

// Panes lists every pane on the server.
func (c Client) Panes() ([]Pane, error) {
	out, err := c.run("list-panes", "-a", "-F",
		"#{pid}\t#{pane_id}\t#{window_id}\t#{pane_pid}\t#{window_index}\t#{pane_index}\t#{session_name}")
	if err != nil {
		return nil, err
	}
	return ParsePanes(string(out)), nil
}

// ParsePanes parses the list-panes format above, skipping malformed rows.
// session_name is the last field so a tab inside it survives SplitN.
func ParsePanes(out string) []Pane {
	var ps []Pane
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(line, "\t", 7)
		if len(f) != 7 {
			continue
		}
		spid, e1 := strconv.Atoi(f[0])
		ppid, e2 := strconv.Atoi(f[3])
		win, e3 := strconv.Atoi(f[4])
		idx, e4 := strconv.Atoi(f[5])
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
			continue
		}
		ps = append(ps, Pane{ServerPID: spid, ID: f[1], WindowID: f[2], PanePID: ppid, Window: win, Index: idx, Session: f[6]})
	}
	return ps
}

// ClientInfo is one attached tmux client.
type ClientInfo struct {
	Name         string
	PaneID       string // the pane the client is currently showing
	Flags        string
	TermFeatures string
}

// Clients lists attached clients. The pane is the client's current pane, which
// is how a key binding learns where the user is (not `display -p -c`, LESSONS L-10).
func (c Client) Clients() ([]ClientInfo, error) {
	out, err := c.run("list-clients", "-F", "#{client_name}\t#{pane_id}\t#{client_flags}\t#{client_termfeatures}")
	if err != nil {
		return nil, err
	}
	return ParseClients(string(out)), nil
}

// ParseClients parses the list-clients format above.
func ParseClients(out string) []ClientInfo {
	var cs []ClientInfo
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(line, "\t", 4)
		if len(f) != 4 || f[0] == "" {
			continue
		}
		cs = append(cs, ClientInfo{Name: f[0], PaneID: f[1], Flags: f[2], TermFeatures: f[3]})
	}
	return cs
}

// Viewing reports whether the user can be looking at the pane: anything
// FocusVerdict does not call NotFocused, so an Unknown verdict counts.
func Viewing(clients []ClientInfo, pane string) bool {
	return FocusVerdict(clients, pane) != platform.NotFocused
}

func hasWord(csv, word string) bool {
	for _, w := range strings.FieldsFunc(csv, func(r rune) bool { return r == ',' || r == ' ' }) {
		if w == word {
			return true
		}
	}
	return false
}

// WindowMark sets (Value != "") or clears the @bosun-wait option of a window.
type WindowMark struct {
	WindowID string
	Value    string
}

// SetMarks applies all marks in one tmux invocation.
func (c Client) SetMarks(marks []WindowMark) error {
	if len(marks) == 0 {
		return nil
	}
	var args []string
	for i, m := range marks {
		if i > 0 {
			args = append(args, ";")
		}
		if m.Value == "" {
			args = append(args, "set-option", "-wu", "-t", m.WindowID, "@bosun-wait")
		} else {
			args = append(args, "set-option", "-w", "-t", m.WindowID, "@bosun-wait", m.Value)
		}
	}
	_, err := c.run(args...)
	return err
}

// SwitchClient moves a client to a pane, across sessions and windows.
func (c Client) SwitchClient(client, pane string) error {
	args := []string{"switch-client"}
	if client != "" {
		args = append(args, "-c", client)
	}
	_, err := c.run(append(args, "-t", pane)...)
	return err
}

// DisplayMessage shows a one-line message on a client's status line. Used
// instead of stdout from run-shell, which would open a view-mode pane.
func (c Client) DisplayMessage(client, msg string) error {
	args := []string{"display-message"}
	if client != "" {
		args = append(args, "-c", client)
	}
	_, err := c.run(append(args, msg)...)
	return err
}

// Check implements platform.Focus through FocusVerdict; a tmux error is Unknown.
func (c Client) Check(pane string) platform.Verdict {
	clients, err := c.Clients()
	if err != nil {
		return platform.Unknown
	}
	return FocusVerdict(clients, pane)
}

// FocusVerdict says whether the user is looking at pane. A focus-capable client
// showing it and flagged focused makes it Focused. Otherwise a client showing it
// that cannot report focus makes it Unknown, which notifies. Anything else,
// including a focus-capable client showing it unfocused, is NotFocused.
func FocusVerdict(clients []ClientInfo, pane string) platform.Verdict {
	verdict := platform.NotFocused
	for _, cl := range clients {
		if cl.PaneID != pane {
			continue
		}
		if !hasWord(cl.TermFeatures, "focus") {
			if verdict == platform.NotFocused {
				verdict = platform.Unknown
			}
			continue
		}
		if hasWord(cl.Flags, "focused") {
			return platform.Focused
		}
	}
	return verdict
}

// SendKeys sends key names to a pane (send-keys -t pane KEY...). Only
// panewrite's guarded writers call it.
func (c Client) SendKeys(pane string, keys []string) error {
	_, err := c.run(append([]string{"send-keys", "-t", pane}, keys...)...)
	return err
}

// PasteText pastes text into a pane as one bracketed paste, so its newlines do
// not submit. The text goes through a named buffer on stdin, never a command
// line, and the buffer is deleted by the paste. The caller sends the submit key.
func (c Client) PasteText(pane, text string) error {
	if _, err := c.runIn(text, "load-buffer", "-b", PasteBuffer, "-"); err != nil {
		return err
	}
	_, err := c.run("paste-buffer", "-p", "-d", "-b", PasteBuffer, "-t", pane)
	return err
}

// windowDir returns the fixed directory passed to new-window -c. tmux expands
// formats in the -c value, so `#(cmd)` in a directory name would run cmd in the
// server (audit H-1); the real directory is entered by the command instead.
func windowDir() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return home
	}
	return "/"
}

// inDir prefixes command with a cd into dir. The trailing command argument of
// new-window is not format-expanded.
func inDir(dir, command string) string {
	return "cd -- " + shell.Quote(dir) + " && " + command
}

// windowName escapes name for new-window -n, which tmux format-expands: a
// literal # is written ##.
func windowName(name string) string {
	return strings.ReplaceAll(name, "#", "##")
}

// NewWindow opens a window named "diff" at dir running command. dir is entered
// by the shell, not by -c (see windowDir, H-1).
func (c Client) NewWindow(dir, command string) error {
	_, err := c.run("new-window", "-c", windowDir(), "-n", windowName("diff"), inDir(dir, command))
	return err
}

// PaneWhere names a pane's session:window, for notices.
func (c Client) PaneWhere(pane string) string {
	out, err := c.run("display-message", "-p", "-t", pane, "#{session_name}:#{window_index}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ClientName returns the name of the client running the command (works inside
// display-popup, where TMUX_PANE does not, LESSONS L-10).
func (c Client) ClientName() (string, error) {
	out, err := c.run("display-message", "-p", "#{client_name}")
	return strings.TrimSpace(string(out)), err
}

// PanePath returns a pane's current working directory.
func (c Client) PanePath(pane string) (string, error) {
	out, err := c.run("display-message", "-p", "-t", pane, "#{pane_current_path}")
	return strings.TrimSpace(string(out)), err
}

// NewNamedWindow opens a window running a shell command at dir. dir is entered
// by the shell, not by -c (see windowDir, H-1). env entries ("NAME=value") are
// set for the command with new-window -e; tmux does not format-expand them.
func (c Client) NewNamedWindow(dir, name string, env []string, command string) error {
	args := []string{"new-window", "-c", windowDir(), "-n", windowName(name)}
	for _, e := range env {
		args = append(args, "-e", e)
	}
	_, err := c.run(append(args, inDir(dir, command))...)
	return err
}
