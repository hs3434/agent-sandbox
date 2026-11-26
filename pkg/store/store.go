// Package store persists control-plane state: the resource registry
// (name-keyed, design-spec-v3 AD-021/AD-022) and the async process handle
// ledger (spec §4.2 — data, not tools; survives daemon restarts).
//
// SQLite (pure-Go driver) is the P0 store; everything here is keyed by
// canonical ResourceID except the name-keyed registry index that makes
// same-name recreate a first-class self-healing path.
package store

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite" // pure-Go driver, no CGO

	"github.com/hs3434/agent-sandbox/pkg/resource"
)

// ErrExists marks duplicate registration (name-keyed identity conflict).
type ErrExists struct{ ID resource.ResourceID }

func (e *ErrExists) Error() string { return fmt.Sprintf("resource %q already exists", e.ID) }

// IsExists reports whether err is a duplicate-registration error.
func IsExists(err error) bool {
	_, ok := err.(*ErrExists)
	return ok
}

// Handle states for the async process ledger (spec §4.2).
const (
	HandleRunning = "running"
	HandleDead    = "dead"
)

// Handle is one ledger row: an async process started via shell_exec
// (async=true). Death transitions are explicit — never silently cleaned.
type Handle struct {
	ID          string
	EnvID       resource.ResourceID
	PGID        int
	Cmd         string
	State       string
	ExitReason  string
	ExeEnv      string // backend that owns the process
	CreatedAt   string // RFC3339; ledger is control-plane data, clock is platform's
}

// Store wraps the SQLite database for registry + ledger persistence.
type Store struct {
	db *sql.DB
}

// Open creates/opens the store, applying the schema idempotently.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS resources (
			id TEXT PRIMARY KEY,
			type TEXT NOT NULL,
			name TEXT NOT NULL,
			tenant TEXT NOT NULL,
			backend TEXT,
			status TEXT NOT NULL,
			spec TEXT,
			tags TEXT,
			owner TEXT,
			created_at TEXT NOT NULL,
			UNIQUE(tenant, type, name)
		)`,
		`CREATE TABLE IF NOT EXISTS handles (
			id TEXT PRIMARY KEY,
			env_id TEXT NOT NULL,
			pgid INTEGER NOT NULL,
			cmd TEXT,
			state TEXT NOT NULL,
			exit_reason TEXT,
			backend TEXT,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_handles_env ON handles(env_id)`,
		`CREATE TABLE IF NOT EXISTS state_doc (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			doc TEXT
		)`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

// PutResource inserts a resource; duplicate (tenant,type,name) is ErrExists.
func (s *Store) PutResource(m *resource.Meta) error {
	tenant, _, name, err := splitID(m.ID)
	if err != nil {
		return err
	}
	tags, err := encodeTags(m.Tags)
	if err != nil {
		return err
	}
	spec, err := encodeSpec(m)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`INSERT INTO resources (id, type, name, tenant, backend, status, spec, tags, owner, created_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`,
		string(m.ID), string(m.Type), name, tenant, m.Backend, string(m.Status),
		spec, tags, m.Owner, m.CreatedAt.UTC().Format(timeRFC3339))
	if isUniqueViolation(err) {
		return &ErrExists{ID: m.ID}
	}
	return err
}

// GetResource loads by canonical ID.
func (s *Store) GetResource(id resource.ResourceID) (*resource.Meta, error) {
	row := s.db.QueryRow(
		`SELECT id, type, backend, status, tags, owner, created_at, spec
		 FROM resources WHERE id = ?`, string(id))
	return scanResource(row)
}

// GetByName loads by name-keyed identity — the self-healing path
// (same name = same resource revived, AD-021).
func (s *Store) GetByName(tenant string, typ resource.Type, name string) (*resource.Meta, error) {
	row := s.db.QueryRow(
		`SELECT id, type, backend, status, tags, owner, created_at, spec
		 FROM resources WHERE tenant = ? AND type = ? AND name = ?`,
		tenant, string(typ), name)
	return scanResource(row)
}

// UpdateStatus transitions the stored status.
func (s *Store) UpdateStatus(id resource.ResourceID, status resource.Status) error {
	res, err := s.db.Exec(
		`UPDATE resources SET status = ? WHERE id = ?`, string(status), string(id))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("resource %q not found", id)
	}
	return nil
}

// ListResources returns all resources of a type.
func (s *Store) ListResources(typ resource.Type) ([]*resource.Meta, error) {
	rows, err := s.db.Query(
		`SELECT id, type, backend, status, tags, owner, created_at, spec
		 FROM resources WHERE type = ? ORDER BY created_at`, string(typ))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*resource.Meta
	for rows.Next() {
		m, err := scanResource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// RemoveResource deletes the registry row. Workdir on disk is untouched —
// purge is a separate explicit decision (workdir invariant, spec §7.0).
func (s *Store) RemoveResource(id resource.ResourceID) error {
	res, err := s.db.Exec(`DELETE FROM resources WHERE id = ?`, string(id))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("resource %q not found", id)
	}
	return nil
}

// PutHandle records a new async process handle.
func (s *Store) PutHandle(h Handle) error {
	_, err := s.db.Exec(
		`INSERT INTO handles (id, env_id, pgid, cmd, state, exit_reason, backend, created_at)
		 VALUES (?,?,?,?,?,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET state=excluded.state, exit_reason=excluded.exit_reason`,
		h.ID, string(h.EnvID), h.PGID, h.Cmd, h.State, h.ExitReason, h.ExeEnv,
		nowOr(h.CreatedAt))
	return err
}

// GetHandle loads one handle.
func (s *Store) GetHandle(id string) (*Handle, error) {
	row := s.db.QueryRow(
		`SELECT id, env_id, pgid, cmd, state, exit_reason, backend, created_at
		 FROM handles WHERE id = ?`, id)
	var h Handle
	var envID string
	if err := row.Scan(&h.ID, &envID, &h.PGID, &h.Cmd, &h.State, &h.ExitReason, &h.ExeEnv, &h.CreatedAt); err != nil {
		return nil, err
	}
	h.EnvID = resource.ResourceID(envID)
	return &h, nil
}

// MarkHandleDead transitions a handle to dead. reason carries the D-21
// terminal state ("exited(N)" | "signaled(N)" | "exit_unknown") — never a
// fabricated 0. Explicit only (spec §4.2).
func (s *Store) MarkHandleDead(id, reason string) error {
	res, err := s.db.Exec(
		`UPDATE handles SET state = 'dead', exit_reason = ? WHERE id = ?`, reason, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("handle %q not found", id)
	}
	return nil
}

// ListRunningHandles returns running rows across all envs — the sandboxd
// restart reconciliation seed (D-06: write-ahead rows are the recovery input).
func (s *Store) ListRunningHandles() ([]Handle, error) {
	rows, err := s.db.Query(
		`SELECT id, env_id, pgid, cmd, state, exit_reason, backend, created_at
		 FROM handles WHERE state = ? ORDER BY created_at`, HandleRunning)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Handle
	for rows.Next() {
		var h Handle
		var env string
		if err := rows.Scan(&h.ID, &env, &h.PGID, &h.Cmd, &h.State, &h.ExitReason, &h.ExeEnv, &h.CreatedAt); err != nil {
			return nil, err
		}
		h.EnvID = resource.ResourceID(env)
		out = append(out, h)
	}
	return out, rows.Err()
}

// ListHandles returns all handles for an env (status reads the ledger here).
func (s *Store) ListHandles(envID resource.ResourceID) ([]Handle, error) {
	rows, err := s.db.Query(
		`SELECT id, env_id, pgid, cmd, state, exit_reason, backend, created_at
		 FROM handles WHERE env_id = ? ORDER BY created_at`, string(envID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Handle
	for rows.Next() {
		var h Handle
		var env string
		if err := rows.Scan(&h.ID, &env, &h.PGID, &h.Cmd, &h.State, &h.ExitReason, &h.ExeEnv, &h.CreatedAt); err != nil {
			return nil, err
		}
		h.EnvID = resource.ResourceID(env)
		out = append(out, h)
	}
	return out, rows.Err()
}

// ---- state doc (single-document memory, AD-012) ----

// ErrStateEmpty marks "never written" (blank-slate guidance on state_get).
var ErrStateEmpty = fmt.Errorf("state doc not written yet")

// GetStateDoc loads the single memory document (ErrStateEmpty if never put).
func (s *Store) GetStateDoc() ([]byte, error) {
	var doc sql.NullString
	err := s.db.QueryRow(`SELECT doc FROM state_doc WHERE id = 1`).Scan(&doc)
	if err == sql.ErrNoRows || (err == nil && !doc.Valid) {
		return nil, ErrStateEmpty
	}
	if err != nil {
		return nil, err
	}
	return []byte(doc.String), nil
}

// PutStateDoc replaces the single memory document wholly.
func (s *Store) PutStateDoc(doc []byte) error {
	_, err := s.db.Exec(`INSERT INTO state_doc (id, doc) VALUES (1, ?)
		ON CONFLICT(id) DO UPDATE SET doc = excluded.doc`, string(doc))
	return err
}
