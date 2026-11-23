// Package crush is a client for the Crush server HTTP API.
package crush

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Client talks to a Crush server over HTTP.
type Client struct {
	base string
	http *http.Client
}

// New returns a Client for the server at baseURL, for example
// "http://127.0.0.1:8484".
func New(baseURL string) *Client {
	return &Client{base: strings.TrimRight(baseURL, "/") + "/v1", http: &http.Client{}}
}

// Workspace is a project registered with the server by this client.
type Workspace struct {
	c        *Client
	id       string
	clientID string
}

// ID returns the server's workspace ID.
func (w *Workspace) ID() string { return w.id }

// OpenWorkspace registers the project at path and returns a handle to it.
func (c *Client) OpenWorkspace(ctx context.Context, path string) (*Workspace, error) {
	clientID, err := newUUID()
	if err != nil {
		return nil, err
	}

	var out struct {
		ID string `json:"id"`
	}
	body := map[string]string{"path": path, "client_id": clientID}
	if err := c.do(ctx, http.MethodPost, "/workspaces", body, &out); err != nil {
		return nil, fmt.Errorf("open workspace: %w", err)
	}
	return &Workspace{c: c, id: out.ID, clientID: clientID}, nil
}

// do sends a JSON request and decodes a JSON response into out when non-nil.
func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body bytes.Buffer
	if in != nil {
		if err := json.MarshalWrite(&body, in); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusConflict {
		return fmt.Errorf("%s %s: %w", method, path, ErrBusy)
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: %s", method, path, resp.Status)
	}
	if out == nil {
		return nil
	}
	return json.UnmarshalRead(resp.Body, out)
}

// newUUID returns a random version 4 UUID. Crush rejects other client IDs.
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}

// NewSession creates a session titled title and returns its ID.
func (w *Workspace) NewSession(ctx context.Context, title string) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	if err := w.c.do(ctx, http.MethodPost, w.path("/sessions"), map[string]string{"title": title}, &out); err != nil {
		return "", fmt.Errorf("new session: %w", err)
	}
	return out.ID, nil
}

// SetCurrentSession reports which session this client is viewing. The server
// delivers permission prompts only to the client viewing the session that
// raised them.
func (w *Workspace) SetCurrentSession(ctx context.Context, sessionID string) error {
	p := w.path("/current-session") + "?client_id=" + url.QueryEscape(w.clientID)
	if err := w.c.do(ctx, http.MethodPost, p, map[string]string{"session_id": sessionID}, nil); err != nil {
		return fmt.Errorf("set current session: %w", err)
	}
	return nil
}

func (w *Workspace) path(suffix string) string { return "/workspaces/" + w.id + suffix }

// ErrBusy is returned by Prompt when the session is already running a turn.
var ErrBusy = errors.New("session is busy")

// Prompt starts an agent turn in the session. The run continues on the server
// after Prompt returns; follow it through Events.
func (w *Workspace) Prompt(ctx context.Context, sessionID, text string) error {
	body := map[string]string{"session_id": sessionID, "prompt": text}
	if err := w.c.do(ctx, http.MethodPost, w.path("/agent"), body, nil); err != nil {
		return fmt.Errorf("prompt: %w", err)
	}
	return nil
}

// Cancel stops the running turn in the session.
func (w *Workspace) Cancel(ctx context.Context, sessionID string) error {
	if err := w.c.do(ctx, http.MethodPost, w.path("/agent/sessions/"+url.PathEscape(sessionID)+"/cancel"), nil, nil); err != nil {
		return fmt.Errorf("cancel: %w", err)
	}
	return nil
}

// Action is a response to a permission request.
type Action string

// Permission actions understood by the grant endpoint.
const (
	Allow           Action = "allow"
	AllowForSession Action = "allow_session"
	Deny            Action = "deny"
)

// Grant answers a permission request. permission is the request exactly as
// the server sent it.
func (w *Workspace) Grant(ctx context.Context, permission jsontext.Value, action Action) error {
	body := struct {
		Action     Action         `json:"action"`
		Permission jsontext.Value `json:"permission"`
	}{action, permission}
	if err := w.c.do(ctx, http.MethodPost, w.path("/permissions/grant"), body, nil); err != nil {
		return fmt.Errorf("grant: %w", err)
	}
	return nil
}

// Events opens the workspace's server-sent event stream and returns the data
// field of each event. The channel closes when the stream ends or ctx is
// cancelled. A server that drops a slow client ends the stream; callers
// reconnect, report the current session again, and re-read state.
func (w *Workspace) Events(ctx context.Context) (<-chan []byte, error) {
	u := w.c.base + w.path("/events") + "?client_id=" + url.QueryEscape(w.clientID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := w.c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("events: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("events: %s", resp.Status)
	}

	out := make(chan []byte, 64)
	go pump(ctx, resp.Body, out)
	return out, nil
}

// pump forwards the data field of each SSE event to out, then closes both body
// and out.
func pump(ctx context.Context, body io.ReadCloser, out chan<- []byte) {
	defer close(out)
	defer func() { _ = body.Close() }()

	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64<<10), 32<<20) // messages carry full tool output
	for sc.Scan() {
		data, ok := bytes.CutPrefix(sc.Bytes(), []byte("data:"))
		if !ok {
			continue
		}
		select {
		case out <- bytes.Clone(bytes.TrimSpace(data)):
		case <-ctx.Done():
			return
		}
	}
}
