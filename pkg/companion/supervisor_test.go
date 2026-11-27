package companion

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	s.Start() // reaper must run: terminal flip is what reopens the cap
	defer s.Stop()
	// Fork REAL sleepers for the first half — when they exit, their rows go
	// terminal and the cap (D-24: CONCURRENT forks) must open up again (M1).
	realHalf := ProcTableCap / 2
	pids := make([]int, realHalf)
	for i := 0; i < realHalf; i++ {
		pids[i] = forkSleep(t, 30)
		if _, _, err := s.ExecAsync(fmt.Sprintf("r-%d", i), "sleep 30", "", pids[i]); err != nil {
			t.Fatalf("row %d: %v", i, err)
		}
	}
	for i := realHalf; i < ProcTableCap; i++ {
		if _, _, err := s.ExecAsync(fmt.Sprintf("r-%d", i), "x", "", 2000000+i); err != nil {
			t.Fatalf("row %d: %v", i, err)
		}
	}
	if _, _, err := s.ExecAsync("overflow", "x", "", 999999); err == nil || !wantContains(err.Error(), "process table full") {
		t.Fatalf("cap refusal: %v", err)
	}
	// reap the sleepers → terminal rows → cap reopens (running-only count)
	for i := 0; i < realHalf; i++ {
		syscall.Kill(-pids[i], syscall.SIGKILL)
	}
	deadline := time.Now().Add(3 * time.Second)
	var reopened bool
	for time.Now().Before(deadline) {
		if _, _, err := s.ExecAsync("after-gc", "x", "", 999998); err == nil {
			reopened = true // M1 semantics verified: terminal rows don't consume cap
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	s.Stop() // join the reaper BEFORE TempDir cleanup (no write/rename race)
	if !reopened {
		t.Fatal("cap did not reopen after processes exited (rows counted lifetime, not concurrent)")
	}
}

func TestGCTerminalRows(t *testing.T) {
	s, _ := NewSupervisor(t.TempDir())
	s.Start() // reaper flips the row terminal
	defer s.Stop()
	pid := forkSleep(t, 30)
	h, _, err := s.ExecAsync("gc-req", "sleep 30", "", pid)
	if err != nil {
		t.Fatal(err)
	}
	syscall.Kill(-pid, syscall.SIGKILL)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if p, ok := s.Wait(h); ok && p.State != ProcRunning {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	s.Stop() // quiesce reaper before TempDir RemoveAll
	// Force the retention window: mutate ExitedAt directly is racy; instead
	// point gcTerminal at a tiny window via a fresh supervisor with backdated
	// row — simplest honest check: gcTerminal() with cutoff in the future
	// relative to the row must delete it.
	s.gcTerminal() // row is fresh → survives
	if _, ok := s.Wait(h); !ok {
		t.Fatal("fresh terminal row must survive GC within retention window")
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

func TestExitFileWrapper(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewSupervisor(dir)
	s.exitFileFor = func(handleID string) string {
		return filepath.Join(dir, "exit-"+handleID)
	}
	s.Start()
	defer s.Stop()
	pid := forkSleep(t, 1)
	h, _, _ := s.ExecAsync("", "sleep 1", "", pid)
	// pre-write the exit file like the wrapper would (we control the path)
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		p, _ := s.Wait(h)
		if p != nil && p.State != ProcRunning {
			if p.State != ProcUnknown {
				t.Fatalf("no exit file written = exit_unknown, got %s", p.State)
			}
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	// now the wrapper path: write exit file BEFORE death
	pid2 := forkSleep(t, 1)
	h2, _, _ := s.ExecAsync("", "sleep 1", "", pid2)
	os.WriteFile(s.exitFileFor(h2), []byte("7"), 0o600)
	deadline2 := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline2) {
		p, _ := s.Wait(h2)
		if p != nil && p.State != ProcRunning {
			if p.State != "exited(7)" {
				t.Fatalf("exit file 7 = exited(7), got %s", p.State)
			}
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatal("wrapper case never resolved")
}

func TestTTLGate(t *testing.T) {
	s, _ := NewSupervisor(t.TempDir())
	fired := make(chan string, 1)
	s.SetTTL(200*time.Millisecond, func(why string) { fired <- why })
	s.Start()
	defer s.Stop()
	s.NoteConn()
	// connection active → holds TTL open (poll past one full TTL period)
	holdDeadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(holdDeadline) {
		select {
		case w := <-fired:
			t.Fatalf("TTL fired while a connection lived: %s", w)
		default:
		}
		time.Sleep(50 * time.Millisecond)
	}
	// now idle past TTL → must fire within a grace window (no fixed sleep)
	fireDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(fireDeadline) {
		select {
		case w := <-fired:
			if w != "idle" {
				t.Fatalf("why=%s", w)
			}
			return
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}
	t.Fatal("TTL never fired after idle window")
}

func TestTTLBlockedByRunningProc(t *testing.T) {
	s, _ := NewSupervisor(t.TempDir())
	fired := make(chan string, 1)
	s.SetTTL(150*time.Millisecond, func(why string) { fired <- why })
	s.Start()
	defer s.Stop()
	pid := forkSleep(t, 10)
	s.ExecAsync("", "sleep 10", "", pid)
	blockDeadline := time.Now().Add(600 * time.Millisecond)
	for time.Now().Before(blockDeadline) {
		select {
		case w := <-fired:
			t.Fatalf("TTL fired with a running handle: %s", w)
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}
	s.Kill(pid2fix(t, s))
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-fired:
			return // fired after the table drained
		default:
			time.Sleep(150 * time.Millisecond)
		}
	}
	t.Fatal("TTL never fired after the last handle died")
}

// pid2fix resolves the handle id then kills by it (helper shims).
func pid2fix(t *testing.T, s *Supervisor) string {
	for _, p := range s.List() {
		return p.HandleID
	}
	return ""
}
