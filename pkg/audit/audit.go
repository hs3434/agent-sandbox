// Package audit implements the audit subsystem (design-spec-v3 §6):
// a replay tool for agents ("what did I do and why did it fail"), not just
// a compliance record. Events carry tenant/task tags for scoped queries;
// secrets are never stored in plaintext (P1 adds fingerprinting with the
// credential subsystem).
//
// The full 12-event taxonomy and metering payloads live in the spec; this
// package grows event types as the platform actually emits them.
package audit

import (
	"time"

	"github.com/hs3434/agent-sandbox/pkg/resource"
)

// EventType enumerates audit event categories (P0 subset; spec §6 has the
// full taxonomy — add constants as emitters appear).
type EventType string

const (
	EventToolCall EventType = "tool_call"
	EventError    EventType = "error"
)

// Outcome of an audited operation.
type Outcome string

const OutcomeOK Outcome = "ok"

// Event is the canonical audit record.
type Event struct {
	ID         string
	Seq        int64 // storage order (rowid); cursor basis for piggyback/events
	Timestamp  time.Time
	Tenant     string
	Subject    string // operator identity (tenant:task:session)
	ResourceID resource.ResourceID
	Action     string
	Outcome    Outcome
	DurationMS int64
	Detail     string // params; secret values are fingerprinted by the sink
	SecretFingerprint string // sha256 prefix when Detail carried a secret (NFR-5)
}

// Sink is the persistence interface for audit events. In-memory and SQLite
// implementations land in P0-m2; until then Bus wraps whatever Sink the
// assembly provides.
type Sink interface {
	Append(Event) error
	Query(filter Query) ([]Event, error)
}

// Bus persists audit events through the configured Sink. The platform-layer
// metering fan-out (audit.Bus + Subscriber) is specified in spec §14.1 and
// will be added when the platform layer exists.
type Bus struct {
	sink Sink
}

func NewBus(sink Sink) *Bus { return &Bus{sink: sink} }

// Emit persists an event.
func (b *Bus) Emit(e Event) error { return b.sink.Append(e) }

func (b *Bus) Query(filter Query) ([]Event, error) { return b.sink.Query(filter) }

// Query is the audit search filter (time range, tenant, resource, type).
type Query struct {
	Tenant     string
	ResourceID string
	EventType  EventType
	Since      time.Time
	Until      time.Time
	Limit      int
	AfterSeq int64 // rowid cursor: deliver only events after this seq (piggyback dedup)
}
