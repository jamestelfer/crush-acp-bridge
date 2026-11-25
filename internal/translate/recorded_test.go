package translate_test

import (
	"encoding/json/v2"
	"path/filepath"
	"testing"

	"github.com/gkampitakis/go-snaps/snaps"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jamestelfer/crush-acp-bridge/internal/translate"
)

// scriptTraces are recorded by scripts/record-trace against a real Crush
// server, one per transport the script supports.
var scriptTraces = []string{"http-bash-allow.sse", "unix-bash-deny.sse"}

// eventHead is the envelope every Crush SSE event shares.
type eventHead struct {
	Type    string `json:"type"`
	Payload struct {
		Type    string `json:"type"`
		Payload struct {
			ID        string `json:"id"`
			SessionID string `json:"session_id"`
		} `json:"payload"`
	} `json:"payload"`
}

func TestScriptTracesOpenWithTheSessionBeingCreated(t *testing.T) {
	for _, name := range scriptTraces {
		t.Run(name, func(t *testing.T) {
			events := replay(t, name)
			require.NotEmpty(t, events)

			// The script subscribes before it creates the session, as the
			// bridge does, so the stream starts with the session's creation.
			var first, second eventHead
			require.NoError(t, json.Unmarshal(events[0], &first))
			assert.Equal(t, "session", first.Type)
			assert.Equal(t, "created", first.Payload.Type)

			require.Greater(t, len(events), 1)
			require.NoError(t, json.Unmarshal(events[1], &second))
			assert.Equal(t, "client_session", second.Type)
			assert.Equal(t, first.Payload.Payload.ID, second.Payload.Payload.SessionID)
		})
	}
}

func TestEveryTraceReplays(t *testing.T) {
	names, err := filepath.Glob(filepath.Join("..", "..", "testdata", "*.sse"))
	require.NoError(t, err)
	require.NotEmpty(t, names)

	for _, path := range names {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			tr := translate.New()
			for _, data := range replay(t, name) {
				updates, err := tr.Feed(data)
				require.NoError(t, err)
				for _, u := range updates {
					assert.NotEmpty(t, u.SessionID, "update kind %d has no session", u.Kind)
				}
			}
			assert.NotEmpty(t, stopReasons(t, name), "trace has no turn end")
		})
	}
}

func TestHTTPTraceTurnEndsOnce(t *testing.T) {
	assert.Equal(t, []string{"end_turn"}, stopReasons(t, "http-bash-allow.sse"))
}

func TestUnixTraceEndsEachPrompt(t *testing.T) {
	assert.Equal(t, []string{"end_turn", "end_turn"}, stopReasons(t, "unix-bash-deny.sse"))
}

func TestUpdateStreamForHTTPRecordedTurn(t *testing.T) {
	snaps.MatchJSON(t, streamOf(t, "http-bash-allow.sse"))
}

func TestUpdateStreamForUnixRecordedTurns(t *testing.T) {
	snaps.MatchJSON(t, streamOf(t, "unix-bash-deny.sse"))
}
