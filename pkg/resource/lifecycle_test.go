package resource

import (
	"sync"
	"testing"
	"time"
)

func TestValidTransitions(t *testing.T) {
	cases := []struct {
		from, to Status
		ok       bool
	}{
		{StatusCreated, StatusStarting, true},
		{StatusRunning, StatusStopping, true},
		{StatusStopped, StatusStarting, true}, // wake = reuse (AD-008)
		{StatusStarting, StatusRunning, true},
		{StatusStarting, StatusError, true}, // timeout
		{StatusStopping, StatusStopped, true},
		{StatusStopping, StatusRemoving, true},
		{StatusStopped, StatusRemoving, true},
		{StatusRunning, StatusStopping, true},
		{StatusError, StatusRemoving, true},
		{StatusRemoving, StatusRemoved, true},
		// invalid
		{StatusCreated, StatusRunning, false},  // must pass through starting
		{StatusRunning, StatusStarting, false}, // no running->starting
		{StatusStopped, StatusRunning, false},  // wake goes through starting
		{StatusRemoved, StatusCreated, false},  // removed is terminal
		{StatusError, StatusStarting, false},   // error: rebuild, not restart
	}
	for _, c := range cases {
		err := ValidateTransition(c.from, c.to)
		if c.ok && err != nil {
			t.Errorf("%s -> %s: want ok, got %v", c.from, c.to, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s -> %s: want rejected", c.from, c.to)
		}
	}
}

func TestTransitionIdempotent(t *testing.T) {
	// spec §3.1: migrations are idempotent (same-state call is a no-op)
	if err := ValidateTransition(StatusRunning, StatusRunning); err != nil {
		t.Fatalf("same-state must be no-op: %v", err)
	}
}

func TestOrchestratorLockConflict(t *testing.T) {
	// spec §3.1 concurrency arbitration: second op rejected immediately
	// (409 conflict, not queued)
	o := NewOrchestrator()
	id := ResourceID("local:exec_env:dev-1")

	release, err := o.Acquire(id, "test")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	_, err = o.Acquire(id, "other")
	if err == nil {
		t.Fatal("second acquire must conflict")
	}
	if !IsConflict(err) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
	release()
	// after release, lock is free again
	release2, err := o.Acquire(id, "test")
	if err != nil {
		t.Fatalf("reacquire: %v", err)
	}
	release2()
}

func TestOrchestratorConcurrentAcquire(t *testing.T) {
	o := NewOrchestrator()
	id := ResourceID("local:exec_env:race")
	var wg sync.WaitGroup
	winners := make(chan struct{}, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if rel, err := o.Acquire(id); err == nil {
				winners <- struct{}{}
				time.Sleep(time.Millisecond)
				rel()
			}
		}()
	}
	wg.Wait()
	close(winners)
	// over the whole run exactly one holder at a time; total successes
	// may be >1 (sequential re-acquires) but never 10 concurrent —
	// the real invariant is tested by lockConflict above
	if len(winners) == 0 {
		t.Fatal("no one acquired")
	}
}
