package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/hs3434/agent-sandbox/internal/mcpserver"
	"github.com/hs3434/agent-sandbox/internal/orchestrator"
	"github.com/hs3434/agent-sandbox/pkg/container"
	"github.com/hs3434/agent-sandbox/pkg/remote"
	"github.com/hs3434/agent-sandbox/pkg/resource"
	"github.com/hs3434/agent-sandbox/pkg/shell"
)

// remoteMachine adapts one named ssh machine to the exec/file faces the
// orchestrator routes to (machine selector semantics, AD-026).
type remoteMachine struct {
	be   *remote.Backend
	name string
	axis string // the axis this target lives on (P0: ssh)
	cc   *remote.CompanionClient // companion channel (nil = never started)
}

// StartCompanion attaches the companion channel (T6 mode flip entry).
func (m *remoteMachine) StartCompanion(ownerID string) {
	m.cc = m.be.NewCompanionClient(m.name, ownerID)
}

// CompanionState is the mode-flip probe (face.go machineMode): "active"
// only when a ping round-trip succeeds — never inferred from config.
func (m *remoteMachine) CompanionState() string {
	if m.cc == nil {
		return "absent"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	res, err := m.cc.Call(ctx, "ping", nil)
	if err != nil || res["ok"] != true {
		return "down"
	}
	return "active"
}

// Axis names the deployed axis (status groups machines by axis).
func (m remoteMachine) Axis() string {
	if m.axis == "" {
		return "ssh"
	}
	return m.axis
}

// ExecAsync supervises through the companion when active (T11): the handle
// is the companion's, the ledger row is ours — write-ahead before return
// (D-06 both axes, same recovery contract).
func (m remoteMachine) ExecAsync(ctx context.Context, id resource.ResourceID, cmd string) (*container.Handle, error) {
	if m.cc == nil || m.CompanionState() != "active" {
		return nil, fmt.Errorf("machine %q: async exec needs an active companion (thin mode is blocking-only)", m.name)
	}
	ctx2, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := m.cc.Call(ctx2, "exec_async", map[string]any{
		"cmd": cmd, "request_id": fmt.Sprintf("%s-%d", m.name, time.Now().UnixNano()),
		"owner_machine": m.name,
	})
	if err != nil {
		return nil, fmt.Errorf("companion exec_async: %w", err)
	}
	hid, _ := res["handle_id"].(string)
	pgidF, _ := res["pgid"].(float64)
	return &container.Handle{
		ID: hid, EnvID: id, ExecID: hid,
		PGID:  int(pgidF),
		Cmd:   cmd,
		State: container.HandleRunning,
	}, nil
}

// Kill routes a group kill through the companion (asyncExecFace's second
// member); the supervisor refuses wrong-identity kills (D-05's twin).
func (m remoteMachine) Kill(ctx context.Context, id resource.ResourceID, pid int) error {
	if m.cc == nil {
		return fmt.Errorf("machine %q: kill needs a companion channel", m.name)
	}
	// address by handle (pid is the docker-axis vocabulary); the supervisor
	// accepts pgid-addressed kills for cross-axis symmetry
	ctx2, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, err := m.cc.Call(ctx2, "kill", map[string]any{"pgid": pid})
	return err
}

// WaitHandle resolves through the companion ledger (sessions.list shaped).
func (m remoteMachine) WaitHandle(ctx context.Context, handleID string) (*container.Handle, error) {
	if m.cc == nil {
		return &container.Handle{ID: handleID, State: container.HandleDead, ExitCode: -1}, nil
	}
	ctx2, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	res, err := m.cc.Call(ctx2, "wait", map[string]any{"handle_id": handleID})
	if err != nil {
		return nil, err
	}
	st, _ := res["state"].(string)
	switch {
	case strings.HasPrefix(st, "exited("):
		code := 0
		fmt.Sscanf(st, "exited(%d)", &code)
		return &container.Handle{ID: handleID, State: container.HandleDead, ExitCode: code}, nil
	case st == "signaled" || strings.HasPrefix(st, "signaled("):
		return &container.Handle{ID: handleID, State: container.HandleDead, ExitCode: 137}, nil
	default:
		if st == "running" {
			return &container.Handle{ID: handleID, State: container.HandleRunning}, nil
		}
		return &container.Handle{ID: handleID, State: container.HandleDead, ExitCode: -1}, nil
	}
}

// ReconcileLedger is the L5 reattach data path (D-06 companion half):
// sessions.list is the daemon's truth; the caller (orchestrator) merges it
// into the platform ledger at boot and after reattach.
func (m remoteMachine) ReconcileLedger(ctx context.Context) ([]map[string]any, error) {
	if m.cc == nil {
		return nil, fmt.Errorf("companion absent on %q", m.name)
	}
	ctx2, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	res, err := m.cc.Call(ctx2, "sessions.list", nil)
	if err != nil {
		return nil, err
	}
	raw, _ := res["sessions"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		if m, ok := r.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out, nil
}

func (m remoteMachine) Exec(ctx context.Context, id resource.ResourceID, cmd string, opts ...container.ExecOptions) (container.ExecResult, error) {
	var o container.ExecOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	r, err := m.be.Exec(ctx, m.name, cmd, remote.ExecOptions{Timeout: o.Timeout})
	if err != nil {
		return container.ExecResult{}, err
	}
	// contract shapes are identical by design; convert between package types
	return container.ExecResult{ExitCode: r.ExitCode, Stdout: r.Stdout,
		Stderr: r.Stderr, ExitReason: r.ExitReason, Duration: r.Duration}, nil
}

// ReadFile routes companion-first (protocol path — audit says so, T5 truth)
// with an explicit thin fallback when the channel is down (D-18's heaviest
// op never falls back silently; here the fallback IS the declared tier).
func (m remoteMachine) ReadFile(ctx context.Context, id resource.ResourceID, path string) (string, error) {
	if m.cc != nil && m.CompanionState() == "active" {
		txt, _, err := m.cc.FileReadViaProtocol(ctx, path)
		if err == nil {
			return txt, nil
		}
		if !remote.IsTransportErr(err) {
			// typed policy refusal (binary/cap) — thin would bypass D-08/D-07
			return "", err
		}
		// channel died mid-call: thin fallback is the declared tier
		if txt2, thinErr := m.be.ReadFile(ctx, m.name, path); thinErr == nil {
			return txt2, nil
		}
		return "", err
	}
	return m.be.ReadFile(ctx, m.name, path)
}

func (m remoteMachine) WriteFile(ctx context.Context, id resource.ResourceID, path, content string) error {
	if m.cc != nil && m.CompanionState() == "active" {
		if _, err := m.cc.FileWriteViaProtocol(ctx, path, content); err == nil {
			return nil
		}
	}
	return m.be.WriteFile(ctx, m.name, path, content)
}

// OpenSession starts a persistent remote shell (delegates to the backend;
// machine name is already resolved by the orchestrator).
func (m remoteMachine) OpenSession(ctx context.Context, _ resource.ResourceID, shellCmd []string) (io.WriteCloser, io.Reader, func(), error) {
	// ssh dialect expects the raw shell command; "/bin/sh" works for posix,
	// the PowerShell line for windows
	return m.be.OpenSession(ctx, m.name, strings.Join(shellCmd, " "))
}

// Health reports the real connection state — never a hardcoded "connected"
// (status must not lie: §4.3 connected/disconnected/reconnecting).
func (m remoteMachine) Health() string { return m.be.State(m.name) }

// TargetInfo exposes the endpoint address and OS type (where to reach it).
func (m remoteMachine) TargetInfo() (string, string) {
	t, ok := m.be.TargetOf(m.name)
	if !ok {
		return "", ""
	}
	return fmt.Sprintf("%s:%d", t.Host, t.Port), t.OSType
}

// orchestratorFaceAdapter exposes the orchestrator as the mcpserver contract
// (face.go interface) — the single seam between the two internal layers.
type orchestratorFaceAdapter struct{ o *orchestrator.Orchestrator }

func (a orchestratorFaceAdapter) HandleState(handleID string) (map[string]any, error) {
	return a.o.HandleState(handleID)
}

func (a orchestratorFaceAdapter) Create(ctx context.Context, opts mcpserver.CreateOpts) (*mcpserver.ResourceMeta, error) {
	m, err := a.o.Create(ctx, orchestrator.CreateOpts(opts))
	if err != nil {
		return nil, err
	}
	return &mcpserver.ResourceMeta{ID: string(m.ID), Status: string(m.Status), Backend: m.Backend}, nil
}

func (a orchestratorFaceAdapter) Remove(ctx context.Context, id string, purge bool) error {
	return a.o.Remove(ctx, id, purge)
}

func (a orchestratorFaceAdapter) Build(ctx context.Context, envID, tag, dockerfile string) error {
	return a.o.Build(ctx, envID, tag, dockerfile)
}

func (a orchestratorFaceAdapter) ListImages(ctx context.Context) ([]string, error) {
	return a.o.ListImages(ctx)
}

func (a orchestratorFaceAdapter) ShellExec(ctx context.Context, opts mcpserver.ExecOpts, cmd string) (map[string]any, error) {
	return a.o.ShellExec(ctx, opts.Machine, opts.ShellID, opts.Async, opts.Timeout, cmd)
}

func (a orchestratorFaceAdapter) ShellNew(ctx context.Context, machine, purpose string) (*mcpserver.ShellInfo, error) {
	info, err := a.o.ShellNew(ctx, machine, purpose)
	if err != nil {
		return nil, err
	}
	return sessionToFace(info), nil
}

func (a orchestratorFaceAdapter) ShellRemove(shellID string) bool {
	return a.o.ShellRemove(shellID)
}

func (a orchestratorFaceAdapter) ShellList() []mcpserver.ShellInfo {
	rows := a.o.ShellList()
	out := make([]mcpserver.ShellInfo, 0, len(rows))
	for i := range rows {
		out = append(out, *sessionToFace(&rows[i]))
	}
	return out
}

func sessionToFace(s *orchestrator.SessionInfo) *mcpserver.ShellInfo {
	return &mcpserver.ShellInfo{ID: s.ID, Env: s.Env, Dialect: s.Dialect,
		Purpose: s.Purpose, State: s.State, Created: s.Created}
}

func (a orchestratorFaceAdapter) FileRead(ctx context.Context, machine, path string) (string, error) {
	return a.o.FileRead(ctx, machine, path)
}

func (a orchestratorFaceAdapter) FileWrite(ctx context.Context, machine, path, content string) error {
	return a.o.FileWrite(ctx, machine, path, content)
}

func (a orchestratorFaceAdapter) FilePatch(ctx context.Context, machine, path, oldS, newS string, all bool) (int, error) {
	return a.o.FilePatch(ctx, machine, path, oldS, newS, all)
}

func (a orchestratorFaceAdapter) FileSearch(ctx context.Context, p shell.SearchParams, machine string) (*shell.SearchResult, error) {
	return a.o.FileSearch(ctx, p, machine)
}

func (a orchestratorFaceAdapter) PathDurable(machine, path string) bool {
	return a.o.PathDurable(machine, path)
}

func (a orchestratorFaceAdapter) Status(ctx context.Context) (map[string]any, error) {
	return a.o.Status(ctx)
}

func (a orchestratorFaceAdapter) Inspect(ctx context.Context, id string) (map[string]any, error) {
	return a.o.Inspect(ctx, id)
}

func (a orchestratorFaceAdapter) WorkdirFor(name string) string {
	return a.o.WorkdirFor(name)
}

var _ = sort.Strings
var _ = strings.TrimSpace
