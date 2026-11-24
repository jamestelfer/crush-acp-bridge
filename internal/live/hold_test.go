//go:build live

package live_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/require"
)

// TestHold opens a bridge session and keeps it alive so a person can attach
// the Crush TUI to the same session. Set LIVE_HOLD to a directory. The test
// writes session-id and editor.log there, sends the contents of prompt.txt
// whenever it appears, and exits when a file named stop appears.
func TestHold(t *testing.T) {
	dir := os.Getenv("LIVE_HOLD")
	if dir == "" {
		t.Skip("LIVE_HOLD not set")
	}
	require.NoError(t, os.MkdirAll(dir, 0o755))

	logFile, err := os.Create(filepath.Join(dir, "editor.log"))
	require.NoError(t, err)
	defer func() { _ = logFile.Close() }()
	write := func(line string) {
		_, _ = fmt.Fprintf(logFile, "%s %s\n", time.Now().Format("15:04:05.000"), line)
	}

	ed := &editor{trace: write}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Minute)
	defer cancel()
	conn, sid, _, _ := dial(t, ctx, ed)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "session-id"), []byte(sid), 0o644))
	write("session " + string(sid))

	promptFile, stopFile := filepath.Join(dir, "prompt.txt"), filepath.Join(dir, "stop")
	for ctx.Err() == nil {
		if _, err := os.Stat(stopFile); err == nil {
			return
		}
		text, err := os.ReadFile(promptFile)
		if err != nil {
			time.Sleep(300 * time.Millisecond)
			continue
		}
		require.NoError(t, os.Remove(promptFile))
		write("prompt " + strings.TrimSpace(string(text)))
		resp, err := conn.Prompt(ctx, acp.PromptRequest{
			SessionId: sid, Prompt: []acp.ContentBlock{acp.TextBlock(string(text))},
		})
		require.NoError(t, err)
		write("stop " + string(resp.StopReason))
	}
}
