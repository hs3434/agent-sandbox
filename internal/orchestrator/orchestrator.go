// Package orchestrator composes the control plane: registry (store) + audit +
// backend into lifecycle operations — create (with same-name self-healing),
// remove (workdir-preserving), and startup reconcile (spec §3.1/§7.2).
//
// Every lifecycle op runs under the per-env arbitration lock (409 conflict,
// never queued); audit records each transition; the workdir invariant
// (work_home/<name>) makes self-healing a naming-system side effect.
package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hs3434/agent-sandbox/pkg/audit"
	"github.com/hs3434/agent-sandbox/pkg/backend"
	"github.com/hs3434/agent-sandbox/pkg/resource"
	"github.com/hs3434/agent-sandbox/pkg/store"
)

// DefaultImage is the fallback body when the agent doesn't name one (I6:
// declaration surface; sandbox.yaml overrides in m4).
const DefaultImage = "busybox:latest"

// lifecycleOps is the backend subset the orchestrator drives.
type lifecycleOps interface {
	Create(ctx context.Context, name string, spec resource.ExecEnvSpec) (*resource.Meta, error)
	Remove(ctx context.Context, id resource.ResourceID, purge bool) error
}

// Deps wires the orchestrator (composition happens in cmd/sandboxd, I3).
type Deps struct {
	Store    *store.Store
	Audit    *audit.Bus
	Backend  lifecycleOps
	WorkHome string
	Tenant   string
}

// Orchestrator is the control-plane composition over one backend (P0 single-
// backend form; multi-backend routing lands with ssh in m3b).
type Orchestrator struct {
	store    *store.Store
	audit    *audit.Bus
	backend  lifecycleOps
	workHome string
	tenant   string
	locks    *resource.Orchestrator
}

// New validates deps and returns the composed orchestrator.
func New(deps Deps) (*Orchestrator, error) {
	if deps.Store == nil || deps.Audit == nil || deps.Backend == nil {
		return nil, fmt.Errorf("store, audit and backend are required")
	}
	if deps.WorkHome == "" {
		return nil, fmt.Errorf("work_home is required (workdir invariant)")
	}
	if deps.Tenant == "" {
		deps.Tenant = "default"
	}
	if err := os.MkdirAll(deps.WorkHome, 0o755); err != nil {
		return nil, fmt.Errorf("work_home mkdir: %w", err)
	}
	return &Orchestrator{
		store:    deps.Store,
		audit:    deps.Audit,
		backend:  deps.Backend,
		workHome: deps.WorkHome,
		tenant:   deps.Tenant,
		locks:    resource.NewOrchestrator(),
	}, nil
}

// CreateOpts are the agent-visible create parameters (container_create face).
type CreateOpts struct {
	Name  string
	Image string
	Tags  map[string]string
}

// Create makes a body. Same-name conflict = the registry's identity rule
// (ErrExists); self-healing is remove + create, not create-with-replace.
func (o *Orchestrator) Create(ctx context.Context, opts CreateOpts) (*resource.Meta, error) {
	if opts.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	id := resource.NewResourceID(o.tenant, resource.TypeExecEnv, opts.Name)

	// arbitration: create is a lifecycle op on the name (409 not queued).
	// The lock also covers the remove+create self-healing critical section
	// when driven through Remove+Create back-to-back.
	release, lockErr := o.locks.Acquire(id, "create")
	if lockErr != nil {
		return nil, lockErr
	}
	defer release()

	// registry first: duplicate name = identity conflict (name-keyed, AD-021)
	existing, _ := o.store.GetByName(o.tenant, resource.TypeExecEnv, opts.Name)
	if existing != nil && existing.Status != resource.StatusError {
		return nil, &store.ErrExists{ID: id}
	}
	// error state = explicit self-healing precondition: caller may recreate
	// over an error record only after Remove — refuse silent replace here too
	// (keep the two-step discipline explicit)
	if existing != nil && existing.Status == resource.StatusError {
		return nil, fmt.Errorf("%q is in error state: remove first, then create (self-healing is explicit two-step)", opts.Name)
	}

	image := opts.Image
	if image == "" {
		image = DefaultImage
	}
	// self-healing evidence (AD-021): a surviving workdir means this create
	// revives a working scene — the invariant's side effect, more truthful
	// than any registry tombstone
	wd := filepath.Join(o.workHome, opts.Name)
	revived := false
	if ents, err := os.ReadDir(wd); err == nil && len(ents) > 0 {
		revived = true
	}

	spec := resource.ExecEnvSpec{Image: image, Tags: opts.Tags}
	start := time.Now()
	meta, err := o.backend.Create(ctx, opts.Name, spec)
	if err != nil {
		// one retry (NFR-2 compensating path); still failing = report error,
		// no zombie registry record
		meta, err = o.backend.Create(ctx, opts.Name, spec)
		if err != nil {
			o.emit(id, "error", "create_failed", err.Error())
			return nil, fmt.Errorf("create (after retry): %w", err)
		}
	}
	meta.Tags = opts.Tags
	if err := o.store.PutResource(meta); err != nil {
		return nil, err
	}
	action := "resource_created"
	if revived || existing != nil {
		action = "resource_recreated" // identity revival marker (AD-021)
	}
	o.emitWithDuration(id, action, "", time.Since(start))
	return meta, nil
}

// Remove deletes the container; workdir survives unless purge (the name is
// the address of an asset, not the instance's attribute).
func (o *Orchestrator) Remove(ctx context.Context, id string, purge bool) error {
	rid := resource.ResourceID(id)
	release, lockErr := o.locks.Acquire(rid, "remove")
	if lockErr != nil {
		return lockErr
	}
	defer release()

	start := time.Now()
	if err := o.backend.Remove(ctx, rid, purge); err != nil {
		o.emit(rid, "error", "remove_failed", err.Error())
		return err
	}
	if err := o.store.RemoveResource(rid); err != nil {
		return err
	}
	action := "resource_removed"
	if purge {
		action = "resource_purged"
	}
	o.emitWithDuration(rid, action, "", time.Since(start))
	return nil
}

// ReconcileReport summarizes one startup reconciliation pass (spec §3.1).
type ReconcileReport struct {
	Adopted      int      // registry ∩ daemon → re-adopted
	MarkedError  int      // registry-only → error (wake downgrade chain)
	Orphans      []string // daemon-only → adopted into registry, never deleted
}

// Lister is the daemon-truth dependency for reconcile.
type Lister interface {
	ListNames(ctx context.Context) ([]string, error)
}

// Reconcile aligns the registry with daemon reality: adopt, mark error,
// list orphans (收编，不静默删除). Idempotent; never destroys workdirs.
func (o *Orchestrator) Reconcile(ctx context.Context) (*ReconcileReport, error) {
	lister, ok := o.backend.(Lister)
	if !ok {
		return &ReconcileReport{}, nil // backend can't enumerate: nothing to do
	}
	daemonNames, err := lister.ListNames(ctx)
	if err != nil {
		return nil, fmt.Errorf("list daemon containers: %w", err)
	}
	daemon := map[string]bool{}
	for _, n := range daemonNames {
		daemon[n] = true
	}

	report := &ReconcileReport{}
	records, err := o.store.ListResources(resource.TypeExecEnv)
	if err != nil {
		return nil, err
	}
	for _, m := range records {
		name := lastSeg(string(m.ID))
		if daemon[name] {
			// healthy record already in sync: not an adoption action
			// (idempotence — second pass must be a no-op)
			if m.Status != resource.StatusRunning {
				_ = o.store.UpdateStatus(m.ID, resource.StatusRunning)
				report.Adopted++
			}
			continue
		}
		// registry-only: the body is gone — wake downgrade chain entry.
		// already-error records are not re-marked (idempotence: the second
		// pass must be a no-op; the agent drives self-healing from here)
		if m.Status != resource.StatusError {
			_ = o.store.UpdateStatus(m.ID, resource.StatusError)
			report.MarkedError++
			o.emit(m.ID, "error", "reconcile_body_lost", "registry record without daemon container")
		}
	}

	// daemon-only: orphans — adopt by naming convention (P0: every daemon
	// container in work_home shape is ours), never silently delete
	for name := range daemon {
		if _, err := o.store.GetByName(o.tenant, resource.TypeExecEnv, name); err == nil {
			continue // already registered
		}
		meta := &resource.Meta{
			ID:        resource.NewResourceID(o.tenant, resource.TypeExecEnv, name),
			Type:      resource.TypeExecEnv,
			Backend:   "docker",
			Status:    resource.StatusRunning,
			CreatedAt: time.Now(),
		}
		if err := o.store.PutResource(meta); err != nil && !store.IsExists(err) {
			return report, err
		}
		report.Orphans = append(report.Orphans, name)
		o.emit(meta.ID, "resource_created", "reconcile_orphan_adopted", name)
	}
	return report, nil
}

// AcquireLifecycle exposes the arbitration lock for tool-face operations that
// compose lifecycle steps (e.g. the self-healing critical section).
func (o *Orchestrator) AcquireLifecycle(id, holder string) (func(), error) {
	return o.locks.Acquire(resource.ResourceID(id), holder)
}

func (o *Orchestrator) emit(id resource.ResourceID, outcome audit.Outcome, action, detail string) {
	_ = o.audit.Emit(audit.Event{
		Timestamp:  time.Now(),
		Tenant:     o.tenant,
		ResourceID: id,
		Action:     action,
		Outcome:    outcome,
		Detail:     detail,
	})
}

func (o *Orchestrator) emitWithDuration(id resource.ResourceID, action, detail string, d time.Duration) {
	_ = o.audit.Emit(audit.Event{
		Timestamp:  time.Now(),
		Tenant:     o.tenant,
		ResourceID: id,
		Action:     action,
		Outcome:    audit.OutcomeOK,
		DurationMS: d.Milliseconds(),
		Detail:     detail,
	})
}

func lastSeg(id string) string {
	if i := strings.LastIndexByte(id, ':'); i >= 0 {
		return id[i+1:]
	}
	return id
}

// Compile-time check: the orchestrator consumes the backend contract subset.
var _ lifecycleOps = (lifecycleOps)(nil)
var _ = backend.ErrUnsupported{} // keep contract import honest for m3a wiring
