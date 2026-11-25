package audit

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/hs3434/agent-sandbox/pkg/resource"
)

// SQLiteSink persists audit events (spec §6: audit = replay for the agent,
// NFR-7 100% traceability after redaction). Secret values in Detail are
// fingerprinted before insert — plaintext never touches disk (NFR-5).
type SQLiteSink struct {
	db *sql.DB
}

// OpenSQLite opens (creating if needed) the audit database.
func OpenSQLite(path string) (*SQLiteSink, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open audit sqlite: %w", err)
	}
	s := &SQLiteSink{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *SQLiteSink) migrate() error {
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS events (
			id TEXT PRIMARY KEY,
			ts TEXT NOT NULL,
			tenant TEXT NOT NULL,
			subject TEXT,
			resource_id TEXT,
			action TEXT NOT NULL,
			outcome TEXT NOT NULL,
			duration_ms INTEGER,
			detail TEXT,
			secret_fp TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_events_tenant ON events(tenant, ts)`,
		`CREATE INDEX IF NOT EXISTS idx_events_resource ON events(resource_id)`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	return nil
}

func (s *SQLiteSink) Close() error { return s.db.Close() }

// Append persists one event. ID and timestamp are assigned when empty.
// If Detail carries a secret value the caller flags it via SecretFingerprint:
// the sink replaces Detail with the fingerprint (plaintext never persisted,
// NFR-5). Convenience: SetSecretFingerprint computes it before Append.
func (s *SQLiteSink) Append(e Event) error {
	if e.ID == "" {
		e.ID = newEventID()
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now()
	}
	if e.SecretFingerprint != "" && e.Detail != "" {
		e.Detail = "[fingerprinted:" + e.SecretFingerprint[:12] + "]"
	}
	_, err := s.db.Exec(
		`INSERT INTO events (id, ts, tenant, subject, resource_id, action, outcome, duration_ms, detail, secret_fp)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`,
		e.ID, e.Timestamp.UTC().Format(time.RFC3339Nano), e.Tenant, e.Subject,
		string(e.ResourceID), e.Action, string(e.Outcome), e.DurationMS,
		e.Detail, e.SecretFingerprint)
	return err
}

// Fingerprint derives the stored sha256 prefix for a secret value.
func Fingerprint(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// Query returns matching events, oldest first.
func (s *SQLiteSink) Query(q Query) ([]Event, error) {
	where := []string{"1=1"}
	var args []any
	if q.Tenant != "" {
		where = append(where, "tenant = ?")
		args = append(args, q.Tenant)
	}
	if q.ResourceID != "" {
		where = append(where, "resource_id = ?")
		args = append(args, q.ResourceID)
	}
	if q.EventType != "" {
		where = append(where, "action = ?")
		args = append(args, string(q.EventType))
	}
	if !q.Since.IsZero() {
		where = append(where, "ts >= ?")
		args = append(args, q.Since.UTC().Format(time.RFC3339Nano))
	}
	if q.AfterSeq > 0 {
		where = append(where, "rowid > ?")
		args = append(args, q.AfterSeq)
	}
	if !q.Until.IsZero() {
		where = append(where, "ts <= ?")
		args = append(args, q.Until.UTC().Format(time.RFC3339Nano))
	}
	if q.Limit <= 0 {
		q.Limit = 100
	}
	args = append(args, q.Limit)

	rows, err := s.db.Query(
		`SELECT rowid, id, ts, tenant, subject, resource_id, action, outcome, duration_ms, detail, secret_fp
		 FROM events WHERE `+joinAnd(where)+` ORDER BY rowid LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var ts, rid, outcome string
		if err := rows.Scan(&e.Seq, &e.ID, &ts, &e.Tenant, &e.Subject, &rid, &e.Action,
			&outcome, &e.DurationMS, &e.Detail, &e.SecretFingerprint); err != nil {
			return nil, err
		}
		e.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
		e.ResourceID = resource.ResourceID(rid)
		e.Outcome = Outcome(outcome)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Compile-time interface check.
var _ Sink = (*SQLiteSink)(nil)

func joinAnd(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " AND "
		}
		out += p
	}
	return out
}

func newEventID() string {
	b := make([]byte, 8)
	// timestamp + entropy is enough for P0 single-node ordering
	now := time.Now().UnixNano()
	for i := 0; i < 8; i++ {
		b[i] = byte(now >> (8 * (7 - i)))
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:6])
}
