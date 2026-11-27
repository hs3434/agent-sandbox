package companion

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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

// ProcTableCap is the D-24 hard cap: the 257th CONCURRENT async gets a typed
// refusal — fd exhaustion must be impossible by construction. Terminal rows
// don't count; they GC after RetentionWindow.
const ProcTableCap = 256

// RetentionWindow is how long terminal rows stay queryable (wait/read) before
// the GC deletes map entry + disk file. Long enough for any realistic poll,
// short enough that a long-lived daemon stays bounded.
const RetentionWindow = 10 * time.Minute

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
	mu          sync.Mutex
	procs       map[string]*Proc // handle_id → row
	killMarks   map[string]bool  // Kill intent markers (memory-only, D-21 honesty)
	dir         string           // disk-backed table dir (D-23)
	dead        []func(handleID string, terminal string)
	once        sync.Once
	stop        chan struct{}
	stopOnce    sync.Once
	loopDone    chan struct{}
	exitFileFor func(handleID string) string // wrapper exit file path (nil = no wrapper)
	ttl         time.Duration
	ttlFire     func(why string)
	lastConnAt  time.Time
}

// NewSupervisor opens (or re-scans) the disk-backed table under dir.
// Boot rescan (D-23): every `running` row is probed against /proc truth;
// dead-at-boot rows land exit_unknown — the companion restart never
// fabricates a code it did not observe.
func NewSupervisor(dir string) (*Supervisor, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	sLoopDone := make(chan struct{})
	close(sLoopDone) // "not started" state: Stop() join returns immediately
	s := &Supervisor{procs: map[string]*Proc{}, killMarks: map[string]bool{}, dir: dir, stop: make(chan struct{}), loopDone: sLoopDone}
	s.exitFileFor = s.ExitFilePath
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

// SetTTL arms the idle-exit gate (F8): the daemon may retire itself only
// when BOTH conditions hold — no sandboxd connection alive AND an empty
// process table. A running handle vetoes exit (TTL never orphans work).
func (s *Supervisor) SetTTL(d time.Duration, onExpire func(why string)) {
	s.mu.Lock()
	s.ttl = d
	s.ttlFire = onExpire
	if s.lastConnAt.IsZero() {
		s.lastConnAt = time.Now() // born idle: the window starts at arm time
	}
	s.mu.Unlock()
}

// NoteConn marks connection liveness (server calls on accept/serve; the
// gate reads the timestamp — liveness is a heartbeat, not a counter race).
func (s *Supervisor) NoteConn() {
	s.mu.Lock()
	s.lastConnAt = time.Now()
	s.mu.Unlock()
}

// gcTerminal deletes terminal rows older than the retention window (bounded
// memory + disk for a long-lived daemon; D-24's other half).
func (s *Supervisor) gcTerminal() {
	cutoff := time.Now().Add(-RetentionWindow)
	s.mu.Lock()
	var doomed []string
	for id, p := range s.procs {
		if p.State == ProcRunning {
			continue
		}
		if t, err := time.Parse(time.RFC3339Nano, p.ExitedAt); err == nil && t.Before(cutoff) {
			doomed = append(doomed, id)
		}
	}
	for _, id := range doomed {
		delete(s.procs, id)
		delete(s.killMarks, id) // m4: prune kill intent with the row
	}
	s.mu.Unlock()
	for _, id := range doomed {
		_ = os.Remove(filepath.Join(s.dir, id+".json"))
		_ = os.Remove(filepath.Join(s.dir, "exit-"+id)) // wrapper file too
	}
}

// ttlCheckTick is the reaper-loop entry (exported state guarded here).
func (s *Supervisor) ttlCheckTick() { s.ttlCheck() }

// ttlCheck runs each reaper tick: expired idle + empty table → fire once.
func (s *Supervisor) ttlCheck() {
	s.mu.Lock()
	fire := s.ttl > 0 && s.ttlFire != nil &&
		time.Since(s.lastConnAt) > s.ttl &&
		len(s.runningProcsLocked()) == 0
	s.mu.Unlock()
	if fire {
		s.mu.Lock()
		fn := s.ttlFire
		s.ttlFire = nil // once
		s.mu.Unlock()
		if fn != nil {
			fn("idle")
		}
	}
}

func (s *Supervisor) runningProcsLocked() []string {
	var out []string
	for _, p := range s.procs {
		if p.State == ProcRunning {
			out = append(out, p.HandleID)
		}
	}
	return out
}

// Start runs the reaper loop (one goroutine for the whole table — D-06's
// whole point: O(1) traffic vs per-handle observers).
func (s *Supervisor) Start() {
	s.once.Do(func() {
		s.loopDone = make(chan struct{}) // un-close the constructor's stub: Stop now JOINS
		go func() {
			defer close(s.loopDone) // Stop() joins on this — no post-stop writes
			tick := time.NewTicker(500 * time.Millisecond)
			defer tick.Stop()
			for {
				select {
				case <-s.stop:
					return
				case <-tick.C:
					s.reapBatch()
					s.gcTerminal()
					s.ttlCheckTick()
				}
			}
		}()
	})
}

// Stop halts the loop (daemon shutdown).
// Stop signals the loop and JOINS it (a caller tearing down the temp dir
// must not race the reaper's final persist).
func (s *Supervisor) Stop() {
	s.stopOnce.Do(func() { close(s.stop) })
	<-s.loopDone // closed at construction (never started) or by loop exit (joined)
}

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
	if len(s.runningProcsLocked()) >= ProcTableCap {
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

// ExitFilePath returns the wrapper exit file for a handle (server wires it
// into exitFileFor at construction — one path discipline, two consumers).
func (s *Supervisor) ExitFilePath(handleID string) string {
	return filepath.Join(s.dir, "exit-"+handleID)
}

// SetPGID backfills the group leader pid after the fork (ExecAsync reserves
// the row first so the cap + idempotency gates see the allocation).
func (s *Supervisor) SetPGID(handleID string, pgid int) {
	s.mu.Lock()
	if p, ok := s.procs[handleID]; ok {
		p.PGID = pgid
	}
	s.mu.Unlock()
	// persist the correction (boot rescan reads pgid)
	s.mu.Lock()
	p, ok := s.procs[handleID]
	s.mu.Unlock()
	if ok {
		_ = s.persist(p)
	}
}

// Remove rolls back a reserved row (fork failed — a processless row would
// make the ledger lie about capacity).
func (s *Supervisor) Remove(handleID string) {
	s.mu.Lock()
	delete(s.procs, handleID)
	delete(s.killMarks, handleID)
	s.mu.Unlock()
	_ = os.Remove(filepath.Join(s.dir, handleID+".json"))
}

// KillByPgid finds the row owning this pgid (cross-axis kill vocabulary).
func (s *Supervisor) KillByPgid(pgid int) error {
	s.mu.Lock()
	var handleID string
	for id, p := range s.procs {
		if p.PGID == pgid {
			handleID = id
			break
		}
	}
	s.mu.Unlock()
	if handleID == "" {
		return fmt.Errorf("no handle owns pgid %d", pgid)
	}
	return s.Kill(handleID)
}

// Kill terminates the handle's process family (platformproc abstraction —
// D-32: unix = process group SIGKILL, windows = Job Object terminate).
// Identity guard: the row's pgid must be an identity the daemon forked
// itself (recorded at ExecAsync); pgids ≤ 1 are refused outright. A full
// starttime comparison lands with the pidfd endgame (D-05) — today the
// fork-origin invariant is the guard.
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
	return killProcFamily(p.PGID)
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
	// Snapshot running candidates under lock; wrapper-file reads + persists
	// happen OUTSIDE s.mu (disk I/O under the lock stalls every RPC — m3).
	// killMarks still reads inline in phase 1: pure map lookup, no I/O.
	s.mu.Lock()
	var cands []*Proc
	for _, p := range s.procs {
		if p.State == ProcRunning && !pidAlive(p.PGID) {
			cands = append(cands, p)
		}
	}
	fns := make([]func(string, string), len(s.dead))
	copy(fns, s.dead)
	s.mu.Unlock()

	type done struct {
		id, state string
	}
	var dones []done
	for _, p := range cands {
		// Terminal truth resolution order (D-21): wrapper exit file first
		// (the child recorded its own code), then our kill-intent marker
		// (signaled), then honest unknown.
		s.mu.Lock()
		marked := s.killMarks[p.HandleID]
		s.mu.Unlock()
		state := ProcUnknown
		if term := s.readWrapperExit(p.HandleID); term != "" { // disk I/O, lock-free
			state = term
		} else if marked {
			state = ProcSignaled
		}
		s.mu.Lock()
		if cur, ok := s.procs[p.HandleID]; ok && cur.State == ProcRunning {
			cur.State = state
			cur.ExitedAt = now()
			dones = append(dones, done{p.HandleID, state})
			s.persist(cur)
		}
		s.mu.Unlock()
	}
	for _, d := range dones {
		for _, fn := range fns {
			fn(d.id, d.state)
		}
	}
}

// MarkKilledByUs records the intent marker.
func (s *Supervisor) MarkKilledByUs(handleID string) {
	s.mu.Lock()
	s.killMarks[handleID] = true
	s.mu.Unlock()
}

// readWrapperExit maps the wrapper file to a terminal state string:
// "7" → exited(7); "sig:9" → signaled(9); missing/garbage → "" (fall through).
func (s *Supervisor) readWrapperExit(handleID string) string {
	if s.exitFileFor == nil {
		return ""
	}
	data, err := os.ReadFile(s.exitFileFor(handleID))
	if err != nil {
		return ""
	}
	out := strings.TrimSpace(string(data))
	if strings.HasPrefix(out, "sig:") {
		return ProcSignaled + "(" + strings.TrimPrefix(out, "sig:") + ")"
	}
	if isDigitsStr(out) {
		return "exited(" + out + ")"
	}
	return ""
}

func isDigitsStr(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
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

// pidAlive is platformproc's liveness probe (os-specific; see
// proc_unix.go / proc_windows.go). Kept here as the single call site shape.

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// jsonMarshal/jsonUnmarshal isolate the encoding dependency (test doubles).
func jsonMarshal(v any) []byte {
	data, _ := json.Marshal(v)
	return data
}

func jsonUnmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }
