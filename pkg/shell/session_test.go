package shell

import (
	"io"
	"regexp"
	"testing"
	"time"
)

var frameRe = regexp.MustCompile(`__SB_END_([0-9a-f]+)__`)

// fakeShell answers each framed command with its payload echo + the true
// END marker carrying the requested exit code.
func fakeShell(stdout io.Writer, stdin io.Reader, exit string) {
	b := make([]byte, 8192)
	for {
		n, err := stdin.Read(b)
		if err != nil {
			return
		}
		in := string(b[:n])
		if m := frameRe.FindStringSubmatch(in); m != nil {
			_, _ = stdout.Write([]byte("out-of:" + in[:8] + "\n"))
			_, _ = stdout.Write([]byte("__SB_END_" + m[1] + "__:" + exit + "\n"))
		}
	}
}

// TestSendBlockingCompletesOnMarker: Send returns when the END marker
// arrives, carrying the marker's exit code.
func TestSendBlockingCompletesOnMarker(t *testing.T) {
	pr, pw := io.Pipe()
	sInR, sInW := io.Pipe() // io.Pipe gives (reader, writer): session's stdin
	s := NewSession("t1", "env", PosixDialect{},
		func(p []byte) error { _, err := sInW.Write(p); return err },
		pr, func() {})
	go fakeShell(pw, sInR, "7")

	out, code, err := s.Send("echo hi", 3*time.Second)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if code != 7 {
		t.Fatalf("exit code = %d, want 7", code)
	}
	if out == "" {
		t.Fatal("output is empty")
	}
	// second send: state carried (no busy leak)
	out2, code2, err := s.Send("echo again", 3*time.Second)
	if err != nil {
		t.Fatalf("send2: %v", err)
	}
	if code2 != 7 {
		t.Fatalf("exit code 2 = %d, want 7", code2)
	}
	if out2 == "" {
		t.Fatal("output 2 is empty")
	}
}

// TestSendTimeoutKeepsSessionAlive: expiry is a fact, not a hang; the
// session stays usable afterwards.
func TestSendTimeoutKeepsSessionAlive(t *testing.T) {
	pr, _ := io.Pipe()
	sInR, sInW := io.Pipe()
	// a shell that never answers
	go func() { _, _ = io.Copy(io.Discard, sInR) }()
	s := NewSession("t2", "env", PosixDialect{},
		func(p []byte) error { _, err := sInW.Write(p); return err },
		pr, func() {})
	_, _, err := s.Send("sleep forever", 300*time.Millisecond)
	if err == nil {
		t.Fatal("want a timeout error")
	}
	if s.Busy() {
		t.Fatal("after timeout the session must not report busy forever — pending must clear")
	}
}
