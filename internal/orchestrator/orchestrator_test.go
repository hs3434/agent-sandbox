package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hs3434/agent-sandbox/pkg/audit"
	"github.com/hs3434/agent-sandbox/pkg/resource"
	"github.com/hs3434/agent-sandbox/pkg/store"
)

// fakeBackend is a scriptable backend for orchestrator unit tests (no docker).
type fakeBackend struct {
	mu         sync.Mutex
	created    []string
	removed    []string
	failCreate bool
	containers map[string]bool // name -> exists
	workHome   string          // set by the harness (workdir invariant)
}

func newFake() *fakeBackend {
	return &fakeBackend{containers: map[string]bool{}}
}

func (f *fakeBackend) Create(_ context.Context, name string, _ resource.ExecEnvSpec) (*resource.Meta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failCreate {
		return nil, fmt.Errorf("simulated create failure")
	}
	f.created = append(f.created, name)
	f.containers[name] = true
	// docker backend contract includes workdir creation (workdir invariant)
	os.MkdirAll(filepath.Join(f.workHome, name), 0o755)
	return &resource.Meta{
		ID:        resource.NewResourceID("local", resource.TypeExecEnv, name),
		Type:      resource.TypeExecEnv,
		Backend:   "docker",
		Status:    resource.StatusRunning,
		CreatedAt: time.Now(),
	}, nil
}

func (f *fakeBackend) Remove(_ context.Context, id resource.ResourceID, purge bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := lastSeg(string(id))
	delete(f.containers, name)
	f.removed = append(f.removed, name)
	return nil
}

func (f *fakeBackend) Exists(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.containers[name]
}

func (f *fakeBackend) ListNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for n := range f.containers {
		out = append(out, n)
	}
	return out
}

func (f *fakeBackend) SetWorkHome(dir string) { f.workHome = dir }

// harness wires store+audit+orchestrator with a fake backend.
type harness struct {
	t        *testing.T
	orch     *Orchestrator
	st       *store.Store
	fb       *fakeBackend
	bus      *audit.Bus
	workHome string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	sink, err := audit.OpenSQLite(filepath.Join(dir, "audit.db"))
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	t.Cleanup(func() { sink.Close() })
	bus := audit.NewBus(sink)
	fb := newFake()
	workHome := filepath.Join(dir, "work_home")
	os.MkdirAll(workHome, 0o755)
	fb.workHome = workHome
	orch, err := New(Deps{
		Store:    st,
		Audit:    bus,
		Backend:  fakeAdapter{fb},
		WorkHome: workHome,
		Tenant:   "local",
	})
	if err != nil {
		t.Fatalf("orchestrator: %v", err)
	}
	return &harness{t: t, orch: orch, st: st, fb: fb, bus: bus, workHome: workHome}
}

// fakeAdapter adapts fakeBackend to the backend.Backend interface subset the
// orchestrator consumes (Create/Remove only for lifecycle tests).
type fakeAdapter struct{ f *fakeBackend }

func (a fakeAdapter) Create(ctx context.Context, name string, spec resource.ExecEnvSpec) (*resource.Meta, error) {
	return a.f.Create(ctx, name, spec)
}

func (a fakeAdapter) Remove(ctx context.Context, id resource.ResourceID, purge bool) error {
	return a.f.Remove(ctx, id, purge)
}

// ListNames exposes daemon truth for reconcile (docker backend implements this).
func (a fakeAdapter) ListNames(ctx context.Context) ([]string, error) {
	return a.f.ListNames(), nil
}

func TestCreateRegistersAndRuns(t *testing.T) {
	h := newHarness(t)
	meta, err := h.orch.Create(context.Background(), CreateOpts{Name: "dev-1"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if meta.Status != resource.StatusRunning {
		t.Fatalf("status = %s", meta.Status)
	}
	got, err := h.st.GetByName("local", resource.TypeExecEnv, "dev-1")
	if err != nil || got == nil {
		t.Fatalf("registry missing: %v", err)
	}
}

func TestDuplicateCreateRejected(t *testing.T) {
	h := newHarness(t)
	if _, err := h.orch.Create(context.Background(), CreateOpts{Name: "dev-1"}); err != nil {
		t.Fatalf("create1: %v", err)
	}
	_, err := h.orch.Create(context.Background(), CreateOpts{Name: "dev-1"})
	if err == nil || !store.IsExists(err) {
		t.Fatalf("want ErrExists, got %v", err)
	}
}

func TestSelfHealSameNameRecreate(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	// body 1
	h.orch.Create(ctx, CreateOpts{Name: "dev-2"})
	// workdir holds an asset
	asset := filepath.Join(h.orch.workHome, "dev-2", "result.txt")
	os.WriteFile(asset, []byte("precious"), 0o644)

	// simulate environment break: registry says error
	h.st.UpdateStatus(resource.NewResourceID("local", resource.TypeExecEnv, "dev-2"), resource.StatusError)

	// self-heal: remove + same-name create
	if err := h.orch.Remove(ctx, "local:exec_env:dev-2", false); err != nil {
		t.Fatalf("remove: %v", err)
	}
	meta, err := h.orch.Create(ctx, CreateOpts{Name: "dev-2"})
	if err != nil {
		t.Fatalf("recreate: %v", err)
	}
	if meta.Status != resource.StatusRunning {
		t.Fatalf("revived status = %s", meta.Status)
	}
	// workdir invariant: asset survived
	data, err := os.ReadFile(asset)
	if err != nil || string(data) != "precious" {
		t.Fatalf("workdir asset lost: %v", err)
	}
	// audit carries resource_recreated (self-healing marker)
	evs, _ := h.bus.Query(audit.Query{Tenant: "local", Limit: 50})
	found := false
	for _, e := range evs {
		if e.Action == "resource_recreated" && e.ResourceID == meta.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("audit missing resource_recreated marker")
	}
}

func TestCreateFailureMarksError(t *testing.T) {
	h := newHarness(t)
	h.fb.failCreate = true
	_, err := h.orch.Create(context.Background(), CreateOpts{Name: "dev-3"})
	if err == nil {
		t.Fatal("want error from failing backend")
	}
	// retry-once-then-error: registry has no zombie running record
	got, _ := h.st.GetByName("local", resource.TypeExecEnv, "dev-3")
	if got != nil && got.Status == resource.StatusRunning {
		t.Fatal("failed create must not leave running status")
	}
}

func TestReconcileConfirmsOursAndMarksDead(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	// registered AND daemon has it → confirm (intent completed / status fixed)
	h.orch.Create(ctx, CreateOpts{Name: "alive-1"})
	// simulate a stale record (sandboxd crashed mid-stop): registry says
	// stopped, daemon says running → reconcile corrects to running
	h.st.UpdateStatus(resource.NewResourceID("local", resource.TypeExecEnv, "alive-1"), resource.StatusStopped)
	// registered but daemon lost it → error
	h.orch.Create(ctx, CreateOpts{Name: "lost-1"})
	h.fb.mu.Lock()
	delete(h.fb.containers, "lost-1")
	h.fb.mu.Unlock()
	// daemon has it but the registry doesn't → NOT ours. The registry is the
	// single record of what we manage; a container we never recorded belongs
	// to someone else (shared daemon). Reported, never claimed, never deleted.
	h.fb.mu.Lock()
	h.fb.containers["foreign-1"] = true
	h.fb.mu.Unlock()

	report, err := h.orch.Reconcile(ctx)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if report.Adopted != 1 {
		t.Fatalf("adopted = %d, want 1 (alive-1)", report.Adopted)
	}
	if report.MarkedError != 1 {
		t.Fatalf("markedError = %d, want 1 (lost-1)", report.MarkedError)
	}
	got, _ := h.st.GetByName("local", resource.TypeExecEnv, "lost-1")
	if got.Status != resource.StatusError {
		t.Fatalf("lost-1 status = %s, want error", got.Status)
	}
	if len(report.Unmanaged) != 1 || report.Unmanaged[0] != "foreign-1" {
		t.Fatalf("unmanaged = %v, want [foreign-1]", report.Unmanaged)
	}
	if foreign, _ := h.st.GetByName("local", resource.TypeExecEnv, "foreign-1"); foreign != nil {
		t.Fatal("foreign container must stay unregistered — the registry is the authority")
	}
	h.fb.mu.Lock()
	stillThere := h.fb.containers["foreign-1"]
	h.fb.mu.Unlock()
	if !stillThere {
		t.Fatal("foreign container must not be deleted")
	}
	// idempotent: second reconcile is a no-op
	report2, err := h.orch.Reconcile(ctx)
	if err != nil {
		t.Fatalf("reconcile2: %v", err)
	}
	if report2.Adopted != 0 || report2.MarkedError != 0 {
		t.Fatalf("second reconcile not idempotent: %+v", report2)
	}
}

// TestCreateWritesIntentBeforeBody: the registry row lands before the
// container exists — a crash in between leaves a row we own (never an
// unclaimed daemon container).
func TestCreateWritesIntentBeforeBody(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.orch.Create(ctx, CreateOpts{Name: "intent-1", Image: "alpine:3"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	m, err := h.st.GetByName("local", resource.TypeExecEnv, "intent-1")
	if err != nil || m == nil {
		t.Fatalf("registry row missing: %v", err)
	}
	if m.Status != resource.StatusRunning {
		t.Fatalf("status = %s, want running", m.Status)
	}
	if m.Image != "alpine:3" {
		t.Fatalf("image = %q, want alpine:3 (the DB records what the body is)", m.Image)
	}
}

// TestCreateFailureLeavesErrorRow: a failed create leaves an error row (the
// two-step self-heal precondition), never a running record.
func TestCreateFailureLeavesErrorRow(t *testing.T) {
	h := newHarness(t)
	h.fb.failCreate = true
	if _, err := h.orch.Create(context.Background(), CreateOpts{Name: "dev-3"}); err == nil {
		t.Fatal("want error from failing backend")
	}
	m, _ := h.st.GetByName("local", resource.TypeExecEnv, "dev-3")
	if m == nil {
		t.Fatal("failed create must leave an error row (self-heal precondition)")
	}
	if m.Status != resource.StatusError {
		t.Fatalf("status = %s, want error", m.Status)
	}
}

func TestConcurrentLifecycleConflicts(t *testing.T) {
	h := newHarness(t)
	// one op holds the lock; the second must conflict, not queue
	if _, err := h.orch.Create(context.Background(), CreateOpts{Name: "dev-4"}); err != nil {
		t.Fatalf("setup create: %v", err)
	}
	release, err := h.orch.AcquireLifecycle("local:exec_env:dev-4", "test-hold")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer release()
	_, err = h.orch.Create(context.Background(), CreateOpts{Name: "dev-4"})
	// duplicate name would be ErrExists; to isolate the conflict path use Remove
	err = h.orch.Remove(context.Background(), "local:exec_env:dev-4", false)
	if err == nil || !resource.IsConflict(err) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
}

// TestRemoveByName resolves the bare name to the canonical resource id
// (the name is the address of an asset — agents think in names).
func TestRemoveByName(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.orch.Create(ctx, CreateOpts{Name: "byname"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := h.orch.Remove(ctx, "byname", false); err != nil {
		t.Fatalf("remove by name: %v", err)
	}
	if m, _ := h.st.GetByName("local", resource.TypeExecEnv, "byname"); m != nil {
		t.Fatal("registry record must be gone after remove")
	}
	if err := h.orch.Remove(ctx, "byname", false); err == nil {
		t.Fatal("remove of unknown name must fail loudly")
	}
}
