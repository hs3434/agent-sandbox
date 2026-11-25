package audit

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newSink(t *testing.T) *SQLiteSink {
	t.Helper()
	s, err := OpenSQLite(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestAppendAndQuery(t *testing.T) {
	s := newSink(t)
	e := Event{
		Timestamp: time.Now(), Tenant: "local", Subject: "local:task-1",
		ResourceID: "local:exec_env:dev-1", Action: "shell_exec",
		Outcome: OutcomeOK, Detail: `{"cmd":"ls"}`,
	}
	if err := s.Append(e); err != nil {
		t.Fatalf("append: %v", err)
	}
	got, err := s.Query(Query{Tenant: "local", Limit: 10})
	if err != nil || len(got) != 1 {
		t.Fatalf("query: %v len=%d", err, len(got))
	}
	if got[0].Action != "shell_exec" || got[0].ID == "" {
		t.Fatalf("roundtrip mismatch: %+v", got[0])
	}
}

func TestQueryFilters(t *testing.T) {
	s := newSink(t)
	now := time.Now()
	s.Append(Event{Timestamp: now, Tenant: "local", Action: "a1",
		ResourceID: "local:exec_env:dev-1"})
	s.Append(Event{Timestamp: now, Tenant: "local", Action: "a2",
		ResourceID: "local:exec_env:dev-2"})
	s.Append(Event{Timestamp: now, Tenant: "other", Action: "a3"})

	if got, _ := s.Query(Query{ResourceID: "local:exec_env:dev-2"}); len(got) != 1 || got[0].Action != "a2" {
		t.Fatalf("resource filter: %+v", got)
	}
	if got, _ := s.Query(Query{Tenant: "other"}); len(got) != 1 {
		t.Fatalf("tenant filter: %+v", got)
	}
}

func TestSecretsNeverPlaintext(t *testing.T) {
	s := newSink(t)
	secret := "super-secret-value"
	s.Append(Event{Timestamp: time.Now(), Tenant: "local", Action: "credential_inject",
		Outcome: OutcomeOK, Detail: secret, SecretFingerprint: Fingerprint(secret)})
	got, _ := s.Query(Query{Tenant: "local"})
	// NFR-5: plaintext secrets never persisted — fingerprint instead
	if len(got) != 1 || strings.Contains(got[0].Detail, secret) {
		t.Fatalf("plaintext secret in audit log: %+v", got)
	}
	if got[0].SecretFingerprint == "" {
		t.Fatal("fingerprint missing")
	}
}
