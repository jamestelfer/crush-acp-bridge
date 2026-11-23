package crushserver

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

// SharedOptions configures EnsureShared.
type SharedOptions struct {
	// Binary is the crush executable. Defaults to "crush" on PATH.
	Binary string
	// Socket is the server's Unix socket. Defaults to DefaultSocket, where
	// a Crush TUI run with CRUSH_CLIENT_SERVER=1 looks.
	Socket string
	// LogFile receives a started server's output. Defaults to
	// crush-acp-bridge/shared-server.log in the user cache directory.
	LogFile string
	// StartTimeout bounds the wait for a started server to answer. Defaults
	// to 30 seconds.
	StartTimeout time.Duration
	// Logger receives lifecycle records.
	Logger *slog.Logger
}

// ErrSharedUnsupported is returned where Crush's shared server is not
// reachable over a Unix socket.
var ErrSharedUnsupported = errors.New("the shared crush server needs a Unix socket; use --managed on this platform")

// maxSocketPath is the macOS sun_path limit, which Crush also applies.
const maxSocketPath = 104

// DefaultSocket returns the socket of Crush's per-user shared server:
// crush-<uid>.sock in $XDG_RUNTIME_DIR, else in the temporary directory, or in
// /tmp when that path is too long to bind. It mirrors Crush's own rule (Crush
// 0.102.0, internal/server.DefaultHost). It returns "" on Windows, where Crush
// uses a named pipe instead.
func DefaultSocket() string {
	uid := os.Getuid()
	if uid < 0 {
		return ""
	}
	name := "crush-" + strconv.Itoa(uid) + ".sock"
	dir := cmp.Or(os.Getenv("XDG_RUNTIME_DIR"), os.TempDir())
	path := filepath.Join(dir, name)
	if len(path) > maxSocketPath {
		path = filepath.Join("/tmp", name)
	}
	return path
}

// EnsureShared makes sure a Crush server answers on the shared socket,
// starting a detached one when none does. A started server outlives this
// process and exits on Crush's own idle timeout once its last client leaves.
//
// Several bridges may call it at once. Each may start a server; Crush lets one
// bind the socket and the rest exit, so EnsureShared waits for the socket to
// answer rather than for its own server.
func EnsureShared(ctx context.Context, opts SharedOptions) error {
	log := cmp.Or(opts.Logger, slog.New(slog.DiscardHandler))
	socket := cmp.Or(opts.Socket, DefaultSocket())
	if socket == "" {
		return ErrSharedUnsupported
	}
	if healthy(ctx, socket) {
		log.Debug("shared crush server found", "socket", socket)
		return nil
	}

	logFile, err := sharedLogFile(opts.LogFile)
	if err != nil {
		return err
	}
	binary := cmp.Or(opts.Binary, "crush")
	args := []string{"server"}
	if socket != DefaultSocket() {
		args = append(args, "--host", "unix://"+socket)
	}
	pid, err := startDetached(ctx, binary, args, logFile)
	if err != nil {
		return err
	}
	log.Info("shared crush server started", "binary", binary, "socket", socket, "pid", pid, "log", logFile)

	timeout := cmp.Or(opts.StartTimeout, defaultStartTimeout)
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for !healthy(waitCtx, socket) {
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("shared crush server not ready on %s after %s; see %s", socket, timeout, logFile)
		case <-time.After(50 * time.Millisecond):
		}
	}
	return nil
}

// healthy reports whether a Crush server answers on socket.
func healthy(ctx context.Context, socket string) bool {
	var d net.Dialer
	client := http.Client{Timeout: time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return d.DialContext(ctx, "unix", socket)
		},
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://crush/v1/health", nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func sharedLogFile(path string) (string, error) {
	if path == "" {
		dir, err := os.UserCacheDir()
		if err != nil {
			return "", fmt.Errorf("find a directory for the shared server log: %w", err)
		}
		path = filepath.Join(dir, "crush-acp-bridge", "shared-server.log")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("create the shared server log directory: %w", err)
	}
	return path, nil
}

// startDetached starts binary in its own session, so it survives this process,
// with output appended to logFile. It returns the child's pid.
func startDetached(ctx context.Context, binary string, args []string, logFile string) (int, error) {
	out, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // path chosen by the operator or the user cache dir
	if err != nil {
		return 0, fmt.Errorf("open the shared server log: %w", err)
	}
	defer func() { _ = out.Close() }()

	// The server must not stop with this process, so its context is detached.
	cmd := exec.CommandContext(context.WithoutCancel(ctx), binary, args...) //nolint:gosec // the crush binary is chosen by the operator
	cmd.Stdout, cmd.Stderr = out, out
	cmd.Dir = filepath.Dir(logFile) // not the project, which the server may outlive
	cmd.SysProcAttr = detachAttr()
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start %s: %w", binary, err)
	}
	pid := cmd.Process.Pid
	go func() { _ = cmd.Wait() }() // reap it if it exits while this process runs
	return pid, nil
}
