package daemon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"runtime/debug"
	"sync"
	"time"

	"evilcode/internal/config"
	"evilcode/internal/provider"
	"evilcode/internal/session"
)

// The command surface (plan-web.md §4, Phase 3): the mutating half of the web
// API. Every handler is a thin adapter that calls the same Server/Session
// method the unix-socket protocol handler calls — no second semantics. The
// reads are the same shape the socket speaks: a session must be live to accept
// a command (a stored session is reopened by POST /api/sessions first), and
// daemon-side errors are user-correctable 4xx with the uniform error body.

// webOK is the response every successful command sends. The event that reports
// the effect arrives on the session's stream, so the body stays minimal.
var webOK = map[string]bool{"ok": true}

// webRecover maps a handler panic into the uniform error shape (§4). A panic
// is always a bug — a bare 500 per the plan — but the client must still
// receive one JSON error rather than a truncated connection.
func webRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("daemon: web handler panic on %s %s: %v\n%s",
					r.Method, r.URL.Path, rec, debug.Stack())
				webError(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// webMaxBodyBytes is the request-body cap: the unix socket's client frame
// limit, so the web surface can never accept more than the protocol paths
// already agree to carry. Base64 inflates images 4/3, which is why the client
// pre-checks ~6 MiB of raw attachments before sending (§8).
const webMaxBodyBytes = MaxClientFrameBytes

// webBodyReadTimeout bounds how long the server waits for one request body
// to arrive (EC-013). Only the read side is bounded: there is no
// WriteTimeout anywhere on this surface, and the SSE stream never sets a
// read deadline, so a client dribbling a POST body cannot tie up a handler
// goroutine while long-lived streams survive past it. Package var so tests
// can shrink it.
var webBodyReadTimeout = 30 * time.Second

// readWebBody decodes one JSON request body into dst. The body is capped at
// webMaxBodyBytes; anything over is 413 with the uniform shape. Malformed
// JSON, wrong types, or trailing garbage is 400 — the client sent something it
// must fix, so the message names the JSON failure.
func readWebBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	// A client that stalls mid-body must not hold the handler forever. The
	// deadline covers only the decode below and is cleared before returning,
	// so keep-alive reuse of the connection is unaffected. Writers without
	// deadline support (test recorders) report an error here that is safe to
	// ignore: the byte cap below still bounds the read.
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(webBodyReadTimeout))
	defer func() { _ = rc.SetReadDeadline(time.Time{}) }()
	r.Body = http.MaxBytesReader(w, r.Body, webMaxBodyBytes)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			webErrorf(w, http.StatusRequestEntityTooLarge,
				"request body exceeds the daemon's %d MiB limit", webMaxBodyBytes/(1<<20))
			return false
		}
		webErrorf(w, http.StatusBadRequest, "malformed JSON body: %v", err)
		return false
	}
	// One value per request: trailing content is a protocol error, not extra
	// input to ignore, because what would be ignored is otherwise undefined.
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		webErrorf(w, http.StatusBadRequest, "malformed JSON body: trailing data after the JSON value")
		return false
	}
	return true
}

// webLiveSession resolves {name} to a live session for a command handler.
// An invalid or unknown name is a 404 exactly as on the read paths, and so is
// a stored (not hydrated) session: commands go to a runtime, and booting an
// agent to receive one is POST /api/sessions' explicit job, never a side
// effect of addressing it.
func (s *Server) webLiveSession(w http.ResponseWriter, name string) *Session {
	if err := session.ValidName(name); err != nil {
		webErrorf(w, http.StatusNotFound, "no session named %q", name)
		return nil
	}
	sess := s.lookupLiveSession(name)
	if sess == nil {
		webErrorf(w, http.StatusNotFound, "no live session named %q (reopen it via POST /api/sessions first)", name)
		return nil
	}
	return sess
}

// webBusy reports whether a turn is in flight. Refusals that co-occur with a
// running turn are the plan's 409 (turn conflict); every other daemon error is
// a 400. Reading both indicators together mirrors Status()'s running count.
func (sess *Session) webBusy() bool {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.running || (sess.built != nil && sess.built.Agent.Running())
}

// webCommandError writes the mapped error for a daemon-side refusal. The busy
// re-check decides 409 vs 400: an operation that failed *because* a turn took
// the session is a conflict, anything else is a request the user can fix.
func webCommandError(w http.ResponseWriter, sess *Session, err error) {
	if sess.webBusy() {
		webErrorf(w, http.StatusConflict, "%v", err)
		return
	}
	webErrorf(w, http.StatusBadRequest, "%v", err)
}

// webInput is POST /api/sessions/{name}/input (§4): a prompt with optional
// base64 images, the render-only hidden marker, and an optional request id
// carried into TurnStart. It is the same call the socket's MsgInput makes.
func (s *Server) webInput(w http.ResponseWriter, r *http.Request) {
	sess := s.webLiveSession(w, r.PathValue("name"))
	if sess == nil {
		return
	}
	var req struct {
		Text      string   `json:"text"`
		Images    []string `json:"images"`
		Hidden    bool     `json:"hidden"`
		RequestID string   `json:"request_id"`
	}
	if !readWebBody(w, r, &req) {
		return
	}
	images := make([][]byte, 0, len(req.Images))
	for i, enc := range req.Images {
		raw, err := base64.StdEncoding.DecodeString(enc)
		if err != nil {
			// Tolerate the unpadded spelling some encoders emit.
			raw, err = base64.RawStdEncoding.DecodeString(enc)
		}
		if err != nil {
			webErrorf(w, http.StatusBadRequest, "images[%d] is not valid base64: %v", i, err)
			return
		}
		images = append(images, raw)
	}
	// EC-001/EC-005: whitespace-only text with no images is a no-op the
	// agent would drop. Refuse it here, before the queue or the turn takes
	// ownership of the decoded image bytes.
	if !hasInputContent(req.Text, images) {
		webErrorf(w, http.StatusBadRequest, "input needs text or at least one image")
		return
	}
	// EC-005: the ack goes out only after the queue or the turn owns the
	// prompt. A full queue is 429 (retry when the turn ends); a closing
	// session is 409 (the runtime is going away, not merely busy).
	status, err := sess.InputRequestHidden(req.RequestID, req.Text, req.Hidden, images)
	if err != nil {
		switch status {
		case inputQueueFull:
			webErrorf(w, http.StatusTooManyRequests, "%v", err)
		case inputClosing:
			webErrorf(w, http.StatusConflict, "%v", err)
		default:
			webCommandError(w, sess, err)
		}
		return
	}
	writeJSON(w, webOK)
}

// webInterrupt is POST /api/sessions/{name}/interrupt (§4). Empty text cancels
// the turn outright; text with urgent injects at the matching safe point. The
// phone-allowed destructive op is exactly this one (decision 10).
func (s *Server) webInterrupt(w http.ResponseWriter, r *http.Request) {
	sess := s.webLiveSession(w, r.PathValue("name"))
	if sess == nil {
		return
	}
	var req struct {
		Text   string `json:"text"`
		Urgent bool   `json:"urgent"`
	}
	if !readWebBody(w, r, &req) {
		return
	}
	sess.Interrupt(req.Text, req.Urgent)
	writeJSON(w, webOK)
}

// webAnswer is POST /api/sessions/{name}/answer (§4): labels for a pending ask
// from Snapshot.Pending. A stale or already-resolved request id is a 409 — the
// client's ask card is out of date and it should resync from a snapshot.
func (s *Server) webAnswer(w http.ResponseWriter, r *http.Request) {
	sess := s.webLiveSession(w, r.PathValue("name"))
	if sess == nil {
		return
	}
	if sess.asks == nil {
		webError(w, http.StatusInternalServerError, "the session has no ask broker")
		return
	}
	var req struct {
		RequestID string   `json:"request_id"`
		Answers   []string `json:"answers"`
	}
	if !readWebBody(w, r, &req) {
		return
	}
	if req.RequestID == "" {
		webErrorf(w, http.StatusBadRequest, "request_id is required")
		return
	}
	if err := sess.asks.Answer(req.RequestID, req.Answers); err != nil {
		webErrorf(w, http.StatusConflict, "%v", err)
		return
	}
	writeJSON(w, webOK)
}

// webModel is POST /api/sessions/{name}/model (§4): switch the live session's
// model, optionally carrying a reasoning effort in the same transition.
func (s *Server) webModel(w http.ResponseWriter, r *http.Request) {
	sess := s.webLiveSession(w, r.PathValue("name"))
	if sess == nil {
		return
	}
	var req struct {
		Model  string `json:"model"`
		Effort string `json:"effort"`
	}
	if !readWebBody(w, r, &req) {
		return
	}
	if req.Model == "" {
		webErrorf(w, http.StatusBadRequest, "model is required")
		return
	}
	if err := sess.SetModelWithEffort(req.Model, provider.ReasoningEffort(req.Effort)); err != nil {
		webCommandError(w, sess, err)
		return
	}
	writeJSON(w, webOK)
}

// webEffort is POST /api/sessions/{name}/effort (§4): the live reasoning
// control. Valid values come from Snapshot.ReasoningEfforts; the daemon stays
// the authority and re-validates.
func (s *Server) webEffort(w http.ResponseWriter, r *http.Request) {
	sess := s.webLiveSession(w, r.PathValue("name"))
	if sess == nil {
		return
	}
	var req struct {
		Effort string `json:"effort"`
	}
	if !readWebBody(w, r, &req) {
		return
	}
	if req.Effort == "" {
		webErrorf(w, http.StatusBadRequest, "effort is required")
		return
	}
	if err := sess.SetReasoningEffort(provider.ReasoningEffort(req.Effort)); err != nil {
		webCommandError(w, sess, err)
		return
	}
	writeJSON(w, webOK)
}

// webCommand is POST /api/sessions/{name}/command (§4): the named slash
// commands with their arg and secret fields. The secret (a credential) is
// passed to Session.Command and deliberately goes nowhere else — it is never
// logged and never echoed back.
func (s *Server) webCommand(w http.ResponseWriter, r *http.Request) {
	sess := s.webLiveSession(w, r.PathValue("name"))
	if sess == nil {
		return
	}
	var req struct {
		Text   string `json:"text"`
		Arg    string `json:"arg"`
		Secret string `json:"secret"`
	}
	if !readWebBody(w, r, &req) {
		return
	}
	if req.Text == "" {
		webErrorf(w, http.StatusBadRequest, "command text is required")
		return
	}
	// A client may send the composer's raw "/connect brave" spelling.
	kind := req.Text
	if len(kind) > 0 && kind[0] == '/' {
		kind = kind[1:]
	}
	if err := sess.Command(kind, req.Arg, req.Secret); err != nil {
		webCommandError(w, sess, err)
		return
	}
	writeJSON(w, webOK)
}

// webMessage is POST /api/sessions/{name}/message (§4): poke a busy agent via
// Server.deliver, which queues the text for the session's next safe point.
func (s *Server) webMessage(w http.ResponseWriter, r *http.Request) {
	sess := s.webLiveSession(w, r.PathValue("name"))
	if sess == nil {
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if !readWebBody(w, r, &req) {
		return
	}
	if req.Text == "" {
		webErrorf(w, http.StatusBadRequest, "message text is required")
		return
	}
	s.deliver(sess.Name, req.Text)
	writeJSON(w, webOK)
}

// webCreateSession is POST /api/sessions (§4): create a new session or reopen
// an existing (live or stored) one. The cwd comes from the `[webui] workspaces`
// allowlist — the daemon never browses directories on a browser's behalf
// (decision 6) — and an empty name lets the daemon's own collision-safe
// allocator pick one. The response is the session's roster row, the same shape
// GET /api/sessions answers with.
func (s *Server) webCreateSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		Cwd     string `json:"cwd"`
		Model   string `json:"model"`
		NoTools bool   `json:"no_tools"`
	}
	if !readWebBody(w, r, &req) {
		return
	}
	if req.Name != "" {
		if err := session.ValidName(req.Name); err != nil {
			webErrorf(w, http.StatusBadRequest, "%v", err)
			return
		}
	}
	if !s.webWorkspaceAllowed(req.Cwd) {
		webErrorf(w, http.StatusBadRequest, "workspace %q is not allowed by [webui] workspaces", req.Cwd)
		return
	}
	opened, err := s.OpenWithOptions(req.Name, OpenOptions{
		Cwd: req.Cwd, Model: req.Model, NoTools: req.NoTools,
	})
	if err != nil {
		webErrorf(w, http.StatusBadRequest, "%v", err)
		return
	}
	s.webSessionRow(w, opened.Name)
}

// webWorkspaceAllowed decides the new-session cwd menu server-side: a client
// cwd must be in the configured `[webui] workspaces` list, or be the daemon's
// own working directory when that list is empty. Paths are compared cleaned
// and absolute so "../proj" and "proj/" name the same allowed directory.
func (s *Server) webWorkspaceAllowed(cwd string) bool {
	if cwd == "" {
		return true // the daemon's own default fills it in
	}
	s.mu.Lock()
	cfg, daemonCwd := s.Cfg, s.Cwd
	s.mu.Unlock()
	allowed := []string{daemonCwd}
	if cfg != nil && len(cfg.WebUI.Workspaces) > 0 {
		allowed = cfg.WebUI.Workspaces
	}
	want, err := filepath.Abs(cwd)
	if err != nil {
		return false
	}
	for _, a := range allowed {
		if clean, err := filepath.Abs(a); err == nil && clean == want {
			return true
		}
	}
	return false
}

// webSessionRow answers with one roster row (or a 500 when the session it just
// reported is already gone — a Close race, not a client mistake).
func (s *Server) webSessionRow(w http.ResponseWriter, name string) {
	rows := s.sessionInfo(name)
	if len(rows) == 0 {
		webError(w, http.StatusInternalServerError, "the session is not in the roster")
		return
	}
	writeJSON(w, rows[0])
}

// webSpawn is POST /api/spawn (§4): start a worker attributed to the named
// spawner, so its result is reported back there. Mirrors the socket's MsgSpawn
// handler, including the start notice delivered to the spawner's session.
func (s *Server) webSpawn(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Session string          `json:"session"`
		Task    string          `json:"task"`
		Files   []string        `json:"files"`
		Schema  json.RawMessage `json:"schema"`
	}
	if !readWebBody(w, r, &req) {
		return
	}
	if req.Task == "" {
		webErrorf(w, http.StatusBadRequest, "task is required")
		return
	}
	sess := s.webLiveSession(w, req.Session)
	if sess == nil {
		return
	}
	name, err := s.SpawnFor(sess.Name, req.Task, req.Files, req.Schema)
	if err != nil {
		webErrorf(w, http.StatusBadRequest, "%v", err)
		return
	}
	s.deliver(sess.Name, fmt.Sprintf("👉 Worker %s started on: %s", name, req.Task))
	s.webSessionRow(w, name)
}

// webWorkspaces is GET /api/workspaces (§4): the new-session picker's menu —
// the daemon's own cwd plus whatever `[webui] workspaces` adds. The list is
// never a directory listing; it is exactly the configured entries.
func (s *Server) webWorkspaces(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	cfg, daemonCwd := s.Cfg, s.Cwd
	s.mu.Unlock()
	var spaces []string
	if cfg != nil {
		spaces = cfg.WebUI.Workspaces
	}
	if spaces == nil {
		spaces = []string{}
	}
	writeJSON(w, struct {
		Cwd        string   `json:"cwd"`
		Workspaces []string `json:"workspaces"`
	}{daemonCwd, spaces})
}

// The model catalogue (§4): every configured provider asked for its models,
// cached for five minutes so the picker does not hammer Ollama on every open.

// webModelsCacheTTL is how long /api/models trusts one aggregation. Package
// var so tests can shrink it.
var webModelsCacheTTL = 5 * time.Minute

// webModelsRequestTimeout bounds one provider's Models call, as the TUI's
// fetchAllModels does. An unreachable provider contributes no rows rather than
// a failure: a missing catalog is not an error.
const webModelsRequestTimeout = 5 * time.Second

// webModelEntry is one row of the catalogue for the picker (§8): identity,
// transport spelling, context window (config overrides applied), advertised
// reasoning efforts, and the user's pinned mark.
type webModelEntry struct {
	Name             string                     `json:"name"`
	Provider         string                     `json:"provider,omitempty"`
	Detail           string                     `json:"detail,omitempty"`
	Via              string                     `json:"via,omitempty"`
	ContextWindow    int                        `json:"context_window,omitempty"`
	ReasoningEfforts []provider.ReasoningEffort `json:"reasoning_efforts,omitempty"`
	Favorite         bool                       `json:"favorite,omitempty"`
}

// webModelsCache is one cached catalogue per Server. Guarded by its own mutex:
// the handler holds no other lock while it may wait on providers.
type webModelsCache struct {
	mu      sync.Mutex
	entries []webModelEntry
	fetched time.Time
	// inflight is non-nil while one fetch is running (EC-I-001): the first
	// miss becomes the leader and fetches, later misses wait on its
	// completion (or their request context) and then read the stored
	// result. No singleflight dependency — one channel under this mutex.
	inflight chan struct{}
}

// webAPIModels serves GET /api/models.
func (s *Server) webAPIModels(w http.ResponseWriter, r *http.Request) {
	entries, wait, leader := s.webModels.beginFetch()
	switch {
	case wait != nil:
		// A fetch is already running: wait for the leader or the client
		// going away, then read whatever it stored.
		select {
		case <-wait:
			entries, _ = s.webModels.cached()
			writeJSON(w, entries)
		case <-r.Context().Done():
		}
	case !leader:
		writeJSON(w, entries)
	default:
		// The leader fetches with no lock held and stores on all paths
		// (deferred, so even a panicking provider releases the waiters),
		// then answers from the same stored result the followers read.
		func() {
			defer func() { s.webModels.finishFetch(entries) }()
			entries = s.webFetchModels()
		}()
		writeJSON(w, entries)
	}
}

// beginFetch checks freshness and joins or starts a fetch under one lock, so
// a burst of concurrent misses produces exactly one upstream aggregation.
// It returns the fresh entries (leader=false, wait=nil), the in-flight
// channel to wait on (wait!=nil), or leadership (leader=true).
func (c *webModelsCache) beginFetch() (entries []webModelEntry, wait chan struct{}, leader bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries != nil && time.Since(c.fetched) < webModelsCacheTTL {
		return c.entries, nil, false
	}
	if c.inflight != nil {
		return nil, c.inflight, false
	}
	c.inflight = make(chan struct{})
	return nil, nil, true
}

// cached returns the entries while they are fresh.
func (c *webModelsCache) cached() ([]webModelEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil || time.Since(c.fetched) >= webModelsCacheTTL {
		return nil, false
	}
	return c.entries, true
}

// store replaces the cached entries.
func (c *webModelsCache) store(entries []webModelEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries, c.fetched = entries, time.Now()
}

// finishFetch stores the leader's result and wakes every waiter. It runs on
// all leader paths, so a closed channel always follows an in-flight one, and
// the mutex is held only for the swap — never across provider I/O.
func (c *webModelsCache) finishFetch(entries []webModelEntry) {
	c.mu.Lock()
	c.entries, c.fetched = entries, time.Now()
	ch := c.inflight
	c.inflight = nil
	c.mu.Unlock()
	if ch != nil {
		close(ch)
	}
}

// webFetchModels asks every configured provider concurrently. A provider that
// fails to build or answer contributes nothing, matching the TUI's
// fetchAllModels: an unreachable Ollama must not blank the picker.
func (s *Server) webFetchModels() []webModelEntry {
	s.mu.Lock()
	cfg := s.Cfg.Clone()
	s.mu.Unlock()
	if cfg == nil {
		return nil
	}

	type result struct {
		pc      config.ProviderConfig
		infos   []provider.ModelInfo
		built   provider.Provider
		buildOK bool
	}
	results := make(chan result, len(cfg.Providers))
	ctx, cancel := context.WithTimeout(context.Background(), webModelsRequestTimeout)
	defer cancel()
	for _, pc := range cfg.Providers {
		pc := pc
		go func() {
			p, err := pc.Build()
			if err != nil {
				results <- result{pc: pc}
				return
			}
			infos, _ := p.Models(ctx)
			results <- result{pc: pc, infos: infos, built: p, buildOK: true}
		}()
	}

	var out []webModelEntry
	for range cfg.Providers {
		res := <-results
		if !res.buildOK {
			continue
		}
		via := "local"
		if res.pc.Kind == config.KindCodex {
			via = "oauth"
		} else if res.pc.APIKeyEnv != "" {
			if res.pc.APIKeyValue() != "" {
				via = "api-key"
			} else {
				via = "no key"
			}
		}
		for _, info := range res.infos {
			e := webModelEntry{
				Name:             info.Name,
				Provider:         res.pc.Name,
				Detail:           info.Size,
				Via:              via,
				ContextWindow:    info.ContextWindow,
				ReasoningEfforts: provider.NormalizeReasoningEfforts(info.ReasoningEfforts),
			}
			ref := config.ModelRef(info.Name, res.pc.Name)
			// Config overrides win over what the provider claims (§4): Ollama
			// in particular under-reports a model's real window.
			if ov := cfg.ModelOverrides(ref); ov.ContextWindow > 0 {
				e.ContextWindow = ov.ContextWindow
			}
			for _, fav := range cfg.FavoriteModels {
				if fav == ref || fav == info.Name {
					e.Favorite = true
					break
				}
			}
			out = append(out, e)
		}
	}
	return out
}
