package daemon

import (
	"compress/gzip"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// The read-only API surface (plan-web.md §4): handlers call the same code the
// unix protocol handlers call — Server.Status, Server.Sessions, Session.snapshot
// — so the browser sees exactly what a TUI window sees. Nothing here mutates
// session state; the command surface is Phase 3.

// writeJSON answers with the daemon's JSON wire shape. Errors use webError, so
// a 200 body is always the payload and never an error envelope.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Headers are already sent; the truncated body is the only honest
		// signal left. This is a client-gone condition in practice.
	}
}

// webAPIStatus serves the same payload `serve -status` prints (§4): the header
// widget's pid/sessions/clients/running/web line.
func (s *Server) webAPIStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.Status())
}

// webAPISessions serves the roster (§4): hydrated sessions plus durable ones
// the daemon is not holding, exactly what `list` answers with over the socket.
// The roster polls this endpoint; it never streams (§5, the iOS 6-connection
// cap makes one EventSource per roster unaffordable).
func (s *Server) webAPISessions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.Sessions())
}

// gzipJSON compresses JSON GET responses (§3). It activates only when the
// handler answered application/json and the client offered gzip; SSE and every
// other content type pass through untouched, so streams are never buffered
// inside an encoder.
func gzipJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet ||
			!strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		gz := gzip.NewWriter(w)
		defer gz.Close()
		gw := &gzipResponseWriter{ResponseWriter: w, gz: gz}
		next.ServeHTTP(gw, r)
		gw.close()
	})
}

// gzipResponseWriter decides at first write whether to compress: by then the
// handler has set its Content-Type. A handler that never writes (or answered
// something other than JSON) streams straight through.
type gzipResponseWriter struct {
	http.ResponseWriter
	gz     *gzip.Writer
	active bool
	done   bool
}

func (g *gzipResponseWriter) Write(b []byte) (int, error) {
	if !g.done {
		g.done = true
		// First write: the handler has set its Content-Type by now. Every
		// handler in this surface sets it before writing — the contract that
		// makes the decision here reliable.
		if strings.Contains(g.Header().Get("Content-Type"), "application/json") {
			g.Header().Set("Content-Encoding", "gzip")
			g.Header().Del("Content-Length")
			g.active = true
		}
	}
	if g.active {
		return g.gz.Write(b)
	}
	return g.ResponseWriter.Write(b)
}

// close flushes the encoder when compression was active. A passthrough
// response was written directly; closing an unused encoder is a no-op anyway.
func (g *gzipResponseWriter) close() {
	if g.active {
		g.gz.Close()
	}
}

// intQuery parses a non-negative integer query parameter, returning def when
// absent, malformed, or negative. Malformed input degrades to the default
// rather than a 400: a pager offset is not worth an error dialog.
func intQuery(r *http.Request, key string, def int) int {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return def
	}
	return n
}
