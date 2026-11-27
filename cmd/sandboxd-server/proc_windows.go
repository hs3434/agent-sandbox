//go:build windows

package main

import (
	"os"
	"os/exec"

	"github.com/hs3434/agent-sandbox/pkg/companion"
)

func init() {
	shutdownSignals = []os.Signal{os.Interrupt}
	requestShutdownHook = func() {
		// windows: no self-signal — run the same cleanup inline (conn.json
		// removal lives in main's shutdown func, exposed via the hook set
		// at boot).
		if onShutdownRequest != nil {
			onShutdownRequest()
		}
		os.Exit(0)
	}
}

// onShutdownRequest is main's cleanup func, registered at boot (windows
// only — unix signals itself instead).
var onShutdownRequest func()

// requestShutdown runs the cleanup+exit path (main calls this on the
// shutdown method — windows has no self-signal, unix kills itself).
func requestShutdown() { requestShutdownHook() }

// forkAsyncChild starts the wrapper shell detached (D-32): windows has no
// setsid/process groups — the child is plain, and postForkChild wraps it in
// a Job Object (kill tree + orphan guard live there).
func forkAsyncChild(shell string, wrapped string) (*exec.Cmd, error) {
	pc := exec.Command(shell, "-c", wrapped)
	if err := pc.Start(); err != nil {
		return nil, err
	}
	go func() { _ = pc.Wait() }()
	return pc, nil
}

// shellFor returns the async wrapper shell: Git Bash's sh — the winbuild
// reality (no WSL guarantee, no POSIX sh otherwise). Empty string = not
// found, exec_async answers unsupported_typed instead of forking blind.
func shellFor() string {
	for _, c := range []string{`C:\Program Files\Git\usr\bin\sh.exe`, `C:\Program Files\Git\bin\sh.exe`} {
		if companion.FileExists(c) {
			return c
		}
	}
	return ""
}

// postForkChild wraps the child in a fresh Job Object (D-32): kill =
// TerminateJobObject (whole tree), liveness = ActiveProcesses > 0, and
// KILL_ON_JOB_CLOSE means a crashed daemon never leaves orphans.
func postForkChild(pid int) error { return companion.WrapChildInJob(pid) }

// killProcFamily is the supervisor's kill vocabulary (D-32 windows: job
// terminate) — the exec_async half-wrap rollback path uses it too.
func killProcFamily(pid int) error { return companion.KillGroup(pid) }

// registerShutdownHook stores main's cleanup for the windows self-exit
// path (no signals — the hook IS the shutdown).
func registerShutdownHook(fn func()) { onShutdownRequest = fn }
