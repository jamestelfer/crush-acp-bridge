package bridge_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jamestelfer/crush-acp-bridge/internal/bridge"
)

func TestSlowOpenDoesNotStallAnOpenDirectory(t *testing.T) {
	tr := loadTrace(t, "long-text-cancel.sse")
	backendA := newFakeCrush(tr.sessionID(t), tr.firstTurn(t, "end_turn"))
	opening := make(chan struct{})
	release := make(chan struct{})
	open := func(ctx context.Context, cwd string) (bridge.Backend, error) {
		if cwd != "/work/b" {
			return backendA, nil
		}
		close(opening)
		select { // an EnsureShared waiting for a server to start
		case <-release:
		case <-ctx.Done():
		}
		return newFakeCrush("sess-b", nil), nil
	}
	ed := &editor{}
	conn := connectWith(t, open, ed)
	t.Cleanup(func() { close(release) })
	sidA := start(t, conn)

	go func() {
		_, _ = conn.NewSession(t.Context(), acp.NewSessionRequest{Cwd: "/work/b", McpServers: []acp.McpServer{}})
	}()
	<-opening

	resp := prompt(t, conn, sidA, "explain TCP")

	assert.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
	assert.NotEmpty(t, agentText(ed), "updates in the open directory still flow")
}

func TestConcurrentSessionsInANewDirectoryOpenItOnce(t *testing.T) {
	var opens atomic.Int32
	open := func(context.Context, string) (bridge.Backend, error) {
		opens.Add(1)
		time.Sleep(200 * time.Millisecond)
		return newFakeCrush("sess-1", nil), nil
	}
	conn := connectWith(t, open, &editor{})
	initialize(t, conn)

	var wg sync.WaitGroup
	errs := make([]error, 3)
	for i := range errs {
		wg.Go(func() {
			_, errs[i] = conn.NewSession(t.Context(), acp.NewSessionRequest{Cwd: "/work/c", McpServers: []acp.McpServer{}})
		})
	}
	wg.Wait()

	for _, err := range errs {
		require.NoError(t, err)
	}
	assert.Equal(t, int32(1), opens.Load())
}

func TestFailedOpenIsRetriedByTheNextSession(t *testing.T) {
	var opens atomic.Int32
	open := func(context.Context, string) (bridge.Backend, error) {
		if opens.Add(1) == 1 {
			return nil, errors.New("connection refused")
		}
		return newFakeCrush("sess-1", nil), nil
	}
	conn := connectWith(t, open, &editor{})
	initialize(t, conn)

	_, err := conn.NewSession(t.Context(), acp.NewSessionRequest{Cwd: "/work/c", McpServers: []acp.McpServer{}})
	require.ErrorContains(t, err, "refused")
	_, err = conn.NewSession(t.Context(), acp.NewSessionRequest{Cwd: "/work/c", McpServers: []acp.McpServer{}})

	require.NoError(t, err)
	assert.Equal(t, int32(2), opens.Load())
}
