package install

import "testing"

func TestSettingsEditKeepsTheFilesStyle(t *testing.T) {
	for name, body := range map[string]string{
		"tabs":       "{\n\t\"a\": 1\n}\n",
		"four":       "{\n    \"a\": 1\n}\n",
		"no newline": `{"a": 1}`,
	} {
		out, err := setStatusLineCommand([]byte(body), "x")
		if err != nil {
			t.Fatal(name, err)
		}
		back, err := removeStatusLine(out)
		if err != nil || string(back) != body {
			t.Errorf("%s: round trip\n%s\n=>\n%s", name, out, back)
		}
	}
	if _, err := setStatusLineCommand([]byte(`[1]`), "x"); err == nil {
		t.Error("a top-level array was accepted")
	}
	// An empty object: the new member goes one unit in from the closing brace's
	// indent, not at the brace's own column.
	got, err := setStatusLineCommand([]byte("{\n}\n"), "x")
	if want := "{\n  \"statusLine\": {\n    \"type\": \"command\",\n    \"command\": \"x\"\n  }\n}\n"; err != nil || string(got) != want {
		t.Errorf("empty object: got %q (%v), want %q", got, err, want)
	}
	if _, err := setStatusLineCommand([]byte(`{"statusLine": "x"}`), "x"); err == nil {
		t.Error("a string statusLine was accepted")
	}
}
