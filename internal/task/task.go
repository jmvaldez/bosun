// Package task is the one issue-key parser. Hook, ingest, popup, and spend all
// call it so they never disagree about a task.
package task

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/jmvaldez/bosun/internal/atomicfile"
	"github.com/jmvaldez/bosun/internal/clean"
	"github.com/jmvaldez/bosun/internal/config"
)

// Sources, in precedence order (highest first).
const (
	SourceTag        = "tag"
	SourceEnv        = "env"
	SourceBranch     = "branch"
	SourceTranscript = "transcript"
)

// Parser extracts an uppercased issue key from free-form text such as a branch
// name.
type Parser struct {
	re       *regexp.Regexp
	prefixes []string
}

// New compiles keyRegex case-insensitively. prefixes, when non-empty, is an
// allowlist for the part of the key before the first '-'.
func New(keyRegex string, prefixes []string) (*Parser, error) {
	re, err := regexp.Compile("(?i)" + keyRegex)
	if err != nil {
		return nil, err
	}
	up := make([]string, len(prefixes))
	for i, p := range prefixes {
		up[i] = strings.ToUpper(p)
	}
	return &Parser{re: re, prefixes: up}, nil
}

// FromConfig builds the parser from c, falling back to Default when the
// configured key regex does not compile.
func FromConfig(c config.Task) *Parser {
	p, err := New(c.KeyRegex, c.Prefixes)
	if err != nil {
		return Default()
	}
	return p
}

// Default parses with the default pattern and no prefix allowlist. Hooks use it
// because they never read config.
func Default() *Parser {
	p, err := New(config.DefaultKeyRegex, nil)
	if err != nil {
		panic(err) // the constant pattern is valid; a failure here is a build bug
	}
	return p
}

// Key returns the first allowed key in s, uppercased, or "".
func (p *Parser) Key(s string) string {
	for _, m := range p.re.FindAllString(s, -1) {
		key := strings.ToUpper(m)
		if p.allowed(key) {
			return key
		}
	}
	return ""
}

func (p *Parser) allowed(key string) bool {
	if len(p.prefixes) == 0 {
		return true
	}
	prefix, _, _ := strings.Cut(key, "-")
	for _, a := range p.prefixes {
		if a == prefix {
			return true
		}
	}
	return false
}

// MaxTagBytes caps a free-form task. It keeps window names, notice titles and
// the popup column readable.
const MaxTagBytes = 64

// Tag normalises user-typed task text. Surrounding whitespace is trimmed and
// inner runs collapse to one space. Text that is, as a whole, an issue key comes
// back as the uppercased key so tickets group consistently; anything else keeps
// its case. Empty text, invalid UTF-8, control characters and text over
// MaxTagBytes are errors.
func (p *Parser) Tag(s string) (string, error) {
	s = strings.TrimSpace(s)
	if err := checkText(s); err != nil {
		return "", err
	}
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > MaxTagBytes {
		return "", fmt.Errorf("the task is %d bytes; the limit is %d", len(s), MaxTagBytes)
	}
	if key := p.Key(s); key != "" && key == strings.ToUpper(s) {
		return key, nil
	}
	return s, nil
}

// checkText is the part of Tag's validation that ReadTag repeats on stored
// text, so the two cannot disagree about what a valid task is. It runs before
// whitespace collapses, because a newline must read as a control character.
func checkText(s string) error {
	if s == "" {
		return errors.New("the task is empty")
	}
	if !utf8.ValidString(s) {
		return errors.New("the task is not valid UTF-8")
	}
	for _, r := range s {
		if clean.Unsafe(r) {
			return errors.New("the task contains a control or bidi-override character")
		}
	}
	return nil
}

// ReadTag returns the task `bosun tag` stored for a session, or "" when the file
// is missing or fails Tag's checks (empty, invalid UTF-8, control characters,
// over MaxTagBytes).
func ReadTag(path string) string {
	b, err := os.ReadFile(path)
	// The +2 is slack for the newline WriteTag appends (and a \r\n). It bounds the
	// work before trimming.
	if err != nil || len(b) > MaxTagBytes+2 {
		return ""
	}
	text := strings.TrimSpace(string(b))
	if checkText(text) != nil || len(text) > MaxTagBytes {
		return ""
	}
	return text
}

// WriteTag stores a task atomically, or removes the tag when text is empty.
func WriteTag(path, text string) error {
	if text == "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	return atomicfile.Write(path, []byte(text+"\n"), 0o600)
}
