// Package remote implements the ssh thin-mode backend (P0 transition rope,
// design-spec-v3 §10.0, AD-028): machines are declarative (sandbox.yaml),
// exec is blocking-only, files go over SFTP, and the connection layer
// self-heals via the ControlMaster trio hardened with round-trip probes.
//
// Honest limitations (thin mode, by design): no async exec (no kernel
// reaping without the companion — architecture debt waits for P1), no
// builds (daemon visibility), no snapshots. Windows handling: PowerShell
// via stdin-script mode, codepage mapping inherited from sandbox-mcp.
package remote

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hs3434/agent-sandbox/pkg/resource"
)

// Declared defaults (I6 declaration surface).
const (
	DefaultExecTimeout = 60 * time.Second
	DefaultProbeTO     = 5 * time.Second   // round-trip zombie probe
	DefaultConnectTO   = 15 * time.Second  // master (re)build
	ControlPersistSecs = 300               // master keepalive (Python parity)
	DefaultSessionCap  = 4                 // per-machine concurrent sessions (conhost-leak guard)
	defaultCodepage    = "936"             // GBK — Chinese Windows fallback (Python parity)
)

// Declared errors (I2: caller-enumerable).
var (
	errSessionCap = fmt.Errorf("per-machine session cap reached (guard against server-side process accumulation)")
)

func isSessionCapErr(err error) bool { return err == errSessionCap }

// Target is one declarative machine (sandbox.yaml SSHTarget shape, AD-026).
type Target struct {
	Name   string `yaml:"name"`
	Host   string `yaml:"host"`
	Port   int    `yaml:"port,omitempty"`
	User   string `yaml:"user,omitempty"`
	Key    string `yaml:"key,omitempty"`
	OSType string `yaml:"os_type"` // linux | windows
}

// ExecOptions tunes a blocking exec.
type ExecOptions struct {
	Timeout time.Duration
}

// ExecResult is the blocking-exec result (same shape as the docker backend —
// the tool face sees one contract, OS differences are internalized).
type ExecResult struct {
	ExitCode   int
	Stdout     string
	Stderr     string
	ExitReason string
	Duration   time.Duration
}

// machineState is one connected machine: master socket + encoding info +
// session counter.
type machineState struct {
	target     Target
	socketPath string
	sessions   int
	state      string // connected | disconnected | reconnecting (§4.3)
	mu         sync.Mutex
}

// Backend is the ssh thin-mode data-plane implementation.
type Backend struct {
	mu       sync.Mutex
	machines map[string]*machineState
	sockDir  string
}

// Open prepares the backend (socket dir etc.).
func Open() (*Backend, error) {
	sockDir, err := os.MkdirTemp("", "agent-sandbox-ssh-*")
	if err != nil {
		return nil, err
	}
	return &Backend{machines: map[string]*machineState{}, sockDir: sockDir}, nil
}

func (b *Backend) Close() error {
	// tear down all masters
	b.mu.Lock()
	states := make([]*machineState, 0, len(b.machines))
	for _, st := range b.machines {
		states = append(states, st)
	}
	b.mu.Unlock()
	for _, st := range states {
		_ = b.masterExit(st)
	}
	return os.RemoveAll(b.sockDir)
}

// Capabilities honestly declares thin-mode limits (no snapshot — ssh machines
// have no body semantics; spec §10 honest declaration).
func (b *Backend) Capabilities() []resource.Capability {
	return []resource.Capability{resource.CapShell, resource.CapFiles}
}

// Connect registers + builds the ControlMaster for a declared machine.
func (b *Backend) Connect(t Target) error {
	if t.Name == "" || t.Host == "" {
		return fmt.Errorf("target name and host are required")
	}
	if t.Port == 0 {
		t.Port = 22
	}
	socket := filepath.Join(b.sockDir, t.Name+".sock")
	st := &machineState{target: t, socketPath: socket, state: "disconnected"}
	b.mu.Lock()
	b.machines[t.Name] = st
	b.mu.Unlock()
	if err := b.masterUp(st); err != nil {
		return err
	}
	st.mu.Lock()
	st.state = "connected"
	st.mu.Unlock()
	return nil
}

// State reports the machine's connection state (§4.3: connected /
// disconnected / reconnecting). Unknown machine → disconnected.
func (b *Backend) State(name string) string {
	b.mu.Lock()
	st := b.machines[name]
	b.mu.Unlock()
	if st == nil {
		return "disconnected"
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.state == "" {
		return "disconnected"
	}
	return st.state
}

// TargetOf returns the declared target (host/port/os) for a machine.
func (b *Backend) TargetOf(name string) (Target, bool) {
	b.mu.Lock()
	st := b.machines[name]
	b.mu.Unlock()
	if st == nil {
		return Target{}, false
	}
	return st.target, true
}

// masterUp builds/rebuilds the ControlMaster (transparent rebuild path).
func (b *Backend) masterUp(st *machineState) error {
	_ = b.masterExit(st) // clean any stale socket
	args := []string{
		"-M", "-S", st.socketPath,
		"-o", fmt.Sprintf("ControlPersist=%ds", ControlPersistSecs),
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "BatchMode=yes",
		"-o", fmt.Sprintf("ConnectTimeout=%d", int(DefaultConnectTO.Seconds())),
		"-N", "-f",
		"-p", fmt.Sprintf("%d", st.target.Port),
	}
	if st.target.Key != "" {
		args = append(args, "-i", st.target.Key)
	}
	addr := st.target.Host
	if st.target.User != "" {
		addr = st.target.User + "@" + st.target.Host
	}
	args = append(args, addr)
	cmd := exec.Command("ssh", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("master up %q: %w: %s", st.target.Name, err, tailStr(string(out), 300))
	}
	return nil
}

func (b *Backend) masterExit(st *machineState) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", "-S", st.socketPath, "-O", "exit",
		st.target.Host)
	return cmd.Run()
}

// probe is the AD-028 zombie check: a real round trip through the master.
// `-O check` only proves the socket file exists — zombies pass it and hang
// every new channel. echo nonce with a hard timeout is the honest probe.
func (b *Backend) probe(st *machineState) error {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultProbeTO)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", "-S", st.socketPath,
		"-o", "BatchMode=yes", st.target.Host, "echo probe-ok")
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("probe: %w", err)
	}
	if !strings.Contains(string(out), "probe-ok") {
		return fmt.Errorf("probe: unexpected output %q", tailStr(string(out), 100))
	}
	return nil
}

// ensureAlive implements the trio: probe → (zombie? force-exit + rebuild) →
// probe again → fail visibly (machine stays registered, status disconnected).
func (b *Backend) ensureAlive(st *machineState) error {
	if err := b.probe(st); err == nil {
		return nil
	}
	// zombie or dead: force teardown + fresh master, then re-probe
	setMachineState(st, "reconnecting")
	_ = b.masterExit(st)
	if err := b.masterUp(st); err != nil {
		setMachineState(st, "disconnected")
		return fmt.Errorf("machine %q disconnected (rebuild failed): %w", st.target.Name, err)
	}
	if err := b.probe(st); err != nil {
		setMachineState(st, "disconnected")
		return fmt.Errorf("machine %q disconnected (probe failed after rebuild): %w", st.target.Name, err)
	}
	setMachineState(st, "connected")
	return nil
}

// setMachineState records the connection state (§4.3 vocabulary).
func setMachineState(st *machineState, s string) {
	st.mu.Lock()
	st.state = s
	st.mu.Unlock()
}

// sshBaseArgs assembles the multiplexed ssh command line for exec.
func (b *Backend) sshBaseArgs(st *machineState) []string {
	args := []string{"-S", st.socketPath, "-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=/dev/null"}
	if st.target.Key != "" {
		args = append(args, "-i", st.target.Key)
	}
	args = append(args, "-p", fmt.Sprintf("%d", st.target.Port))
	addr := st.target.Host
	if st.target.User != "" {
		addr = st.target.User + "@" + st.target.Host
	}
	return append(args, addr)
}

// Exec runs a blocking command (thin mode's only exec mode — async reports
// unsupported; the architecture gap waits for the companion, P1).
func (b *Backend) Exec(ctx context.Context, machine, cmd string, opts ...ExecOptions) (ExecResult, error) {
	cfg := ExecOptions{Timeout: DefaultExecTimeout}
	if len(opts) > 0 && opts[0].Timeout > 0 {
		cfg = opts[0]
	}
	b.mu.Lock()
	st, ok := b.machines[machine]
	b.mu.Unlock()
	if !ok {
		return ExecResult{}, fmt.Errorf("machine %q not declared (sandbox.yaml machines)", machine)
	}
	if err := b.ensureAlive(st); err != nil {
		return ExecResult{}, err
	}

	var sh string
	if st.target.OSType == "windows" {
		// PowerShell stdin-script mode: no PTY interference (Python parity)
		sh = "powershell.exe -NoLogo -NoProfile -Command -"
	} else {
		sh = "/bin/sh -c"
	}
	sshCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	wrapped := sh + " " + shellQuotePOSIX(cmd)
	ecmd := exec.CommandContext(sshCtx, "ssh", append(b.sshBaseArgs(st), wrapped)...)
	ecmd.Cancel = func() error { return ecmd.Process.Kill() } // hard kill on expiry
	ecmd.WaitDelay = 2 * time.Second                          // close pipes even if ssh lingers
	var out, errBuf bytes.Buffer
	ecmd.Stdout = &out
	ecmd.Stderr = &errBuf
	start := time.Now()
	runErr := ecmd.Run()
	res := ExecResult{
		Stdout:   clipStr(out.String()),
		Stderr:   clipStr(errBuf.String()),
		Duration: time.Since(start),
	}
	if ctxErr := sshCtx.Err(); ctxErr == context.DeadlineExceeded {
		res.ExitReason = "timeout"
		return res, nil // deterministic expiry is a fact, not an error
	}
	if exitErr, ok := runErr.(*exec.ExitError); ok {
		res.ExitCode = exitErr.ExitCode()
	} else if runErr != nil {
		return res, fmt.Errorf("ssh exec: %w", runErr)
	}
	return res, nil
}

// openSession registers a concurrent session under the per-machine cap
// (guard against server-side process-tree accumulation — AD-028).
func (b *Backend) openSession(machine, purpose string) error {
	b.mu.Lock()
	st, ok := b.machines[machine]
	b.mu.Unlock()
	if !ok {
		return fmt.Errorf("machine %q not declared", machine)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.sessions >= DefaultSessionCap {
		return errSessionCap
	}
	st.sessions++
	return nil
}

// ReadFile reads via SFTP (byte stream — no encoding issues, thin-mode tier ✅).
func (b *Backend) ReadFile(ctx context.Context, machine, path string) (string, error) {
	data, err := b.sftpRead(ctx, machine, path)
	if err != nil {
		return "", err
	}
	return string(decodeText(data)), nil
}

// WriteFile writes atomically via SFTP (temp + rename), content normalized.
func (b *Backend) WriteFile(ctx context.Context, machine, path, content string) error {
	return b.sftpWrite(ctx, machine, path, normalizeUTF8LF(content))
}

// zombieForTest force-corrupts the master socket state (test-only hook for
// the self-healing path).
func (b *Backend) zombieForTest(machine string) {
	b.mu.Lock()
	st := b.machines[machine]
	b.mu.Unlock()
	if st != nil {
		_ = b.masterExit(st) // master gone; backend still believes it's healthy
	}
}

func shellQuotePOSIX(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\'"'"'\'`) + "'"
}

func clipStr(s string) string {
	const maxOut = 64 * 1024
	if len(s) <= maxOut {
		return s
	}
	return s[len(s)-maxOut:]
}

func tailStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func u16le(b []byte) []uint16 {
	out := make([]uint16, len(b)/2)
	for i := range out {
		out[i] = binary.LittleEndian.Uint16(b[i*2:])
	}
	return out
}
