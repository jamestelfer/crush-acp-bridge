// Command crush-acp-bridge is the command line entry point.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/urfave/cli/v3"

	"github.com/jamestelfer/crush-acp-bridge/internal/bridge"
	"github.com/jamestelfer/crush-acp-bridge/internal/crush"
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
			&cli.StringFlag{
				Name:  "cwd",
				Usage: "project directory to open in Crush (default: current directory)",
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.Args().Present() {
				return cli.Exit(
					fmt.Sprintf("crush-acp-bridge takes no arguments, got %q", cmd.Args().Slice()),
					exitUsage)
			}
			return serve(ctx, cmd.String("crush-url"), cmd.String("cwd"), stdin, stdout)
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

// serve opens the project in Crush and relays ACP traffic until the editor
// disconnects. stdout carries ACP messages only; diagnostics go to stderr.
func serve(ctx context.Context, crushURL, cwd string, stdin io.Reader, stdout io.Writer) error {
	if cwd == "" {
		var err error
		if cwd, err = os.Getwd(); err != nil {
			return fmt.Errorf("resolve working directory: %w", err)
		}
	}

	ws, err := crush.New(crushURL).OpenWorkspace(ctx, cwd)
	if err != nil {
		return fmt.Errorf("%w (is `crush server` running at %s?)", err, crushURL)
	}

	if err := bridge.Serve(ctx, ws, stdin, stdout); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}
