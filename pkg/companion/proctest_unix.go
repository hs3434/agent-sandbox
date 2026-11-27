//go:build !windows

package companion

import (
	"os/exec"
	"syscall"
)

// forkTestChild starts a detached test child (setsid'd — the real async
// shape) so linux tests exercise the same group semantics prod uses.
func forkTestChild(name string, args ...string) (int, error) {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	go func() { _ = cmd.Wait() }()
	return cmd.Process.Pid, nil
}
