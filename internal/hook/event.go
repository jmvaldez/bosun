package hook

import (
	"encoding/json"
	"errors"
	"unicode/utf8"
)

const (
	// MaxLine is the cap on one events.jsonl line, newline included (SPEC
	// section 3: max 2 KiB). appendLine writes it in one O_APPEND write.
	MaxLine = 2048
	// maxField caps every string before marshaling (LESSONS L-20).
	maxField = 256
)

// Event is one line of sessions/<id>/events.jsonl.
type Event struct {
	V         int         `json:"v"`
	TS        string      `json:"ts"`
	Ev        string      `json:"ev"`
	SID       string      `json:"sid"`
	AID       string      `json:"aid,omitempty"`
	AType     string      `json:"atype,omitempty"`
	PID       int         `json:"pid,omitempty"`
	Tmux      *TmuxLoc    `json:"tmux,omitempty"`
	PromptID  string      `json:"prompt_id,omitempty"`
	Tool      string      `json:"tool,omitempty"`
	TUID      string      `json:"tuid,omitempty"`
	NType     string      `json:"ntype,omitempty"`
	BG        []BGTask    `json:"bg,omitempty"`
	Src       string      `json:"src,omitempty"`
	Reason    string      `json:"reason,omitempty"`
	Start     *StartExtra `json:"start,omitempty"`
	Truncated bool        `json:"truncated,omitempty"`
}

// TmuxLoc is the pane locator: socket path, server pid, pane id.
type TmuxLoc struct {
	Sock string `json:"sock"`
	SPID int    `json:"spid"`
	Pane string `json:"pane,omitempty"`
}

// StartExtra is the extra block only SessionStart carries.
type StartExtra struct {
	Cwd        string `json:"cwd,omitempty"`
	Transcript string `json:"transcript,omitempty"`
	Repo       string `json:"repo,omitempty"`
	Branch     string `json:"branch,omitempty"`
	Task       string `json:"task,omitempty"`
	TaskSrc    string `json:"task_src,omitempty"`
	Account    string `json:"acct,omitempty"`
	StartTok   string `json:"stok,omitempty"`
	Entrypoint string `json:"entrypoint,omitempty"`
	// HandoffFrom is the session this one continues, set by `bosun handoff`.
	HandoffFrom string `json:"handoff_from,omitempty"`
}

// encode returns one line (with trailing newline) no longer than MaxLine. When
// the line is too long it drops the extras and then the background list, in
// that order, and sets truncated, so what lands on disk is always valid JSON
// with the locator intact. It never cuts bytes off encoded JSON.
func (e Event) encode() ([]byte, error) {
	e.clip()
	for step := 0; ; step++ {
		b, err := json.Marshal(e)
		if err != nil {
			return nil, err
		}
		if len(b)+1 <= MaxLine {
			return append(b, '\n'), nil
		}
		switch step {
		case 0:
			e.Start = nil
		case 1:
			e.BG = nil
		default:
			return nil, errTooLong
		}
		e.Truncated = true
	}
}

var errTooLong = errors.New("hook: event line too long")

func (e *Event) clip() {
	for _, s := range []*string{&e.AID, &e.AType, &e.PromptID, &e.Tool, &e.TUID, &e.NType, &e.Src, &e.Reason} {
		*s = clipString(*s)
	}
	if e.Tmux != nil {
		e.Tmux.Sock = clipString(e.Tmux.Sock)
	}
	for i := range e.BG {
		b := &e.BG[i]
		b.ID, b.Type, b.Status = clipString(b.ID), clipString(b.Type), clipString(b.Status)
	}
	if x := e.Start; x != nil {
		for _, s := range []*string{&x.Cwd, &x.Transcript, &x.Repo, &x.Branch, &x.Task, &x.TaskSrc, &x.Account, &x.StartTok, &x.Entrypoint, &x.HandoffFrom} {
			*s = clipString(*s)
		}
	}
}

// clipString cuts at a rune boundary so the result is still valid UTF-8.
func clipString(s string) string {
	if len(s) <= maxField {
		return s
	}
	n := maxField
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
