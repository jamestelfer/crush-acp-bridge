//go:build !windows

package crushserver

import "syscall"

// detachAttr starts the child in a new session, out of this process's group
// and away from its terminal signals.
func detachAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
