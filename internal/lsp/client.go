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
	"encoding/json"
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

	// diagnostics accumulate from the server's unsolicited notifications, which
	// is the only way they ever arrive: there is no "give me the diagnostics"
	// request in the protocol.
	diagMu sync.Mutex
	diags  map[string][]Diagnostic

	// opened tracks which files the server has been told about, so a second
	// operation on one file does not re-send its contents.
	opened map[string]bool

	closeOnce sync.Once
	err       error
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
		opened:  map[string]bool{},
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
	if _, err := c.call(ctx, "initialize", params); err != nil {
		return err
	}
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

func (c *Client) readMessage() ([]byte, error) {
	length := 0
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
			fmt.Sscanf(strings.TrimSpace(value), "%d", &length)
		}
	}
	if length <= 0 {
		return nil, fmt.Errorf("lsp: message with no Content-Length")
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(c.out, body); err != nil {
		return nil, err
	}
	return body, nil
}

func (c *Client) write(payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = fmt.Fprintf(c.in, "Content-Length: %d\r\n\r\n%s", len(body), body)
	return err
}

func (c *Client) notify(method string, params any) error {
	return c.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
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

	if err := c.write(map[string]any{
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
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	c.mu.Lock()
	already := c.opened[abs]
	c.mu.Unlock()
	if already {
		return nil
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		return err
	}
	if err := c.notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri":        URIFromPath(abs),
			"languageId": LanguageID(abs),
			"version":    1,
			"text":       string(data),
		},
	}); err != nil {
		return err
	}
	c.mu.Lock()
	c.opened[abs] = true
	c.mu.Unlock()
	return nil
}

// Forget drops a file's opened state, so the next operation re-sends it. A
// rename rewrites files on disk; without this the server keeps answering from
// the text it was given before the edit.
func (c *Client) Forget(path string) {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	c.mu.Lock()
	delete(c.opened, abs)
	c.mu.Unlock()
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
		Root:     root,
		Commands: commands,
		clients:  map[string]*Client{},
		failed:   map[string]error{},
	}
}

// For returns the server for a file's language, starting it if needed.
func (m *Manager) For(ctx context.Context, path string) (*Client, error) {
	lang := LanguageID(path)

	m.mu.Lock()
	if c, ok := m.clients[lang]; ok {
		m.mu.Unlock()
		return c, nil
	}
	// A server that failed to start is not retried on every call: the usual
	// reason is that it is not installed, and re-running LookPath per tool call
	// only slows the failure down.
	if err, ok := m.failed[lang]; ok {
		m.mu.Unlock()
		return nil, err
	}
	command, ok := m.Commands[lang]
	m.mu.Unlock()

	if !ok {
		return nil, fmt.Errorf("no language server is configured for %s files", lang)
	}

	c, err := Start(ctx, command[0], m.Root, command)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.failed[lang] = err
		return nil, err
	}
	m.clients[lang] = c
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
