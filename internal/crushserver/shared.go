package crushserver

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/jamestelfer/crush-acp-bridge/internal/crush"
)

// SharedOptions configures EnsureShared.
type SharedOptions struct {
	// Binary is the crush executable. Defaults to "crush" on PATH.
	Binary string
	// Socket is the server's address: a Unix socket path, or a named pipe
	// such as \\.\pipe\name on Windows. Defaults to DefaultSocket, where a
	// Crush TUI run with CRUSH_CLIENT_SERVER=1 looks.
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

// ErrSharedUnsupported is returned when the current user's ID cannot be read,
// so Crush's shared server has no address. Crush refuses the same case rather
// than fall back to an address every user would share.
var ErrSharedUnsupported = errors.New("the current user's ID is unknown, so Crush's shared server has no address; pass --crush-socket or use --managed")

// maxSocketPath is the macOS sun_path limit, which Crush also applies.
const maxSocketPath = 104

// DefaultSocket returns the address of Crush's per-user shared server,
// without its URL scheme. It mirrors Crush's own rule (Crush 0.102.0,
// internal/server.DefaultHost).
//
// On Windows it is the named pipe //./pipe/crush-<sid>.sock, where <sid> is
// the current user's SID. Elsewhere it is the Unix socket crush-<uid>.sock in
// $XDG_RUNTIME_DIR, else in the temporary directory, or in /tmp when that path
// is too long to bind. It returns "" when the user's ID cannot be read.
func DefaultSocket() string {
	if runtime.GOOS == "windows" {
		return defaultPipe(currentSID())
	}
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

// defaultPipe returns Crush's named pipe for the user with the given SID, or
// "" for an unknown user. Crush writes it with forward slashes, which Windows
// accepts in a pipe path.
func defaultPipe(sid string) string {
	if sid == "" {
		return ""
	}
	return "//./pipe/crush-" + sid + ".sock"
}

// SharedURL returns the client URL for the shared server at socket, in
// Crush's own form: npipe:// for a named pipe on Windows, unix:// elsewhere.
func SharedURL(socket string) string {
	if runtime.GOOS == "windows" {
		return "npipe://" + socket
	}
	return "unix://" + socket
}

// EnsureShared makes sure a Crush server answers on the shared address,
// starting a detached one when none does. A started server outlives this
// process and exits on Crush's own idle timeout once its last client leaves.
//
// Several bridges may call it at once. Each may start a server; Crush lets one
// bind the address and the rest exit, so EnsureShared waits for the address to
// answer rather than for its own server. On Unix, Crush's <socket>.lock guards
// the bind. On Windows, go-winio creates a pipe's first instance only if the
// name is free, so a second server's bind fails.
func EnsureShared(ctx context.Context, opts SharedOptions) error {
	log := cmp.Or(opts.Logger, slog.New(slog.DiscardHandler))
	socket := cmp.Or(opts.Socket, DefaultSocket())
	if socket == "" {
		return ErrSharedUnsupported
	}
	client := crush.New(SharedURL(socket))
	if ping(ctx, client) {
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
		args = append(args, "--host", SharedURL(socket))
	}
	pid, err := startDetached(ctx, binary, args, logFile)
	if err != nil {
		return err
	}
	log.Info("shared crush server started", "binary", binary, "socket", socket, "pid", pid, "log", logFile)

	timeout := cmp.Or(opts.StartTimeout, defaultStartTimeout)
	// The started server may exit when another bridge's server binds the
	// address first, so only the address is watched.
	if err := pollHealthy(ctx, client, timeout, nil); err != nil {
		return fmt.Errorf("shared crush server not ready on %s after %s; see %s", socket, timeout, logFile)
	}
	return nil
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

// startDetached starts binary detached from this process, so it survives it,
// with output appended to logFile. It returns the child's pid.
func startDetached(ctx context.Context, binary string, args []string, logFile string) (int, error) {
	out, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // path chosen by the operator or the user cache dir
	if err != nil {
		return 0, fmt.Errorf("open the shared server log: %w", err)
	}
	defer func() { _ = out.Close() }()

	cmd, err := startProcess(func() *exec.Cmd {
		// The server must not stop with this process, so its context is detached.
		cmd := exec.CommandContext(context.WithoutCancel(ctx), binary, args...) //nolint:gosec // the crush binary is chosen by the operator
		cmd.Stdout, cmd.Stderr = out, out
		cmd.Dir = filepath.Dir(logFile) // not the project, which the server may outlive
		return cmd
	})
	if err != nil {
		return 0, fmt.Errorf("start %s: %w", binary, err)
	}
	pid := cmd.Process.Pid
	go func() { _ = cmd.Wait() }() // reap it if it exits while this process runs
	return pid, nil
}
