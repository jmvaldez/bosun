// This file edits the user's settings.json as text, so every other byte (key
// order, indent, unknown keys, trailing newline) stays exactly as it was.
// encoding/json cannot do that: a round trip through a map reorders keys and
// rewrites the indent. The input is valid JSON.

package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

type member struct {
	key              string
	keyStart         int
	valStart, valEnd int
}

func skipWS(b []byte, i int) int {
	for i < len(b) && (b[i] == ' ' || b[i] == '\t' || b[i] == '\n' || b[i] == '\r') {
		i++
	}
	return i
}

func valueEnd(b []byte, i int) int {
	switch b[i] {
	case '"':
		for i++; i < len(b); i++ {
			if b[i] == '\\' {
				i++
			} else if b[i] == '"' {
				return i + 1
			}
		}
		return len(b)
	case '{', '[':
		depth := 0
		for ; i < len(b); i++ {
			switch b[i] {
			case '"':
				i = valueEnd(b, i) - 1
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return i + 1
				}
			}
		}
		return len(b)
	}
	for i < len(b) && !strings.ContainsRune(",}] \t\r\n", rune(b[i])) {
		i++
	}
	return i
}

// objectMembers lists the members of the object starting at b[start] == '{' and
// returns the index of its closing brace.
func objectMembers(b []byte, start int) (ms []member, closeIdx int) {
	i := skipWS(b, start+1)
	for i < len(b) && b[i] != '}' {
		ks := i
		ke := valueEnd(b, i)
		var key string
		_ = json.Unmarshal(b[ks:ke], &key)
		i = skipWS(b, ke)
		i = skipWS(b, i+1) // the colon
		ve := valueEnd(b, i)
		ms = append(ms, member{key: key, keyStart: ks, valStart: i, valEnd: ve})
		i = skipWS(b, ve)
		if i < len(b) && b[i] == ',' {
			i = skipWS(b, i+1)
		}
	}
	return ms, i
}

func find(ms []member, key string) int {
	for i, m := range ms {
		if m.key == key {
			return i
		}
	}
	return -1
}

func encodeString(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimRight(buf.String(), "\n")
}

// indentOf returns the whitespace that starts the line holding b[i].
func indentOf(b []byte, i int) string {
	j := bytes.LastIndexByte(b[:i], '\n') + 1
	k := j
	for k < len(b) && (b[k] == ' ' || b[k] == '\t') {
		k++
	}
	return string(b[j:k])
}

// insertMember adds `"key": value` to the object at start/closeIdx in the
// file's own style: new line and indent when the object spans lines, inline
// otherwise.
func insertMember(b []byte, start, closeIdx int, ms []member, key, value, unit string) []byte {
	text := encodeString(key) + ": " + value
	multi := bytes.IndexByte(b[start:closeIdx], '\n') >= 0
	var ins string
	var at int
	switch {
	case len(ms) == 0 && multi:
		base := indentOf(b, closeIdx)
		ins, at = "\n"+base+unit+text, start+1
	case len(ms) == 0:
		ins, at = text, start+1
	case multi:
		ins, at = ",\n"+indentOf(b, ms[0].keyStart)+text, ms[len(ms)-1].valEnd
	default:
		ins, at = ", "+text, ms[len(ms)-1].valEnd
	}
	out := append([]byte{}, b[:at]...)
	out = append(out, ins...)
	return append(out, b[at:]...)
}

func indentUnit(b []byte, ms []member) string {
	if len(ms) > 0 {
		if ind := indentOf(b, ms[0].keyStart); ind != "" && bytes.IndexByte(b[:ms[0].keyStart], '\n') >= 0 {
			return ind
		}
	}
	return "  "
}

var errNotObject = errors.New("the top level is not a JSON object")

// setStatusLineCommand sets statusLine.command, creating what is missing.
func setStatusLineCommand(b []byte, command string) ([]byte, error) {
	if b == nil {
		return []byte("{\n  \"statusLine\": {\n    \"type\": \"command\",\n    \"command\": " + encodeString(command) + "\n  }\n}\n"), nil
	}
	top := skipWS(b, 0)
	if top >= len(b) || b[top] != '{' {
		return nil, errNotObject
	}
	ms, closeIdx := objectMembers(b, top)
	unit := indentUnit(b, ms)
	enc := encodeString(command)
	i := find(ms, "statusLine")
	if i < 0 {
		sl := "{\"type\": \"command\", \"command\": " + enc + "}"
		if bytes.IndexByte(b[top:closeIdx], '\n') >= 0 {
			base := memberIndent(ms, b, closeIdx, unit)
			sl = "{\n" + base + unit + "\"type\": \"command\",\n" + base + unit + "\"command\": " + enc + "\n" + base + "}"
		}
		return insertMember(b, top, closeIdx, ms, "statusLine", sl, unit), nil
	}
	m := ms[i]
	if b[m.valStart] != '{' {
		return nil, errors.New("statusLine is not an object")
	}
	sub, subClose := objectMembers(b, m.valStart)
	if j := find(sub, "command"); j >= 0 {
		if b[sub[j].valStart] != '"' {
			return nil, errors.New("statusLine.command is not a string")
		}
		return splice(b, sub[j].valStart, sub[j].valEnd, enc), nil
	}
	return insertMember(b, m.valStart, subClose, sub, "command", enc, unit), nil
}

// memberIndent is the indent a new top-level member's line gets. In an empty
// object the closing brace's indent is the parent's, so members sit one unit
// deeper; using it as-is nests the new value at the key's own level.
func memberIndent(ms []member, b []byte, closeIdx int, unit string) string {
	if len(ms) > 0 {
		return indentOf(b, ms[0].keyStart)
	}
	return indentOf(b, closeIdx) + unit
}

func splice(b []byte, from, to int, with string) []byte {
	out := append([]byte{}, b[:from]...)
	out = append(out, with...)
	return append(out, b[to:]...)
}

// removeStatusLine drops the statusLine member (used when bosun had added it).
func removeStatusLine(b []byte) ([]byte, error) {
	top := skipWS(b, 0)
	if top >= len(b) || b[top] != '{' {
		return nil, errNotObject
	}
	ms, closeIdx := objectMembers(b, top)
	i := find(ms, "statusLine")
	if i < 0 {
		return b, nil
	}
	switch {
	case len(ms) == 1:
		return splice(b, top+1, closeIdx, ""), nil
	case i < len(ms)-1:
		return splice(b, ms[i].keyStart, ms[i+1].keyStart, ""), nil
	default:
		return splice(b, ms[i-1].valEnd, ms[i].valEnd, ""), nil
	}
}
