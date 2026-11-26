// companion_client is the sandboxd-side half of the companion protocol
// (T6, D-02/D-20/D-21): reach the remote unix socket via ssh direct-tcpip,
// handshake with the token from conn.json, and track the L2 state machine
// (connected/disconnected/reconnecting). Routing is exec-only in this
// increment — file ops stay thin (SFTP) until T9 protocolizes them.
package remote

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os/exec"
	"sync"
	"time"

	"github.com/hs3434/agent-sandbox/pkg/companion"
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
	mu       sync.Mutex
	backend  *Backend // the thin backend whose master/machine we ride
	machine  string   // machine name (backend registry key)
	ownerID  string   // platform owner for socket addressing (D-18)
	conn     net.Conn // live protocol connection (nil = down)
	br       *bufio.Reader
	state    string // connected | disconnected | reconnecting
	lastErr  string
	fetchTTL time.Time // conn.json re-fetch throttle (adopt path needs fresh tokens)
}

// NewCompanionClient binds a client to one declared machine.
func (b *Backend) NewCompanionClient(machine, ownerID string) *CompanionClient {
	return &CompanionClient{backend: b, machine: machine, ownerID: ownerID, state: "disconnected"}
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
		"cat ~/.sandboxd-server/conn.json")...)
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

// dialConn opens a stdio bridge to the remote unix socket and performs the
// token handshake (D-16 fail-closed) with the 15s budget. The bridge is
// `ssh host nc -U <socket>`: OpenSSH's -W endpoint is TCP-only on the
// deployed matrix (8.9), so the unix hop rides the remote nc — one process,
// killed with the connection, no listeners left behind.
func (c *CompanionClient) dialConn(ctx context.Context, conn *companion.Conn) (net.Conn, *bufio.Reader, error) {
	st, ok := c.backend.machines[c.machine]
	if !ok {
		return nil, nil, fmt.Errorf("machine %q not declared", c.machine)
	}
	// NO defer cancel: the context owns the ssh process for the connection's
	// whole lifetime (CommandContext kills on cancel — cancelling at dial
	// return would sever the bridge right after handshake). Release happens
	// in sshPipeConn.Close.
	dialCtx := ctx
	// unix-socket stdio bridge: ssh runs `nc -U <socket>` and pipes us in
	wcmd := exec.CommandContext(dialCtx, "ssh", append(c.backend.sshBaseArgs(st),
		"nc -U "+conn.Socket+" 2>/dev/null")...)
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
		Arch:         "amd64",
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
	c.mu.Unlock()
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
func (c *CompanionClient) Call(ctx context.Context, method string, params map[string]any) (map[string]any, error) {
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
		if r.msg.ID != req.ID {
			return nil, fmt.Errorf("companion %q: response id mismatch (got %q want %q)", c.machine, r.msg.ID, req.ID)
		}
		if raw := r.msg.Raw; raw != nil {
			var envelope struct {
				Result map[string]any    `json:"result"`
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
		return r.msg.Params, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// drop tears the channel down and records the L2 flip.
func (c *CompanionClient) drop(why error) {
	c.mu.Lock()
	conn := c.conn
	c.conn, c.br = nil, nil
	c.state = "disconnected"
	c.lastErr = why.Error()
	c.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
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
