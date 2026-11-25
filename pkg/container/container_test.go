package container

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hs3434/agent-sandbox/pkg/resource"
)

// newBackend builds a Backend against the local docker daemon.
// Pipeline tests require docker; unit-level fileops tests do not.
func newBackend(t *testing.T) *Backend {
	t.Helper()
	if testing.Short() {
		t.Skip("pipeline test (requires docker daemon)")
	}
	b, err := Open(Options{WorkHome: t.TempDir()})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { b.Close() })
	return b
}

func spec(name string) resource.ExecEnvSpec {
	return resource.ExecEnvSpec{Image: "busybox:latest"}
}

func TestCapabilities(t *testing.T) {
	b := newBackend(t)
	caps := b.Capabilities()
	want := map[resource.Capability]bool{resource.CapShell: false, resource.CapFiles: false}
	for _, c := range caps {
		want[c] = true
	}
	for c, ok := range want {
		if !ok {
			t.Fatalf("capability %q missing", c)
		}
	}
}

func TestCreateAndRemove(t *testing.T) {
	b := newBackend(t)
	ctx := context.Background()
	meta, err := b.Create(ctx, "t-create-1", spec("t-create-1"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if meta.Status != resource.StatusRunning {
		t.Fatalf("status = %s, want running", meta.Status)
	}
	// workdir invariant: work_home/<name> exists and is the bind source
	if _, err := os.Stat(filepath.Join(b.workHome, "t-create-1")); err != nil {
		t.Fatalf("workdir not derived from name: %v", err)
	}
	if err := b.Remove(ctx, meta.ID, false); err != nil {
		t.Fatalf("remove: %v", err)
	}
	// default remove keeps the workdir (purge=false)
	if _, err := os.Stat(filepath.Join(b.workHome, "t-create-1")); err != nil {
		t.Fatal("workdir must survive default remove")
	}
}

func TestBlockingExec(t *testing.T) {
	b := newBackend(t)
	ctx := context.Background()
	meta, _ := b.Create(ctx, "t-exec-1", spec("t-exec-1"))
	defer b.Remove(ctx, meta.ID, true)

	res, err := b.Exec(ctx, meta.ID, "echo hello-sandbox")
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d", res.ExitCode)
	}
	if want := "hello-sandbox"; !contains(res.Stdout, want) {
		t.Fatalf("stdout = %q, want contains %q", res.Stdout, want)
	}
}

func TestBlockingExecTimeoutKillsProcessGroup(t *testing.T) {
	b := newBackend(t)
	ctx := context.Background()
	meta, _ := b.Create(ctx, "t-timeout-1", spec("t-timeout-1"))
	defer b.Remove(ctx, meta.ID, true)

	start := time.Now()
	res, err := b.Exec(ctx, meta.ID, "sleep 30", ExecOptions{Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if res.ExitReason != "timeout" {
		t.Fatalf("exit_reason = %q, want timeout", res.ExitReason)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("timeout kill took %v — process group not killed", elapsed)
	}
}

func TestAsyncExecHandleLedger(t *testing.T) {
	b := newBackend(t)
	ctx := context.Background()
	meta, _ := b.Create(ctx, "t-async-1", spec("t-async-1"))
	defer b.Remove(ctx, meta.ID, true)

	h, err := b.ExecAsync(ctx, meta.ID, "sleep 60")
	if err != nil {
		t.Fatalf("exec_async: %v", err)
	}
	if h.State != HandleRunning {
		t.Fatalf("state = %s", h.State)
	}
	// kill via process group (userland-equivalent path the platform uses internally)
	if err := b.Kill(ctx, meta.ID, h.PGID); err != nil {
		t.Fatalf("kill: %v", err)
	}
	// kernel reaping: Wait marks the handle dead promptly
	deadline := time.Now().Add(5 * time.Second)
	for {
		h2, err := b.WaitHandle(ctx, h.ID)
		if err != nil {
			t.Fatalf("wait_handle: %v", err)
		}
		if h2.State == HandleDead {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("handle not reaped within 5s of kill")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestFileOperations(t *testing.T) {
	b := newBackend(t)
	ctx := context.Background()
	meta, _ := b.Create(ctx, "t-file-1", spec("t-file-1"))
	defer b.Remove(ctx, meta.ID, true)

	// write (atomic + normalized)
	if err := b.WriteFile(ctx, meta.ID, "note.txt", "line1\r\nline2\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := b.ReadFile(ctx, meta.ID, "note.txt")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != "line1\nline2\n" {
		t.Fatalf("CRLF not normalized: %q", got)
	}
}

func TestContainerBuildAndRecreate(t *testing.T) {
	b := newBackend(t)
	ctx := context.Background()
	meta, _ := b.Create(ctx, "t-build-1", spec("t-build-1"))
	defer b.Remove(ctx, meta.ID, true)

	dockerfile := "FROM busybox:latest\nRUN echo custom > /opt-marker\n"
	if err := b.WriteFile(ctx, meta.ID, "Dockerfile", dockerfile); err != nil {
		t.Fatalf("write dockerfile: %v", err)
	}
	if err := b.Build(ctx, "t-build-1", BuildOptions{Tag: "agent-sandbox-test:t-build-1"}); err != nil {
		t.Fatalf("build: %v", err)
	}
	// path constraint: dockerfile outside workdir must be rejected
	err := b.Build(ctx, "t-build-1", BuildOptions{Tag: "x:y", Dockerfile: "../escape"})
	if err == nil {
		t.Fatal("path escape must be rejected (constraint: resolution ⊆ workdir)")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		(func() bool {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		})())
}
