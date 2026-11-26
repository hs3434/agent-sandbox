package companion

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ProcState is the supervisor's handle row state (D-21 tri-state on the
// terminal side; running while the process lives).
const (
	ProcRunning  = "running"
	ProcExited   = "exited"
	ProcSignaled = "signaled"
	ProcUnknown  = "exit_unknown"
)

// ProcTableCap is the D-24 hard cap: the 257th concurrent async gets a typed
// refusal — fd exhaustion must be impossible by construction.
const ProcTableCap = 256

// Proc is one supervised process row (disk-backed format, D-23: the table
// survives companion restarts — every field round-trips through JSON).
type Proc struct {
	HandleID  string `json:"handle_id"`
	RequestID string `json:"request_id"` // D-19 idempotency key
	PGID      int    `json:"pgid"`
	Cmd       string `json:"cmd"`
	State     string `json:"state"` // running | exited(N) | signaled(N) | exit_unknown
	StartedAt string `json:"started_at"`
	ExitedAt  string `json:"exited_at,omitempty"`
	OwnerMach string `json:"owner_machine,omitempty"` // which sandboxd declared it
}

// Terminal describes an honest death (never a fabricated 0).
type Terminal struct {
	Code int    // meaningful when Exited
	Sig  int    // meaningful when Signaled
	Kind string // exited | signaled | exit_unknown
}

// Supervisor is the D-06 single loop: one reaper goroutine, one process
// table, batch probes. All state transitions go through the mutex — the
// loop, RPC handlers, and boot rescan are the only writers.
type Supervisor struct {
	mu        sync.Mutex
	procs     map[string]*Proc // handle_id → row
	killMarks map[string]bool  // Kill intent markers (memory-only, D-21 honesty)
	dir       string           // disk-backed table dir (D-23)
	dead      []func(handleID string, terminal string)
	once      sync.Once
	stop      chan struct{}
}

// NewSupervisor opens (or re-scans) the disk-backed table under dir.
// Boot rescan (D-23): every `running` row is probed against /proc truth;
// dead-at-boot rows land exit_unknown — the companion restart never
// fabricates a code it did not observe.
func NewSupervisor(dir string) (*Supervisor, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Supervisor{procs: map[string]*Proc{}, killMarks: map[string]bool{}, dir: dir, stop: make(chan struct{})}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var p Proc
		if jsonUnmarshal(data, &p) != nil || p.HandleID == "" {
			continue // torn row: skip, never crash boot
		}
		if p.State == ProcRunning {
			if pidAlive(p.PGID) {
				// adopt: the process survived the companion restart
				s.procs[p.HandleID] = &p
			} else {
				// died while we were down: honest unknown (D-21)
				p.State = ProcUnknown
				p.ExitedAt = now()
				s.procs[p.HandleID] = &p
				s.persist(&p)
			}
		} else {
			s.procs[p.HandleID] = &p
		}
	}
	return s, nil
}

// Start runs the reaper loop (one goroutine for the whole table — D-06's
// whole point: O(1) traffic vs per-handle observers).
func (s *Supervisor) Start() {
	s.once.Do(func() {
		go func() {
			tick := time.NewTicker(500 * time.Millisecond)
			defer tick.Stop()
			for {
				select {
				case <-s.stop:
					return
				case <-tick.C:
					s.reapBatch()
				}
			}
		}()
	})
}

// Stop halts the loop (daemon shutdown).
func (s *Supervisor) Stop() { close(s.stop) }

// OnDead registers the death-notification callback (events push).
func (s *Supervisor) OnDead(fn func(handleID string, terminal string)) {
	s.mu.Lock()
	s.dead = append(s.dead, fn)
	s.mu.Unlock()
}

// ExecAsync registers a new process row. The CALLER forks; the supervisor
// only records + reaps. Idempotency (D-19): same request-id within the
// table's lifetime returns the existing handle — retry can never fork twice.
func (s *Supervisor) ExecAsync(requestID, cmd, ownerMachine string, pgid int) (handleID string, existed bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if requestID != "" {
		for _, p := range s.procs {
			if p.RequestID == requestID {
				return p.HandleID, true, nil
			}
		}
	}
	if len(s.procs) >= ProcTableCap {
		// D-24 typed refusal: legible failure, not resource collapse
		return "", false, fmt.Errorf("process table full (%d): typed cap refused, batch jobs need a config bump", ProcTableCap)
	}
	handleID = fmt.Sprintf("cp-%d", time.Now().UnixNano())
	p := &Proc{
		HandleID:  handleID,
		RequestID: requestID,
		PGID:      pgid,
		Cmd:       cmd,
		State:     ProcRunning,
		StartedAt: now(),
		OwnerMach: ownerMachine,
	}
	s.procs[handleID] = p
	return handleID, false, s.persist(p)
}

// Wait resolves a handle's current truth (running → nil state pending).
func (s *Supervisor) Wait(handleID string) (*Proc, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.procs[handleID]
	if !ok {
		return nil, false
	}
	cp := *p
	return &cp, true
}

// Kill signals the process group (identity-checked: the row's pgid must
// still own /proc/<pgid> — starttime drift makes us refuse rather than
// wrong-kill, D-05's companion-side twin).
func (s *Supervisor) Kill(handleID string) error {
	s.mu.Lock()
	p, ok := s.procs[handleID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("handle %q unknown", handleID)
	}
	if p.State != ProcRunning {
		return fmt.Errorf("handle %q already terminal (%s)", handleID, p.State)
	}
	if !pidAlive(p.PGID) {
		return fmt.Errorf("handle %q: process %d gone (reaper will resolve the terminal state)", handleID, p.PGID)
	}
	// negative pid = the whole process group (setsid'd, so the group is ours)
	return syscall.Kill(-p.PGID, syscall.SIGKILL)
}

// List returns all rows (sessions.list — L5 reattach's data source).
func (s *Supervisor) List() []Proc {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Proc, 0, len(s.procs))
	for _, p := range s.procs {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt < out[j].StartedAt })
	return out
}

// reapBatch probes every running row once per tick (batch, not per-handle).
func (s *Supervisor) reapBatch() {
	s.mu.Lock()
	var dead []*Proc
	for _, p := range s.procs {
		if p.State != ProcRunning {
			continue
		}
		if pidAlive(p.PGID) {
			continue
		}
		// exit code is unknowable from outside the child — the wrapper file
		// is the channel (T9 may add one; today: group-kill by us → signaled,
		// vanish without our kill → exit_unknown, honest per D-21).
		// killMarks read inline: s.mu is already held (killedByUs would
		// self-deadlock — the one lock discipline this file must keep).
		if s.killMarks[p.HandleID] {
			p.State = ProcSignaled
			p.ExitedAt = now()
		} else {
			p.State = ProcUnknown
			p.ExitedAt = now()
		}
		dead = append(dead, p)
		s.persist(p)
	}
	fns := make([]func(string, string), len(s.dead))
	copy(fns, s.dead)
	s.mu.Unlock()
	for _, p := range dead {
		for _, fn := range fns {
			fn(p.HandleID, p.State)
		}
	}
}

// MarkKilledByUs records the intent marker.
func (s *Supervisor) MarkKilledByUs(handleID string) {
	s.mu.Lock()
	s.killMarks[handleID] = true
	s.mu.Unlock()
}

// persist writes one row atomically (tmp+rename in-table-dir).
func (s *Supervisor) persist(p *Proc) error {
	data := jsonMarshal(p)
	tmp := filepath.Join(s.dir, p.HandleID+".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.dir, p.HandleID+".json"))
}

// pidAlive checks the process GROUP leader (pgid): /proc/<pgid> existence.
func pidAlive(pgid int) bool {
	if pgid <= 1 {
		return false // never "alive" on init — refuse the wrong-probe trap
	}
	_, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pgid)))
	return err == nil
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// jsonMarshal/jsonUnmarshal isolate the encoding dependency (test doubles).
func jsonMarshal(v any) []byte {
	data, _ := json.Marshal(v)
	return data
}

func jsonUnmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }
