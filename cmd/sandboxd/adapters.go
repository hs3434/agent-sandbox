package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hs3434/agent-sandbox/internal/mcpserver"
	"github.com/hs3434/agent-sandbox/internal/orchestrator"
	"github.com/hs3434/agent-sandbox/pkg/container"
	"github.com/hs3434/agent-sandbox/pkg/remote"
	"github.com/hs3434/agent-sandbox/pkg/resource"
)

// remoteMachine adapts one named ssh machine to the exec/file faces the
// orchestrator routes to (machine selector semantics, AD-026).
type remoteMachine struct {
	be   *remote.Backend
	name string
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

func (m remoteMachine) ReadFile(ctx context.Context, id resource.ResourceID, path string) (string, error) {
	return m.be.ReadFile(ctx, m.name, path)
}

func (m remoteMachine) WriteFile(ctx context.Context, id resource.ResourceID, path, content string) error {
	return m.be.WriteFile(ctx, m.name, path, content)
}

func (m remoteMachine) Health() string { return "connected" }

// orchestratorFaceAdapter exposes the orchestrator as the mcpserver contract
// (face.go interface) — the single seam between the two internal layers.
type orchestratorFaceAdapter struct{ o *orchestrator.Orchestrator }

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

func (a orchestratorFaceAdapter) ShellNew(ctx context.Context, machine, purpose string) (any, error) {
	return nil, fmt.Errorf("shell_new: sessions land with the MCP face hardening (P0 = stateless exec per call)")
}

func (a orchestratorFaceAdapter) ShellRemove(shellID string) bool { return false }

func (a orchestratorFaceAdapter) ShellList() []any { return nil }

func (a orchestratorFaceAdapter) FileRead(ctx context.Context, machine, path string) (string, error) {
	return a.o.FileRead(ctx, machine, path)
}

func (a orchestratorFaceAdapter) FileWrite(ctx context.Context, machine, path, content string) error {
	return a.o.FileWrite(ctx, machine, path, content)
}

func (a orchestratorFaceAdapter) FilePatch(ctx context.Context, machine, path, oldS, newS string, all bool) (int, error) {
	return a.o.FilePatch(ctx, machine, path, oldS, newS, all)
}

func (a orchestratorFaceAdapter) FileSearch(ctx context.Context, machine, dir, pattern, searchType string) ([]string, error) {
	return a.o.FileSearch(ctx, machine, dir, pattern, searchType)
}

func (a orchestratorFaceAdapter) Status(ctx context.Context) (map[string]any, error) {
	return a.o.Status(ctx)
}

func (a orchestratorFaceAdapter) WorkdirFor(name string) string {
	return a.o.WorkdirFor(name)
}

var _ = sort.Strings
var _ = strings.TrimSpace
