package daemon

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"evilcode/internal/config"
)

// The web surface is the daemon's opt-in HTTP listener beside its unix socket
// (plan-web.md §2). It is an adapter over the same session surface the unix
// protocol handlers call: no new state ownership, no second semantics. The
// listener is independent of the socket — either failing to bind must not take
// the other down — and it is closed by Server.Close.
//
// Every handler here must obey two rules the socket path enforces by
// construction: never hold s.mu or sess.mu across client I/O, and never trust
// a request that did not come through webauth (plan-web.md §3).

// DefaultWebReadHeaderTimeout bounds how long a client may take to send its
// request headers. A browser is fast; a hostile peer should not be able to tie
// up a goroutine by dribbling bytes.
const webReadHeaderTimeout = 10 * time.Second

// webState is one live HTTP listener. Guarded by Server.mu.
type webState struct {
	ln  net.Listener
	srv *http.Server
	// addr is the resolved host:port actually bound — what startup prints and
	// what the Host allowlist is built from. It is a copy, not the listener,
	// so Close can still be called after the listener is gone.
	addr string
}

// errServerClosed is returned by ListenWeb when the daemon is already tearing
// down: the caller must not interpret it as a bind failure.
var errServerClosed = errors.New("daemon: the server is shutting down")

// ListenWeb starts the HTTP surface on addr (host:port; empty uses the
// configured default). It returns an error if the bind fails; the unix socket
// keeps working either way. Starting twice is refused — the second caller gets
// the first listener's address in the error.
func (s *Server) ListenWeb(addr string) error {
	if addr == "" {
		addr = config.DefaultWebUIAddr
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("daemon: web UI cannot bind %s: %w", addr, err)
	}
	// ":0" and hostless forms resolve to something concrete; report and
	// enforce that form, not the requested one.
	bound := ln.Addr().String()

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		ln.Close()
		return errServerClosed
	}
	if s.web != nil {
		old := s.web.addr
		s.mu.Unlock()
		ln.Close()
		return fmt.Errorf("daemon: the web UI is already listening on %s", old)
	}
	w := &webState{ln: ln, addr: bound}
	s.web = w
	s.mu.Unlock()

	srv := &http.Server{
		Handler:           s.webMux(),
		ReadHeaderTimeout: webReadHeaderTimeout,
	}
	w.srv = srv
	go func() {
		// Serve returns ErrServerClosed on Close; anything else means the
		// listener died underneath us, which the Close path already handles.
		_ = srv.Serve(ln)
	}()
	return nil
}

// webMux builds the HTTP surface's routes. Handlers are added as the phases
// land; webauth.go wraps the whole thing once auth exists, so an
// unauthenticated request reaches nothing.
func (s *Server) webMux() http.Handler {
	mux := http.NewServeMux()
	return mux
}

// webAddr reports the bound host:port, or "" when the web surface is off.
// This is what `-status` and the startup line print.
func (s *Server) webAddr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.web == nil {
		return ""
	}
	return s.web.addr
}
