package term

import (
	"reflect"
	"testing"
)

func TestDecode(t *testing.T) {
	tests := []struct {
		in   string
		want []Key
	}{
		{"j", []Key{"j"}},
		{"jk", []Key{"j", "k"}},
		{"\x1b[A\x1b[B\x1b[C\x1b[D", []Key{"up", "down", "right", "left"}},
		{"\x1bOA", []Key{"up"}},
		{"\r", []Key{"enter"}},
		{"\t", []Key{"tab"}},
		{"\x1b", []Key{"esc"}},
		{"q\x1b", []Key{"q", "esc"}},
		{"\x1b[5~\x1b[6~", []Key{"pgup", "pgdn"}},
		{"é→", []Key{"é", "→"}},
		{"\x7f", []Key{"backspace"}},
		{"\x03", []Key{"ctrl-c"}},
		{"\x1b[", []Key{"esc", "["}},
	}
	for _, tt := range tests {
		if got := Decode([]byte(tt.in)); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Decode(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
