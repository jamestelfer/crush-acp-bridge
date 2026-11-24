package bridge

import (
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/coder/acp-go-sdk"

	"github.com/jamestelfer/crush-acp-bridge/internal/crush"
)

// promptCapabilities is the content the bridge forwards beyond text and
// resource links, which every agent accepts. Crush's POST /agent takes
// attachments: it inlines text/* content into the prompt and passes image/*
// content to a model with vision. Audio is not forwarded.
var promptCapabilities = acp.PromptCapabilities{Image: true, EmbeddedContext: true}

// errContent marks prompt content the bridge cannot send to Crush.
var errContent = errors.New("unsupported prompt content")

// refuseContent logs prompt content the bridge will not send and returns the
// error that tells the editor why.
func (a *Agent) refuseContent(id acp.SessionId, err error) error {
	a.log.Warn("prompt content refused", "session_id", string(id), "error", err.Error())
	return acp.NewInvalidParams(map[string]string{"error": err.Error()})
}

// crushPrompt maps ACP content blocks onto a Crush prompt. Text and resource
// links become the prompt text; images and embedded resources become
// attachments. It fails on content Crush would drop, rather than drop it.
func crushPrompt(blocks []acp.ContentBlock) (crush.Prompt, error) {
	var text strings.Builder
	var p crush.Prompt
	for _, b := range blocks {
		switch {
		case b.Text != nil:
			text.WriteString(b.Text.Text)
		case b.ResourceLink != nil:
			// Crush reads the file with its own tools.
			fmt.Fprintf(&text, "[@%s](%s)", b.ResourceLink.Name, b.ResourceLink.Uri)
		case b.Image != nil:
			a, err := imageAttachment(b.Image)
			if err != nil {
				return crush.Prompt{}, err
			}
			p.Attachments = append(p.Attachments, a)
		case b.Resource != nil:
			a, err := resourceAttachment(b.Resource.Resource)
			if err != nil {
				return crush.Prompt{}, err
			}
			p.Attachments = append(p.Attachments, a)
		case b.Audio != nil:
			return crush.Prompt{}, fmt.Errorf("%w: crush does not accept audio", errContent)
		default:
			return crush.Prompt{}, fmt.Errorf("%w: unknown content block", errContent)
		}
	}
	p.Text = text.String()
	// Crush refuses a prompt with neither text nor a text attachment.
	hasText := slices.ContainsFunc(p.Attachments, func(a crush.Attachment) bool { return isText(a.MimeType) })
	if strings.TrimSpace(p.Text) == "" && !hasText {
		return crush.Prompt{}, fmt.Errorf("%w: a prompt needs text; Crush refuses an empty prompt or one with only image attachments", errContent)
	}
	return p, nil
}

func imageAttachment(img *acp.ContentBlockImage) (crush.Attachment, error) {
	if !isImage(img.MimeType) {
		return crush.Attachment{}, fmt.Errorf("%w: image block of type %q", errContent, img.MimeType)
	}
	data, err := base64.StdEncoding.DecodeString(img.Data)
	if err != nil {
		return crush.Attachment{}, fmt.Errorf("%w: image data is not base64: %w", errContent, err)
	}
	a := crush.Attachment{FileName: "image", MimeType: img.MimeType, Content: data}
	if img.Uri != nil {
		a.FilePath, a.FileName = uriFile(*img.Uri)
	}
	return a, nil
}

func resourceAttachment(r acp.EmbeddedResourceResource) (crush.Attachment, error) {
	switch {
	case r.TextResourceContents != nil:
		res := r.TextResourceContents
		a := crush.Attachment{MimeType: "text/plain", Content: []byte(res.Text)}
		// Crush inlines only text/* content; any other type would reach the
		// model as a binary file.
		if res.MimeType != nil && isText(*res.MimeType) {
			a.MimeType = *res.MimeType
		}
		a.FilePath, a.FileName = uriFile(res.Uri)
		return a, nil
	case r.BlobResourceContents != nil:
		res := r.BlobResourceContents
		mime := ""
		if res.MimeType != nil {
			mime = *res.MimeType
		}
		if !isText(mime) && !isImage(mime) {
			return crush.Attachment{}, fmt.Errorf("%w: binary resource %s of type %q", errContent, res.Uri, mime)
		}
		data, err := base64.StdEncoding.DecodeString(res.Blob)
		if err != nil {
			return crush.Attachment{}, fmt.Errorf("%w: resource %s is not base64: %w", errContent, res.Uri, err)
		}
		a := crush.Attachment{MimeType: mime, Content: data}
		a.FilePath, a.FileName = uriFile(res.Uri)
		return a, nil
	}
	return crush.Attachment{}, fmt.Errorf("%w: empty resource", errContent)
}

func isText(mime string) bool  { return strings.HasPrefix(mime, "text/") }
func isImage(mime string) bool { return strings.HasPrefix(mime, "image/") }

// uriFile names the file at uri: its local path for a file URI, else the URI
// itself, and the last element of its path. Crush only shows the path to the
// model, so a URI it cannot open still tells the model where content came from.
func uriFile(uri string) (filePath, name string) {
	if p, ok := localPath(uri); ok {
		return p, filepath.Base(p)
	}
	name = path.Base(uri)
	if u, err := url.Parse(uri); err == nil && u.Path != "" {
		name = path.Base(u.Path)
	}
	return uri, name
}

// localPath returns the path a file URI names.
func localPath(uri string) (string, bool) {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" || u.Path == "" {
		return "", false
	}
	p := u.Path
	if runtime.GOOS == "windows" && len(p) >= 3 && p[0] == '/' && p[2] == ':' {
		p = p[1:] // file:///C:/x names C:/x
	}
	return filepath.FromSlash(p), true
}

// checkVision fails when p carries an image and the workspace's current model
// is known not to accept one: Crush would drop the image without telling the
// model. When the model cannot be identified, Crush decides.
func (a *Agent) checkVision(ctx context.Context, backend Backend, p crush.Prompt) error {
	if !slices.ContainsFunc(p.Attachments, func(att crush.Attachment) bool { return isImage(att.MimeType) }) {
		return nil
	}
	models, err := backend.Models(ctx)
	if err != nil {
		a.log.Warn("model capabilities unknown; sending the image", "error", err)
		return nil
	}
	i := slices.IndexFunc(models.Available, func(m crush.Model) bool {
		return m.Provider == models.Current.Provider && m.ID == models.Current.Model
	})
	if i < 0 {
		return nil
	}
	if m := models.Available[i]; !m.Vision {
		return fmt.Errorf("%w: the current model, %s, does not accept images", errContent, cmp.Or(m.Name, m.ID))
	}
	return nil
}
