package install

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jmvaldez/bosun/internal/term"
)

// ErrAborted is returned when the question is cancelled (Esc, Ctrl-C, or the
// input closing). Nothing has been changed.
var ErrAborted = errors.New("setup cancelled")

// KeyReader returns the next keypress.
type KeyReader func() (term.Key, error)

// LiveCostQuestion is the question's wording, shared by the screen and the line
// setup leaves once it is answered.
const LiveCostQuestion = "Show live session cost?"

// setupScreen is the one question `bosun setup` asks (D-31). yes is the
// highlighted answer; color bolds the heading and the highlighted row.
func setupScreen(yes, color bool) []string {
	bold := func(s string) string {
		if color {
			return "\x1b[1m" + s + "\x1b[0m"
		}
		return s
	}
	yesMark, noMark := "  ", "  "
	yesRow := "Yes   exact cost as sessions run"
	noRow := "No    running sessions show —, cost appears when they end"
	if yes {
		yesMark, yesRow = "› ", bold(yesRow)
	} else {
		noMark, noRow = "› ", bold(noRow)
	}
	return []string{
		"  " + bold("bosun setup"),
		"",
		"  " + LiveCostQuestion,
		"  bosun reads your Claude Code status line to get it.",
		"",
		"  " + yesMark + yesRow,
		"          (wraps your status line, reversible)",
		"  " + noMark + noRow,
		"",
		"  ↑/↓ select · enter confirm",
	}
}

// Prompt draws the question and reads keys until it is answered: arrows (or
// j/k, tab) move, y and n answer at once, enter confirms the highlighted row,
// and Esc, Ctrl-C or q cancel. def is the row highlighted first. The screen
// redraws in place and is erased when the answer is given, so the progress
// lines that follow start where it began; recording the answer is the caller's
// job.
func Prompt(next KeyReader, out io.Writer, def, color bool) (bool, error) {
	sel := def
	lines := setupScreen(sel, color)
	fmt.Fprint(out, "\x1b[?25l"+strings.Join(lines, "\r\n"))
	finish := func() {
		// Back to the first line, then clear to the end of the screen.
		fmt.Fprintf(out, "\x1b[%dA\r\x1b[J\x1b[?25h", len(lines)-1)
	}
	for {
		k, err := next()
		if err != nil {
			finish()
			return false, ErrAborted
		}
		switch k {
		case "y", "Y":
			finish()
			return true, nil
		case "n", "N":
			finish()
			return false, nil
		case "enter":
			finish()
			return sel, nil
		case "esc", "ctrl-c", "q":
			finish()
			return false, ErrAborted
		case "up", "k":
			sel = true
		case "down", "j":
			sel = false
		case "tab":
			sel = !sel
		default:
			continue
		}
		lines = setupScreen(sel, color)
		fmt.Fprintf(out, "\x1b[%dA\r", len(lines)-1)
		for i, l := range lines {
			if i > 0 {
				fmt.Fprint(out, "\r\n")
			}
			fmt.Fprint(out, l+"\x1b[K")
		}
	}
}

// AskLiveCost runs Prompt on a terminal: raw mode on in's fd for the duration,
// restored on every exit.
func AskLiveCost(in *os.File, out io.Writer, def, color bool) (bool, error) {
	st, err := term.MakeRaw(int(in.Fd()))
	if err != nil {
		return false, fmt.Errorf("bosun setup needs a terminal: %w", err)
	}
	defer st.Restore()
	return Prompt(fileKeys(in), out, def, color)
}

// fileKeys decodes keys from f. One read can hold several (a paste, or a held
// arrow), so the extras wait in pending.
func fileKeys(f *os.File) KeyReader {
	var pending []term.Key
	buf := make([]byte, 64)
	return func() (term.Key, error) {
		for len(pending) == 0 {
			n, err := f.Read(buf)
			pending = term.Decode(buf[:n])
			if err != nil && len(pending) == 0 {
				return "", err
			}
		}
		k := pending[0]
		pending = pending[1:]
		return k, nil
	}
}
