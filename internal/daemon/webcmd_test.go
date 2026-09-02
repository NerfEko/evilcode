package daemon

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"evilcode/internal/agent"
	"evilcode/internal/config"
	"evilcode/internal/provider"
	"evilcode/internal/session"
)

// The command-surface battery (plan-web.md §4, Phase 3). Every test drives a
// real Server through the HTTP surface with the same bearer+origin discipline
// a browser page is held to, and asserts the daemon-side effect the unix
// protocol would have produced — the web layer is an adapter, not new
// semantics.

// webScenarioServer is webTestServer with a chosen mock scenario, for tests
// that need a turn shape other than "chat" (an ask that blocks mid-turn).
func webScenarioServer(t *testing.T, scenario string) (*Server, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("HOME", home)
	t.Setenv("EVILCODE_PROVIDER", "mock")
	t.Setenv(provider.ScenarioEnv, scenario)
	t.Setenv("EVILCODE_CONFIG", filepath.Join(home, "nonexistent.toml"))

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "evild")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	srv := NewServer(cfg, t.TempDir(), "")
	srv.Path = filepath.Join(dir, "s.sock")
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	if err := srv.ListenWeb("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	return srv, srv.webAddr()
}

// webPostJSON POSTs a JSON body with the bearer token and the same-origin
// Origin header every mutating web request must carry (§3).
func webPostJSON(t *testing.T, srv *Server, addr, path string, body any) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return webPostRaw(t, srv, addr, path, raw)
}

// webPostRaw POSTs pre-encoded bytes, for batteries that send deliberately
// malformed bodies.
func webPostRaw(t *testing.T, srv *Server, addr, path string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://"+addr+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+webTokenOf(t, srv))
	req.Header.Set("Content-Type", "application/json")
	// The Origin check is mandatory on POSTs; a test that omitted it would
	// exercise 403s instead of the handlers.
	req.Header.Set("Origin", "http://"+addr)
	resp, err := webClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// webBody reads a response body as a string, transparently decoding gzip.
func webBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	r := io.Reader(resp.Body)
	if resp.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(resp.Body)
		if err != nil {
			t.Fatalf("body is not gzip: %v", err)
		}
		r = zr
	}
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return string(b)
}

// webErrorOf asserts a uniform error body and returns its message.
func webErrorOf(t *testing.T, resp *http.Response, wantCode int, path string) string {
	t.Helper()
	if resp.StatusCode != wantCode {
		t.Fatalf("POST %s: status %d, want %d (body %q)", path, resp.StatusCode, wantCode, webBody(t, resp))
	}
	var got struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(strings.NewReader(webBody(t, resp))).Decode(&got); err != nil || got.Error == "" {
		t.Fatalf("POST %s: body is not the uniform error shape: %q (%v)", path, webBody(t, resp), err)
	}
	return got.Error
}

// webOKOf asserts a 200 {"ok":true} answer.
func webOKOf(t *testing.T, resp *http.Response, path string) {
	t.Helper()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s: status %d, want 200 (body %q)", path, resp.StatusCode, webBody(t, resp))
	}
	var got map[string]bool
	if err := json.NewDecoder(strings.NewReader(webBody(t, resp))).Decode(&got); err != nil || !got["ok"] {
		t.Fatalf("POST %s: body %q is not {\"ok\":true} (%v)", path, webBody(t, resp), err)
	}
}

// webRowOf asserts a 200 answer that is one SessionInfo roster row.
func webRowOf(t *testing.T, resp *http.Response, path string) SessionInfo {
	t.Helper()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s: status %d, want 200 (body %q)", path, resp.StatusCode, webBody(t, resp))
	}
	var row SessionInfo
	if err := json.NewDecoder(strings.NewReader(webBody(t, resp))).Decode(&row); err != nil {
		t.Fatalf("POST %s: body is not a SessionInfo row: %v", path, err)
	}
	return row
}

// eventTap follows one session's live event stream from the subscription
// point, so a test can observe what a browser's EventSource would receive.
type eventTap struct {
	t    *testing.T
	sess *Session
	sub  chan ServerMsg
}

func tapEvents(t *testing.T, sess *Session) *eventTap {
	t.Helper()
	tap := &eventTap{t: t, sess: sess, sub: sess.subscribe()}
	t.Cleanup(func() { sess.unsubscribe(tap.sub) })
	return tap
}

// next returns the next event matching want, failing the test after a timeout.
func (tap *eventTap) next(want func(*agent.Event) bool, desc string) *agent.Event {
	tap.t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case msg := <-tap.sub:
			if msg.Kind == MsgEvent && msg.Event != nil && want(msg.Event) {
				return msg.Event
			}
		case <-deadline:
			tap.t.Fatalf("timed out waiting for %s", desc)
		}
	}
}

// waitIdle polls until the session's turn is over.
func waitIdle(t *testing.T, sess *Session, desc string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !sess.webBusy() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", desc)
}

// lastUserTurnContaining reports whether the durable conversation gained a
// user-role message whose content contains want.
func durableUserSaid(t *testing.T, srv *Server, name, want string) bool {
	t.Helper()
	msgs, err := srv.sessionLogMessages(name)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if m.Role == provider.RoleUser && strings.Contains(m.Content, want) {
			return true
		}
	}
	return false
}

// --- P3.1 input -------------------------------------------------------------

func TestWebInputStartsTurnWithImages(t *testing.T) {
	srv, addr := webTestServer(t)
	sess, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	tap := tapEvents(t, sess)

	// An image-bearing turn: base64 in, raw bytes in the durable conversation.
	png := []byte("\x89PNG fake image bytes for the web test")
	resp := webPostJSON(t, srv, addr, "/api/sessions/"+sess.Name+"/input", map[string]any{
		"text":       "what is in this picture?",
		"images":     []string{base64.StdEncoding.EncodeToString(png)},
		"request_id": "web-img-1",
	})
	webOKOf(t, resp, "/input")

	ts := tap.next(func(e *agent.Event) bool { return e.Kind == agent.EventTurnStart }, "turn_start")
	if ts.RequestID != "web-img-1" {
		t.Errorf("turn_start request_id = %q, want the web client's", ts.RequestID)
	}
	waitIdle(t, sess, "the image turn to end")

	msgs, err := srv.sessionLogMessages(sess.Name)
	if err != nil {
		t.Fatal(err)
	}
	var user *provider.Message
	for i := range msgs {
		if msgs[i].Role == provider.RoleUser && msgs[i].Content == "what is in this picture?" {
			user = &msgs[i]
		}
	}
	if user == nil {
		t.Fatalf("durable log has no user turn for the image input: %+v", msgs)
	}
	if len(user.Images) != 1 || !bytes.Equal(user.Images[0], png) {
		t.Errorf("durable image = %v, want the decoded %d bytes", user.Images, len(png))
	}

	// A hidden turn reaches the provider but not the attached windows.
	resp = webPostJSON(t, srv, addr, "/api/sessions/"+sess.Name+"/input", map[string]any{
		"text": "silent harness question", "hidden": true, "request_id": "web-hide-1",
	})
	webOKOf(t, resp, "/input")
	hs := tap.next(func(e *agent.Event) bool { return e.Kind == agent.EventTurnStart && e.Hidden }, "hidden turn_start")
	if hs.Text != "" || hs.RequestID != "web-hide-1" {
		t.Errorf("hidden turn_start = text %q id %q, want a blank render with the request id", hs.Text, hs.RequestID)
	}
	waitIdle(t, sess, "the hidden turn to end")

	// Validation: nothing to send, or un-decodable base64, is a 400.
	resp = webPostJSON(t, srv, addr, "/api/sessions/"+sess.Name+"/input", map[string]any{})
	webErrorOf(t, resp, http.StatusBadRequest, "/input")
	resp = webPostJSON(t, srv, addr, "/api/sessions/"+sess.Name+"/input", map[string]any{
		"text": "look", "images": []string{"not base64 !!!"},
	})
	webErrorOf(t, resp, http.StatusBadRequest, "/input")

	// A stored session is not a command target: reopen first.
	name := storedSessionFixture(t, "quiet", 1)
	resp = webPostJSON(t, srv, addr, "/api/sessions/"+name+"/input", map[string]any{"text": "hi"})
	webErrorOf(t, resp, http.StatusNotFound, "/input")
}

// --- P3.2 interrupt + answer ------------------------------------------------

func TestWebInterruptCancelsATurn(t *testing.T) {
	srv, addr := webScenarioServer(t, "ask")
	sess, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	tap := tapEvents(t, sess)

	resp := webPostJSON(t, srv, addr, "/api/sessions/"+sess.Name+"/input", map[string]any{"text": "One decision first."})
	webOKOf(t, resp, "/input")
	tap.next(func(e *agent.Event) bool { return e.Kind == agent.EventAsk }, "the pending ask")

	// A text interrupt is an interject, not a cancel: the ask still stands and
	// the turn keeps waiting for its answer.
	resp = webPostJSON(t, srv, addr, "/api/sessions/"+sess.Name+"/interrupt", map[string]any{
		"text": "actually, decide yourself", "urgent": true,
	})
	webOKOf(t, resp, "/interrupt")

	// Empty text cancels the turn outright (Session.Interrupt's contract).
	resp = webPostJSON(t, srv, addr, "/api/sessions/"+sess.Name+"/interrupt", map[string]any{})
	webOKOf(t, resp, "/interrupt")
	end := tap.next(func(e *agent.Event) bool { return e.Kind == agent.EventTurnEnd }, "turn_end")
	if end.Reason != agent.EndInterrupted {
		t.Errorf("turn_end reason = %q, want interrupted", end.Reason)
	}
	waitIdle(t, sess, "the cancelled turn to settle")

	resp = webPostJSON(t, srv, addr, "/api/sessions/does-not-exist/interrupt", map[string]any{})
	webErrorOf(t, resp, http.StatusNotFound, "/interrupt")
}

func TestWebAnswerUnblocksAnAsk(t *testing.T) {
	srv, addr := webScenarioServer(t, "ask")
	sess, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	tap := tapEvents(t, sess)

	resp := webPostJSON(t, srv, addr, "/api/sessions/"+sess.Name+"/input", map[string]any{"text": "One decision first."})
	webOKOf(t, resp, "/input")
	ask := tap.next(func(e *agent.Event) bool { return e.Kind == agent.EventAsk }, "the pending ask")
	if ask.Ask == nil || ask.Ask.ID == "" {
		t.Fatalf("ask event carries no id: %+v", ask)
	}

	resp = webPostJSON(t, srv, addr, "/api/sessions/"+sess.Name+"/answer", map[string]any{
		"request_id": ask.Ask.ID, "answers": []string{"Exponential backoff"},
	})
	webOKOf(t, resp, "/answer")
	end := tap.next(func(e *agent.Event) bool { return e.Kind == agent.EventTurnEnd }, "turn_end")
	if end.Reason != agent.EndComplete {
		t.Errorf("turn_end reason = %q, want complete after the answer", end.Reason)
	}
	waitIdle(t, sess, "the answered turn to finish")

	msgs, err := srv.sessionLogMessages(sess.Name)
	if err != nil {
		t.Fatal(err)
	}
	last := msgs[len(msgs)-1]
	if last.Role != provider.RoleAssistant || !strings.Contains(last.Content, "Exponential it is.") {
		t.Errorf("final assistant message = %q, want the post-answer turn", last.Content)
	}

	// The request id is spent: answering it again is a conflict, not a 500.
	resp = webPostJSON(t, srv, addr, "/api/sessions/"+sess.Name+"/answer", map[string]any{
		"request_id": ask.Ask.ID, "answers": []string{"Exponential backoff"},
	})
	webErrorOf(t, resp, http.StatusConflict, "/answer")
}

// --- P3.3 model + effort ----------------------------------------------------

func TestWebModelAndEffort(t *testing.T) {
	srv, addr := webTestServer(t)
	// A provider whose effort control is wire-real, added before the session
	// is built so the session's config clone carries it.
	srv.mu.Lock()
	srv.Cfg.Providers = append(srv.Cfg.Providers, config.ProviderConfig{
		Name: "ds", Kind: config.KindDeepSeek, BaseURL: "https://api.deepseek.com",
	})
	srv.mu.Unlock()
	sess, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}

	resp := webPostJSON(t, srv, addr, "/api/sessions/"+sess.Name+"/model", map[string]any{"model": "mock-small@mock"})
	webOKOf(t, resp, "/model")
	if got := sess.snapshot().Model; got != "mock-small" {
		t.Errorf("snapshot model = %q, want mock-small", got)
	}

	resp = webPostJSON(t, srv, addr, "/api/sessions/"+sess.Name+"/model", map[string]any{"model": "nope@nothere"})
	webErrorOf(t, resp, http.StatusBadRequest, "/model")

	// The mock has no effort control; the daemon stays the authority.
	resp = webPostJSON(t, srv, addr, "/api/sessions/"+sess.Name+"/model", map[string]any{
		"model": "mock-small@mock", "effort": "high",
	})
	webErrorOf(t, resp, http.StatusBadRequest, "/model")
	resp = webPostJSON(t, srv, addr, "/api/sessions/"+sess.Name+"/effort", map[string]any{"effort": "high"})
	webErrorOf(t, resp, http.StatusBadRequest, "/effort")

	// Switch to a provider that supports efforts, then set one.
	resp = webPostJSON(t, srv, addr, "/api/sessions/"+sess.Name+"/model", map[string]any{"model": "deepseek-chat@ds"})
	webOKOf(t, resp, "/model")
	resp = webPostJSON(t, srv, addr, "/api/sessions/"+sess.Name+"/effort", map[string]any{"effort": "high"})
	webOKOf(t, resp, "/effort")
	snap := sess.snapshot()
	if snap.ReasoningEffort != "high" {
		t.Errorf("snapshot reasoning effort = %q, want high", snap.ReasoningEffort)
	}
	if snap.Provider != "ds" || snap.Model != "deepseek-chat" {
		t.Errorf("snapshot model/provider = %s@%s, want deepseek-chat@ds", snap.Model, snap.Provider)
	}

	resp = webPostJSON(t, srv, addr, "/api/sessions/"+sess.Name+"/model", map[string]any{})
	webErrorOf(t, resp, http.StatusBadRequest, "/model")
}

// --- P3.4 command -----------------------------------------------------------

func TestWebCommandAndSecretIsNeverLogged(t *testing.T) {
	srv, addr := webTestServer(t)
	sess, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}

	// /save pins the session durably — a command with visible effect.
	resp := webPostJSON(t, srv, addr, "/api/sessions/"+sess.Name+"/command", map[string]any{"text": "/save"})
	webOKOf(t, resp, "/command")
	info, err := session.Describe(config.DataDir(), sess.Name)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Saved {
		t.Error("/save over HTTP did not pin the session")
	}

	// A secret-bearing command: the daemon refuses it here (no such provider
	// to credential), after the secret has passed through the handler. Nothing
	// — daemon log or response — may echo the secret back.
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	secret := "sk-hunter2-do-not-log"
	resp = webPostJSON(t, srv, addr, "/api/sessions/"+sess.Name+"/command", map[string]any{
		"text": "credential", "arg": "no-such-provider", "secret": secret,
	})
	msg := webErrorOf(t, resp, http.StatusBadRequest, "/command")
	if strings.Contains(msg, secret) || strings.Contains(logs.String(), secret) {
		t.Errorf("the secret leaked: response %q logs %q", msg, logs.String())
	}

	resp = webPostJSON(t, srv, addr, "/api/sessions/"+sess.Name+"/command", map[string]any{"text": "definitely-not-a-command"})
	if msg := webErrorOf(t, resp, http.StatusBadRequest, "/command"); !strings.Contains(msg, "unknown server command") {
		t.Errorf("unknown command error = %q", msg)
	}
	resp = webPostJSON(t, srv, addr, "/api/sessions/"+sess.Name+"/command", map[string]any{"text": ""})
	webErrorOf(t, resp, http.StatusBadRequest, "/command")
}

// --- P3.5 message -----------------------------------------------------------

func TestWebMessageLandsAtNextSafePoint(t *testing.T) {
	srv, addr := webTestServer(t)
	sess, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}

	// The session is idle, so the poke queues for the next turn's safe point.
	resp := webPostJSON(t, srv, addr, "/api/sessions/"+sess.Name+"/message", map[string]any{
		"text": "psst, the build went red while you were away",
	})
	webOKOf(t, resp, "/message")
	resp = webPostJSON(t, srv, addr, "/api/sessions/"+sess.Name+"/input", map[string]any{"text": "status?"})
	webOKOf(t, resp, "/input")
	waitIdle(t, sess, "the turn that carries the interjection")

	if !durableUserSaid(t, srv, sess.Name, "the build went red") {
		msgs, _ := srv.sessionLogMessages(sess.Name)
		t.Errorf("the delivered message never reached the conversation: %+v", msgs)
	}

	resp = webPostJSON(t, srv, addr, "/api/sessions/"+sess.Name+"/message", map[string]any{"text": ""})
	webErrorOf(t, resp, http.StatusBadRequest, "/message")
	resp = webPostJSON(t, srv, addr, "/api/sessions/nobody/message", map[string]any{"text": "hi"})
	webErrorOf(t, resp, http.StatusNotFound, "/message")
}

// --- P3.6 create/reopen -----------------------------------------------------

func TestWebCreateSessionWorkspaceAllowlist(t *testing.T) {
	srv, addr := webTestServer(t)

	// No name: the daemon's allocator proposes one; the row is live immediately.
	row := webRowOf(t, webPostJSON(t, srv, addr, "/api/sessions", map[string]any{}), "/api/sessions")
	if row.Name == "" || !row.Live || !row.Stored {
		t.Fatalf("created row = %+v, want a live roster row with a generated name", row)
	}
	if row.Cwd != srv.Cwd {
		t.Errorf("created cwd = %q, want the daemon's %q", row.Cwd, srv.Cwd)
	}

	// An existing live session reopens (same row, no duplicate).
	if got := webRowOf(t, webPostJSON(t, srv, addr, "/api/sessions", map[string]any{"name": row.Name}), "/api/sessions"); got.Name != row.Name {
		t.Fatalf("live reopen = %+v, want %q", got, row.Name)
	}

	// A stored session reopens live without a new log.
	storedSessionFixture(t, "replay", 2)
	if got := webRowOf(t, webPostJSON(t, srv, addr, "/api/sessions", map[string]any{"name": "replay"}), "/api/sessions"); !got.Live || got.Name != "replay" {
		t.Fatalf("stored reopen = %+v, want the stored session hydrated", got)
	}

	// A name that names nothing cannot be conjured: the daemon's own attach
	// path refuses it the same way (only the empty name creates).
	if msg := webErrorOf(t, webPostJSON(t, srv, addr, "/api/sessions", map[string]any{"name": "no-such"}), http.StatusBadRequest, "/api/sessions"); !strings.Contains(msg, "no session named") {
		t.Errorf("unknown-name create error = %q", msg)
	}

	// The workspace allowlist: an empty list means the daemon cwd only.
	if msg := webErrorOf(t, webPostJSON(t, srv, addr, "/api/sessions", map[string]any{"name": "elsewhere", "cwd": "/opt/nowhere"}), http.StatusBadRequest, "/api/sessions"); !strings.Contains(msg, "not allowed") {
		t.Errorf("rejected cwd error = %q", msg)
	}
	workspace := t.TempDir()
	srv.mu.Lock()
	srv.Cfg.WebUI.Workspaces = []string{workspace}
	srv.mu.Unlock()
	// The same allowed directory, spelled with a redundant "..", is accepted —
	// and the new session is created in it (the empty name generates one).
	spell := filepath.Join(workspace, "..", filepath.Base(workspace))
	if got := webRowOf(t, webPostJSON(t, srv, addr, "/api/sessions", map[string]any{"cwd": spell}), "/api/sessions"); got.Cwd != filepath.Clean(workspace) {
		t.Errorf("allowed cwd resolved to %q, want %q", got.Cwd, filepath.Clean(workspace))
	}

	// A name that can never be a session is a request error.
	webErrorOf(t, webPostJSON(t, srv, addr, "/api/sessions", map[string]any{"name": "../evil"}), http.StatusBadRequest, "/api/sessions")
}

func TestWebWorkspacesEndpoint(t *testing.T) {
	srv, addr := webTestServer(t)
	resp := authedWebGet(t, srv, addr, "/api/workspaces")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/workspaces: status %d", resp.StatusCode)
	}
	var got struct {
		Cwd        string   `json:"cwd"`
		Workspaces []string `json:"workspaces"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Cwd != srv.Cwd || len(got.Workspaces) != 0 {
		t.Errorf("workspaces payload = %+v, want the daemon cwd and an empty (non-null) list", got)
	}
}

// --- P3.7 spawn ---------------------------------------------------------------

func TestWebSpawnAttributesToTheSpawner(t *testing.T) {
	srv, addr := webTestServer(t)
	spawner, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}

	resp := webPostJSON(t, srv, addr, "/api/spawn", map[string]any{
		"session": spawner.Name, "task": "count the creatures", "files": []string{"internal/daemon/server.go"},
	})
	row := webRowOf(t, resp, "/api/spawn")
	if !row.Worker || row.Task != "count the creatures" || row.Name == "" {
		t.Fatalf("spawn row = %+v, want a named worker carrying the task", row)
	}

	// The start notice is delivered to the spawner's session, so it lands in
	// its conversation at the next safe point — attribution made visible.
	resp = webPostJSON(t, srv, addr, "/api/sessions/"+spawner.Name+"/input", map[string]any{"text": "anything?"})
	webOKOf(t, resp, "/input")
	waitIdle(t, spawner, "the spawner's turn")
	if !durableUserSaid(t, srv, spawner.Name, "👉 Worker "+row.Name+" started on: count the creatures") {
		t.Errorf("the start notice never reached the spawner %q", spawner.Name)
	}

	// Schema passthrough: a schema that cannot compile is refused up front,
	// before any worker is built.
	resp = webPostJSON(t, srv, addr, "/api/spawn", map[string]any{
		"session": spawner.Name, "task": "x", "schema": map[string]any{"type": 42},
	})
	webErrorOf(t, resp, http.StatusBadRequest, "/api/spawn")

	webErrorOf(t, webPostJSON(t, srv, addr, "/api/spawn", map[string]any{"session": spawner.Name, "task": ""}), http.StatusBadRequest, "/api/spawn")
	webErrorOf(t, webPostJSON(t, srv, addr, "/api/spawn", map[string]any{"session": "nobody", "task": "x"}), http.StatusNotFound, "/api/spawn")
}

// --- P3.8 models --------------------------------------------------------------

func TestWebModelsCatalogCacheOverridesFavorites(t *testing.T) {
	srv, addr := webTestServer(t)
	// Isolate the catalog to the mock provider, so the test never waits on a
	// real Ollama endpoint.
	srv.mu.Lock()
	srv.Cfg.Providers = []config.ProviderConfig{{Name: "mock", Kind: config.KindMock}}
	srv.mu.Unlock()
	oldTTL := webModelsCacheTTL
	webModelsCacheTTL = time.Hour
	t.Cleanup(func() { webModelsCacheTTL = oldTTL })

	resp := authedWebGet(t, srv, addr, "/api/models")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/models: status %d", resp.StatusCode)
	}
	var entries []webModelEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		t.Fatal(err)
	}
	byName := map[string]webModelEntry{}
	for _, e := range entries {
		byName[e.Name] = e
	}
	if len(byName) != 2 || byName["mock-small"].ContextWindow != 8192 || byName["mock-large"].ContextWindow != 200000 {
		t.Fatalf("catalog = %+v, want the Mock.Models values", entries)
	}
	if byName["mock-small"].Provider != "mock" || byName["mock-small"].Via != "local" {
		t.Errorf("mock entry transport = %+v, want provider mock via local", byName["mock-small"])
	}

	// Within the TTL the cache answers: overrides and favorites changed
	// server-side stay invisible until it expires.
	srv.mu.Lock()
	srv.Cfg.Models = []config.ModelConfig{{Name: "mock-large", ContextWindow: 4096}}
	srv.Cfg.FavoriteModels = []string{"mock-small"}
	srv.mu.Unlock()
	resp = authedWebGet(t, srv, addr, "/api/models")
	var cached []webModelEntry
	if err := json.NewDecoder(resp.Body).Decode(&cached); err != nil {
		t.Fatal(err)
	}
	for _, e := range cached {
		if e.Favorite || e.ContextWindow != 200000 && e.Name == "mock-large" {
			t.Errorf("cache did not hold: %+v", cached)
		}
	}

	srv.mu.Lock()
	srv.webModels.fetched = time.Now().Add(-2 * webModelsCacheTTL)
	srv.mu.Unlock()
	resp = authedWebGet(t, srv, addr, "/api/models")
	var fresh []webModelEntry
	if err := json.NewDecoder(resp.Body).Decode(&fresh); err != nil {
		t.Fatal(err)
	}
	byName = map[string]webModelEntry{}
	for _, e := range fresh {
		byName[e.Name] = e
	}
	if byName["mock-large"].ContextWindow != 4096 {
		t.Errorf("config override missing after expiry: %+v", byName["mock-large"])
	}
	if !byName["mock-small"].Favorite {
		t.Errorf("favorite missing after expiry: %+v", byName["mock-small"])
	}
}

// --- P3.9 malformed-body battery ----------------------------------------------

func TestWebMalformedBodyBattery(t *testing.T) {
	srv, addr := webTestServer(t)
	sess, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	inputPath := "/api/sessions/" + sess.Name + "/input"

	t.Run("bad json and wrong types are 400 with the uniform shape", func(t *testing.T) {
		for _, tt := range []struct {
			path string
			body string
		}{
			{inputPath, `{"text": `},
			{inputPath, `{"text": 42}`},
			{inputPath, `{"text": "a", "hidden": "yes"}`},
			{inputPath, `{"text": "a"} {"text": "b"}`},
			{"/api/sessions/" + sess.Name + "/answer", `[]`},
			{"/api/sessions", `{"name": 7}`},
			{"/api/spawn", `"just a string"`},
		} {
			resp := webPostRaw(t, srv, addr, tt.path, []byte(tt.body))
			webErrorOf(t, resp, http.StatusBadRequest, tt.path)
		}
	})

	t.Run("over the body cap is 413", func(t *testing.T) {
		// The cap trips while the decoder is still consuming a syntactically
		// valid value — the same shape a huge base64 image arrives in.
		big := []byte(`{"text": "` + strings.Repeat("a", webMaxBodyBytes+1024) + `"}`)
		webErrorOf(t, webPostRaw(t, srv, addr, inputPath, big), http.StatusRequestEntityTooLarge, inputPath)
		// A small body rides the same reader without the size refusal.
		resp := webPostRaw(t, srv, addr, inputPath, []byte(`{"text": "`+strings.Repeat("a", 4096)+`"}`))
		if resp.StatusCode == http.StatusRequestEntityTooLarge {
			t.Errorf("a 4 KiB body was rejected as oversized")
		}
	})

	t.Run("path tricks never reach a handler as a usable name", func(t *testing.T) {
		for _, path := range []string{
			"/api/sessions/a%2Fb/input", // %2F stays inside the segment; ValidName refuses
			"/api/sessions/..%2Fescape/input",
			"/api/sessions/%2E%2E/input",
		} {
			resp := webPostRaw(t, srv, addr, path, []byte(`{"text": "hi"}`))
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("POST %s: status %d, want 404", path, resp.StatusCode)
				continue
			}
			webErrorOf(t, resp, http.StatusNotFound, path)
		}
	})

	t.Run("method mismatches answer the uniform shape", func(t *testing.T) {
		// A wrong method on a known route falls through to the /api/ fallback:
		// one failure format, never the mux's plain-text 405. The request needs
		// the Origin header because POST is a mutating verb.
		resp := webPostRaw(t, srv, addr, "/api/status", []byte(`{}`))
		webErrorOf(t, resp, http.StatusNotFound, "POST /api/status")
		resp = webPostRaw(t, srv, addr, "/api/sessions/"+sess.Name+"/interrupt%2F", []byte(`{}`))
		webErrorOf(t, resp, http.StatusNotFound, "POST .../interrupt%2F")
	})

	t.Run("a handler panic is the uniform error shape", func(t *testing.T) {
		var logs bytes.Buffer
		log.SetOutput(&logs)
		t.Cleanup(func() { log.SetOutput(os.Stderr) })
		boom := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			panic("boom")
		})
		rec := httptest.NewRecorder()
		webRecover(boom).ServeHTTP(rec, mustRequest(t, http.MethodGet, "/x"))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("panic status = %d, want 500", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"error"`) {
			t.Errorf("panic body %q is not the uniform error shape", rec.Body.String())
		}
		if !strings.Contains(logs.String(), "boom") {
			t.Errorf("the panic was not logged for the daemon side: %q", logs.String())
		}
	})
}

func mustRequest(t *testing.T, method, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

// --- Phase 3 verify: socket client (the TUI's path) and the browser share one
// session's turn stream. Two inputs to one session must serialize into one
// turn at a time — the daemon's queue — never two concurrent turns.

func TestWebAndSocketClientsShareOneTurnStream(t *testing.T) {
	srv, path := testServer(t)
	if err := srv.ListenWeb("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	addr := srv.webAddr()

	client, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	snap, err := client.Attach("", 0)
	if err != nil {
		t.Fatal(err)
	}
	name := snap.Session

	// The browser sits on the stream from before any turn: the snapshot plus
	// live tail carries one turn_start per input, no double-start, identical
	// to what the socket client saw live.
	stream := openSSE(t, srv, addr, name, "", "")
	defer stream.resp.Body.Close()
	if _, kind, _ := stream.next(); kind != "snapshot" {
		t.Fatalf("first SSE frame was %q, want snapshot", kind)
	}

	// Both consumers send at once: the first turn starts, the second queues.
	if err := client.Send(ClientMsg{Kind: MsgInput, Text: "from the socket"}); err != nil {
		t.Fatal(err)
	}
	resp := webPostJSON(t, srv, addr, "/api/sessions/"+name+"/input", map[string]any{"text": "from the web"})
	webOKOf(t, resp, "/input")

	sess := srv.lookupLiveSession(name)
	if sess == nil {
		t.Fatal("the socket-attached session is not live")
	}
	waitIdle(t, sess, "both turns to complete serially")

	msgs, err := srv.sessionLogMessages(name)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, m := range msgs {
		if m.Role == provider.RoleUser &&
			(m.Content == "from the socket" || m.Content == "from the web") {
			order = append(order, m.Content)
		}
	}
	if len(order) != 2 {
		t.Fatalf("durable turns = %v, want both inputs in one conversation", order)
	}
	if order[0] != "from the socket" {
		t.Errorf("turn order = %v, want the socket's input to win the first turn", order)
	}

	// Count turn_starts across the tail.
	seen := map[string]int{}
	for frames := 0; len(seen) < 2 && frames < 64; frames++ {
		_, kind, data := stream.next()
		if kind != "event" {
			continue
		}
		var msg ServerMsg
		if err := json.Unmarshal([]byte(data), &msg); err != nil {
			t.Fatalf("frame %q: %v", data, err)
		}
		if msg.Event != nil && msg.Event.Kind == agent.EventTurnStart {
			seen[msg.Event.Text]++
		}
	}
	if seen["from the socket"] != 1 || seen["from the web"] != 1 {
		t.Errorf("turn_starts on the stream = %v, want one per input, no double-start", seen)
	}
}