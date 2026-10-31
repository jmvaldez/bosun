package statusline

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmvaldez/bosun/internal/paths"
	"github.com/jmvaldez/bosun/internal/platform"
)

func adapters(t *testing.T) platform.Adapters {
	t.Helper()
	root := t.TempDir()
	return platform.Adapters{
		Paths: paths.Paths{State: filepath.Join(root, "state")},
		Clock: platform.SystemClock{},
	}
}

const sid = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

func stdinJSON(cost float64) []byte {
	b, _ := json.Marshal(map[string]any{
		"session_id": sid, "session_name": "SENTINEL-TEXT-FROM-PROMPT", "version": "2.1.309",
		"model":     map[string]any{"id": "claude-haiku-5-5", "display_name": "Haiku"},
		"cost":      map[string]any{"total_cost_usd": cost, "total_duration_ms": 1234},
		"workspace": map[string]any{"current_dir": "/x"},
	})
	return b
}

func snapshot(t *testing.T, ad platform.Adapters) Snapshot {
	t.Helper()
	b, err := os.ReadFile(ad.Paths.Session(sid) + "/statusline.json")
	if err != nil {
		t.Fatal(err)
	}
	var s Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// LESSONS L-35.
func TestBank(t *testing.T) {
	tests := []struct {
		name                       string
		prevMax, prevBanked, total float64
		wantMax, wantBanked        float64
	}{
		{"first sample", 0, 0, 1.25, 1.25, 0},
		{"growth", 5, 0, 5.5, 5.5, 0},
		{"small dip does not bank", 5.00, 0, 4.98, 5.00, 0},
		{"just above half does not bank", 5, 0, 2.6, 5, 0},
		{"below half banks", 5, 0, 2.4, 2.4, 5},
		{"login reset banks the max", 5.00, 0, 0.02, 0.02, 5.00},
		{"reset to zero", 5, 1, 0, 0, 6},
		{"sub-cent after small max", 0.3, 0, 0.005, 0.005, 0.3},
		{"zero stays zero", 0, 0, 0, 0, 0},
		{"second reset accumulates", 0.5, 5, 0.01, 0.01, 5.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, b := Bank(tt.prevMax, tt.prevBanked, tt.total)
			if m != tt.wantMax || b != tt.wantBanked {
				t.Errorf("Bank = (%v, %v), want (%v, %v)", m, b, tt.wantMax, tt.wantBanked)
			}
		})
	}
}

func TestSequenceOnDisk(t *testing.T) {
	ad := adapters(t)
	for _, c := range []float64{3.0, 5.0, 4.98, 5.1, 0.02, 0.4} {
		Run(ad, nil, bytes.NewReader(stdinJSON(c)), &bytes.Buffer{}, &bytes.Buffer{})
	}
	s := snapshot(t, ad)
	if s.CostUSD != 0.4 || s.BankedUSD != 5.1 || s.MaxUSD != 0.4 {
		t.Errorf("snapshot = %+v", s)
	}
	if s.Model != "claude-haiku-5-5" || s.Version != "2.1.309" || s.DurationMS != 1234 {
		t.Errorf("snapshot = %+v", s)
	}
}

func TestSnapshotHasNoPromptDerivedText(t *testing.T) {
	ad := adapters(t)
	Run(ad, nil, bytes.NewReader(stdinJSON(1)), &bytes.Buffer{}, &bytes.Buffer{})
	filepath.WalkDir(ad.Paths.State, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if b, _ := os.ReadFile(p); strings.Contains(string(b), "SENTINEL") {
				t.Errorf("session_name leaked into %s", p)
			}
		}
		return nil
	})
}

func TestBadInputStillRunsWrappedCommand(t *testing.T) {
	ad := adapters(t)
	for _, in := range []string{"", "not json", `{"session_id":"../../x","cost":{"total_cost_usd":1}}`, `{"session_id":"` + sid + `"}`} {
		var out bytes.Buffer
		Run(ad, []string{"--", "cat"}, strings.NewReader(in), &out, &bytes.Buffer{})
		if out.String() != in {
			t.Errorf("input %q: output %q", in, out.String())
		}
	}
	if _, err := os.Stat(ad.Paths.Sessions()); !os.IsNotExist(err) {
		t.Errorf("bad input created state: %v", err)
	}
}

type panicClock struct{}

func (panicClock) Now() time.Time { panic("forced") }

func TestPanicInRecordingStillRunsWrappedCommand(t *testing.T) {
	ad := adapters(t)
	ad.Clock = panicClock{}
	var out bytes.Buffer
	if code := Run(ad, []string{"--", "cat"}, bytes.NewReader(stdinJSON(1)), &out, &bytes.Buffer{}); code != 0 {
		t.Fatalf("code = %d", code)
	}
	if !bytes.Equal(out.Bytes(), stdinJSON(1)) {
		t.Errorf("wrapped command did not run: %q", out.String())
	}
}

// The output must be byte-identical to running the real script unwrapped:
// no trailing newline, ANSI escapes, a NUL, and stderr kept separate.
func TestOutputByteIdentical(t *testing.T) {
	script := filepath.Join(t.TempDir(), "sl.sh")
	body := "#!/bin/sh\n" +
		"in=$(cat)\n" +
		"printf '\\033[1;32m%s\\033[0m|' \"$(printf '%s' \"$in\" | wc -c | tr -d ' ')\"\n" +
		"printf 'a\\000b\\377 \\303\\251'\n" +
		"echo oops >&2\n"
	os.WriteFile(script, []byte(body), 0o755)
	in := stdinJSON(2.5)

	cmd := exec.Command(script)
	cmd.Stdin = bytes.NewReader(in)
	want, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}

	ad := adapters(t)
	var got, errw bytes.Buffer
	Run(ad, []string{"--", script}, bytes.NewReader(in), &got, &errw)
	if !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("not byte-identical\n got %q\nwant %q", got.Bytes(), want)
	}
	if errw.String() != "oops\n" {
		t.Errorf("stderr = %q", errw.String())
	}
	if snapshot(t, ad).CostUSD != 2.5 {
		t.Error("snapshot not written")
	}
}

func TestFailingWrappedCommandStillExitsZero(t *testing.T) {
	ad := adapters(t)
	for _, argv := range [][]string{{"--", "false"}, {"--", "/nonexistent/binary"}, {"--"}, nil} {
		if code := Run(ad, argv, bytes.NewReader(stdinJSON(1)), &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
			t.Errorf("%v: code %d", argv, code)
		}
	}
}

// The captured stdin from Claude Code 2.1.309 (redacted) must decode with the
// real key names.
func TestCapturedStdinFixture(t *testing.T) {
	b, err := os.ReadFile("../../testdata/statusline-stdin.json")
	if err != nil {
		t.Fatal(err)
	}
	b = bytes.ReplaceAll(b, []byte("<session-1>"), []byte(sid))
	ad := adapters(t)
	var out bytes.Buffer
	Run(ad, []string{"--", "cat"}, bytes.NewReader(b), &out, &bytes.Buffer{})
	if !bytes.Equal(out.Bytes(), b) {
		t.Error("fixture not passed through byte-identical")
	}
	s := snapshot(t, ad)
	if s.CostUSD != 0.00414144 || s.Model != "claude-haiku-5-5" || s.Version != "2.1.309" || s.DurationMS != 213504 {
		t.Errorf("snapshot = %+v", s)
	}
	if pct, ok := s.Ctx(); !ok || pct != 4 || s.CtxSize != 1000000 {
		t.Errorf("ctx = %v, %v, size %d; want 4%% of 1000000", pct, ok, s.CtxSize)
	}
}

// Absent, null and full context fill: absent stays unknown, never 0.
func TestContextFill(t *testing.T) {
	fixture, err := os.ReadFile("../../testdata/statusline-stdin-noctx.json")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		raw    []byte
		want   float64
		wantOK bool
	}{
		{"absent", fixture, 0, false},
		{"null", withContext(t, `{"used_percentage": null, "context_window_size": 200000}`), 0, false},
		{"full", withContext(t, `{"used_percentage": 100.0, "context_window_size": 200000}`), 100, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ad := adapters(t)
			raw := bytes.ReplaceAll(c.raw, []byte("<session-1>"), []byte(sid))
			Run(ad, nil, bytes.NewReader(raw), &bytes.Buffer{}, &bytes.Buffer{})
			pct, ok := snapshot(t, ad).Ctx()
			if ok != c.wantOK || pct != c.want {
				t.Errorf("Ctx() = %v, %v; want %v, %v", pct, ok, c.want, c.wantOK)
			}
		})
	}
}

// withContext returns the no-context fixture with a context_window object added.
func withContext(t *testing.T, window string) []byte {
	t.Helper()
	b, err := os.ReadFile("../../testdata/statusline-stdin-noctx.json")
	if err != nil {
		t.Fatal(err)
	}
	b = bytes.ReplaceAll(b, []byte("<session-1>"), []byte(sid))
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	m["context_window"] = json.RawMessage(window)
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
