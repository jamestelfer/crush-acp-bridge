//go:build windows

package crushserver

import "syscall"

// detachAttr is unused on Windows: DefaultSocket is empty there, so
// EnsureShared stops before starting anything.
func detachAttr() *syscall.SysProcAttr { return nil }
