package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/hs3434/agent-sandbox/pkg/resource"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func meta(name string) *resource.Meta {
	return &resource.Meta{
		ID:        resource.NewResourceID("local", resource.TypeExecEnv, name),
		Type:      resource.TypeExecEnv,
		Backend:   "docker",
		Status:    resource.StatusCreated,
		CreatedAt: time.Now(),
	}
}

func TestRegistryPutGet(t *testing.T) {
	s := newTestStore(t)
	m := meta("dev-1")
	if err := s.PutResource(m); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := s.GetResource(m.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != m.ID || got.Status != resource.StatusCreated {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
}

func TestRegistryNameKeyed(t *testing.T) {
	s := newTestStore(t)
	// name-keyed identity: same tenant/type/name = same resource (AD-021)
	m := meta("dev-1")
	if err := s.PutResource(m); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := s.GetByName("local", resource.TypeExecEnv, "dev-1")
	if err != nil {
		t.Fatalf("getByName: %v", err)
	}
	if got.ID != m.ID {
		t.Fatalf("name keying broken: %s != %s", got.ID, m.ID)
	}
}

func TestRegistryDuplicatePutRejected(t *testing.T) {
	s := newTestStore(t)
	if err := s.PutResource(meta("dev-1")); err != nil {
		t.Fatalf("put1: %v", err)
	}
	err := s.PutResource(meta("dev-1"))
	if err == nil {
		t.Fatal("duplicate put must fail")
	}
	if !IsExists(err) {
		t.Fatalf("want ErrExists, got %v", err)
	}
}

func TestRegistryUpdateStatus(t *testing.T) {
	s := newTestStore(t)
	m := meta("dev-1")
	s.PutResource(m)
	if err := s.UpdateStatus(m.ID, resource.StatusRunning); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ := s.GetResource(m.ID)
	if got.Status != resource.StatusRunning {
		t.Fatalf("status not updated: %s", got.Status)
	}
}

func TestRegistryListAndRemove(t *testing.T) {
	s := newTestStore(t)
	s.PutResource(meta("dev-1"))
	s.PutResource(meta("dev-2"))
	all, err := s.ListResources(resource.TypeExecEnv)
	if err != nil || len(all) != 2 {
		t.Fatalf("list: %v len=%d", err, len(all))
	}
	if err := s.RemoveResource(all[0].ID); err != nil {
		t.Fatalf("remove: %v", err)
	}
	all, _ = s.ListResources(resource.TypeExecEnv)
	if len(all) != 1 {
		t.Fatalf("after remove len=%d", len(all))
	}
}

func TestHandleLedgerPersistence(t *testing.T) {
	s := newTestStore(t)
	h := Handle{
		ID:     "h-1",
		EnvID:  resource.NewResourceID("local", resource.TypeExecEnv, "dev-1"),
		PGID:   4242,
		Cmd:    "npm start",
		State:  HandleRunning,
		ExeEnv: "docker",
	}
	if err := s.PutHandle(h); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := s.GetHandle("h-1")
	if err != nil || got.PGID != 4242 || got.State != HandleRunning {
		t.Fatalf("get: %v %+v", err, got)
	}
	// death transition: explicit dead mark, never silently cleaned (spec §4.2)
	if err := s.MarkHandleDead("h-1", "exit 0"); err != nil {
		t.Fatalf("mark dead: %v", err)
	}
	got, _ = s.GetHandle("h-1")
	if got.State != HandleDead || got.ExitReason != "exit 0" {
		t.Fatalf("dead mark lost: %+v", got)
	}
	// list per env
	s.PutHandle(Handle{ID: "h-2", EnvID: h.EnvID, PGID: 5, State: HandleRunning})
	hs, err := s.ListHandles(h.EnvID)
	if err != nil || len(hs) != 2 {
		t.Fatalf("list: %v len=%d", err, len(hs))
	}
}

func TestHandleSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "test.db")
	s, _ := Open(db)
	h := Handle{ID: "h-1", PGID: 9, State: HandleRunning}
	s.PutHandle(h)
	s.Close()

	s2, err := Open(db)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	got, err := s2.GetHandle("h-1")
	if err != nil || got.PGID != 9 {
		t.Fatalf("handle lost across restart: %v %+v", err, got)
	}
}
