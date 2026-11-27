//go:build windows

package companion

import (
	"os/exec"
)

// forkTestChild starts a plain test child (windows async shape: no groups —
// the Job wrap in WrapChildInJob is the prod caller's business).
func forkTestChild(name string, args ...string) (int, error) {
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	go func() { _ = cmd.Wait() }()
	return cmd.Process.Pid, nil
}
