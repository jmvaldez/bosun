package runner

import (
	"strings"
	"testing"
	"time"
)

func TestRunTimesOut(t *testing.T) {
	start := time.Now()
	_, err := Exec{}.Run(100*time.Millisecond, "sleep", "30")
	if err == nil {
		t.Fatal("want error from killed child")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %v", d)
	}
}

func TestRunOutput(t *testing.T) {
	out, err := Exec{}.Run(time.Second, "echo", "hi")
	if err != nil || string(out) != "hi\n" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestErrorCarriesStderr(t *testing.T) {
	_, err := Exec{}.Run(time.Second, "sh", "-c", "echo refused because of reasons >&2; exit 3")
	if err == nil || !strings.Contains(err.Error(), "refused because of reasons") {
		t.Errorf("err = %v", err)
	}
}

func TestStreamStopsEarlyAndKillsTheChild(t *testing.T) {
	start := time.Now()
	var got []string
	err := Exec{}.Stream(10*time.Second, "sh", []string{"-c", "echo one; echo two; sleep 30"}, func(l string) bool {
		got = append(got, l)
		return l == "two"
	})
	if err != nil || strings.Join(got, ",") != "one,two" {
		t.Fatalf("got %v err %v", got, err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %v: the sleeping child was not killed", d)
	}
}

func TestStreamHonorsTheDeadline(t *testing.T) {
	start := time.Now()
	Exec{}.Stream(300*time.Millisecond, "sleep", []string{"30"}, func(string) bool { return false })
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("took %v", d)
	}
}
