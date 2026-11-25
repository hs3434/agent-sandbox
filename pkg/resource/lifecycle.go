package resource

import (
	"fmt"
	"sync"
)

// ErrConflict is the concurrency-arbitration rejection (spec §3.1: second
// lifecycle op on the same env fails immediately — 409 conflict, not queued).
type ErrConflict struct {
	ID     ResourceID
	Holder string
}

func (e *ErrConflict) Error() string {
	return fmt.Sprintf("resource %q busy: %s (concurrent lifecycle op rejected)", e.ID, e.Holder)
}

// IsConflict reports whether err is an arbitration conflict.
func IsConflict(err error) bool {
	_, ok := err.(*ErrConflict)
	return ok
}

// ErrInvalidTransition is a state-machine violation.
type ErrInvalidTransition struct {
	From, To Status
}

func (e *ErrInvalidTransition) Error() string {
	return fmt.Sprintf("invalid transition %s -> %s", e.From, e.To)
}

// transitionTable is the P0 lifecycle (spec §3.1):
//
//	created → starting → running → stopping → stopped → removing → removed
//	                              └→ error (timeout/failure; rebuild, not restart)
//
// Idempotence rule: same-state is always a no-op. Wake = stopped→starting
// (reuse, AD-008). Error is terminal until explicit remove+same-name recreate
// (AD-021 self-healing) — error→starting is deliberately absent.
var transitionTable = map[Status][]Status{
	StatusCreated:  {StatusStarting, StatusRemoving},
	StatusStarting: {StatusRunning, StatusError, StatusRemoving},
	StatusRunning:  {StatusStopping, StatusRemoving},
	StatusStopping: {StatusStopped, StatusRemoving},
	StatusStopped:  {StatusStarting, StatusRemoving}, // wake
	StatusError:    {StatusRemoving},                 // rebuild only
	StatusRemoving: {StatusRemoved},
	StatusRemoved:  {}, // terminal
}

// ValidateTransition checks from→to against the lifecycle table.
// Same-state calls are idempotent no-ops (spec §3.1).
func ValidateTransition(from, to Status) error {
	if from == to {
		return nil
	}
	for _, allowed := range transitionTable[from] {
		if allowed == to {
			return nil
		}
	}
	return &ErrInvalidTransition{From: from, To: to}
}

// Orchestrator is the per-env arbitration lock registry (spec §3.1 P0
// requirement). One lifecycle migration or self-healing rebuild at a time;
// the second concurrent op is rejected, never queued.
type Orchestrator struct {
	mu    sync.Mutex
	locks map[ResourceID]*envLock
}

type envLock struct {
	holder string
}

// NewOrchestrator returns an empty arbitration registry.
func NewOrchestrator() *Orchestrator {
	return &Orchestrator{locks: make(map[ResourceID]*envLock)}
}

// Acquire takes the lifecycle lock for id. Returns a release func.
// holder labels the operation for the conflict error (e.g. "rebuild", "stop").
func (o *Orchestrator) Acquire(id ResourceID, holder ...string) (func(), error) {
	who := "op"
	if len(holder) > 0 && holder[0] != "" {
		who = holder[0]
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if l, busy := o.locks[id]; busy {
		return nil, &ErrConflict{ID: id, Holder: l.holder}
	}
	o.locks[id] = &envLock{holder: who}
	released := false
	return func() {
		o.mu.Lock()
		defer o.mu.Unlock()
		if !released {
			released = true
			delete(o.locks, id)
		}
	}, nil
}

// Busy reports whether id currently holds the lock (status/diagnostics).
func (o *Orchestrator) Busy(id ResourceID) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	_, busy := o.locks[id]
	return busy
}
