//go:build windows

package crushserver

import (
	"errors"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// startProcess starts the command newCmd builds as Crush starts its own
// detached server (Crush 0.102.0, internal/cmd/root_windows.go).
//
// The child gets a new process group and a console of its own with no
// window, which the console programs it runs (git, LSP and MCP servers,
// shell commands) inherit. DETACHED_PROCESS is not used: it would leave the
// server without a console, so each such program would open a visible
// window, and Windows ignores CREATE_NO_WINDOW alongside it.
//
// The child also breaks away from this process's job object, if any: some
// jobs kill their processes when they close, which would take the shared
// server down with this bridge. A job that forbids breakaway fails the start
// with ERROR_ACCESS_DENIED, and the command is then started inside the job
// from a fresh exec.Cmd, since one cannot be started twice.
func startProcess(newCmd func() *exec.Cmd) (*exec.Cmd, error) {
	const flags = syscall.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW
	cmd := newCmd()
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags | windows.CREATE_BREAKAWAY_FROM_JOB}
	err := cmd.Start()
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		cmd = newCmd()
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags}
		err = cmd.Start()
	}
	if err != nil {
		return nil, err
	}
	return cmd, nil
}

// currentSID returns the SID of the user that owns this process, or "" if it
// cannot be read. Crush names its default pipe from the same value (Crush
// 0.102.0, internal/server/user_windows.go).
func currentSID() string {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return ""
	}
	return user.User.Sid.String()
}
