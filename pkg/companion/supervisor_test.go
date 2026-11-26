package companion

import (
	"fmt"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// forkSleep starts a detached sleep in its own process group (the real
// async shape: setsid semantics via Setpgid) and returns its pgid.
func forkSleep(t *testing.T, secs int) int {
	t.Helper()
	cmd := exec.Command("/bin/sleep", fmt.Sprintf("%d", secs))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	pgid := cmd.Process.Pid
	t.Cleanup(func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })
	go func() { _ = cmd.Wait() }()
	return pgid
}

func TestExecAsyncIdempotency(t *testing.T) {
	s, err := NewSupervisor(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h1, existed, err := s.ExecAsync("req-1", "sleep 5", "loop", 999001)
	if err != nil || existed {
		t.Fatalf("first: h=%s existed=%v err=%v", h1, existed, err)
	}
	h2, existed, _ := s.ExecAsync("req-1", "sleep 5", "loop", 999002)
	if !existed || h2 != h1 {
		t.Fatalf("same request-id must return the SAME handle: got %s/%v", h2, existed)
	}
}

func TestTableCapTyped(t *testing.T) {
	s, _ := NewSupervisor(t.TempDir())
	for i := 0; i < ProcTableCap; i++ {
		if _, _, err := s.ExecAsync(fmt.Sprintf("r-%d", i), "x", "", 2000000+i); err != nil {
			t.Fatalf("row %d: %v", i, err)
		}
	}
	_, _, err := s.ExecAsync("overflow", "x", "", 999999)
	if err == nil || wantContains(err.Error(), "process table full") == false {
		t.Fatalf("cap refusal: %v", err)
	}
}

func wantContains(s, sub string) bool { return len(s) >= len(sub) && containsStr(s, sub) }
func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestBootRescanAdoptAndUnknown(t *testing.T) {
	dir := t.TempDir()
	// seed: one alive row (a real sleep), one dead row
	s1, _ := NewSupervisor(dir)
	live := forkSleep(t, 30)
	hLive, _, _ := s1.ExecAsync("", "sleep 30", "", live)
	hDead, _, _ := s1.ExecAsync("", "true", "", 4000000) // pgid that never existed
	_ = hDead
	s1.Stop()
	// simulate companion restart
	s2, err := NewSupervisor(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := s2.Wait(hLive)
	if !ok || p.State != ProcRunning {
		t.Fatalf("live row must be adopted as running: %+v ok=%v", p, ok)
	}
	pd, ok := s2.Wait(hDead)
	if !ok || pd.State != ProcUnknown {
		t.Fatalf("dead-at-boot row must be exit_unknown: %+v", pd)
	}
	// cleanup the sleep
	syscall.Kill(-live, syscall.SIGKILL)
}

func TestReapRealProcess(t *testing.T) {
	s, _ := NewSupervisor(t.TempDir())
	s.Start()
	defer s.Stop()
	pid := forkSleep(t, 1) // dies on its own in ~1s
	h, _, err := s.ExecAsync("", "sleep 1", "", pid)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		p, ok := s.Wait(h)
		if ok && p.State != ProcRunning {
			if p.State != ProcUnknown {
				t.Fatalf("natural death without wrapper = exit_unknown, got %s", p.State)
			}
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("reaper never resolved the terminal state")
}

func TestKillGroup(t *testing.T) {
	s, _ := NewSupervisor(t.TempDir())
	s.Start()
	defer s.Stop()
	pid := forkSleep(t, 60)
	h, _, _ := s.ExecAsync("", "sleep 60", "", pid)
	s.MarkKilledByUs(h)
	if err := s.Kill(h); err != nil {
		t.Fatalf("kill: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		p, _ := s.Wait(h)
		if p != nil && p.State == ProcSignaled {
			return // honest: WE killed it → signaled
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatal("kill → reaper never marked signaled")
}

func TestListOrderAndShape(t *testing.T) {
	s, _ := NewSupervisor(t.TempDir())
	s.ExecAsync("a", "x", "", 5000001)
	time.Sleep(2 * time.Millisecond)
	s.ExecAsync("b", "y", "", 5000002)
	rows := s.List()
	if len(rows) != 2 || rows[0].RequestID != "a" || rows[1].RequestID != "b" {
		t.Fatalf("list order: %+v", rows)
	}
}
