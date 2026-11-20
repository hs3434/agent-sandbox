// Package snapshot implements the snapshot/revert subsystem
// (design-spec-v3 §7).
//
// Revert follows the five-step flow defined in spec §7.2 (validate, stop +
// quiesce workdir, rebuild + re-mount workdir, reconcile non-state
// resources, healthcheck + audit). Snapshots NEVER carry policies,
// credentials, or the workdir: environment state only — everything else
// re-binds from its live channel after restore.
package snapshot

import (
	"time"

	"github.com/hs3434/agent-sandbox/pkg/resource"
)

// Type classifies snapshot origin.
type Type string

const (
	TypeBaseline Type = "baseline" // automatic, at resource creation
	TypeManual   Type = "manual"   // agent-requested checkpoint
)

// Scope classifies the recovery axis a snapshot covers (spec §7.0).
// P0 implements ScopeEnv only; workdir/full scopes arrive with their
// consumers (spec documents the design, code follows demand).
type Scope string

const ScopeEnv Scope = "env"

// Snapshot is the registry record for one captured env state.
// Secrets are never stored (credentials are detached before snapshotting);
// the workdir lives outside the env snapshot axis entirely (spec §7.0).
type Snapshot struct {
	ID                    string              `json:"id"`
	ResourceID            resource.ResourceID `json:"resource_id"`
	Name                  string              `json:"name"`
	Type                  Type                `json:"type"`
	Scope                 Scope               `json:"scope"`
	CreatedAt             time.Time           `json:"created_at"`
	SizeBytes             int64               `json:"size_bytes"`
	Backend               string              `json:"backend"`
	BackendPayload        string              `json:"backend_payload"` // docker tag / disk snapshot ref
	Status                string              `json:"status"`          // available | corrupted
	ExcludedCredentialIDs []string            `json:"excluded_credentials"`
}
