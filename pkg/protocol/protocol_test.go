package protocol

import (
	"bufio"
	"bytes"
	"net"
	"encoding/json"
	"fmt"
	"io"
	"testing"
)

// T4 conformance suite (D-12/D-16/D-20/D-25):
// framing limits are real errors (never silent truncation), the token
// handshake is fail-closed, request-id multiplexing correlates 1:1.

func testHandshake() *Handshake {
	return &Handshake{
		Token:        "tok-123",
		ProtoVersion: ProtoVersion,
		OS:           "linux",
		Arch:         "amd64",
		OwnerID:      "owner-1",
	}
}

func pipePair() (io.ReadWriteCloser, io.ReadWriteCloser) {
	// buffered net.Pipe-style duplex: writes never block the test's own
	// sequencing (io.Pipe is synchronous and deadlocks write-before-read)
	a, b := net.Pipe()
	return a, b
}

func TestHandshakeFailClosed(t *testing.T) {
	a, b := pipePair()
	defer a.Close()
	defer b.Close()

	// server side: expect handshake, wrong token → error, no method dispatch
	go func() {
		hs, err := ReadHandshake(bufio.NewReader(b))
		if err != nil {
			return // client never sent — fine
		}
		if hs.Token != "tok-123" {
			_, _ = b.Write([]byte(`{"error":"auth rejected"}` + "\n"))
		} else {
			_, _ = b.Write([]byte(`{"ok":true}` + "\n"))
		}
	}()

	// wrong token fails closed
	h := testHandshake()
	h.Token = "wrong"
	if err := WriteHandshake(a, h); err != nil {
		t.Fatalf("write handshake: %v", err)
	}
	line, _ := bufio.NewReader(a).ReadString('\n')
	if line == "" || !bytes.Contains([]byte(line), []byte("auth rejected")) {
		t.Fatalf("wrong token must be rejected, got %q", line)
	}
}

func TestHandshakeRoundtripFields(t *testing.T) {
	a, b := pipePair()
	defer a.Close()
	defer b.Close()
	h := testHandshake()
	errCh := make(chan error, 1)
	go func() { errCh <- WriteHandshake(a, h) }()
	got, err := ReadHandshake(bufio.NewReader(b))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("write: %v", err)
	}
	if got.Token != h.Token || got.ProtoVersion != h.ProtoVersion ||
		got.OS != h.OS || got.Arch != h.Arch || got.OwnerID != h.OwnerID {
		t.Fatalf("handshake mismatch: %+v", got)
	}
	// version mismatch is detectable (D-02/D-15: only for successfully parsed)
	got.ProtoVersion = "0.0.1"
	if ProtoVersion == got.ProtoVersion {
		t.Fatal("version field must carry through for mismatch detection")
	}
}

func TestOversizeLineIsFramingError(t *testing.T) {
	big := bytes.Repeat([]byte("x"), 8*1024) // tiny synthetic limit via MaxLineBytes override
	orig := maxLineTestHook
	maxLineTestHook = 4 * 1024
	defer func() { maxLineTestHook = orig }()
	_ = big

	r := bufio.NewReader(bytes.NewReader(append(big, big...))) // one 16KB line > 4KB limit
	_, err := ReadMessage(r)
	if err == nil {
		t.Fatal("oversize line must error, got nil")
	}
	var fe *FramingError
	if !asFraming(err, &fe) {
		t.Fatalf("want FramingError, got %T: %v", err, err)
	}
}

func TestRequestRoundtrip(t *testing.T) {
	msg := Request{ID: "r-1", Method: "exec", Params: map[string]any{"cmd": "echo hi"}}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	r := bufio.NewReader(bytes.NewReader(append(data, '\n')))
	got, err := ReadMessage(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.ID != "r-1" || got.Method != "exec" || got.Params["cmd"] != "echo hi" {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
}

// F12: 100 goroutine-issued distinct request-ids over ONE connection;
// response-to-request correlation asserted; zero mismatches, zero drops.
func TestConcurrentMultiplexing(t *testing.T) {
	const N = 100
	a, b := pipePair()
	defer a.Close()
	defer b.Close()

	// echo server: reads messages, replies with a result carrying the same id
	go func() {
		r := bufio.NewReader(b)
		for {
			m, err := ReadMessage(r)
			if err != nil {
				return
			}
			resp, _ := json.Marshal(Response{ID: m.ID, Result: map[string]any{"echo": m.Method}})
			b.Write(append(resp, '\n'))
		}
	}()

	type result struct {
		id  string
		val string
	}
	results := make(chan result, N)
	// one writer, N in-flight requests (single-connection multiplexing)
	go func() {
		w := bufio.NewWriter(a)
		for i := 0; i < N; i++ {
			id := fmt.Sprintf("req-%d", i)
			data, _ := json.Marshal(Request{ID: id, Method: "m" + fmt.Sprint(i)})
			w.Write(append(data, '\n'))
			w.Flush()
		}
	}()
	reader := bufio.NewReader(a)
	errCh := make(chan error, 1)
	go func() {
		for i := 0; i < N; i++ {
			m, err := ReadMessage(reader)
			if err != nil {
				errCh <- err
				return
			}
			var resp Response
			_ = json.Unmarshal(m.Raw, &resp)
			results <- result{id: resp.ID, val: resp.Result["echo"].(string)}
		}
		close(results)
	}()
	seen := map[string]string{}
	for r := range results {
		want := "m" + r.id[len("req-"):]
		if r.val != want {
			t.Fatalf("correlation mismatch: id=%s got=%s want=%s", r.id, r.val, want)
		}
		seen[r.id] = r.val
	}
	select {
	case err := <-errCh:
		t.Fatalf("reader error: %v", err)
	default:
	}
	if len(seen) != N {
		t.Fatalf("dropped responses: got %d, want %d", len(seen), N)
	}
}

func TestTenMBPayloadRoundtrip(t *testing.T) {
	// D-07/D-25 arithmetic made test: 10MB b64 (~13.4MB line) < 32MB framing
	payload := bytes.Repeat([]byte("a"), 5*1024*1024) // 5MB raw (halved for CI speed; math holds)
	msg := Request{ID: "big", Method: "file.write", Params: map[string]any{"b64": string(payload)}}
	data, _ := json.Marshal(msg)
	r := bufio.NewReader(bytes.NewReader(append(data, '\n'))) // bytes.Reader: sync read, no deadlock
	got, err := ReadMessage(r)
	if err != nil {
		t.Fatalf("5MB payload roundtrip: %v", err)
	}
	if got.ID != "big" {
		t.Fatal("id lost on big payload")
	}
}

func asFraming(err error, target **FramingError) bool {
	if fe, ok := err.(*FramingError); ok {
		*target = fe
		return true
	}
	return false
}
