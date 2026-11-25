package mcpserver

import (
	"context"
)

// OrchestratorFace is the composition the tool face drives (lifecycle +
// exec + files). The concrete implementation lives in internal/orchestrator;
// this interface is the I1 single contract surface between the two layers.
type OrchestratorFace interface {
	Create(ctx context.Context, opts CreateOpts) (*ResourceMeta, error)
	Remove(ctx context.Context, id string, purge bool) error
	Build(ctx context.Context, envID, tag, dockerfile string) error
	ListImages(ctx context.Context) ([]string, error)
	ShellExec(ctx context.Context, opts ExecOpts, cmd string) (map[string]any, error)
	ShellNew(ctx context.Context, machine, purpose string) (any, error)
	ShellRemove(shellID string) bool
	ShellList() []any
	FileRead(ctx context.Context, machine, path string) (string, error)
	FileWrite(ctx context.Context, machine, path, content string) error
	FilePatch(ctx context.Context, machine, path, oldS, newS string, all bool) (int, error)
	FileSearch(ctx context.Context, machine, dir, pattern, searchType string) ([]string, error)
	Status(ctx context.Context) (map[string]any, error)
	WorkdirFor(name string) string
}

// CreateOpts mirrors orchestrator.CreateOpts without the import (contract-only
// inter-layer traffic, self-describing payloads — I7).
type CreateOpts struct {
	Name  string
	Image string
	Tags  map[string]string
}

// ExecOpts carries shell_exec parameters.
type ExecOpts struct {
	Async   bool
	Machine string
	ShellID string
	Timeout int // seconds; 0 = contract default
}

// ResourceMeta is the self-describing payload crossing the layer boundary.
type ResourceMeta struct {
	ID      string
	Status  string
	Backend string
}
