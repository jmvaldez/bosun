// Package subcost estimates what a running subagent has spent by reading its
// transcript incrementally. The estimate is always marked est.: transcripts
// miss side requests and the final output counts of many subagent requests.
package subcost

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"

	"github.com/jmvaldez/bosun/internal/prices"
)

// Req is one deduped request.
type Req struct {
	Model string `json:"m"`
	In    int64  `json:"i"`
	Out   int64  `json:"o"`
	CR    int64  `json:"cr"`
	C5    int64  `json:"c5"`
	C1    int64  `json:"c1"`
	Web   int64  `json:"w,omitempty"`
	Speed string `json:"s,omitempty"`
	Geo   string `json:"g,omitempty"`
}

// Tracker is the incremental state; it lives in live.json beside the subagent.
type Tracker struct {
	Offset int64          `json:"off"`
	Reqs   map[string]Req `json:"reqs,omitempty"`
}

var assistantMark = []byte(`"type":"assistant"`)

type lineJSON struct {
	RequestID string `json:"requestId"`
	Message   struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage struct {
			In            int64 `json:"input_tokens"`
			Out           int64 `json:"output_tokens"`
			CR            int64 `json:"cache_read_input_tokens"`
			Create        int64 `json:"cache_creation_input_tokens"`
			CacheCreation struct {
				T5 int64 `json:"ephemeral_5m_input_tokens"`
				T1 int64 `json:"ephemeral_1h_input_tokens"`
			} `json:"cache_creation"`
			Tool struct {
				Web int64 `json:"web_search_requests"`
			} `json:"server_tool_use"`
			Speed string `json:"speed"`
			Geo   string `json:"inference_geo"`
		} `json:"usage"`
	} `json:"message"`
}

// Update reads the complete lines past the offset and returns the estimate.
// Requests dedupe on message id plus request id keeping the largest output
// (LESSONS L-33).
func (t *Tracker) Update(path string) float64 {
	if fi, err := os.Stat(path); err == nil && fi.Size() != t.Offset {
		if fi.Size() < t.Offset {
			// Truncated or replaced, as in ledger ingest: start over, or the
			// seek lands past the end and the transcript never counts again.
			t.Offset, t.Reqs = 0, nil
		}
		t.read(path)
	}
	var sum float64
	for _, r := range t.Reqs {
		sum += prices.Cost(r.Model, prices.Usage{Input: r.In, Output: r.Out, CacheRead: r.CR,
			CacheWrite5: r.C5, CacheWrite1: r.C1, WebSearches: r.Web, Speed: r.Speed, Geo: r.Geo}).USD
	}
	return sum
}

func (t *Tracker) read(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	if _, err := f.Seek(t.Offset, io.SeekStart); err != nil {
		return
	}
	if t.Reqs == nil {
		t.Reqs = map[string]Req{}
	}
	r := bufio.NewReaderSize(f, 64<<10)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return // a partial last line (or a read error) waits for the next tick
		}
		t.Offset += int64(len(line))
		if !bytes.Contains(line, assistantMark) {
			continue
		}
		var l lineJSON
		if json.Unmarshal(line, &l) != nil || l.Message.ID == "" || l.Message.Model == "" || l.Message.Model == "<synthetic>" {
			continue
		}
		u := l.Message.Usage
		c5, c1 := u.CacheCreation.T5, u.CacheCreation.T1
		if rest := u.Create - c5 - c1; rest > 0 {
			c5 += rest // untiered writes are priced as 5 minute writes, as in ledger ingest
		}
		key := l.Message.ID + "/" + l.RequestID
		if old, ok := t.Reqs[key]; ok && old.Out >= u.Out {
			continue
		}
		t.Reqs[key] = Req{Model: l.Message.Model, In: u.In, Out: u.Out, CR: u.CR, C5: c5, C1: c1, Web: u.Tool.Web, Speed: u.Speed, Geo: u.Geo}
	}
}
