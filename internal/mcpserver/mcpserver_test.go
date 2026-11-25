package mcpserver

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/hs3434/agent-sandbox/pkg/audit"
)

func TestToolFaceComplete(t *testing.T) {
	// the P0 face: 17 tools / 5 families (spec §4, AD-026/027)
	srv := New(Deps{}) // no backend wiring — face audit only
	got := srv.ToolNames()
	want := []string{
		// container family (4)
		"container_create", "container_remove", "container_build", "container_images",
		// shell family (4)
		"shell_exec", "shell_new", "shell_remove", "shell_list",
		// file family (4)
		"file_read", "file_write", "file_patch", "file_search",
		// sense family (3)
		"status", "events", "resource_inspect",
		// memory family (2)
		"state_get", "state_put",
	}
	if len(got) != len(want) {
		t.Fatalf("tool count = %d, want %d: %v", len(got), len(want), got)
	}
	set := map[string]bool{}
	for _, n := range got {
		set[n] = true
	}
	for _, n := range want {
		if !set[n] {
			t.Errorf("missing tool %q", n)
		}
	}
}

func TestFaceAuditNFR4And6(t *testing.T) {
	// NFR-4: update interface count = 0 — no tool named *_update/*_set/*_modify
	// NFR-6: credential_read count = 0 — structurally absent
	srv := New(Deps{})
	for _, n := range srv.ToolNames() {
		for _, banned := range []string{"update", "modify", "credential_read", "revert", "snapshot"} {
			if containsSub(n, banned) {
				t.Errorf("tool %q violates face audit (contains %q)", n, banned)
			}
		}
	}
}

func TestStateDocRoundtrip(t *testing.T) {
	// memory: single doc, no keys (AD-012)
	srv := New(Deps{})
	ctx := context.Background()
	res, err := srv.CallTool(ctx, "state_get", map[string]any{})
	if err != nil {
		t.Fatalf("state_get: %v", err)
	}
	// first read = blank slate with guidance (v3.10)
	if !containsSub(resText(res), "status") {
		t.Fatalf("blank template must point at status: %s", resText(res))
	}
	if _, err := srv.CallTool(ctx, "state_put", map[string]any{
		"doc": map[string]any{"plan": "ship m4"},
	}); err != nil {
		t.Fatalf("state_put: %v", err)
	}
	res, err = srv.CallTool(ctx, "state_get", map[string]any{})
	if err != nil {
		t.Fatalf("state_get2: %v", err)
	}
	if !containsSub(resText(res), "ship m4") {
		t.Fatalf("memory lost: %s", resText(res))
	}
}

func TestEventsCursor(t *testing.T) {
	sink, _ := audit.OpenSQLite(filepath.Join(t.TempDir(), "audit.db"))
	defer sink.Close()
	srv := New(Deps{Audit: audit.NewBus(sink), Tenant: "local"})
	ctx := context.Background()
	res, err := srv.CallTool(ctx, "events", map[string]any{"after_seq": 0})
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	// empty stream returns an explicit empty shape, not an error
	if !containsSub(resText(res), "events") {
		t.Fatalf("events shape wrong: %s", resText(res))
	}
}

func containsSub(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

// TestPiggybackDeliversEachEventOnce: the response body carries body events
// exactly once per session (cursor semantics — no replay of history).
func TestPiggybackDeliversEachEventOnce(t *testing.T) {
	sink, _ := audit.OpenSQLite(filepath.Join(t.TempDir(), "audit.db"))
	defer sink.Close()
	bus := audit.NewBus(sink)
	srv := New(Deps{Audit: bus, Tenant: "local"})

	for i := 0; i < 3; i++ {
		_ = bus.Emit(audit.Event{Tenant: "local", Action: "tick", Outcome: audit.OutcomeOK})
	}
	first := srv.piggyback(context.Background())
	if len(first) != 3 {
		t.Fatalf("first piggyback = %d events, want 3", len(first))
	}
	second := srv.piggyback(context.Background())
	if len(second) != 0 {
		t.Fatalf("second piggyback = %d events, want 0 (already delivered)", len(second))
	}
	// a new event is delivered once more
	_ = bus.Emit(audit.Event{Tenant: "local", Action: "tick2", Outcome: audit.OutcomeOK})
	third := srv.piggyback(context.Background())
	if len(third) != 1 {
		t.Fatalf("third piggyback = %d events, want 1", len(third))
	}
}
