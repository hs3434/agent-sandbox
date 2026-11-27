// companion_client is the sandboxd-side half of the companion protocol
// (T6, D-02/D-20/D-21): reach the remote unix socket via ssh direct-tcpip,
// handshake with the token from conn.json, and track the L2 state machine
// (connected/disconnected/reconnecting). Routing is exec-only in this
// increment — file ops stay thin (SFTP) until T9 protocolizes them.
package remote

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"github.com/hs3434/agent-sandbox/pkg/companion"
	"github.com/hs3434/agent-sandbox/pkg/file"
	"github.com/hs3434/agent-sandbox/pkg/protocol"
)

// companion budget (D-02 vscode-derived): dial 10s, ping 5s — 15s total to
// declare disconnected. Anything slower is a dead companion, not a slow one.
const (
	companionDialTO = 10 * time.Second
	companionPingTO = 5 * time.Second
)

// CompanionClient owns one machine's companion channel: dial-on-demand,
// sticky connection, L2 vocabulary for status, typed failures for routing.
type CompanionClient struct {
	mu      sync.Mutex
	backend *Backend // the thin backend whose master/machine we ride
	machine string   // machine name (backend registry key)
	ownerID string   // platform owner for socket addressing (D-18)
	conn    net.Conn // live protocol connection (nil = down)
	br      *bufio.Reader
	state   string // connected | disconnected | reconnecting
	lastErr string
	callMu  sync.Mutex // M5: one in-flight call per client — br is not synchronized
	dialMu  sync.Mutex // M6: single-flight dial — losers must not leak their bridge
	dialing bool

	deadFn     func(handleID, terminal string) // death events → platform ledger
	deadMu     sync.Mutex
	eventsOnce sync.Once
	stopEvents chan struct{}

	pendingMu sync.Mutex
	pending   map[string]chan callResult // request-id → reply slot (pump-read mode)
	pumpArmed bool
}

type callResult struct {
	msg *protocol.Message
	err error
}

// NewCompanionClient binds a client to one declared machine.
func (b *Backend) NewCompanionClient(machine, ownerID string) *CompanionClient {
	return &CompanionClient{backend: b, machine: machine, ownerID: ownerID, state: "disconnected", stopEvents: make(chan struct{}), pending: map[string]chan callResult{}}
}

// State exposes the L2 vocabulary (§4.3 status reads this).
func (c *CompanionClient) State() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// LastError reports why the channel is down (status honesty).
func (c *CompanionClient) LastError() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastErr
}

// setState records L2 transitions.
func (c *CompanionClient) setState(s, why string) {
	c.mu.Lock()
	c.state = s
	c.lastErr = why
	c.mu.Unlock()
}

// fetchConn reads conn.json over the thin channel (ssh cat). The file is
// 0600 in a 0700 dir — only our ssh user can read it, which IS the D-16
// bootstrap path: auth rides ssh, not the network.
func (c *CompanionClient) fetchConn(ctx context.Context) (*companion.Conn, error) {
	st, ok := c.backend.machines[c.machine]
	if !ok {
		return nil, fmt.Errorf("machine %q not declared", c.machine)
	}
	if err := c.backend.ensureAlive(st); err != nil {
		return nil, err
	}
	ctx2, cancel := context.WithTimeout(ctx, companionDialTO)
	defer cancel()
	cmd := exec.CommandContext(ctx2, "ssh", append(c.backend.sshBaseArgs(st),
		"cat ~/.sandboxd-server/"+companion.ConnFileName(c.ownerID))...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("fetch conn.json: %v: %s", err, tailErr(err, 200))
	}
	var conn companion.Conn
	if err := json.Unmarshal(out, &conn); err != nil || conn.Token == "" {
		// D-18: corrupt discovery file is a report, never an install
		return nil, fmt.Errorf("conn.json corrupt on %q (thin until daemon restarts)", c.machine)
	}
	return &conn, nil
}

// dialConn opens a stdio bridge to the remote loopback listener and performs
// the token handshake (D-16 fail-closed) with the 15s budget. The bridge is
// `ssh host -W 127.0.0.1:<port>` (D-31): OpenSSH's -W natively forwards stdio
// to a TCP endpoint on all platforms (Windows ssh.exe included) — no remote
// helper (the nc dependency of the unix-socket era is gone), one process,
// killed with the connection, no listeners left behind.
func (c *CompanionClient) dialConn(ctx context.Context, conn *companion.Conn) (net.Conn, *bufio.Reader, error) {
	st, ok := c.backend.machines[c.machine]
	if !ok {
		return nil, nil, fmt.Errorf("machine %q not declared", c.machine)
	}
	// The bridge process must NOT bind to the caller's per-call context:
	// CommandContext kills the ssh on cancel, and callers cancel when they
	// return (CompanionState's defer) — that severed every reused conn.
	// The conn lives for the client's lifetime; release is sshPipeConn.Close
	// (drop/Close paths). Dial latency still bounded by the handshake ack
	// budget below, which is the honest 15s gate.
	dialCtx := context.Background()
	// TCP stdio bridge: ssh -W connects to the loopback endpoint from inside
	// the remote host (D-31) — same path on linux and windows
	wcmd := exec.CommandContext(dialCtx, "ssh", append(c.backend.sshBaseArgs(st),
		"-W", conn.Endpoint())...)
	wcmd.Stderr = nil // keep stderr silent (quiet posture D-27)
	stdin, err := wcmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	stdout, err := wcmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := wcmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("ssh -W start: %w", err)
	}
	pc := &sshPipeConn{cmd: wcmd, stdin: stdin, stdout: stdout, done: make(chan struct{})}
	go func() { _ = wcmd.Wait(); close(pc.done) }()

	br := bufio.NewReaderSize(pc, 64*1024)
	hs := &protocol.Handshake{
		Token:        conn.Token,
		ProtoVersion: companion.ProtoVersion,
		OS:           hostOSForMatrix(st.target.OSType),
		Arch:         runtime.GOARCH, // truthful matrix field (D-20)
		OwnerID:      c.ownerID,
	}
	if err := protocol.WriteHandshake(pc, hs); err != nil {
		pc.Close()
		return nil, nil, fmt.Errorf("handshake write: %w", err)
	}
	// the ack line must arrive within the ping budget (fail-closed: no ack
	// = dead companion, the caller degrades to thin)
	ackCtx, ackCancel := context.WithTimeout(ctx, companionPingTO)
	defer ackCancel()
	type ackRes struct {
		line []byte
		err  error
	}
	ackCh := make(chan ackRes, 1)
	go func() {
		line, err := protocol.ReadMessage(br)
		ackCh <- ackRes{rawLine(line), err}
	}()
	select {
	case r := <-ackCh:
		if r.err != nil {
			pc.Close()
			return nil, nil, fmt.Errorf("handshake ack: %w", r.err)
		}
		var ack struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		_ = json.Unmarshal(r.line, &ack)
		if !ack.OK {
			pc.Close()
			return nil, nil, fmt.Errorf("handshake refused: %s", ack.Error)
		}
		return pc, br, nil
	case <-ackCtx.Done():
		pc.Close()
		return nil, nil, fmt.Errorf("handshake ack timeout (%s): companion dead or stalled", companionPingTO)
	}
}

func rawLine(m *protocol.Message) []byte {
	if m == nil {
		return nil
	}
	return m.Raw
}

func hostOSForMatrix(osType string) string {
	if osType == "" {
		return "linux"
	}
	return osType
}

// ensureConnected implements the sticky-dial contract: reuse a live channel,
// else dial (fetch token → ssh -W → handshake), updating L2 vocabulary.
func (c *CompanionClient) ensureConnected(ctx context.Context) (net.Conn, *bufio.Reader, error) {
	c.mu.Lock()
	if c.conn != nil {
		conn, br := c.conn, c.br
		c.mu.Unlock()
		return conn, br, nil
	}
	if c.dialing {
		// M6 single-flight: a concurrent dial owns the slot; fail fast (the
		// caller retries and reuses the winner's conn — no leaked bridges)
		c.mu.Unlock()
		return nil, nil, fmt.Errorf("companion %q: dial in flight", c.machine)
	}
	c.dialing = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.dialing = false
		c.mu.Unlock()
	}()
	c.setState("reconnecting", "dialing")
	connJSON, err := c.fetchConn(ctx)
	if err != nil {
		c.setState("disconnected", err.Error())
		return nil, nil, err
	}
	pc, br, err := c.dialConn(ctx, connJSON)
	if err != nil {
		c.setState("disconnected", err.Error())
		return nil, nil, err
	}
	c.mu.Lock()
	c.conn, c.br = pc, br
	c.state = "connected"
	c.lastErr = ""
	c.mu.Unlock()
	return pc, br, nil
}

// Call performs one protocol round-trip (request-id correlation is
// protocol-owned). On transport failure the channel drops to disconnected
// and the error carries the machine + reason (fail-fast, no silent retry
// storm — the next Call re-dials).
// OnDead registers the death-event consumer (platform ledger flip). The
// first registration arms the events subscription (one dedicated bridge —
// request/response conns stay synchronous).
func (c *CompanionClient) OnDead(fn func(handleID, terminal string)) {
	c.pendingMu.Lock()
	first := c.deadFn == nil
	c.deadFn = fn
	if first {
		c.pumpArmed = true // Call switches to pump-routed replies
	}
	c.pendingMu.Unlock()
	if first {
		c.eventsOnce.Do(func() { go c.runEvents() })
	}
}

// runEvents holds a dedicated protocol conn, subscribes to "events", and
// forwards process.exited pushes to the hook. Transport errors retry with
// backoff — the ledger catches up on the next push or the next pull (D-21).
func (c *CompanionClient) runEvents() {
	for {
		c.deadMu.Lock()
		fn := c.deadFn
		c.deadMu.Unlock()
		if fn == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		ec, ebr, err := c.ensureConnected(ctx)
		cancel()
		if err == nil {
			req := &protocol.Request{ID: fmt.Sprintf("ev-%d", time.Now().UnixNano()), Method: "events"}
			if err := protocol.WriteMessage(ec, req); err == nil {
				// consume THIS conn's replies until death; each pushed line
				// that names process.exited goes to the hook
				c.pumpEvents(ec, ebr, req.ID, fn)
			}
		}
		select {
		case <-time.After(2 * time.Second):
		case <-c.stopEvents:
			return
		}
	}
}

// pumpEvents reads pushed lines on the events conn (request replies on the
// main conn are untouched). Returns when the conn dies.
func (c *CompanionClient) pumpEvents(ec net.Conn, br *bufio.Reader, subID string, fn func(handleID, terminal string)) {
	for {
		m, err := protocol.ReadMessage(br)
		if err != nil {
			c.drop(err) // shared state: main conn rides the same daemon
			return
		}
		if m.ID == subID {
			continue // subscription ack
		}
		// request reply? route to the pending waiter (callViaPump)
		if m.ID != "" {
			c.pendingMu.Lock()
			ch, ok := c.pending[m.ID]
			c.pendingMu.Unlock()
			if ok {
				ch <- callResult{msg: m}
				continue
			}
		}
		var ev struct {
			Event    string `json:"event"`
			HandleID string `json:"handle_id"`
			Terminal string `json:"terminal"`
		}
		if jsonErr := json.Unmarshal(m.Raw, &ev); jsonErr == nil && ev.Event == "process.exited" && ev.HandleID != "" {
			fn(ev.HandleID, ev.Terminal)
		}
	}
}

func (c *CompanionClient) Call(ctx context.Context, method string, params map[string]any) (map[string]any, error) {
	// ONE reader (the events pump when OnDead is armed; direct read here
	// otherwise) owns the conn. With the pump armed, Call registers a
	// pending reply slot and the pump routes the response to it — two
	// competing readers on one conn was the recovery deadlock (each held
	// the other's EOF/drop; every post-restart dial starved).
	c.pendingMu.Lock()
	pumpArmed := c.pumpArmed
	c.pendingMu.Unlock()
	if pumpArmed {
		return c.callViaPump(ctx, method, params)
	}
	c.callMu.Lock()
	defer c.callMu.Unlock()
	pc, br, err := c.ensureConnected(ctx)
	if err != nil {
		return nil, err
	}
	req := &protocol.Request{ID: fmt.Sprintf("r-%d", time.Now().UnixNano()), Method: method, Params: params}
	if err := protocol.WriteMessage(pc, req); err != nil {
		c.drop(err)
		return nil, err
	}
	type res struct {
		msg *protocol.Message
		err error
	}
	ch := make(chan res, 1)
	go func() {
		m, err := protocol.ReadMessage(br)
		ch <- res{m, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			c.drop(r.err)
			return nil, fmt.Errorf("companion %q: transport: %w", c.machine, r.err)
		}
		return c.interpret(r.msg, req.ID)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// callViaPump writes the request and waits for the pump to route our reply.
func (c *CompanionClient) callViaPump(ctx context.Context, method string, params map[string]any) (map[string]any, error) {
	pc, _, err := c.ensureConnected(ctx)
	if err != nil {
		return nil, err
	}
	req := &protocol.Request{ID: fmt.Sprintf("r-%d", time.Now().UnixNano()), Method: method, Params: params}
	if err := protocol.WriteMessage(pc, req); err != nil {
		c.drop(err)
		return nil, err
	}
	ch := make(chan callResult, 1)
	c.pendingMu.Lock()
	c.pending[req.ID] = ch
	c.pendingMu.Unlock()
	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, req.ID)
		c.pendingMu.Unlock()
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, fmt.Errorf("companion %q: transport: %w", c.machine, r.err)
		}
		return c.interpret(r.msg, req.ID)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// interpret decodes one correlated response (shared by both read paths).
func (c *CompanionClient) interpret(msg *protocol.Message, wantID string) (map[string]any, error) {
	if raw := msg.Raw; raw != nil {
		var envelope struct {
			Result map[string]any       `json:"result"`
			Error  *protocol.ProtoError `json:"error"`
		}
		if err := json.Unmarshal(raw, &envelope); err == nil {
			if envelope.Error != nil {
				return nil, envelope.Error
			}
			if envelope.Result != nil {
				return envelope.Result, nil
			}
		}
	}
	return msg.Params, nil
}

// drop tears the channel down and records the L2 flip.
func (c *CompanionClient) drop(why error) {
	log.Printf("companion %q drop: %v", c.machine, why)
	c.mu.Lock()
	conn := c.conn
	c.conn, c.br = nil, nil
	c.state = "disconnected"
	c.lastErr = why.Error()
	c.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
	// wake pump-routed waiters: their conn died; hanging to ctx is a lie
	c.pendingMu.Lock()
	for id, ch := range c.pending {
		ch <- callResult{err: why}
		delete(c.pending, id)
	}
	c.pendingMu.Unlock()
}

// ProtoError reports typed (policy) failures from the companion side —
// binary refusal, cap refusal — which must SURFACE, never fall back to a
// channel that lacks the semantics (thin would happily return binary text).
type ProtoError struct{ Code, Message string }

func (e *ProtoError) Error() string { return e.Code + ": " + e.Message }

// IsTransportErr distinguishes channel failures (fall back to thin is
// correct) from typed policy failures (falling back would bypass D-08/D-07).
func IsTransportErr(err error) bool {
	if _, ok := err.(*ProtoError); ok {
		return false
	}
	if _, ok := err.(*protocol.ProtoError); ok {
		return false // typed envelope = policy, not transport
	}
	return true
}

// FileReadViaProtocol reads a file through the companion (T9 routing):
// returns decoded text + the durable fact. Typed errors propagate so the
// caller can fall back to thin explicitly (never silently).
func (c *CompanionClient) FileReadViaProtocol(ctx context.Context, path string) (string, bool, error) {
	res, err := c.Call(ctx, "file.read", map[string]any{"path": path})
	if err != nil {
		return "", false, err
	}
	b64, _ := res["b64"].(string)
	raw, derr := base64.StdEncoding.DecodeString(b64)
	if derr != nil {
		return "", false, fmt.Errorf("file.read: b64 decode: %w", derr)
	}
	durable, _ := res["durable"].(bool)
	return string(file.DecodeText(raw)), durable, nil
}

// FileWriteViaProtocol writes through the companion (normalized on arrival
// server-side by pkg/file-equivalent semantics — the wire is bytes).
func (c *CompanionClient) FileWriteViaProtocol(ctx context.Context, path, content string) (bool, error) {
	res, err := c.Call(ctx, "file.write", map[string]any{
		"path": path, "b64": base64.StdEncoding.EncodeToString([]byte(content))})
	if err != nil {
		return false, err
	}
	durable, _ := res["durable"].(bool)
	return durable, nil
}

// FilePatchViaProtocol applies old→new on the companion (read-modify-write
// happens target-side — the file text never crosses the wire twice).
func (c *CompanionClient) FilePatchViaProtocol(ctx context.Context, path, oldS, newS string, all bool) (int, error) {
	res, err := c.Call(ctx, "file.patch", map[string]any{
		"path": path, "old": oldS, "new": newS, "all": all})
	if err != nil {
		return 0, err
	}
	n, _ := res["replacements"].(float64)
	return int(n), nil
}

// FileSearchViaProtocol runs the search ON THE TARGET (truth T5): the
// daemon executes rg→grep locally; only match lines cross the wire.
// FileSearchQuery is the public search request (unexported types in public
// signatures are unusable — code-quality m2).
type FileSearchQuery struct {
	Root     string
	Pattern  string
	FileGlob string
	Limit    int
}

func (c *CompanionClient) FileSearchViaProtocol(ctx context.Context, p FileSearchQuery) (string, string, error) {
	res, err := c.Call(ctx, "file.search", map[string]any{
		"root": p.Root, "pattern": p.Pattern, "glob": p.FileGlob, "limit": p.Limit})
	if err != nil {
		return "", "", err
	}
	matches, _ := res["matches"].(string)
	diag, _ := res["diagnostics"].(string)
	return matches, diag, nil
}

// Close releases the channel (machine shutdown path).
func (c *CompanionClient) Close() error {
	c.mu.Lock()
	conn := c.conn
	c.conn, c.br = nil, nil
	c.mu.Unlock()
	if conn != nil {
		return conn.Close()
	}
	return nil
}

// sshPipeConn adapts an `ssh -W` process's stdio to net.Conn semantics
// (protocol codec needs Read/Write/Close; deadlines are best-effort).
type sshPipeConn struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	done   chan struct{}
	once   sync.Once
}

func (p *sshPipeConn) Read(b []byte) (int, error)  { return p.stdout.Read(b) }
func (p *sshPipeConn) Write(b []byte) (int, error) { return p.stdin.Write(b) }

// net.Conn deadline methods: no real deadlines over a process pipe — the
// protocol layer enforces its own via context/goroutine timeouts.
func (p *sshPipeConn) LocalAddr() net.Addr                { return pipeAddr{} }
func (p *sshPipeConn) RemoteAddr() net.Addr               { return pipeAddr{} }
func (p *sshPipeConn) SetDeadline(t time.Time) error      { return nil }
func (p *sshPipeConn) SetReadDeadline(t time.Time) error  { return nil }
func (p *sshPipeConn) SetWriteDeadline(t time.Time) error { return nil }

type pipeAddr struct{}

func (pipeAddr) Network() string { return "ssh-pipe" }
func (pipeAddr) String() string  { return "ssh-pipe" }

func (p *sshPipeConn) Close() error {
	var err error
	p.once.Do(func() {
		_ = p.stdin.Close()
		_ = p.cmd.Process.Kill()
		select {
		case <-p.done:
		case <-time.After(2 * time.Second):
		}
		err = p.stdout.Close()
	})
	return err
}

func tailErr(err error, n int) string {
	if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
		s := string(ee.Stderr)
		if len(s) > n {
			s = s[len(s)-n:]
		}
		return s
	}
	return err.Error()
}
