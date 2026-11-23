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
	"syscall"
	"time"
)

// EnvMode selects how a re-executed test binary behaves as `crush server`:
// "serve" (default when set), "exit" (fail immediately) or "silent" (run but
// never listen).
const EnvMode = "CRUSH_FAKE_SERVER"

// Handler returns an http.Handler for the fake server. onOpen, when non-nil,
// is called with the path of each workspace the client opens.
func Handler(onOpen func(path string)) http.Handler {
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
		if onOpen != nil {
			onOpen(body.Path)
		}
		_, _ = w.Write([]byte(`{"id":"ws-1"}`))
	})
	mux.HandleFunc("GET /v1/workspaces/ws-1/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	mux.HandleFunc("POST /v1/workspaces/ws-1/sessions", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"sess-1"}`))
	})
	mux.HandleFunc("POST /v1/workspaces/ws-1/current-session", func(http.ResponseWriter, *http.Request) {})
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

	addr := hostArg(os.Args[1:])
	srv := &http.Server{Addr: addr, Handler: Handler(nil), ReadHeaderTimeout: time.Second}
	go func() {
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
		<-stop
		os.Exit(0)
	}()
	fmt.Fprintln(os.Stderr, "fake crush: listening on", addr)
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, "fake crush:", err)
		os.Exit(1)
	}
}

// hostArg returns the host:port from `--host <url>` in args.
func hostArg(args []string) string {
	for i, a := range args {
		if a == "--host" && i+1 < len(args) {
			if u, err := url.Parse(args[i+1]); err == nil {
				return u.Host
			}
		}
	}
	return "127.0.0.1:0"
}
