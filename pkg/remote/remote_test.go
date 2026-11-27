package remote

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hs3434/agent-sandbox/pkg/file"
	"github.com/hs3434/agent-sandbox/pkg/resource"
)

// testTarget points at the loopback sshd spun up for pipeline tests
// (port 39222, key id_ed25519 — see testdata/README or repo docs).
func testTarget() Target {
	home, _ := os.UserHomeDir()
	return Target{
		Name:   "loopback-test",
		Host:   "127.0.0.1",
		Port:   39222,
		User:   os.Getenv("USER"),
		Key:    filepath.Join(home, ".ssh", "id_ed25519"),
		OSType: "linux",
	}
}

func newRemote(t *testing.T) *Backend {
	t.Helper()
	if testing.Short() {
		t.Skip("pipeline test (requires sshd)")
	}
	b, err := Open()
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { b.Close() })
	return b
}

func connect(b *Backend, t *testing.T) {
	t.Helper()
	if err := b.Connect(testTarget()); err != nil {
		t.Fatalf("connect: %v", err)
	}
}

func TestConnectAndExec(t *testing.T) {
	b := newRemote(t)
	connect(b, t)
	res, err := b.Exec(context.Background(), "loopback-test", "echo remote-ok", ExecOptions{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if res.ExitCode != 0 || !containsStr(res.Stdout, "remote-ok") {
		t.Fatalf("exec result: %+v", res)
	}
}

func TestExecTimeout(t *testing.T) {
	b := newRemote(t)
	connect(b, t)
	start := time.Now()
	res, err := b.Exec(context.Background(), "loopback-test", "sleep 30", ExecOptions{Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if res.ExitReason != "timeout" {
		t.Fatalf("exit_reason = %q", res.ExitReason)
	}
	if time.Since(start) > 15*time.Second {
		t.Fatalf("timeout kill took too long: %v", time.Since(start))
	}
}

func TestSFTPReadWrite(t *testing.T) {
	b := newRemote(t)
	connect(b, t)
	ctx := context.Background()
	// write then read roundtrip (SFTP byte stream, no encoding issues)
	if err := b.WriteFile(ctx, "loopback-test", "/tmp/agent-sandbox-test/roundtrip.txt", "hello\r\nsftp\r\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := b.ReadFile(ctx, "loopback-test", "/tmp/agent-sandbox-test/roundtrip.txt")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != "hello\nsftp\n" {
		t.Fatalf("CRLF not normalized: %q", got)
	}
}

func TestConnectionSelfHealing(t *testing.T) {
	b := newRemote(t)
	connect(b, t)
	// simulate a zombie master: force-close the underlying connection while
	// the backend still thinks it's healthy
	b.zombieForTest("loopback-test")
	// next operation must transparently rebuild and succeed (round-trip
	// probe detects the zombie — AD-028)
	res, err := b.Exec(context.Background(), "loopback-test", "echo healed", ExecOptions{Timeout: 15 * time.Second})
	if err != nil {
		t.Fatalf("exec after zombie: %v", err)
	}
	if !containsStr(res.Stdout, "healed") {
		t.Fatalf("stdout = %q", res.Stdout)
	}
}

func TestEncodingLayerWindows(t *testing.T) {
	// unit-level: the codepage mapping table (inherited semantics, AD-025)
	cases := []struct {
		cp   string
		want string
	}{
		{"936", "gbk"}, {"65001", "utf-8"}, {"1252", "windows-1252"}, {"", "gbk"},
	}
	for _, c := range cases {
		if got := mapCodepage(c.cp); got != c.want {
			t.Errorf("mapCodepage(%q) = %q, want %q", c.cp, got, c.want)
		}
	}
	// GBK decode now lives in pkg/file (unified semantic library, T1)
	if got := string(file.DecodeText([]byte{0xC4, 0xE3, 0xBA, 0xC3})); got != "你好" {
		t.Fatalf("gbk decode via pkg/file = %q", got)
	}
}

func TestSessionCap(t *testing.T) {
	b := newRemote(t)
	connect(b, t)
	// SessionCap default 4: opening beyond the cap must fail fast with a
	// clear error, not queue (guard against conhost leak accumulation)
	var lastErr error
	for i := 0; i < 6; i++ {
		lastErr = b.openSession("loopback-test", fmt.Sprintf("cap-test-%d", i))
	}
	if lastErr == nil || !isSessionCapErr(lastErr) {
		t.Fatalf("want session-cap error after 4 sessions, got %v", lastErr)
	}
}

func TestCapabilitiesHonest(t *testing.T) {
	b := newRemote(t)
	// thin mode: shell+files only — no async, no build, no snapshots
	caps := b.Capabilities()
	for _, c := range caps {
		if c == resource.CapSnapshot {
			t.Fatal("thin mode must not claim snapshot (honest declaration)")
		}
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
