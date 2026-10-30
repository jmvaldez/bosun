package term

// Key is one decoded keypress: a printable rune as itself, or a name such as
// "up", "enter", "tab", "esc".
type Key string

// Decode splits a read buffer into keys. ESC [ or ESC O, then digits and ';',
// then a final byte, is one navigation key (arrows, home, end, page up/down).
// Any other ESC, including one at the end of the buffer, is the Escape key.
func Decode(b []byte) []Key {
	var keys []Key
	for i := 0; i < len(b); {
		c := b[i]
		switch {
		case c == 0x1b:
			if i+2 < len(b) && (b[i+1] == '[' || b[i+1] == 'O') {
				j := i + 2
				for j < len(b) && (b[j] >= '0' && b[j] <= '9' || b[j] == ';') {
					j++
				}
				if j < len(b) {
					keys = append(keys, csi(b[i+2:j], b[j]))
					i = j + 1
					continue
				}
			}
			keys = append(keys, "esc")
			i++
		case c == '\r' || c == '\n':
			keys = append(keys, "enter")
			i++
		case c == '\t':
			keys = append(keys, "tab")
			i++
		case c == 0x7f || c == 0x08:
			keys = append(keys, "backspace")
			i++
		case c == 0x03:
			keys = append(keys, "ctrl-c")
			i++
		case c < 0x20:
			i++ // other control bytes are ignored
		default:
			n := runeLen(c)
			if i+n > len(b) {
				n = len(b) - i
			}
			keys = append(keys, Key(b[i:i+n]))
			i += n
		}
	}
	return keys
}

func csi(params []byte, final byte) Key {
	switch final {
	case 'A':
		return "up"
	case 'B':
		return "down"
	case 'C':
		return "right"
	case 'D':
		return "left"
	case 'H':
		return "home"
	case 'F':
		return "end"
	case '~':
		switch string(params) {
		case "1", "7":
			return "home"
		case "4", "8":
			return "end"
		case "5":
			return "pgup"
		case "6":
			return "pgdn"
		}
	}
	return "unknown"
}

func runeLen(c byte) int {
	switch {
	case c >= 0xf0:
		return 4
	case c >= 0xe0:
		return 3
	case c >= 0xc0:
		return 2
	}
	return 1
}
