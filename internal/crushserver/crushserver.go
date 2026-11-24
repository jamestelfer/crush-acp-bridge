// Package crushserver runs `crush server` as a child process.
//
// Crush's server exits when it has been idle for 60 seconds. A bridge that
// owns the server decides how long it lives, so Start raises that limit
// unless the caller chose one.
package crushserver

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jamestelfer/crush-acp-bridge/internal/crush"
)

// Options configures Start.
type Options struct {
	// Binary is the crush executable. Defaults to "crush" on PATH.
	Binary string
	// Listen is the host:port to bind. Empty picks a free loopback port.
	Listen string
	// DataDir is passed to crush as -D when set.
	DataDir string
	// Env holds extra KEY=value entries for the child, added to this
	// process's environment.
	Env []string
	// StartTimeout bounds the wait for the server to become healthy.
	// Defaults to 30 seconds.
	StartTimeout time.Duration
	// Logger receives the child's output and lifecycle records.
	Logger *slog.Logger
}

const (
	defaultStartTimeout = 30 * time.Second
	stopGrace           = 5 * time.Second
	idleTimeoutVar      = "CRUSH_SERVER_IDLE_TIMEOUT"
	// idleTimeoutSeconds keeps the server up for a day. Zero would shut it
	// down immediately, not disable the limit.
	idleTimeoutSeconds = "86400"
	outputTailLines    = 20
)

// Server is a running child `crush server`.
type Server struct {
	url    string
	log    *slog.Logger
	cancel context.CancelFunc // asks the child to exit
	done   chan struct{}

	tailMu sync.Mutex
	tail   []string
}

// URL returns the server's base URL, for example "http://127.0.0.1:41233".
func (s *Server) URL() string { return s.url }

// Start launches the server and returns once it answers health checks. The
// server stops when ctx ends or Stop is called.
func Start(ctx context.Context, opts Options) (*Server, error) {
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	binary := cmp.Or(opts.Binary, "crush")
	timeout := opts.StartTimeout
	if timeout <= 0 {
		timeout = defaultStartTimeout
	}

	addr := opts.Listen
	if addr == "" {
		var err error
		if addr, err = freeAddr(); err != nil {
			return nil, fmt.Errorf("pick a port for crush server: %w", err)
		}
	}
	args := []string{"server", "--host", "http://" + addr}
	if opts.DataDir != "" {
		args = append(args, "-D", opts.DataDir)
	}

	// Cancelling runCtx, directly or through ctx, sends SIGTERM and kills the
	// child if it has not exited after stopGrace.
	runCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(runCtx, binary, args...) //nolint:gosec // the crush binary is chosen by the operator
	cmd.Env = childEnv(opts.Env)
	cmd.Cancel = func() error {
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			return cmd.Process.Kill() // no SIGTERM on this platform
		}
		return nil
	}
	cmd.WaitDelay = stopGrace
	s := &Server{url: "http://" + addr, log: log, cancel: cancel, done: make(chan struct{})}

	// Never let the child inherit this process's stdout: it carries ACP.
	outR, outW := io.Pipe()
	cmd.Stdout, cmd.Stderr = outW, outW
	go s.consume(outR)

	if err := cmd.Start(); err != nil {
		cancel()
		_ = outW.Close()
		return nil, fmt.Errorf("start %s: %w", binary, err)
	}
	log.Info("crush server started", "binary", binary, "url", s.url, "pid", cmd.Process.Pid)
	go func() {
		err := cmd.Wait()
		_ = outW.Close()
		close(s.done)
		log.Info("crush server exited", "error", err)
	}()

	switch err := pollHealthy(ctx, crush.New(s.url), timeout, s.done); {
	case errors.Is(err, errExited):
		_ = s.Stop()
		return nil, fmt.Errorf("crush server exited before it was ready: %s", s.outputTail())
	case err != nil:
		_ = s.Stop()
		return nil, fmt.Errorf("crush server not ready after %s: %s", timeout, s.outputTail())
	}
	return s, nil
}

// Stop asks the server to exit and waits for it, killing it after a grace
// period. It is safe to call more than once.
func (s *Server) Stop() error {
	s.cancel()
	<-s.done
	return nil
}

// errExited reports that a server being polled exited.
var errExited = errors.New("crush server exited")

// pollInterval is how often pollHealthy asks a starting server for its health.
const pollInterval = 50 * time.Millisecond

// pollHealthy asks c for its health until it answers, timeout passes, ctx
// ends, or exited is closed (errExited). A nil exited is never closed.
func pollHealthy(ctx context.Context, c *crush.Client, timeout time.Duration, exited <-chan struct{}) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for !ping(ctx, c) {
		select {
		case <-exited:
			return errExited
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
	return nil
}

// ping reports whether c's server answers a health check within a second.
func ping(ctx context.Context, c *crush.Client) bool {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	return c.Ping(ctx) == nil
}

// consume logs the child's output and keeps the last lines for errors.
func (s *Server) consume(r io.Reader) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		s.log.Debug("crush server output", "line", line)
		s.tailMu.Lock()
		s.tail = append(s.tail, line)
		if len(s.tail) > outputTailLines {
			s.tail = s.tail[1:]
		}
		s.tailMu.Unlock()
	}
}

func (s *Server) outputTail() string {
	s.tailMu.Lock()
	defer s.tailMu.Unlock()
	if len(s.tail) == 0 {
		return "no output"
	}
	return strings.Join(s.tail, "; ")
}

// childEnv returns this process's environment plus extra, with the idle
// timeout raised unless either already sets it.
func childEnv(extra []string) []string {
	env := append(os.Environ(), extra...)
	for _, kv := range env {
		if strings.HasPrefix(kv, idleTimeoutVar+"=") {
			return env
		}
	}
	return append(env, idleTimeoutVar+"="+idleTimeoutSeconds)
}

// freeAddr returns a loopback address with a currently unused port.
func freeAddr() (string, error) {
	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer func() { _ = l.Close() }()
	return l.Addr().String(), nil
}
