// Package lsp is a minimal Language Server Protocol client (plan.md §17).
//
// It speaks just enough of the protocol for the operations the `lsp` tool
// exposes — diagnostics, definition, references, hover, rename, symbols — and
// nothing else. A full client is a large surface, and every part of it that is
// not driving a tool call is a part that can only break.
package lsp

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// RequestTimeout bounds a single request. A language server that has stopped
// answering must not take the turn with it: gopls in particular goes quiet
// while it indexes a large module, and an unbounded wait there looks exactly
// like a hang.
const RequestTimeout = 30 * time.Second

const startupRetryCooldown = 30 * time.Second

// InitTimeout is the longer bound on the handshake, which includes the server's
// first index of the workspace.
const InitTimeout = 90 * time.Second

// Client is a running language server.
type Client struct {
	Name string
	Root string

	cmd  *exec.Cmd
	in   io.WriteCloser
	out  *bufio.Reader
	stop context.CancelFunc

	mu      sync.Mutex
	nextID  int
	pending map[int]chan json.RawMessage
	writeMu sync.Mutex

	// diagnostics accumulate from the server's unsolicited notifications, which
	// is the only way they ever arrive: there is no "give me the diagnostics"
	// request in the protocol.
	diagMu sync.Mutex
	diags  map[string][]Diagnostic

	// opened tracks the version, digest, and end position the server last saw for each file.
	// Tools can edit a file behind the server's back; comparing a digest on every
	// Open lets the next operation send a full didChange instead of asking for
	// symbols from stale text.
	docOnce sync.Once
	docLock chan struct{}
	opened  map[string]openedDocument

	// syncKind is the server's negotiated textDocumentSync change mode. When
	// syncKnown is false the response omitted it, so full changes are the
	// conservative fallback; a known zero means no changes, with syncOpenClose
	// preserving the separate didOpen/didClose capability.
	syncKind      int
	syncKnown     bool
	syncOptions   bool
	syncOpenClose bool

	closeOnce sync.Once
	err       error
}

type openedDocument struct {
	version int
	digest  [sha256.Size]byte
	end     Position
}

const incrementalDocumentSync = 2

func documentSyncDetails(raw json.RawMessage) (kind int, known bool, openClose bool) {
	var result struct {
		Capabilities struct {
			TextDocumentSync json.RawMessage `json:"textDocumentSync"`
		} `json:"capabilities"`
	}
	if json.Unmarshal(raw, &result) != nil || len(result.Capabilities.TextDocumentSync) == 0 {
		return 0, false, false
	}
	if json.Unmarshal(result.Capabilities.TextDocumentSync, &kind) == nil {
		return kind, true, false
	}
	var details struct {
		Change    *int `json:"change"`
		OpenClose bool `json:"openClose"`
	}
	if json.Unmarshal(result.Capabilities.TextDocumentSync, &details) == nil {
		if details.Change == nil {
			return 0, true, details.OpenClose
		}
		return *details.Change, true, details.OpenClose
	}
	return 0, false, false
}

// documentSyncOptions reports whether the server used the structured
// TextDocumentSyncOptions form. The legacy numeric form has no openClose
// member; retain the historical didOpen behavior for it, while respecting an
// explicit openClose:false in the structured form.
func documentSyncOptions(raw json.RawMessage) bool {
	var result struct {
		Capabilities struct {
			TextDocumentSync json.RawMessage `json:"textDocumentSync"`
		} `json:"capabilities"`
	}
	if json.Unmarshal(raw, &result) != nil || len(result.Capabilities.TextDocumentSync) == 0 {
		return false
	}
	var kind int
	if json.Unmarshal(result.Capabilities.TextDocumentSync, &kind) == nil {
		return false
	}
	var details map[string]json.RawMessage
	return json.Unmarshal(result.Capabilities.TextDocumentSync, &details) == nil && details != nil
}

func documentSyncCapability(raw json.RawMessage) (kind int, known bool) {
	kind, known, _ = documentSyncDetails(raw)
	return kind, known
}

func documentSyncKind(raw json.RawMessage) int {
	kind, _ := documentSyncCapability(raw)
	return kind
}

func documentEnd(text string) Position {
	lines := strings.Split(text, "\n")
	last := lines[len(lines)-1]
	characters := 0
	for _, r := range last {
		if r > 0xFFFF {
			characters += 2
		} else {
			characters++
		}
	}
	return Position{Line: len(lines) - 1, Character: characters}
}

func fullDocumentRange(end Position) Range {
	return Range{
		Start: Position{},
		End:   end,
	}
}

// Diagnostic is one problem the server reported.
type Diagnostic struct {
	Range    Range  `json:"range"`
	Severity int    `json:"severity"`
	Code     any    `json:"code"`
	Source   string `json:"source"`
	Message  string `json:"message"`
}

// SeverityName renders the numeric severity the protocol uses.
func (d Diagnostic) SeverityName() string {
	switch d.Severity {
	case 1:
		return "error"
	case 2:
		return "warning"
	case 3:
		return "info"
	case 4:
		return "hint"
	}
	return "diagnostic"
}

// Position is a zero-based line and UTF-16 character offset.
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Range is a span.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Location is a range in a file.
type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

// Path is the filesystem path a location refers to.
func (l Location) Path() string { return PathFromURI(l.URI) }

// TextEdit is one replacement inside a file.
type TextEdit struct {
	Range   Range  `json:"range"`
	NewText string `json:"newText"`
}

// WorkspaceEdit is a rename's full set of changes.
type WorkspaceEdit struct {
	Changes map[string][]TextEdit `json:"changes"`

	// DocumentChanges is the newer form; servers pick one or the other, so both
	// are read and normalized by Edits.
	DocumentChanges []struct {
		TextDocument struct {
			URI string `json:"uri"`
		} `json:"textDocument"`
		Edits []TextEdit `json:"edits"`
	} `json:"documentChanges"`
}

// Edits normalizes both encodings into one map of path → edits.
func (w WorkspaceEdit) Edits() map[string][]TextEdit {
	out := map[string][]TextEdit{}
	for uri, edits := range w.Changes {
		out[PathFromURI(uri)] = append(out[PathFromURI(uri)], edits...)
	}
	for _, dc := range w.DocumentChanges {
		p := PathFromURI(dc.TextDocument.URI)
		out[p] = append(out[p], dc.Edits...)
	}
	return out
}

// URIFromPath encodes a path as a file URI.
func URIFromPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	return "file://" + (&url.URL{Path: abs}).EscapedPath()
}

// PathFromURI decodes a file URI back to a path.
func PathFromURI(uri string) string {
	trimmed := strings.TrimPrefix(uri, "file://")
	if unescaped, err := url.PathUnescape(trimmed); err == nil {
		return unescaped
	}
	return trimmed
}

// Start launches a server and completes the initialize handshake.
func Start(ctx context.Context, name, root string, command []string) (*Client, error) {
	if len(command) == 0 {
		return nil, fmt.Errorf("no command configured for %s", name)
	}
	if _, err := exec.LookPath(command[0]); err != nil {
		return nil, fmt.Errorf("%s is not installed", command[0])
	}

	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	cmd := exec.CommandContext(runCtx, command[0], command[1:]...)
	cmd.Dir = root
	cmd.Stderr = io.Discard

	in, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}

	c := &Client{
		Name: name, Root: root,
		cmd: cmd, in: in, out: bufio.NewReader(out), stop: cancel,
		pending: map[int]chan json.RawMessage{},
		diags:   map[string][]Diagnostic{},
		opened:  map[string]openedDocument{},
	}
	go c.readLoop()

	initCtx, initCancel := context.WithTimeout(ctx, InitTimeout)
	defer initCancel()
	if err := c.initialize(initCtx); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func (c *Client) initialize(ctx context.Context) error {
	params := map[string]any{
		"processId": os.Getpid(),
		"rootUri":   URIFromPath(c.Root),
		"capabilities": map[string]any{
			"textDocument": map[string]any{
				"synchronization": map[string]any{"didSave": true},
				"publishDiagnostics": map[string]any{
					"relatedInformation": false,
				},
				"hover":          map[string]any{"contentFormat": []string{"plaintext", "markdown"}},
				"definition":     map[string]any{},
				"references":     map[string]any{},
				"rename":         map[string]any{"prepareSupport": false},
				"documentSymbol": map[string]any{"hierarchicalDocumentSymbolSupport": true},
			},
			"workspace": map[string]any{
				"workspaceEdit": map[string]any{"documentChanges": true},
				"symbol":        map[string]any{},
			},
		},
	}
	result, err := c.call(ctx, "initialize", params)
	if err != nil {
		return err
	}
	c.syncKind, c.syncKnown, c.syncOpenClose = documentSyncDetails(result)
	c.syncOptions = documentSyncOptions(result)
	return c.notify("initialized", map[string]any{})
}

// readLoop demultiplexes responses and notifications.
func (c *Client) readLoop() {
	for {
		body, err := c.readMessage()
		if err != nil {
			c.mu.Lock()
			c.err = err
			for id, ch := range c.pending {
				close(ch)
				delete(c.pending, id)
			}
			c.mu.Unlock()
			return
		}

		var env struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(body, &env) != nil {
			continue
		}

		switch {
		case env.ID != nil && env.Method == "":
			// A response to something we asked.
			payload := env.Result
			if env.Error != nil {
				payload, _ = json.Marshal(map[string]string{"__error": env.Error.Message})
			}
			c.mu.Lock()
			ch := c.pending[*env.ID]
			delete(c.pending, *env.ID)
			c.mu.Unlock()
			if ch != nil {
				ch <- payload
				close(ch)
			}

		case env.Method == "textDocument/publishDiagnostics":
			var note struct {
				URI         string       `json:"uri"`
				Diagnostics []Diagnostic `json:"diagnostics"`
			}
			if json.Unmarshal(env.Params, &note) == nil {
				c.diagMu.Lock()
				c.diags[PathFromURI(note.URI)] = note.Diagnostics
				c.diagMu.Unlock()
			}

		case env.ID != nil:
			// A request from the server. Everything it can ask for is optional
			// to support, and answering null is a valid "no" — better than
			// leaving it waiting, which wedges gopls.
			c.respondNull(*env.ID)
		}
	}
}

// MaxFrameBytes bounds one protocol message.
//
// The header is a claim, not a fact: the allocation happens before any of the
// body has arrived, so a broken server — or a proxy that mangled a frame — can
// name a size larger than memory and take the process down without sending
// anything. Generous enough for a real response; gopls' largest are workspace
// symbol dumps in the low megabytes.
const MaxFrameBytes = 64 << 20

func (c *Client) readMessage() ([]byte, error) {
	length := 0
	sawLength := false
	for {
		line, err := c.out.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if name, value, ok := strings.Cut(line, ":"); ok &&
			strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			if _, err := fmt.Sscanf(strings.TrimSpace(value), "%d", &length); err == nil {
				sawLength = true
			}
		}
	}
	if !sawLength || length == 0 {
		return nil, fmt.Errorf("lsp: message with no Content-Length")
	}
	if length < 0 {
		return nil, fmt.Errorf("lsp: message with a negative Content-Length (%d)", length)
	}
	if length > MaxFrameBytes {
		return nil, fmt.Errorf("lsp: message of %d bytes is too large (limit %d)",
			length, MaxFrameBytes)
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(c.out, body); err != nil {
		return nil, err
	}
	return body, nil
}

func (c *Client) write(payload any) error {
	return c.writeContext(context.Background(), payload)
}

// writeContext serializes protocol writes and gives callers with a bounded
// request a way out if a server stops consuming stdin. Closing the pipe on a
// timed-out write wakes the blocked writer and leaves this client unusable,
// which is the only safe state after its protocol stream has been abandoned.
func (c *Client) writeContext(ctx context.Context, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		c.writeMu.Lock()
		defer c.writeMu.Unlock()
		// A caller can time out while waiting for an earlier protocol write to
		// release the serializer. Do not let this queued payload leak onto the
		// replacement stream after the caller has already abandoned it.
		if err := ctx.Err(); err != nil {
			done <- err
			return
		}
		_, writeErr := fmt.Fprintf(c.in, "Content-Length: %d\r\n\r\n%s", len(body), body)
		done <- writeErr
	}()
	select {
	case err := <-done:
		if err != nil {
			c.markUnusable(err)
		}
		return err
	case <-ctx.Done():
		err := ctx.Err()
		c.markUnusable(err)
		return err
	}
}

// markUnusable records that the protocol stream can no longer be trusted and
// closes its input. A timed-out write may have been only partly delivered; the
// next request must start a fresh client rather than reuse a desynchronized
// stream.
func (c *Client) markUnusable(err error) {
	if err == nil {
		err = errors.New("lsp client protocol stream is unusable")
	}
	c.mu.Lock()
	if c.err == nil {
		c.err = err
	}
	c.mu.Unlock()
	if c.in != nil {
		_ = c.in.Close()
	}
}

func (c *Client) unusable() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err != nil
}

func (c *Client) notify(method string, params any) error {
	return c.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *Client) notifyContext(ctx context.Context, method string, params any) error {
	return c.writeContext(ctx, map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *Client) respondNull(id int) {
	_ = c.write(map[string]any{"jsonrpc": "2.0", "id": id, "result": nil})
}

// call sends a request and waits for its response.
func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	if c.err != nil {
		err := c.err
		c.mu.Unlock()
		return nil, fmt.Errorf("%s stopped: %w", c.Name, err)
	}
	c.nextID++
	id := c.nextID
	ch := make(chan json.RawMessage, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	if err := c.writeContext(ctx, map[string]any{
		"jsonrpc": "2.0", "id": id, "method": method, "params": params,
	}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}

	select {
	case payload, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("%s stopped before answering %s", c.Name, method)
		}
		var maybeErr struct {
			Err string `json:"__error"`
		}
		if json.Unmarshal(payload, &maybeErr) == nil && maybeErr.Err != "" {
			return nil, fmt.Errorf("%s: %s", method, maybeErr.Err)
		}
		return payload, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("%s did not answer %s in time", c.Name, method)
	}
}

// Open tells the server about a file, which every other operation needs first.
func (c *Client) Open(path string) error {
	return c.OpenContext(context.Background(), path)
}

// OpenContext is Open with a cancellation boundary around both the file
// snapshot and the notification. Symbol enrichment uses this because a
// language server is optional and must not turn grep into an unbounded wait.
func (c *Client) OpenContext(ctx context.Context, path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// Serialize document notifications. A channel gate, rather than a plain
	// mutex, lets a bounded symbol request stop waiting behind a legacy caller
	// whose file read or notification is stuck.
	if err := c.lockDocuments(ctx); err != nil {
		return err
	}
	defer c.unlockDocuments()
	if c.opened == nil {
		c.opened = make(map[string]openedDocument)
	}
	previous, already := c.opened[abs]
	data, err := readFileContext(ctx, abs)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	if already && previous.digest == digest {
		return nil
	}
	if already {
		// Diagnostics are published asynchronously and keyed only by URI. Once
		// the file changed, an entry from the previous snapshot must not satisfy
		// Diagnostics before the server has reported the new one.
		c.diagMu.Lock()
		delete(c.diags, abs)
		c.diagMu.Unlock()
	}
	end := documentEnd(string(data))
	if c.syncKnown && c.syncKind == 0 && !c.syncOpenClose {
		// Explicit TextDocumentSyncKind.None: this server reads the workspace
		// itself and does not want didOpen/didChange notifications.
		version := previous.version
		if version == 0 {
			version = 1
		}
		c.opened[abs] = openedDocument{version: version, digest: digest, end: end}
		return nil
	}
	if already && c.syncKnown && c.syncKind == 0 {
		// Open/close-only servers still receive didOpen, but do not accept a
		// change notification. Keep the local snapshot fresh for the next
		// operation without inventing a didChange they did not negotiate.
		version := previous.version
		if version == 0 {
			version = 1
		}
		c.opened[abs] = openedDocument{version: version, digest: digest, end: end}
		return nil
	}
	if already && !c.syncKnown {
		// A server that omitted textDocumentSync has not opted into change
		// notifications. Keep the snapshot current for local bookkeeping, but do
		// not send a didChange it may reject or ignore.
		c.opened[abs] = openedDocument{version: previous.version, digest: digest, end: end}
		return nil
	}
	if !already && c.syncOptions && !c.syncOpenClose {
		// Structured capabilities explicitly disable open/close notifications.
		// The first snapshot is still remembered so a later edit can be sent as
		// didChange according to the negotiated change kind.
		c.opened[abs] = openedDocument{version: 1, digest: digest, end: end}
		return nil
	}
	version := 1
	method := "textDocument/didOpen"
	params := map[string]any{
		"textDocument": map[string]any{
			"uri":        URIFromPath(abs),
			"languageId": LanguageID(abs),
			"version":    version,
			"text":       string(data),
		},
	}
	if already {
		version = previous.version + 1
		method = "textDocument/didChange"
		params = map[string]any{
			"textDocument": map[string]any{
				"uri":     URIFromPath(abs),
				"version": version,
			},
			"contentChanges": []map[string]any{{
				"text": string(data),
				// Incremental servers require a range. Replacing the complete
				// previous document is still one bounded change and avoids a
				// second parser or a diff implementation here.
				"range": fullDocumentRange(previous.end),
			}},
		}
		if c.syncKind != incrementalDocumentSync {
			delete(params["contentChanges"].([]map[string]any)[0], "range")
		}
	}
	if err := c.notifyContext(ctx, method, params); err != nil {
		return err
	}
	c.opened[abs] = openedDocument{version: version, digest: digest, end: end}
	return nil
}

func (c *Client) lockDocuments(ctx context.Context) error {
	c.docOnce.Do(func() {
		c.docLock = make(chan struct{}, 1)
		c.docLock <- struct{}{}
	})
	select {
	case <-c.docLock:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) unlockDocuments() {
	c.docLock <- struct{}{}
}

// readFileContext closes the file if its caller gives up while a filesystem
// read is in flight. The result channel is buffered so the reader goroutine can
// finish without leaking after cancellation.
func readFileContext(ctx context.Context, path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		data, readErr := io.ReadAll(f)
		_ = f.Close()
		done <- result{data: data, err: readErr}
	}()
	select {
	case result := <-done:
		return result.data, result.err
	case <-ctx.Done():
		_ = f.Close()
		return nil, ctx.Err()
	}
}

// Forget drops a file's opened state, so the next operation re-sends it. A
// rename rewrites files on disk; without this the server keeps answering from
// the text it was given before the edit.
func (c *Client) Forget(path string) {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	if err := c.lockDocuments(context.Background()); err != nil {
		return
	}
	delete(c.opened, abs)
	c.unlockDocuments()
}

// LanguageID guesses the protocol's language identifier from an extension.
func LanguageID(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".ts", ".tsx":
		return "typescript"
	case ".js", ".jsx":
		return "javascript"
	case ".py":
		return "python"
	case ".rs":
		return "rust"
	case ".c", ".h":
		return "c"
	case ".cc", ".cpp", ".hpp":
		return "cpp"
	default:
		return "plaintext"
	}
}

// Close shuts the server down.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		if c.cmd == nil {
			if c.stop != nil {
				c.stop()
			}
			return
		}
		// Best-effort politeness, then the hammer. A server that ignores
		// shutdown must not keep the process alive.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, _ = c.call(ctx, "shutdown", nil)
		_ = c.notify("exit", nil)
		cancel()

		c.in.Close()
		c.stop()
		_ = c.cmd.Wait()
	})
	return nil
}

// Diagnostics returns what the server has reported for a file.
//
// Diagnostics arrive unsolicited and asynchronously, so this waits briefly for
// the first batch after a file is opened rather than reporting an empty list
// that only means "not yet".
func (c *Client) Diagnostics(ctx context.Context, path string) []Diagnostic {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		c.diagMu.Lock()
		diags, ok := c.diags[abs]
		c.diagMu.Unlock()
		if ok || time.Now().After(deadline) {
			return diags
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// AllDiagnostics returns everything reported so far, across files.
func (c *Client) AllDiagnostics() map[string][]Diagnostic {
	c.diagMu.Lock()
	defer c.diagMu.Unlock()
	out := make(map[string][]Diagnostic, len(c.diags))
	for path, diags := range c.diags {
		if len(diags) > 0 {
			out[path] = diags
		}
	}
	return out
}

// Manager owns one server per language, started on first use.
//
// Lazily, because starting gopls costs seconds and indexes the module: a
// session that never calls the tool should never pay for it.
type Manager struct {
	Root     string
	Commands map[string][]string

	mu      sync.Mutex
	clients map[string]*Client
	failed  map[string]error
	// retryAfter holds a short cooldown for a startup that hit a caller's
	// deadline. Grep deliberately gives indexing only a few seconds; retrying
	// that same slow launch on every later search just repeats the delay.
	retryAfter map[string]time.Time

	// starting holds one channel per language being launched, closed when the
	// attempt finishes. Concurrent callers wait on it rather than each starting
	// their own server — the losers used to be overwritten in clients and leak
	// a process, a pipe pair and two goroutines apiece until exit.
	starting map[string]chan struct{}
	closed   bool

	// start is the launcher, swappable so tests do not need a real server.
	start func(ctx context.Context, name, root string, command []string) (*Client, error)
}

// DefaultCommands is the preconfigured server set. gopls is the only one the
// plan requires; the rest are here because they cost a line each and a Go-only
// LSP tool would be a strange thing to ship.
func DefaultCommands() map[string][]string {
	return map[string][]string{
		"go":         {"gopls"},
		"typescript": {"typescript-language-server", "--stdio"},
		"javascript": {"typescript-language-server", "--stdio"},
		"python":     {"pyright-langserver", "--stdio"},
		"rust":       {"rust-analyzer"},
	}
}

// NewManager builds a manager over the configured commands, filling in the
// defaults for languages the user did not mention.
func NewManager(root string, configured map[string][]string) *Manager {
	commands := DefaultCommands()
	for lang, cmd := range configured {
		commands[lang] = cmd
	}
	return &Manager{
		Root:       root,
		Commands:   commands,
		clients:    map[string]*Client{},
		failed:     map[string]error{},
		retryAfter: map[string]time.Time{},
	}
}

// For returns the server for a file's language, starting it if needed.
func (m *Manager) For(ctx context.Context, path string) (*Client, error) {
	lang := LanguageID(path)

	var command []string
	var launching chan struct{}
	for {
		m.mu.Lock()
		if c, ok := m.clients[lang]; ok {
			if !c.unusable() {
				m.mu.Unlock()
				return c, nil
			}
			delete(m.clients, lang)
			m.mu.Unlock()
			// A timed-out write closes the stream but may leave the child process
			// alive. Reap it before launching the replacement.
			_ = c.Close()
			continue
		}
		// A server that failed to start is not retried on every call: the usual
		// reason is that it is not installed, and re-running LookPath per tool
		// call only slows the failure down. Deadline failures get a cooldown,
		// rather than becoming permanent, because a later explicit LSP call may
		// reasonably succeed after the server has finished indexing.
		if err, ok := m.failed[lang]; ok {
			if until, retry := m.retryAfter[lang]; retry {
				if time.Now().Before(until) {
					m.mu.Unlock()
					return nil, err
				}
				delete(m.retryAfter, lang)
				delete(m.failed, lang)
			} else {
				m.mu.Unlock()
				return nil, err
			}
		}
		if wait, busy := m.starting[lang]; busy {
			m.mu.Unlock()
			select {
			case <-wait:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			// Round again: the launcher recorded either a client or a failure,
			// and this caller should see whichever it was.
			continue
		}
		var ok bool
		if m.closed {
			m.mu.Unlock()
			return nil, fmt.Errorf("language-server manager is closed")
		}
		command, ok = m.Commands[lang]
		if !ok || len(command) == 0 || strings.TrimSpace(command[0]) == "" {
			m.mu.Unlock()
			return nil, fmt.Errorf("no language server is configured for %s files", lang)
		}
		if m.starting == nil {
			m.starting = map[string]chan struct{}{}
		}
		launching = make(chan struct{})
		m.starting[lang] = launching
		m.mu.Unlock()
		break
	}

	start := m.start
	if start == nil {
		start = Start
	}
	c, err := start(ctx, command[0], m.Root, command)

	m.mu.Lock()
	delete(m.starting, lang)
	closed := m.closed
	if err != nil {
		// A caller may intentionally use a short context (grep does this so a
		// slow index cannot hold the search hostage). Do not turn that transient
		// cancellation into a permanent "server failed" state for the whole
		// session; a later LSP operation with a larger budget should retry.
		if m.failed == nil {
			m.failed = map[string]error{}
		}
		if ctx.Err() == context.DeadlineExceeded || errors.Is(err, context.DeadlineExceeded) {
			m.failed[lang] = err
			if m.retryAfter == nil {
				m.retryAfter = map[string]time.Time{}
			}
			m.retryAfter[lang] = time.Now().Add(startupRetryCooldown)
		} else if ctx.Err() == nil {
			m.failed[lang] = err
		}
	} else if closed {
		// Close can race a slow startup. Do not publish a live process into a
		// manager that has already dropped its client map.
		m.mu.Unlock()
		c.Close()
		close(launching)
		return nil, fmt.Errorf("language-server manager is closed")
	} else {
		m.clients[lang] = c
		delete(m.failed, lang)
		delete(m.retryAfter, lang)
	}
	m.mu.Unlock()
	close(launching)

	if err != nil {
		return nil, err
	}
	return c, nil
}

// Status describes one configured server.
type Status struct {
	Language string
	Command  string
	Running  bool
	Err      string
}

// Status reports every configured server, for `/lsp status`.
func (m *Manager) Status() []Status {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []Status
	for lang, command := range m.Commands {
		s := Status{Language: lang, Command: strings.Join(command, " ")}
		if _, ok := m.clients[lang]; ok {
			s.Running = true
		} else if err, ok := m.failed[lang]; ok {
			s.Err = err.Error()
		} else if len(command) == 0 || strings.TrimSpace(command[0]) == "" {
			s.Err = "disabled"
		} else if _, err := exec.LookPath(command[0]); err != nil {
			s.Err = "not installed"
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Language < out[j].Language })
	return out
}

// Close stops every running server.
func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	clients := make([]*Client, 0, len(m.clients))
	for _, c := range m.clients {
		clients = append(clients, c)
	}
	m.clients = map[string]*Client{}
	m.mu.Unlock()

	for _, c := range clients {
		c.Close()
	}
}
