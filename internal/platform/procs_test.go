package platform

import (
	"os"
	"os/exec"
	"testing"
)

func TestAliveRejectsNonPositive(t *testing.T) {
	p := NewProcs()
	for _, pid := range []int{0, -1, -1234} {
		if p.Alive(pid, "") {
			t.Errorf("Alive(%d) = true", pid)
		}
	}
}

func TestAliveSelfAndToken(t *testing.T) {
	p := NewProcs()
	tok, err := p.StartToken(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if tok == "" || tok == "0" {
		t.Fatalf("token = %q", tok)
	}
	if !p.Alive(os.Getpid(), tok) {
		t.Error("self with its own token should be alive")
	}
	if p.Alive(os.Getpid(), tok+"1") {
		t.Error("self with a wrong token must not be alive (pid reuse)")
	}
}

func TestAliveEPERMIsAlive(t *testing.T) {
	// pid 1 belongs to root; kill(1, 0) gives EPERM for a normal user.
	if os.Getuid() == 0 {
		t.Skip("running as root")
	}
	if !NewProcs().Alive(1, "") {
		t.Error("pid 1 should count as alive on EPERM")
	}
}

func TestTokensDifferAcrossProcesses(t *testing.T) {
	cmd := exec.Command("sleep", "5")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	p := NewProcs()
	a, err := p.StartToken(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	b, err := p.StartToken(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Errorf("two live pids share token %q", a)
	}
}

func TestDeadPid(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if NewProcs().Alive(cmd.Process.Pid, "") {
		t.Error("reaped pid reported alive")
	}
}

func TestParent(t *testing.T) {
	p := NewProcs()
	got, err := p.Parent(os.Getpid())
	if err != nil || got != os.Getppid() {
		t.Errorf("Parent(self) = %d, %v; want %d", got, err, os.Getppid())
	}
	if _, err := p.Parent(0); err == nil {
		t.Error("Parent(0) should fail")
	}
}
