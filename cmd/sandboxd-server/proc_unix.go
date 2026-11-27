//go:build !windows

package main

import (
	"os"
	"os/exec"
	"syscall"

	"github.com/hs3434/agent-sandbox/pkg/companion"
)

func init() {
	shutdownSignals = []os.Signal{syscall.SIGINT, syscall.SIGTERM}
	requestShutdownHook = func() { _ = syscall.Kill(os.Getpid(), syscall.SIGTERM) }
}

// requestShutdown self-signals SIGTERM (the signal handler in main does the
// cleanup — same path as a ctrl-C at the console).
func requestShutdown() { requestShutdownHook() }

// forkAsyncChild starts the wrapper shell detached (D-32): unix = setsid'd
// new process group — the daemon's kill path reaches the whole family via
// -pgid.
func forkAsyncChild(shell string, wrapped string) (*exec.Cmd, error) {
	pc := exec.Command(shell, "-c", wrapped)
	pc.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := pc.Start(); err != nil {
		return nil, err
	}
	go func() { _ = pc.Wait() }()
	return pc, nil
}

// shellFor returns the async wrapper shell (POSIX sh on every unix).
func shellFor() string { return "/bin/sh" }

// postForkChild is the per-platform hook after a successful fork (windows:
// wrap the child in a Job Object — unix: nothing).
func postForkChild(pid int) error { return nil }

// killProcFamily is the supervisor's kill vocabulary (D-32 unix: group
// SIGKILL) — the exec_async half-wrap rollback path uses it too.
func killProcFamily(pgid int) error { return companion.KillGroup(pgid) }

// registerShutdownHook is a no-op on unix (the signal goroutine owns
// cleanup; self-signal reaches it through the normal path).
func registerShutdownHook(fn func()) {}
