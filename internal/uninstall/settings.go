package uninstall

import "encoding/json"

func statusLineCommand(settings []byte) string {
	var s struct {
		StatusLine struct {
			Command string `json:"command"`
		} `json:"statusLine"`
	}
	_ = json.Unmarshal(settings, &s)
	return s.StatusLine.Command
}
