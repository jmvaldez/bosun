package hook

import "github.com/jmvaldez/bosun/internal/clean"

// Enum fields collapse to a fixed set; see package clean for the id checks.

func source(s string) string {
	if s == "" {
		return ""
	}
	return clean.OneOf(s, "other", "startup", "resume", "clear", "compact", "fork")
}

func endReason(s string) string {
	if s == "" {
		return ""
	}
	return clean.OneOf(s, "other", "clear", "resume", "logout", "prompt_input_exit", "bypass_permissions_disabled")
}

func isKnownEvent(ev string) bool {
	for _, e := range Events {
		if e == ev {
			return true
		}
	}
	return false
}

func cleanBG(in []BGTask) []BGTask {
	var out []BGTask
	for _, t := range in {
		c := BGTask{ID: clean.Ident(t.ID, 64), Type: clean.Ident(t.Type, 32), Status: clean.Ident(t.Status, 32)}
		if t.Count > 0 && t.Count < 1<<20 {
			c.Count = t.Count
		}
		out = append(out, c)
		if len(out) == 16 {
			break
		}
	}
	return out
}
