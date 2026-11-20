// Package backend defines the data-plane contract for execution backends.
//
// Control plane (resource model, policy, audit) is backend-agnostic; the
// data plane implements actual execution. Each backend declares its
// capabilities honestly — a physical machine over SSH does not claim
// snapshot support (design-spec-v3 §10).
package backend

import (
	"context"
	"fmt"

	"github.com/hs3434/agent-sandbox/pkg/resource"
)

// ErrUnsupported is returned when a tool is invoked on a backend that does
// not declare the required capability.
type ErrUnsupported struct {
	Op         string
	Capability resource.Capability
}

func (e *ErrUnsupported) Error() string {
	return fmt.Sprintf("backend does not support %q (missing capability %q)", e.Op, e.Capability)
}

// Backend is the data-plane contract every execution backend implements.
// Not every backend implements every method: guard with capability checks
// and return ErrUnsupported otherwise.
type Backend interface {
	// Capabilities declares what this backend can do.
	Capabilities() []resource.Capability

	// Lifecycle.
	Create(ctx context.Context, spec resource.ExecEnvSpec) (*resource.Meta, error)
	Start(ctx context.Context, id resource.ResourceID) error
	Stop(ctx context.Context, id resource.ResourceID) error
	Remove(ctx context.Context, id resource.ResourceID) error
	Inspect(ctx context.Context, id resource.ResourceID) (*resource.Meta, error)

	// Exec runs a command in the environment (CapShell).
	Exec(ctx context.Context, id resource.ResourceID, command string) (ExecResult, error)

	// Snapshot captures env state (CapSnapshot), excluding secrets.
	// Returns the backend payload reference (docker tag, disk snapshot id)
	// stored in the snapshot registry record.
	Snapshot(ctx context.Context, id resource.ResourceID, name string) (string, error)

	// Restore rebuilds an environment from a backend payload reference.
	Restore(ctx context.Context, payload string, spec resource.ExecEnvSpec) (*resource.Meta, error)
}

// ExecResult is the unified command execution result.
type ExecResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Duration int64 // ms
}

// Registry maps backend names to instances. Backends register on startup.
type Registry struct {
	backends map[string]Backend
}

func NewRegistry() *Registry {
	return &Registry{backends: make(map[string]Backend)}
}

func (r *Registry) Register(name string, b Backend) {
	r.backends[name] = b
}

func (r *Registry) Get(name string) (Backend, error) {
	b, ok := r.backends[name]
	if !ok {
		return nil, fmt.Errorf("backend %q not registered", name)
	}
	return b, nil
}
