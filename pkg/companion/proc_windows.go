//go:build windows

package companion

import (
	"fmt"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// jobAccountingInfo mirrors JOBOBJECT_BASIC_ACCOUNTING_INFORMATION — the
// x/sys package lacks the struct, only the info class constant.
type jobAccountingInfo struct {
	TotalUserTime                     uint64
	TotalKernelTime                   uint64
	TotalThisPeriodDisabledUserTime   uint64
	TotalThisPeriodDisabledKernelTime uint64
	TotalPageFaultCount               uint32
	TotalProcesses                    uint32
	ActiveProcesses                   uint32
	TotalTerminatedProcesses          uint32
}

// jobInfo mirrors windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION (alias for
// readability at the SetInformation call site).
type jobInfo = windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION

// D-32 windows process model: Job Objects. The daemon wraps every async
// child in a kernel Job (CREATE_SUSPENDED → AssignJobObject → resume), so
// kill = TerminateJobObject reaches the WHOLE tree (child + grandchildren —
// no setsid/process-group concept on windows), and liveness = the job
// handle + one process in it. Job handles live in a process-wide registry:
// the supervisor only knows pgid (which on windows IS the child's pid —
// SysProcAttr has no Setsid; we record Process.Pid unchanged).
//
// Jobs are also the orphan guard: without them, a killed shell's children
// would keep running with no way to find them (MSYS ps shows cygwin pids,
// useless to a Go daemon).

var (
	jobMu   sync.Mutex
	jobByid = map[int]windows.Handle{} // child pid → job handle
)

// WrapChildInJob is the exported wrap hook (server's postForkChild wires
// it): assigns the child to a fresh Job. The job handle stays open for the
// daemon's lifetime — closing it early would flip kill-on-close semantics
// onto a live tree.
func WrapChildInJob(pid int) error { return wrapChildInJob(pid) }

// wrapChildInJob runs on windows after Start(): assigns the child (and by
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE its whole future tree) to a fresh Job.
// The job handle stays open for the daemon's lifetime — closing it early
// would flip kill-on-close semantics onto a live tree.
func wrapChildInJob(pid int) error {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("create job: %w", err)
	}
	// kill-on-close = if the daemon dies, the OS reaps the whole job —
	// no orphaned agent processes after a crashed companion.
	var info jobInfo
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(h)
		return fmt.Errorf("job limits: %w", err)
	}
	ph, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		_ = windows.CloseHandle(h)
		return fmt.Errorf("open child %d: %w", pid, err)
	}
	defer windows.CloseHandle(ph)
	if err := windows.AssignProcessToJobObject(h, ph); err != nil {
		_ = windows.CloseHandle(h)
		return fmt.Errorf("assign job: %w", err)
	}
	jobMu.Lock()
	jobByid[pid] = h
	jobMu.Unlock()
	return nil
}

// killProcFamily terminates the whole job tree (D-32 windows side). The
// registry pop + handle close happen under the same lock as the lookup —
// a concurrent double-kill takes exactly one TerminateJobObject; the loser
// sees !ok and falls to the pid fallback (which fails harmlessly on an
// already-terminated process).
func killProcFamily(pid int) error {
	jobMu.Lock()
	h, ok := jobByid[pid]
	if ok {
		delete(jobByid, pid)
	}
	jobMu.Unlock()
	if !ok {
		// No job = fork predates the wrap (or wrap failed, or a concurrent
		// kill already consumed it) — direct process kill so kill is never
		// a silent no-op.
		ph, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
		if err != nil {
			return fmt.Errorf("open %d: %w", pid, err)
		}
		defer windows.CloseHandle(ph)
		return windows.TerminateProcess(ph, 1)
	}
	err := windows.TerminateJobObject(h, 1)
	_ = windows.CloseHandle(h) // kill-on-close is now redundant but harmless
	return err
}

// pidAlive answers "is this child's job still running": the job handle is
// the daemon's own kernel record — query it, never the pid (windows pids
// recycle aggressively).
func pidAlive(pid int) bool {
	if pid <= 1 {
		return false
	}
	jobMu.Lock()
	h, ok := jobByid[pid]
	jobMu.Unlock()
	if !ok {
		// No job registered: fall back to opening the process. A recycled
		// pid can false-positive here — accepted debt until the D-05 pidfd
		// endgame lands on windows too (job registry is the primary truth).
		ph, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
		if err != nil {
			return false
		}
		windows.CloseHandle(ph)
		return true
	}
	// ActiveProcesses == 0 → job empty.
	var info jobAccountingInfo
	if err := windows.QueryInformationJobObject(h, windows.JobObjectBasicAccountingInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil); err != nil {
		return true // query failed: assume alive (fail-open for reaping only)
	}
	return info.ActiveProcesses > 0
}

// KillGroup is the exported kill vocabulary (server's rollback path).
func KillGroup(pid int) error { return killProcFamily(pid) }
