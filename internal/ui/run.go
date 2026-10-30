package ui

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jmvaldez/bosun/internal/term"
)

// Terminal control sequences: enter or leave the alternate screen with the
// cursor hidden, and move the cursor home.
const (
	enterScreen = "\x1b[?1049h\x1b[?25l"
	leaveScreen = "\x1b[?25h\x1b[?1049l"
	home        = "\x1b[H"
)

// Run drives the popup on a terminal until the model asks to close. load is
// called at start and on every tick; it runs on the loop goroutine, so it
// must be quick or cache what it can.
func Run(in *os.File, out io.Writer, m *Model, load func() Data, tick time.Duration) error {
	fd := int(in.Fd())
	st, err := term.MakeRaw(fd)
	if err != nil {
		return fmt.Errorf("bosun ui needs a terminal: %w", err)
	}
	defer st.Restore()
	fmt.Fprint(out, enterScreen)
	defer fmt.Fprint(out, leaveScreen)

	keys := make(chan term.Key, 32)
	go func() {
		buf := make([]byte, 256)
		for {
			n, err := in.Read(buf)
			if n > 0 {
				for _, k := range term.Decode(buf[:n]) {
					keys <- k
				}
			}
			if err != nil {
				close(keys)
				return
			}
		}
	}()
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)

	draw := func() {
		w, h, err := term.Size(fd)
		if err != nil || w == 0 {
			w, h = 100, 30
		}
		lines := m.View(w, h)
		var b strings.Builder
		b.WriteString(home)
		for i, l := range lines {
			if i > 0 {
				b.WriteString("\r\n")
			}
			b.WriteString(l)
			b.WriteString("\x1b[K")
		}
		fmt.Fprint(out, b.String())
	}

	m.SetData(load())
	draw()
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case k, ok := <-keys:
			if !ok || m.Update(k) {
				return nil
			}
			draw()
		case <-t.C:
			m.SetData(load())
			draw()
		case <-winch:
			draw()
		}
	}
}
