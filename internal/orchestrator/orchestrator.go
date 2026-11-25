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
	Store       *store.Store
	Audit       *audit.Bus
	Backend     lifecycleOps
	Backends    map[string]any // named machines (ssh) — target selectors, AD-026
	WorkHome    string
	Tenant      string
	ExecTimeout time.Duration         // user-configurable (I6 layer 1)
	ImageLister func() ([]string, error) // docker images (container_images tool)
}

// Orchestrator is the control-plane composition: one default backend (docker)
// plus named backends (ssh machines from sandbox.yaml, AD-026 selector
// semantics).
type Orchestrator struct {
	store          *store.Store
	audit          *audit.Bus
	backend        lifecycleOps
	backends       map[string]any // machine name -> backend (ssh machines)
	workHome       string
	tenant         string
	locks          *resource.Orchestrator
	defaultEnvName string              // follow-style default (dwelling ops switch)
	execTimeout    time.Duration       // I6: config override ?: 60s contract default
	imageLister    func() ([]string, error) // docker image enumeration (nil = unsupported)
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
	timeout := deps.ExecTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &Orchestrator{
		store:       deps.Store,
		audit:       deps.Audit,
		backend:     deps.Backend,
		backends:    deps.Backends,
		workHome:    deps.WorkHome,
		tenant:      deps.Tenant,
		locks:       resource.NewOrchestrator(),
		execTimeout: timeout,
		imageLister: deps.ImageLister,
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

	// write-ahead intent: the registry row lands BEFORE the container does.
	// The database is the single record of what we manage — a crash between
	// intent and action leaves a row we own (reconcile completes or marks it
	// error), never an unclaimed daemon container to guess about.
	if existing != nil {
		if err := o.store.UpdateStatus(existing.ID, resource.StatusCreated); err != nil {
			return nil, err
		}
	} else {
		intent := &resource.Meta{
			ID: id, Type: resource.TypeExecEnv, Backend: o.backendName(),
			Status: resource.StatusCreated, Tags: opts.Tags, Image: image,
			CreatedAt: time.Now(),
		}
		if err := o.store.PutResource(intent); err != nil {
			return nil, err
		}
	}

	meta, err := o.backend.Create(ctx, opts.Name, spec)
	if err != nil {
		// one retry (NFR-2 compensating path); still failing = error state
		meta, err = o.backend.Create(ctx, opts.Name, spec)
		if err != nil {
			_ = o.store.UpdateStatus(id, resource.StatusError)
			o.emit(id, "error", "create_failed", err.Error())
			return nil, fmt.Errorf("create (after retry): %w", err)
		}
	}
	// identity is the control plane's: the backend never mints its own layer
	meta.ID = id
	meta.Tags = opts.Tags
	meta.Image = image
	if err := o.store.UpdateStatus(id, resource.StatusRunning); err != nil {
		return nil, err
	}
	action := "resource_created"
	if revived || existing != nil {
		action = "resource_recreated" // identity revival marker (AD-021)
	}
	o.emitWithDuration(id, action, "", time.Since(start))
	return meta, nil
}

// backendName labels the default route's backend (registry rows need a
// backend label; named ssh machines are labeled by their own adapter).
func (o *Orchestrator) backendName() string {
	if n, ok := o.backend.(interface{ BackendName() string }); ok {
		return n.BackendName()
	}
	return "docker"
}

// ResolveID accepts either a full resource id ("tenant:exec_env:name") or a
// bare name ("name") and returns the canonical id. The name is the address
// of an asset (spec §7.0) — agents think in names.
func (o *Orchestrator) ResolveID(id string) (resource.ResourceID, error) {
	if id == "" {
		return "", fmt.Errorf("empty resource id")
	}
	if strings.Contains(id, ":") {
		return resource.ResourceID(id), nil
	}
	m, err := o.store.GetByName(o.tenant, resource.TypeExecEnv, id)
	if err != nil {
		return "", fmt.Errorf("resource %q not found", id)
	}
	return m.ID, nil
}

// Remove deletes the container; workdir survives unless purge (the name is
// the address of an asset, not the instance's attribute).
func (o *Orchestrator) Remove(ctx context.Context, id string, purge bool) error {
	rid, err := o.ResolveID(id)
	if err != nil {
		return err
	}
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
	Ours        int      // registry ∩ daemon → bodies we own that exist
	Adopted     int      // registry ∩ daemon → status corrected (stale/created → running)
	MarkedError int      // registry-only → error (wake downgrade chain)
	Unmanaged   []string // daemon-only → NOT ours (registry is the authority): reported, never claimed
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
			// our body exists: in sync = nothing to do (idempotence — the
			// second pass must be a no-op); stale or half-created = correct
			report.Ours++
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

	// daemon-only: NOT ours. The registry is the single record of what we
	// manage (every body we make lands there before it exists), so a daemon
	// container without a row belongs to someone else — on a shared daemon
	// that is the common case. Report, never claim, never delete.
	for name := range daemon {
		if _, err := o.store.GetByName(o.tenant, resource.TypeExecEnv, name); err == nil {
			continue // already registered — ours
		}
		report.Unmanaged = append(report.Unmanaged, name)
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
