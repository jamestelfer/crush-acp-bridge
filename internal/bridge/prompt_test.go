package bridge_test

import (
	"context"
	"encoding/base64"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jamestelfer/crush-acp-bridge/internal/bridge"
	"github.com/jamestelfer/crush-acp-bridge/internal/crush"
)

// visionModels has a current model that accepts images.
var visionModels = crush.Models{
	Current: crush.ModelRef{Provider: "vertexai", Model: "gemini"},
	Available: []crush.Model{
		{Provider: "vertexai", ID: "gemini", Name: "Gemini", Vision: true},
		{Provider: "vertexai", ID: "text-only", Name: "Text Only"},
	},
}

// attachmentBackend is a fake Crush whose prompts finish a turn and whose
// current model accepts images.
func attachmentBackend(t *testing.T) *fakeCrush {
	t.Helper()
	tr := loadTrace(t, "long-text-cancel.sse")
	backend := newFakeCrush(tr.sessionID(t), tr.firstTurn(t, "end_turn"))
	backend.models = visionModels
	return backend
}

func promptBlocks(t *testing.T, conn *acp.ClientSideConnection, sid acp.SessionId, blocks ...acp.ContentBlock) (acp.PromptResponse, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	return conn.Prompt(ctx, acp.PromptRequest{SessionId: sid, Prompt: blocks})
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// requireInvalidParams asserts err is a JSON-RPC invalid-params error whose
// data mentions want.
func requireInvalidParams(t *testing.T, err error, want string) {
	t.Helper()
	var reqErr *acp.RequestError
	require.ErrorAs(t, err, &reqErr)
	assert.Equal(t, acp.NewInvalidParams(nil).Code, reqErr.Code)
	assert.Contains(t, string(reqErr.Data), want)
}

func TestInitializeAdvertisesTheContentTheBridgeForwards(t *testing.T) {
	conn := connect(t, newFakeCrush("s1", nil), &editor{})

	caps := initialize(t, conn).AgentCapabilities.PromptCapabilities

	assert.True(t, caps.Image, "images are sent to Crush as attachments")
	assert.True(t, caps.EmbeddedContext, "embedded resources are sent to Crush as attachments")
	assert.False(t, caps.Audio, "audio is not forwarded")
}

func TestImagePromptReachesCrushAsAnAttachment(t *testing.T) {
	backend := attachmentBackend(t)
	conn := connect(t, backend, &editor{})
	sid := start(t, conn)

	img := acp.ImageBlock(b64("PNG"), "image/png")
	uri := "file:///work/proj/logo.png"
	img.Image.Uri = &uri
	resp, err := promptBlocks(t, conn, sid, acp.TextBlock(tcpPrompt), img)
	require.NoError(t, err)

	assert.Equal(t, acp.StopReasonEndTurn, resp.StopReason)
	assert.Equal(t, []string{tcpPrompt}, backend.prompts)
	assert.Equal(t, [][]crush.Attachment{{{
		FilePath: filepath.FromSlash("/work/proj/logo.png"),
		FileName: "logo.png",
		MimeType: "image/png",
		Content:  []byte("PNG"),
	}}}, backend.attachments)
}

func TestImageWithoutAURIIsNamedForItsType(t *testing.T) {
	backend := attachmentBackend(t)
	conn := connect(t, backend, &editor{})
	sid := start(t, conn)

	_, err := promptBlocks(t, conn, sid, acp.TextBlock(tcpPrompt), acp.ImageBlock(b64("JPG"), "image/jpeg"))
	require.NoError(t, err)

	require.Len(t, backend.attachments, 1)
	assert.Equal(t, []crush.Attachment{{FileName: "image", MimeType: "image/jpeg", Content: []byte("JPG")}},
		backend.attachments[0])
}

func TestImageWithAnotherURISchemeKeepsTheURIAsItsPath(t *testing.T) {
	backend := attachmentBackend(t)
	conn := connect(t, backend, &editor{})
	sid := start(t, conn)

	img := acp.ImageBlock(b64("PNG"), "image/png")
	uri := "zed:///clipboard/paste.png"
	img.Image.Uri = &uri
	_, err := promptBlocks(t, conn, sid, acp.TextBlock(tcpPrompt), img)
	require.NoError(t, err)

	require.Len(t, backend.attachments, 1)
	assert.Equal(t, []crush.Attachment{{FilePath: uri, FileName: "paste.png", MimeType: "image/png", Content: []byte("PNG")}},
		backend.attachments[0], "images and resources name a non-file URI the same way")
}

func TestEmbeddedTextResourceReachesCrushAsATextAttachment(t *testing.T) {
	backend := attachmentBackend(t)
	conn := connect(t, backend, &editor{})
	sid := start(t, conn)

	goMime := "text/x-go"
	jsonMime := "application/json"
	_, err := promptBlocks(t, conn, sid,
		acp.TextBlock(tcpPrompt),
		acp.ResourceBlock(acp.EmbeddedResourceResource{TextResourceContents: &acp.TextResourceContents{
			Uri: "file:///work/proj/main.go", MimeType: &goMime, Text: "package main",
		}}),
		acp.ResourceBlock(acp.EmbeddedResourceResource{TextResourceContents: &acp.TextResourceContents{
			Uri: "file:///work/proj/config.json", MimeType: &jsonMime, Text: "{}",
		}}),
		acp.ResourceBlock(acp.EmbeddedResourceResource{TextResourceContents: &acp.TextResourceContents{
			Uri: "zed:///agent/rules", Text: "be brief",
		}}),
	)
	require.NoError(t, err)

	require.Len(t, backend.attachments, 1)
	assert.Equal(t, []crush.Attachment{
		{FilePath: filepath.FromSlash("/work/proj/main.go"), FileName: "main.go", MimeType: "text/x-go", Content: []byte("package main")},
		// Crush inlines only text/* attachments; any other type would go to
		// the model as a binary file.
		{FilePath: filepath.FromSlash("/work/proj/config.json"), FileName: "config.json", MimeType: "text/plain", Content: []byte("{}")},
		{FilePath: "zed:///agent/rules", FileName: "rules", MimeType: "text/plain", Content: []byte("be brief")},
	}, backend.attachments[0])
}

func TestEmbeddedBlobResourceIsSentByType(t *testing.T) {
	backend := attachmentBackend(t)
	conn := connect(t, backend, &editor{})
	sid := start(t, conn)

	png := "image/png"
	md := "text/markdown"
	_, err := promptBlocks(t, conn, sid,
		acp.TextBlock(tcpPrompt),
		acp.ResourceBlock(acp.EmbeddedResourceResource{BlobResourceContents: &acp.BlobResourceContents{
			Uri: "file:///work/proj/shot.png", MimeType: &png, Blob: b64("PNG"),
		}}),
		acp.ResourceBlock(acp.EmbeddedResourceResource{BlobResourceContents: &acp.BlobResourceContents{
			Uri: "file:///work/proj/README.md", MimeType: &md, Blob: b64("# hi"),
		}}),
	)
	require.NoError(t, err)

	require.Len(t, backend.attachments, 1)
	assert.Equal(t, []crush.Attachment{
		{FilePath: filepath.FromSlash("/work/proj/shot.png"), FileName: "shot.png", MimeType: "image/png", Content: []byte("PNG")},
		{FilePath: filepath.FromSlash("/work/proj/README.md"), FileName: "README.md", MimeType: "text/markdown", Content: []byte("# hi")},
	}, backend.attachments[0])
}

func TestResourceLinkIsMentionedInThePromptText(t *testing.T) {
	backend := attachmentBackend(t)
	conn := connect(t, backend, &editor{})
	sid := start(t, conn)

	_, err := promptBlocks(t, conn, sid,
		acp.TextBlock("Review "),
		acp.ResourceLinkBlock("main.go", "file:///work/proj/main.go"),
		acp.TextBlock(" please"),
	)
	require.NoError(t, err)

	assert.Equal(t, []string{"Review [@main.go](file:///work/proj/main.go) please"}, backend.prompts,
		"Crush reads the linked file with its own tools")
	assert.Equal(t, [][]crush.Attachment{nil}, backend.attachments)
}

func TestUnsupportedPromptContentIsRefused(t *testing.T) {
	pdf := "application/pdf"
	cases := map[string]struct {
		blocks []acp.ContentBlock
		want   string
	}{
		"audio": {
			blocks: []acp.ContentBlock{acp.TextBlock("hi"), acp.AudioBlock(b64("RIFF"), "audio/wav")},
			want:   "audio",
		},
		"binary resource of another type": {
			blocks: []acp.ContentBlock{acp.TextBlock("hi"), acp.ResourceBlock(acp.EmbeddedResourceResource{
				BlobResourceContents: &acp.BlobResourceContents{Uri: "file:///work/spec.pdf", MimeType: &pdf, Blob: b64("%PDF")},
			})},
			want: "application/pdf",
		},
		"image that is not base64": {
			blocks: []acp.ContentBlock{acp.TextBlock("hi"), acp.ImageBlock("not base64!", "image/png")},
			want:   "base64",
		},
		"image block of another type": {
			blocks: []acp.ContentBlock{acp.TextBlock("hi"), acp.ImageBlock(b64("x"), "text/plain")},
			want:   "text/plain",
		},
		"image without text": {
			blocks: []acp.ContentBlock{acp.ImageBlock(b64("PNG"), "image/png")},
			want:   "text",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			backend := attachmentBackend(t)
			logs := &logCapture{}
			conn := connect(t, backend, &editor{}, bridge.WithLogger(logs.logger()))
			sid := start(t, conn)

			_, err := promptBlocks(t, conn, sid, tc.blocks...)

			requireInvalidParams(t, err, tc.want)
			assert.Empty(t, backend.prompts, "Crush never received the prompt")
			rec := logs.find(t, "prompt content refused")
			require.NotNil(t, rec, "the refusal is logged")
			assert.Contains(t, rec["error"], tc.want)
		})
	}
}

func TestImageIsRefusedWhenTheModelCannotSeeIt(t *testing.T) {
	backend := attachmentBackend(t)
	backend.models.Current = crush.ModelRef{Provider: "vertexai", Model: "text-only"}
	logs := &logCapture{}
	conn := connect(t, backend, &editor{}, bridge.WithLogger(logs.logger()))
	sid := start(t, conn)

	_, err := promptBlocks(t, conn, sid, acp.TextBlock("hi"), acp.ImageBlock(b64("PNG"), "image/png"))

	requireInvalidParams(t, err, "Text Only")
	assert.Empty(t, backend.prompts, "Crush would drop the image without telling the model")
	rec := logs.find(t, "prompt content refused")
	require.NotNil(t, rec, "the refusal is logged")
	assert.Contains(t, rec["error"], "Text Only")
}

func TestSessionWhoseImageWasRefusedIsStillDeletedWhenEmpty(t *testing.T) {
	backend := attachmentBackend(t)
	backend.models.Current = crush.ModelRef{Provider: "vertexai", Model: "text-only"}
	backend.stored = []crush.Session{{ID: backend.sessionID}}
	srv := serveOne(t, backend)
	sid := start(t, srv.conn)

	_, err := promptBlocks(t, srv.conn, sid, acp.TextBlock("hi"), acp.ImageBlock(b64("PNG"), "image/png"))
	requireInvalidParams(t, err, "Text Only")

	srv.disconnect(t)

	assert.Equal(t, []string{string(sid)}, backend.deletedSessions(), "nothing reached Crush, so the session is empty")
}

func TestImageIsSentWhenTheModelIsUnknown(t *testing.T) {
	backend := attachmentBackend(t)
	backend.models = crush.Models{Current: crush.ModelRef{Provider: "custom", Model: "unlisted"}}
	conn := connect(t, backend, &editor{})
	sid := start(t, conn)

	_, err := promptBlocks(t, conn, sid, acp.TextBlock(tcpPrompt), acp.ImageBlock(b64("PNG"), "image/png"))

	require.NoError(t, err, "Crush decides when the bridge cannot tell")
	assert.Len(t, backend.attachments, 1)
}
