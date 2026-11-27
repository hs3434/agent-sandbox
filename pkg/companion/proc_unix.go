//go:build !windows

package companion

import (
	"errors"
	"syscall"
)

// killProcFamily terminates the whole process group (D-32 unix side): the
// daemon forked the leader setsid'd, so -pgid reaches every member — the
// LEADER may already be reaped while group members live, and a leader-only
// kill would orphan the rest.
func killProcFamily(pgid int) error {
	if pgid <= 1 {
		return nil // never signal init — the wrong-kill trap, refused
	}
	return syscall.Kill(-pgid, syscall.SIGKILL)
}

// pidAlive answers "does this process family still have live processes"
// (group-level: kill(0) on -pgid). EPERM = the group exists but isn't ours —
// still alive.
func pidAlive(pgid int) bool {
	if pgid <= 1 {
		return false // never "alive" on init — refuse the wrong-probe trap
	}
	if syscall.Kill(-pgid, 0) == nil {
		return true
	}
	return errors.Is(syscall.Kill(-pgid, 0), syscall.EPERM)
}

// KillGroup is the exported kill vocabulary (server's rollback path).
func KillGroup(pgid int) error { return killProcFamily(pgid) }
