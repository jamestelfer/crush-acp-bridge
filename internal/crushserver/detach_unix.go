//go:build !windows

package crushserver

import (
	"os/exec"
	"syscall"
)

// startProcess starts the command newCmd builds in a new session, out of this
// process's group and away from its terminal signals.
func startProcess(newCmd func() *exec.Cmd) (*exec.Cmd, error) {
	cmd := newCmd()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

// currentSID is Windows-only: Unix users are identified by uid.
func currentSID() string { return "" }
