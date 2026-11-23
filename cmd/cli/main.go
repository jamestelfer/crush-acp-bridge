// Command crush-acp-bridge is the command line entry point.
package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/urfave/cli/v3"

	"github.com/jamestelfer/crush-acp-bridge/internal/bridge"
	"github.com/jamestelfer/crush-acp-bridge/internal/crush"
	"github.com/jamestelfer/crush-acp-bridge/internal/crushserver"
)

// Exit codes. Anything a supervisor might branch on lives here rather than
// being scattered as literals.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

func main() {
	os.Exit(run(os.Args, os.Stdin, os.Stdout, os.Stderr))
}

// run is the whole program, parameterised on its arguments and standard
// streams so it can be exercised from tests without a process boundary.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	log := slog.New(slog.NewTextHandler(stderr, nil))

	cmd := &cli.Command{
		Name:      "crush-acp-bridge",
		Usage:     "Serve a Crush server session to an editor over ACP on stdio",
		Version:   buildVersion(),
		ArgsUsage: " ",
		Writer:    stdout,
		ErrWriter: stderr,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "crush-url",
				Usage:   "base URL of a running `crush server --host http://...`",
				Value:   "http://127.0.0.1:8484",
				Sources: cli.EnvVars("CRUSH_ACP_URL"),
			},
			&cli.BoolFlag{
				Name:  "managed",
				Usage: "start `crush server` as a child process and stop it on exit (no separate server needed)",
			},
			&cli.BoolFlag{
				Name:  "shared",
				Usage: "use Crush's per-user shared server, the one `CRUSH_CLIENT_SERVER=1 crush` uses, starting it if needed; it outlives the bridge",
			},
			&cli.StringFlag{
				Name:    "crush-socket",
				Usage:   "Unix socket, or named pipe on Windows, of the --shared server (default: Crush's own)",
				Sources: cli.EnvVars("CRUSH_ACP_CRUSH_SOCKET"),
			},
			&cli.StringFlag{
				Name:    "crush-bin",
				Usage:   "crush executable for --managed and --shared",
				Value:   "crush",
				Sources: cli.EnvVars("CRUSH_ACP_CRUSH_BIN"),
			},
			&cli.StringFlag{
				Name:  "listen",
				Usage: "host:port for the --managed server, so other Crush clients can attach (default: a free local port)",
			},
			&cli.StringFlag{
				Name:    "log-level",
				Usage:   "log level: debug, info, warn or error",
				Value:   "info",
				Sources: cli.EnvVars("CRUSH_ACP_LOG_LEVEL"),
			},
			&cli.StringFlag{
				Name:    "log-format",
				Usage:   "log format on stderr: text or json",
				Value:   "text",
				Sources: cli.EnvVars("CRUSH_ACP_LOG_FORMAT"),
			},
			&cli.StringFlag{
				Name:  "cwd",
				Usage: "always open this project directory in Crush, ignoring the directory the editor sends",
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.Args().Present() {
				return cli.Exit(
					fmt.Sprintf("crush-acp-bridge takes no arguments, got %q", cmd.Args().Slice()),
					exitUsage)
			}
			logger, err := newLogger(stderr, cmd.String("log-level"), cmd.String("log-format"))
			if err != nil {
				return cli.Exit(err.Error(), exitUsage)
			}
			// Only an explicit flag conflicts: CRUSH_ACP_URL may be set for
			// unmanaged runs in the same shell.
			if cmd.Bool("managed") && flagGiven(args, "crush-url") {
				return cli.Exit("--managed starts its own server; remove --crush-url", exitUsage)
			}
			if cmd.Bool("shared") && (cmd.Bool("managed") || flagGiven(args, "crush-url")) {
				return cli.Exit("--shared uses Crush's shared server; remove --managed and --crush-url", exitUsage)
			}
			if cmd.IsSet("crush-socket") && !cmd.Bool("shared") {
				return cli.Exit("--crush-socket and CRUSH_ACP_CRUSH_SOCKET apply only to --shared", exitUsage)
			}
			return serve(ctx, logger, serveOptions{
				crushURL: cmd.String("crush-url"),
				forceCwd: cmd.String("cwd"),
				managed:  cmd.Bool("managed"),
				shared:   cmd.Bool("shared"),
				socket:   cmd.String("crush-socket"),
				crushBin: cmd.String("crush-bin"),
				listen:   cmd.String("listen"),
			}, stdin, stdout)
		},
		// A usage error is the operator's mistake, not a runtime failure, and
		// gets its own exit code.
		OnUsageError: func(_ context.Context, _ *cli.Command, err error, _ bool) error {
			return cli.Exit(err.Error(), exitUsage)
		},
		// The default handler writes to a package-global writer and calls
		// os.Exit, which would make run untestable without a process boundary.
		// Reporting and exit-code mapping happen below instead.
		ExitErrHandler: func(_ context.Context, _ *cli.Command, _ error) {},
	}

	// os.Interrupt and syscall.SIGTERM are both defined on Windows, so no
	// build-tagged signal handling is needed.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err := cmd.Run(ctx, args)
	if err == nil {
		return exitOK
	}

	if exit, ok := errors.AsType[cli.ExitCoder](err); ok {
		if msg := exit.Error(); msg != "" {
			fmt.Fprintln(stderr, msg) //nolint:errcheck // nothing useful to do if stderr is gone
		}
		return exit.ExitCode()
	}
	log.Error("command failed", slog.Any("error", err))
	return exitFailure
}

// serveOptions selects where Crush runs and how the bridge reaches it.
type serveOptions struct {
	crushURL string
	forceCwd string // open this directory for every session when set
	managed  bool   // run `crush server` as a child instead of connecting
	shared   bool   // use, or start, Crush's per-user shared server
	socket   string // the shared server's socket or pipe when not Crush's default
	crushBin string
	listen   string
}

// serve relays ACP traffic until the editor disconnects. Crush is opened in
// the directory each ACP session names, or in forceCwd when set. stdout
// carries ACP messages only; diagnostics go to stderr.
func serve(ctx context.Context, log *slog.Logger, opts serveOptions, stdin io.Reader, stdout io.Writer) error {
	crushURL := opts.crushURL
	if opts.managed {
		srv, err := crushserver.Start(ctx, crushserver.Options{
			Binary: opts.crushBin,
			Listen: opts.listen,
			Logger: log,
		})
		if err != nil {
			return fmt.Errorf("start crush server: %w", err)
		}
		defer func() { _ = srv.Stop() }()
		crushURL = srv.URL()
	}
	// ensure brings the shared server up; it runs again before every reopen,
	// so a reconnect replaces a server that has gone.
	ensure := func(context.Context) error { return nil }
	if opts.shared {
		shared := crushserver.SharedOptions{
			Binary: opts.crushBin,
			Socket: cmp.Or(opts.socket, crushserver.DefaultSocket()),
			Logger: log,
		}
		ensure = func(ctx context.Context) error {
			if err := crushserver.EnsureShared(ctx, shared); err != nil {
				return fmt.Errorf("shared crush server: %w", err)
			}
			return nil
		}
		if err := ensure(ctx); err != nil {
			return err
		}
		crushURL = crushserver.SharedURL(shared.Socket)
	}

	client := crush.New(crushURL)
	if err := client.Ping(ctx); err != nil {
		return fmt.Errorf("%w (is `crush server` running at %s?)", err, crushURL)
	}

	open := func(ctx context.Context, cwd string) (bridge.Backend, error) {
		if opts.forceCwd != "" {
			cwd = opts.forceCwd
		}
		if err := ensure(ctx); err != nil {
			return nil, err
		}
		return client.OpenWorkspace(ctx, cwd)
	}
	log.Info("serving ACP on stdio",
		"crush_url", crushURL, "managed", opts.managed, "shared", opts.shared, "force_cwd", opts.forceCwd)
	if err := bridge.Serve(ctx, open, stdin, stdout, bridge.WithLogger(log)); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// flagGiven reports whether args contains --name or --name=value.
func flagGiven(args []string, name string) bool {
	for _, a := range args {
		if a == "--"+name || strings.HasPrefix(a, "--"+name+"=") {
			return true
		}
	}
	return false
}

// newLogger builds the structured logger. Logs go to stderr because stdout
// carries ACP messages.
func newLogger(w io.Writer, level, format string) (*slog.Logger, error) {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("--log-level: %w", err)
	}
	opts := &slog.HandlerOptions{Level: lvl}
	switch format {
	case "text":
		return slog.New(slog.NewTextHandler(w, opts)), nil
	case "json":
		return slog.New(slog.NewJSONHandler(w, opts)), nil
	default:
		return nil, fmt.Errorf("--log-format: unknown format %q (use text or json)", format)
	}
}
