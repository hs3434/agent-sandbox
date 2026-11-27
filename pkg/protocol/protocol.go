// Package protocol is the sandboxd ⇔ sandboxd-server wire contract (T4,
// D-12/D-16/D-20/D-25): JSON-lines with request-id correlation, a fail-closed
// token handshake, and hard framing limits (32MB lines carry the 10MB file
// cap base64-inflated). Every limit violation is a FramingError — silence is
// the one thing this layer must never produce.
package protocol

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ProtoVersion rides the handshake (D-02): mismatch (successfully parsed) is
// the only reinstall trigger (D-15); corrupt handshakes degrade, never install.
const ProtoVersion = "1.0.0"

// maxLineTestHook lets conformance tests shrink the framing limit; production
// code never touches it.
var maxLineTestHook int

// MaxLineBytes is the bidirectional framing ceiling (D-25): 10MB file →
// ~13.4MB base64 line, carried with headroom.
func MaxLineBytes() int {
	if maxLineTestHook > 0 {
		return maxLineTestHook
	}
	return 32 * 1024 * 1024
}

// FramingError signals a hard limit breach (oversize line) — a protocol
// crime, not a business error: the connection is suspect afterwards.
type FramingError struct{ Limit, Got int }

func (e *FramingError) Error() string {
	return fmt.Sprintf("framing: line %d bytes exceeds the %d byte limit", e.Got, e.Limit)
}

// Handshake is the first line both ways (D-16/D-20): token fail-closed,
// proto_version for pin-aware mismatch, os/arch for the platform matrix,
// owner_id for multi-daemon isolation (D-18).
type Handshake struct {
	Token        string `json:"token"`
	ProtoVersion string `json:"proto_version"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	OwnerID      string `json:"owner_id"`
}

// Request is one protocol call.
type Request struct {
	ID     string         `json:"id"`
	Method string         `json:"method"`
	Params map[string]any `json:"params,omitempty"`
}

// Response is one protocol reply (result xor error).
type Response struct {
	ID     string         `json:"id"`
	Result map[string]any `json:"result,omitempty"`
	Error  *ProtoError    `json:"error,omitempty"`
}

// ProtoError is the typed error envelope (business errors ride here; framing
// errors kill the connection instead).
type ProtoError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *ProtoError) Error() string { return e.Code + ": " + e.Message }

// Message is the decoded envelope (id + method or raw bytes for responses).
type Message struct {
	ID     string
	Method string
	Params map[string]any
	Raw    []byte // the original line (for typed re-decode)
}

// WriteHandshake emits the handshake as the first line.
func WriteHandshake(w io.Writer, h *Handshake) error {
	data, err := json.Marshal(h)
	if err != nil {
		return err
	}
	_, err = w.Write(append(data, '\n'))
	return err
}

// ReadHandshake consumes the handshake line, enforcing the framing limit.
func ReadHandshake(r *bufio.Reader) (*Handshake, error) {
	line, err := readLimitedLine(r)
	if err != nil {
		return nil, err
	}
	var h Handshake
	if err := json.Unmarshal(line, &h); err != nil {
		return nil, fmt.Errorf("handshake: malformed: %w", err)
	}
	return &h, nil
}

// WriteMessage emits one envelope line.
func WriteMessage(w io.Writer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(data)+1 > MaxLineBytes() {
		return &FramingError{Limit: MaxLineBytes(), Got: len(data) + 1}
	}
	_, err = w.Write(append(data, '\n'))
	return err
}

// ReadMessage consumes one envelope line with the framing limit enforced.
func ReadMessage(r *bufio.Reader) (*Message, error) {
	line, err := readLimitedLine(r)
	if err != nil {
		return nil, err
	}
	m := &Message{Raw: line}
	// id is number-or-string on the wire (JSON-RPC allows both; we ran a
	// real client hang on refusing numbers — D-31's TCP probe caught it)
	var probe struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params map[string]any  `json:"params"`
	}
	if err := json.Unmarshal(line, &probe); err != nil {
		return nil, fmt.Errorf("message: malformed: %w", err)
	}
	if len(probe.ID) > 0 {
		if err := json.Unmarshal(probe.ID, &m.ID); err != nil {
			// number id: normalize to its literal (correlation is byte-exact)
			m.ID = string(probe.ID)
		}
	}
	m.Method, m.Params = probe.Method, probe.Params
	return m, nil
}

// readLimitedLine reads one '\n'-terminated line; exceeding the limit is a
// FramingError (never silent truncation — D-25's whole point).
func readLimitedLine(r *bufio.Reader) ([]byte, error) {
	limit := MaxLineBytes()
	var buf bytes.Buffer
	for {
		chunk, err := r.ReadSlice('\n')
		buf.Write(chunk)
		if buf.Len() > limit {
			return nil, &FramingError{Limit: limit, Got: buf.Len()}
		}
		if err == nil {
			return bytes.TrimRight(buf.Bytes(), "\n"), nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue // keep consuming until newline or limit breach
		}
		if errors.Is(err, io.EOF) {
			if buf.Len() > 0 {
				return bytes.TrimRight(buf.Bytes(), "\n"), nil
			}
			return nil, io.EOF
		}
		return nil, err
	}
}
