// Package fakecrush is a stand-in for the Crush server, for tests.
//
// Handler serves the small part of the Crush HTTP API the bridge uses.
// MaybeRunAsServer lets a test binary act as the `crush` executable: spawn
// os.Args[0] with CRUSH_FAKE_SERVER set and call MaybeRunAsServer first in
// TestMain.
package fakecrush

import (
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/jamestelfer/crush-acp-bridge/internal/crushserver"
)

// EnvMode selects how a re-executed test binary behaves as `crush server`:
// "serve" (default when set), "exit" (fail immediately) or "silent" (run but
// never listen).
const EnvMode = "CRUSH_FAKE_SERVER"

// Hooks report what a client asks the fake server to do. Nil hooks are
// skipped.
type Hooks struct {
	// OnOpen is called with the path of each workspace the client opens.
	OnOpen func(path string)
	// OnDelete is called with the ID of each session the client deletes.
	OnDelete func(sessionID string)
	// OnSubscribe is called with the client ID of each event-stream
	// subscription. The stream ends when the returned channel closes, as a
	// server that drops a slow client ends it; a nil channel never closes.
	OnSubscribe func(clientID string) <-chan struct{}
}

// Handler returns an http.Handler for the fake server.
func Handler(hooks Hooks) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", func(http.ResponseWriter, *http.Request) {})
	mux.HandleFunc("GET /_fake/env", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprint(w, os.Getenv(r.URL.Query().Get("name"))) //nolint:gosec // test fake; plain text, not HTML
	})
	mux.HandleFunc("POST /v1/workspaces", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Path string `json:"path"`
		}
		_ = json.UnmarshalRead(r.Body, &body)
		if hooks.OnOpen != nil {
			hooks.OnOpen(body.Path)
		}
		_, _ = w.Write([]byte(`{"id":"ws-1"}`))
	})
	mux.HandleFunc("GET /v1/workspaces/ws-1/events", func(w http.ResponseWriter, r *http.Request) {
		var drop <-chan struct{}
		if hooks.OnSubscribe != nil {
			drop = hooks.OnSubscribe(r.URL.Query().Get("client_id"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-drop:
		}
	})
	mux.HandleFunc("POST /v1/workspaces/ws-1/sessions", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"sess-1"}`))
	})
	mux.HandleFunc("POST /v1/workspaces/ws-1/current-session", func(http.ResponseWriter, *http.Request) {})
	// sess-1 never holds messages, so a bridge deletes it when it exits.
	mux.HandleFunc("GET /v1/workspaces/ws-1/sessions", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"id":"sess-1","message_count":0}]`))
	})
	mux.HandleFunc("DELETE /v1/workspaces/ws-1/sessions/{sid}", func(_ http.ResponseWriter, r *http.Request) {
		if hooks.OnDelete != nil {
			hooks.OnDelete(r.PathValue("sid"))
		}
	})
	return mux
}

// MaybeRunAsServer turns the process into a fake `crush server` when EnvMode
// is set, and never returns in that case. Otherwise it does nothing.
func MaybeRunAsServer() {
	mode := os.Getenv(EnvMode)
	if mode == "" {
		return
	}

	switch mode {
	case "exit":
		fmt.Fprintln(os.Stderr, "fake crush: refusing to start")
		os.Exit(3)
	case "silent":
		time.Sleep(time.Minute)
		os.Exit(0)
	}

	network, addr := hostArg(os.Args[1:])
	ln, err := listen(network, addr)
	if err != nil {
		// Like Crush, a server that finds its address taken gives up.
		fmt.Fprintln(os.Stderr, "fake crush:", err)
		os.Exit(1)
	}
	logFile := openLog(network, addr)
	exit := func() {
		_ = ln.Close() // removes a Unix socket file
		if logFile != nil {
			_ = logFile.Close()
		}
		os.Exit(0)
	}

	mux := http.NewServeMux()
	// A test reads deletions from the bridge's log of the child's output.
	mux.Handle("/", Handler(Hooks{OnDelete: func(sid string) {
		fmt.Fprintln(os.Stderr, "fake crush: deleted session", sid)
	}}))
	mux.HandleFunc("GET /_fake/pid", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, os.Getpid())
	})
	mux.HandleFunc("POST /_fake/exit", func(http.ResponseWriter, *http.Request) {
		go func() {
			time.Sleep(50 * time.Millisecond)
			exit()
		}()
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	go func() {
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
		<-stop
		exit()
	}()
	fmt.Fprintln(os.Stderr, "fake crush: listening on", network, addr)
	if err := srv.Serve(ln); err != nil {
		fmt.Fprintln(os.Stderr, "fake crush:", err)
		os.Exit(1)
	}
}

// safeName matches the characters Crush replaces to name a server's cache
// directory (Crush 0.102.0, internal/cmd/root.go safeNameRegexp).
var safeName = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

// openLog opens the server log where Crush keeps it,
// $CRUSH_CACHE_DIR/server-<safe host>/crush.log, and holds it open as Crush
// does. It writes nothing unless a test sets CRUSH_CACHE_DIR, so the fake
// never touches the real cache.
func openLog(network, addr string) *os.File {
	cache := os.Getenv("CRUSH_CACHE_DIR")
	if cache == "" {
		return nil
	}
	dir := filepath.Join(cache, "server-"+safeName.ReplaceAllString(network+"://"+addr, "_"))
	if err := os.MkdirAll(dir, 0o700); err != nil { //nolint:gosec // test fake; the test chooses the directory
		fmt.Fprintln(os.Stderr, "fake crush:", err)
		os.Exit(1)
	}
	f, err := os.OpenFile(filepath.Join(dir, "crush.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // test fake; the test chooses the directory
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake crush:", err)
		os.Exit(1)
	}
	_, _ = fmt.Fprintln(f, `{"msg":"Starting Crush server..."}`)
	return f
}

// hostArg returns the listen address from `--host <url>` in args. Without
// one, the fake listens where Crush's default server does. Like Crush, it
// takes everything after a socket or pipe URL's "://" as the address, so a
// pipe may be written with backslashes.
func hostArg(args []string) (network, addr string) {
	for i, a := range args {
		if a == "--host" && i+1 < len(args) {
			scheme, rest, _ := strings.Cut(args[i+1], "://")
			switch scheme {
			case "unix", "npipe":
				return scheme, rest
			}
			u, err := url.Parse(args[i+1])
			if err != nil {
				break
			}
			return "tcp", u.Host
		}
	}
	scheme, rest, _ := strings.Cut(crushserver.SharedURL(crushserver.DefaultSocket()), "://")
	return scheme, rest
}
