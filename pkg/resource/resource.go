// Package resource defines the unified resource model for the agent sandbox
// platform. Following the "everything is a resource" philosophy from the v3
// design spec: all resource types share identity, governance (audit/auth/tags),
// and lifecycle management; operations are differentiated by capability.
//
// Design principle (spec §10 v3.2): a capability is a platform-guaranteed
// runtime primitive, NOT software inventory. See design-spec-v3 for the
// full narrative; code comments here stay minimal to avoid drift.
package resource

import (
	"fmt"
	"time"
)

// Type enumerates the resource categories in the platform.
type Type string

const (
	TypeExecEnv    Type = "exec_env"   // containers, VMs, pods (stateful)
	TypePolicy     Type = "policy"     // network/command/quota rules (versioned config)
	TypeCredential Type = "credential" // secrets: inject/recycle only, never snapshot
	TypeSnapshot   Type = "snapshot"   // captured state of an exec_env
)

// Capability declares what operations a resource supports.
// Platform-guaranteed primitives only — see design-spec-v3 §10 (v3.2).
type Capability string

const (
	CapShell    Capability = "shell"
	CapFiles    Capability = "files"
	CapSnapshot Capability = "snapshot"
)

// Limits are resource quotas attached to an exec_env (keep-alive
// mechanism, spec §8).
type Limits struct {
	CPU    float64
	Memory string // e.g. "4Gi"
	PIDs   int
}

// ResourceID is the globally unique identifier: "{tenant}:{type}:{name}".
type ResourceID string

// NewResourceID builds a canonical resource identifier.
func NewResourceID(tenant string, typ Type, name string) ResourceID {
	return ResourceID(fmt.Sprintf("%s:%s:%s", tenant, typ, name))
}

// Status is the runtime state machine for exec_env resources
// (design-spec-v3 §3.1, full P0 set):
//
//	created → starting → running → stopping → stopped → removing → removed
//	                              └→ error (rebuild-only terminal, AD-021)
type Status string

const (
	StatusCreated  Status = "created"
	StatusStarting Status = "starting"
	StatusRunning  Status = "running"
	StatusStopping Status = "stopping"
	StatusStopped  Status = "stopped"
	StatusRemoving Status = "removing"
	StatusRemoved  Status = "removed"
	StatusError    Status = "error"
)

// Meta is the shared metadata attached to every resource.
type Meta struct {
	ID           ResourceID
	Type         Type
	Capabilities []Capability // canonical representation; spec capability list
	Backend      string
	Status       Status
	Tags         map[string]string
	Owner        string // task/session that created it
	CreatedAt    time.Time
	Image        string // exec_env: the image this body was created from (spec)
}

// ExecEnvSpec is the create-time specification for an exec_env resource.
// Specs are immutable: changing a spec means rebuild (immutable
// infrastructure, design-spec-v3 §3.1).
type ExecEnvSpec struct {
	Image        string
	Capabilities []Capability
	Limits       Limits
	Tags         map[string]string
}
