package bridge_test

import (
	"bytes"
	"cmp"
	"encoding/json/v2"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/coder/acp-go-sdk"
	"github.com/gkampitakis/go-snaps/snaps"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jamestelfer/crush-acp-bridge/internal/crush"
)

// Tool calls in the edit and write traces.
const (
	editCall      acp.ToolCallId = "call_16713861530277631741"
	multiEditCall acp.ToolCallId = "call_5185534490520432532"
	newFileCall   acp.ToolCallId = "call_8171991326521080277"
	overwriteCall acp.ToolCallId = "call_9574960797699994523"
)

// asked records the permission requests an editor receives, and what the
// editor had been told about each tool call when it was asked.
type asked struct {
	ed *editor

	mu       sync.Mutex
	requests []acp.RequestPermissionRequest
	before   map[acp.ToolCallId][]acp.SessionUpdate
}

func newAsked() *asked {
	a := &asked{before: map[acp.ToolCallId][]acp.SessionUpdate{}}
	a.ed = &editor{onPermisson: a.allow}
	return a
}

func (a *asked) allow(r acp.RequestPermissionRequest) acp.RequestPermissionResponse {
	seen := a.ed.snapshot()
	a.mu.Lock()
	a.requests = append(a.requests, r)
	a.before[r.ToolCall.ToolCallId] = seen
	a.mu.Unlock()
	return allow(r)
}

// sorted returns the requests ordered by tool call ID: the bridge asks
// concurrently, so arrival order varies.
func (a *asked) sorted() []acp.RequestPermissionRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := slices.Clone(a.requests)
	slices.SortFunc(out, func(x, y acp.RequestPermissionRequest) int {
		return cmp.Compare(x.ToolCall.ToolCallId, y.ToolCall.ToolCallId)
	})
	return out
}

// runTrace prompts once against a recorded trace and waits for every
// permission request it raises.
func runTrace(t *testing.T, tr trace, permissions int) *asked {
	t.Helper()

	backend := newFakeCrush(tr.sessionID(t), tr)
	a := newAsked()
	conn := connect(t, backend, a.ed)
	sid := start(t, conn)
	prompt(t, conn, sid, "edit it")
	require.Eventually(t, func() bool { return len(a.sorted()) == permissions },
		time.Second*2, 10*time.Millisecond)
	return a
}

// diffs returns the diff content of the latest update that set a tool call's
// content.
func diffs(updates []acp.SessionUpdate, id acp.ToolCallId) []acp.ToolCallContentDiff {
	var out []acp.ToolCallContentDiff
	for _, u := range updates {
		if u.ToolCallUpdate == nil || u.ToolCallUpdate.ToolCallId != id {
			continue
		}
		content, ok := u.ToolCallUpdate.Content.Value()
		if !ok {
			continue
		}
		out = nil
		for _, c := range content {
			if c.Diff != nil {
				out = append(out, *c.Diff)
			}
		}
	}
	return out
}

func TestEditorViewOfEditTurn(t *testing.T) {
	a := runTrace(t, loadTrace(t, "edit-permission.sse"), 2)

	snaps.MatchJSON(t, received(t, a.ed))
	out, err := json.Marshal(a.sorted())
	require.NoError(t, err)
	snaps.MatchJSON(t, out)
}

func TestEditorViewOfWriteTurn(t *testing.T) {
	a := runTrace(t, loadTrace(t, "write-permission.sse"), 1)

	snaps.MatchJSON(t, received(t, a.ed))
	out, err := json.Marshal(a.sorted())
	require.NoError(t, err)
	snaps.MatchJSON(t, out)
}

func TestEditPermissionCarriesTheWholeFileDiff(t *testing.T) {
	a := runTrace(t, loadTrace(t, "edit-permission.sse"), 2)

	want := []acp.ToolCallContentDiff{{
		Type: "diff", Path: "/tmp/crushlive/diffproj/greek.txt",
		OldText: new("alpha\nbeta\ngamma\ndelta\n"), NewText: "alpha\nBETA\ngamma\ndelta\n",
	}}
	var req acp.RequestPermissionRequest
	for _, r := range a.sorted() {
		if r.ToolCall.ToolCallId == editCall {
			req = r
		}
	}
	content, ok := req.ToolCall.Content.Value()
	require.True(t, ok, "the request must show the change")
	require.Len(t, content, 1)
	require.NotNil(t, content[0].Diff)
	assert.Equal(t, want[0], *content[0].Diff)

	// Paseo shows the tool call it already holds, not the request's copy.
	assert.Equal(t, want, diffs(a.before[editCall], editCall),
		"the editor must hold the diff before it is asked")
}

func TestEditShowsTheChangedTextBeforeCrushAsks(t *testing.T) {
	a := runTrace(t, loadTrace(t, "edit-permission.sse"), 2)

	var input []acp.SessionUpdate
	for _, u := range a.ed.snapshot() {
		input = append(input, u)
		if u.ToolCallUpdate != nil && u.ToolCallUpdate.ToolCallId == multiEditCall &&
			!u.ToolCallUpdate.RawInput.IsUnset() {
			break
		}
	}
	path := "/tmp/crushlive/diffproj/greek.txt"
	assert.Equal(t, []acp.ToolCallContentDiff{
		{Type: "diff", Path: path, OldText: new("BETA\ngamma\ndelta"), NewText: "BETA\nGAMMA\ndelta"},
		{Type: "diff", Path: path, OldText: new("BETA\nGAMMA\ndelta"), NewText: "BETA\nGAMMA\nDELTA"},
	}, diffs(input, multiEditCall), "each edit's own text, until Crush supplies the file")
}

func TestCompletedEditShowsTheWholeFileDiff(t *testing.T) {
	a := runTrace(t, loadTrace(t, "edit-permission.sse"), 2)

	assert.Equal(t, []acp.ToolCallContentDiff{{
		Type: "diff", Path: "/tmp/crushlive/diffproj/greek.txt",
		OldText: new("alpha\nBETA\ngamma\ndelta\n"), NewText: "alpha\nBETA\nGAMMA\nDELTA\n",
	}}, diffs(a.ed.snapshot(), multiEditCall))
}

func TestNewFileWriteHasNoOldText(t *testing.T) {
	a := runTrace(t, loadTrace(t, "write-permission.sse"), 1)

	assert.Equal(t, []acp.ToolCallContentDiff{{
		Type: "diff", Path: "/tmp/crushlive/diffproj/notes.txt", NewText: "one\ntwo\n",
	}}, diffs(a.ed.snapshot(), newFileCall))
}

func TestUnaskedOverwriteShowsCrushsUnifiedDiff(t *testing.T) {
	a := runTrace(t, loadTrace(t, "write-permission.sse"), 1)

	// Crush did not ask, so the old content is known only from its diff.
	var text string
	for _, u := range a.ed.snapshot() {
		if u.ToolCallUpdate == nil || u.ToolCallUpdate.ToolCallId != overwriteCall {
			continue
		}
		if content, ok := u.ToolCallUpdate.Content.Value(); ok {
			require.Len(t, content, 1)
			require.NotNil(t, content[0].Content)
			text = content[0].Content.Content.Text.Text
		}
	}
	assert.Equal(t,
		"--- a/greek.txt\n+++ b/greek.txt\n@@ -1,4 +1 @@\n-alpha\n-BETA\n-GAMMA\n-DELTA\n+omega\n", text)
}

func TestRelativeEditPathIsResolvedAgainstTheSessionDirectory(t *testing.T) {
	tr := loadTrace(t, "edit-permission.sse")
	// Only the tool call input carries an escaped path.
	for i := range tr {
		tr[i] = bytes.ReplaceAll(tr[i],
			[]byte(`\"file_path\":\"/tmp/crushlive/diffproj/greek.txt\"`), []byte(`\"file_path\":\"greek.txt\"`))
	}
	a := runTrace(t, tr, 2)

	var input []acp.SessionUpdate
	for _, u := range a.ed.snapshot() {
		input = append(input, u)
		if u.ToolCallUpdate != nil && u.ToolCallUpdate.ToolCallId == editCall && !u.ToolCallUpdate.RawInput.IsUnset() {
			break
		}
	}
	got := diffs(input, editCall)
	require.Len(t, got, 1)
	assert.Equal(t, filepath.Join("/work/proj", "greek.txt"), got[0].Path)
}

func TestLoadedEditShowsTheWholeFileDiff(t *testing.T) {
	tr := loadTrace(t, "edit-permission.sse")
	sid := tr.sessionID(t)
	backend := newFakeCrush(sid, nil)
	backend.history = tr
	backend.stored = []crush.Session{{ID: sid, Title: "trace", MessageCount: 8}}
	ed := &editor{}
	conn := connect(t, backend, ed)
	initialize(t, conn)

	_, err := load(t, conn, acp.SessionId(sid))
	require.NoError(t, err)

	// Stored messages hold no permission requests; the result metadata does.
	assert.Equal(t, []acp.ToolCallContentDiff{{
		Type: "diff", Path: "/tmp/crushlive/diffproj/greek.txt",
		OldText: new("alpha\nbeta\ngamma\ndelta\n"), NewText: "alpha\nBETA\ngamma\ndelta\n",
	}}, diffs(ed.snapshot(), editCall))
}
