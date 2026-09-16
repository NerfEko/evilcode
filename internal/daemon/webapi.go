package daemon

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"evilcode/internal/agent"
	"evilcode/internal/config"
	"evilcode/internal/provider"
	"evilcode/internal/session"
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
		gw := &gzipResponseWriter{ResponseWriter: w, gz: gzip.NewWriter(w)}
		defer gw.close()
		next.ServeHTTP(gw, r)
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

// Flush and Unwrap keep streaming handlers working underneath this wrapper:
// the SSE route passes through uncompressed but still needs a working
// Flusher, and http.NewResponseController unwraps via Unwrap to reach the
// real writer's deadline support.
func (g *gzipResponseWriter) Flush() {
	if g.active {
		g.gz.Flush()
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap exposes the underlying writer for http.ResponseController.
func (g *gzipResponseWriter) Unwrap() http.ResponseWriter {
	return g.ResponseWriter
}

// close flushes the encoder when compression was active. A passthrough
// response was written directly; closing an unused encoder is a no-op anyway.
func (g *gzipResponseWriter) close() {
	if g.active {
		g.gz.Close()
	}
}

// webAPIMessages serves the deep-history pager (§6): `before` is an exclusive
// upper bound into the shaped conversation list (0-based, oldest = 0), so a
// client scrolling up passes the index of its oldest rendered message and gets
// the page directly above it. `limit` defaults to 50 and caps at
// webHistoryLimitMax. The live transcript's seam merges on these boundaries.
func (s *Server) webAPIMessages(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := session.ValidName(name); err != nil {
		webErrorf(w, http.StatusNotFound, "no session named %q", name)
		return
	}
	limit := intQuery(r, "limit", webHistoryLimitDefault)
	if limit > webHistoryLimitMax {
		limit = webHistoryLimitMax
	}
	if limit == 0 {
		limit = webHistoryLimitDefault
	}
	msgs, err := s.sessionLogMessages(name)
	if err != nil {
		if os.IsNotExist(err) {
			webErrorf(w, http.StatusNotFound, "no session named %q", name)
			return
		}
		// The log exists but cannot be parsed; that is mid-log corruption the
		// resume path would also hit. Name the failure, stay 5xx-free.
		webErrorf(w, http.StatusUnprocessableEntity, "session %q history is unreadable: %v", name, err)
		return
	}
	shaped := shapeConversationMessages(msgs)

	// Omitted before means the newest page. An explicit before is an exclusive
	// upper bound (§6): 0 names the oldest message, so before=0 legitimately
	// returns an empty page.
	before := len(shaped)
	if r.URL.Query().Has("before") {
		before = intQuery(r, "before", 0)
	}
	// Clamp rather than 404 so a client that raced a compaction gets the
	// newest page instead of nothing.
	if before > len(shaped) {
		before = len(shaped)
	}
	start := before - limit
	if start < 0 {
		start = 0
	}
	writeJSON(w, webHistoryPage{
		Messages: shaped[start:before],
		HasMore:  start > 0,
		Oldest:   start,
	})
}

// History page bounds (§6): the default page is what a screen of scrollback
// needs; the cap keeps one request from marshaling a whole multi-megabyte log.
const (
	webHistoryLimitDefault = 50
	webHistoryLimitMax     = 200
)

// webHistoryPage is one page of durable history.
type webHistoryPage struct {
	Messages []Message `json:"messages"`
	HasMore  bool      `json:"hasMore"`
	// Oldest is the shaped-list index of Messages[0]; the next request passes
	// it as before.
	Oldest int `json:"oldest"`
}

// The live-event stream (plan-web.md §5). One EventSource per open transcript:
// the connect sequence mirrors the unix protocol's MsgAttach — subscribe, send
// a snapshot (always; it is self-healing), replay the ring from the client's
// last seen sequence, then tail live frames. `id:` is the ring sequence, so a
// reconnecting browser carries Last-Event-ID automatically and the replay
// covers exactly the gap.

// Web SSE tuning. Package vars so tests can shrink the deadlines; production
// values are the plan's contract.
var (
	webSSEHeartbeat    = 15 * time.Second
	webSSEWriteTimeout = 10 * time.Second
)

// webEvents serves GET /api/sessions/{name}/events.
//
// Slow-client rule (§5): Session.broadcast never blocks the session's pump;
// on the first full send it evicts the subscription and closes its done
// signal (EC-003). This loop then exits, terminating the stream, and the
// client reconnects with its last sequence, which the ring replays. Each
// frame write also runs under a deadline: a client whose TCP buffer is full
// is disconnected rather than buffered without bound.
func (s *Server) webEvents(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := session.ValidName(name); err != nil {
		webErrorf(w, http.StatusNotFound, "no session named %q", name)
		return
	}
	sess := s.lookupLiveSession(name)
	if sess == nil {
		// A stored session has no live ring to tail; reopening it is the
		// Phase 3 command surface's job, not a silent empty stream.
		webErrorf(w, http.StatusNotFound, "no live session named %q (stored sessions stream nothing until reopened)", name)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		webError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	rc := http.NewResponseController(w)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Subscribe before the snapshot, exactly like MsgAttach: an event landing
	// mid-sequence queues instead of falling into the gap (EC-002). replay is
	// bounded to highWater; queued live MsgEvents at or below it duplicate
	// the replay and are skipped. Snapshots and non-event frames always pass.
	since := intQuery(r, "since", 0)
	if since == 0 {
		// The browser replays its last seen id automatically on reconnect.
		if last := r.Header.Get("Last-Event-ID"); last != "" {
			if n, err := strconv.Atoi(strings.TrimSpace(last)); err == nil && n > 0 {
				since = n
			}
		}
	}
	sub, replay, highWater := sess.attachSubscribe(since)
	defer sess.unsubscribe(sub)

	write := func(frame string) bool {
		rc.SetWriteDeadline(time.Now().Add(webSSEWriteTimeout))
		if _, err := io.WriteString(w, frame); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	sendSnapshot := func() bool {
		// EC-F-001: the initial snapshot goes through the same bounded
		// path as the socket attach (fitServerFrame), so a huge
		// conversation arrives truncated with Oldest set instead of as an
		// unbounded frame. The data line reuses the fitted bytes, so the
		// cap the fitter enforced is the cap the client reads.
		snap := sess.snapshot()
		fitted, buf, ok := fitServerFrame(ServerMsg{
			Version: ProtocolVersion, Kind: MsgSnapshot, Snapshot: snap,
		})
		if !ok || fitted.Snapshot == nil {
			return false
		}
		return write(sseDataFrame(fitted.Snapshot.Seq, "snapshot", buf))
	}
	if !sendSnapshot() {
		return
	}

	// Replay: an explicit since (query param or the browser's automatic
	// Last-Event-ID) gets exactly the gap; a fresh connect replays only the
	// turn in flight, matching MsgAttach so completed history is not drawn
	// twice on top of the snapshot. replay is already bounded to highWater by
	// attachSubscribe.
	for i := range replay {
		if !write(sseEvent(&replay[i])) {
			return
		}
	}

	heartbeat := time.NewTicker(webSSEHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case msg := <-sub.ch:
			if isLiveDuplicate(msg, highWater) {
				continue
			}
			switch {
			case msg.Kind == MsgEvent && msg.Event != nil:
				if !write(sseEvent(msg.Event)) {
					return
				}
			case msg.Kind == MsgSnapshot && msg.Snapshot != nil:
				// Compact, rewind, or rename republished durable state; the
				// client resets its mirror from this frame (§5, epoch rule).
				if !write(sseFrame(msg.Snapshot.Seq, "snapshot", msg)) {
					return
				}
			default:
				if !write(sseFrame(0, "message", msg)) {
					return
				}
			}
		case <-heartbeat.C:
			if !write(": ping\n\n") {
				return
			}
		case <-sub.done:
			// Overflow (EC-003): the queue lost at least one event, so the
			// stream has a gap only a reconnect replay can repair. Exit to
			// terminate the stream; the client reconnects with its last id.
			return
		case <-r.Context().Done():
			return
		}
	}
}

// sseEvent renders one agent event as an SSE frame carrying the daemon's
// standard ServerMsg JSON, with the ring sequence as the EventSource id.
func sseEvent(e *agent.Event) string {
	return sseFrame(e.Seq, "event", ServerMsg{
		Version: ProtocolVersion, Kind: MsgEvent, Event: e,
	})
}

// sseFrame renders one SSE frame: id for reconnect replay, event for the
// client's dispatch, data as one line of JSON.
func sseFrame(id int, kind string, msg ServerMsg) string {
	b, err := json.Marshal(msg)
	if err != nil {
		// A ServerMsg always marshals; if one ever does not, send the failure
		// as an error frame rather than killing the stream silently.
		b = []byte(fmt.Sprintf(`{"version":%d,"kind":"error","error":%q}`,
			ProtocolVersion, err.Error()))
	}
	return sseDataFrame(id, kind, b)
}

// sseDataFrame renders one SSE frame around already-encoded JSON, for callers
// that fitted the payload first and must send exactly those bytes.
func sseDataFrame(id int, kind string, data []byte) string {
	return fmt.Sprintf("id: %d\nevent: %s\ndata: %s\n\n", id, kind, data)
}

// webErrorf is webError with formatting: uniform body, caller picks the code.
func webErrorf(w http.ResponseWriter, code int, format string, args ...any) {
	webError(w, code, fmt.Sprintf(format, args...))
}

// webAPISession serves one session (§4): a live one renders exactly what a
// just-attached TUI gets — Session.snapshot() — while a stored one is metadata
// plus durable history, marked read-only. A stored session is never booted
// here: viewing it must not build an agent, load tools, or spend a model call.
func (s *Server) webAPISession(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := session.ValidName(name); err != nil {
		// The name is client input, but an invalid one can never name a
		// session, so it is a 404 rather than a 400: there is nothing to
		// correct, the resource does not exist.
		webErrorf(w, http.StatusNotFound, "no session named %q", name)
		return
	}
	if sess := s.lookupLiveSession(name); sess != nil {
		writeJSON(w, sess.snapshot())
		return
	}
	view, err := s.storedSessionView(name)
	if err != nil {
		webErrorf(w, http.StatusNotFound, "no session named %q", name)
		return
	}
	writeJSON(w, view)
}

// lookupLiveSession returns the hydrated runtime for name, or nil.
func (s *Server) lookupLiveSession(name string) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[name]
}

// webStoredHistoryCap bounds the history embedded in a stored session view.
// The full log stays reachable through the /messages pager; a stored session
// viewed read-only needs enough to render, not necessarily all of it.
const webStoredHistoryCap = 200

// StoredSession is the read-only view of a session nobody has hydrated.
type StoredSession struct {
	Session SessionInfo `json:"session"`
	// Messages is the durable conversation, newest window capped at
	// webStoredHistoryCap; Truncated says older pages exist behind
	// /api/sessions/{name}/messages.
	Messages  []Message `json:"messages,omitempty"`
	Truncated bool      `json:"truncated,omitempty"`
}

// storedSessionView reads a stored session's metadata and history straight
// from the durable log. No runtime is built, no store is opened for append,
// and no session lock is held across the read (§6).
func (s *Server) storedSessionView(name string) (*StoredSession, error) {
	info, err := session.Describe(config.DataDir(), name)
	if err != nil {
		return nil, err
	}
	msgs, err := s.sessionLogMessages(name)
	if err != nil {
		return nil, err
	}
	shaped := shapeConversationMessages(msgs)
	truncated := false
	if len(shaped) > webStoredHistoryCap {
		shaped = shaped[len(shaped)-webStoredHistoryCap:]
		truncated = true
	}
	return &StoredSession{
		Session: SessionInfo{
			Name:     info.Name,
			Model:    info.Model,
			Cwd:      info.Cwd,
			Title:    info.Title,
			Modified: info.Modified,
			Crashed:  info.Crashed,
			Stored:   true,
			Live:     false,
			Messages: info.Messages,
		},
		Messages:  shaped,
		Truncated: truncated,
	}, nil
}

// loadSessionMessages is the durable-log loader seam. Production always
// uses the resume-path loader; tests substitute a flaky first read to drive
// the torn-read retry deterministically.
var loadSessionMessages = session.Messages

// sessionLogMessages loads a session's durable conversation through the
// resume-path loader — the same session.Messages call a resume uses, so the
// log's only parser stays in internal/session. The one retry covers reads that
// race a compact/rewind (the log is rewritten by rename, so a reader can catch
// the file mid-swap or briefly missing) and a torn line an append just
// landed (§6). Never call this while holding sess.mu.
func (s *Server) sessionLogMessages(name string) ([]provider.Message, error) {
	// ValidName has already run in every caller; the join mirrors pathFor.
	path := filepath.Join(session.Dir(config.DataDir()), name+".jsonl")
	msgs, err := loadSessionMessages(path)
	if err != nil && !os.IsNotExist(err) {
		time.Sleep(webTornReadRetryDelay)
		msgs, err = loadSessionMessages(path)
	}
	if err != nil {
		return nil, err
	}
	return msgs, nil
}

// webTornReadRetryDelay gives a concurrent compact-or-append a moment to
// finish before the one retry. Small enough to keep a 404 snappy, long enough
// to clear the narrow rename window.
const webTornReadRetryDelay = 25 * time.Millisecond

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
