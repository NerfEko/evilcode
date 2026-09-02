// Package mcp adapts Model Context Protocol servers into evilcode's tool set.
//
// It is a thin wrapper over the official SDK: the protocol is never hand-rolled
// (plan.md §1.5). Everything here is about lifecycle and naming — connecting,
// surviving a server that is not there, and giving MCP tools names that cannot
// collide with the built-ins.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"evilcode/internal/tools"
)

// ServerConfig is one configured MCP server.
type ServerConfig struct {
	Name string
	// Command starts a stdio server; URL points at a remote one (streamable
	// HTTP). Exactly one is set — config validation enforces it.
	Command string
	URL     string
	Args    []string
	Env     []string

	// Timeout bounds one tool call. Zero means CallTimeout.
	Timeout time.Duration
}

const (
	// ConnectTimeout bounds the handshake. A server that does not answer promptly
	// must not hold up the session — the harness is usable without it.
	ConnectTimeout = 10 * time.Second

	// CallTimeout bounds one tool call when the server's config names no own
	// timeout. MCP servers are third-party subprocesses with no deadline of
	// their own: without a bound, one wedged server stalls the whole turn until
	// the user interrupts manually. The bound costs at most one tool result,
	// which the model can read and route around.
	CallTimeout = 120 * time.Second
)

// callTimeoutOf resolves a server's per-call bound: its configured timeout,
// or CallTimeout when none was set.
func callTimeoutOf(cfg ServerConfig) time.Duration {
	if cfg.Timeout > 0 {
		return cfg.Timeout
	}
	return CallTimeout
}

const (
	// maxListPages bounds tool discovery: a catalog that still returns
	// cursors after this many pages is either pathological or lying, and
	// walking it forever would hang the connect path.
	maxListPages = 100

	// maxListTools bounds how many tools one server may expose. The list
	// feeds the model's context every turn, so a runaway catalog must fail
	// loudly rather than be truncated silently.
	maxListTools = 4096
)

// Server is a live connection.
type Server struct {
	Name    string
	Session *sdk.ClientSession

	// timeout bounds one tool call (callTimeoutOf at connect time).
	timeout time.Duration

	// cfg re-arms the transport on reconnect: stdio re-execs the command,
	// HTTP dials the endpoint again.
	cfg ServerConfig

	// closed is set by Close; a monitor that wakes on an intentional close
	// must not try to bring the server back.
	closed bool
	done   chan struct{}

	mu    sync.Mutex
	tools []tools.Tool

	// connected reflects the transport's liveness, lastConnErr the most
	// recent connection-level failure (empty when none), and lastReloadErr a
	// failed tools/list_changed refresh. All guarded by mu.
	connected     bool
	lastConnErr   error
	lastReloadErr error

	// connMu serializes reattach attempts: two calls discovering a dead
	// transport must produce one new session, not two.
	connMu sync.Mutex

	// onChange, when set, runs after a tools/list_changed reload produced a
	// different set. Guarded by mu because the SDK's notification goroutine
	// and the reload goroutine both touch it.
	onChange func(tools.Set)

	// reloading marks a single-flight reload so a burst of notifications
	// costs one pass.
	reloading bool
}

// Client holds every connected server.
type Client struct {
	mu      sync.Mutex
	servers []*Server

	// hook, when registered, runs after any server's tool set changes.
	hook func(tools.Set)
}

// New builds an empty client.
func New() *Client { return &Client{} }

// OnToolsChanged registers fn to run with the new set after any server's
// tools/list_changed notification produced a different tool set. Register it
// before Connect when the consumer exists by then; a consumer built after
// Connect (the agent comes out of wiring) registers late and picks changes up
// from the next notification on — the set itself is never lost, only the
// first push to that consumer can be.
func (c *Client) OnToolsChanged(fn func(tools.Set)) {
	c.mu.Lock()
	c.hook = fn
	c.mu.Unlock()
}

func (c *Client) fireHook(ts tools.Set) {
	c.mu.Lock()
	hook := c.hook
	c.mu.Unlock()
	if hook != nil {
		hook(ts)
	}
}

// Connect starts the configured servers, returning the ones that came up and
// the errors from the ones that did not.
//
// A failed server is reported rather than fatal: an MCP server being absent is
// a normal state — the binary may not be installed on this machine — and it
// must not stop evilcode from starting.
func (c *Client) Connect(ctx context.Context, configs []ServerConfig) []error {
	var errs []error
	for _, cfg := range configs {
		srv, err := connectOne(ctx, cfg)
		if err != nil {
			errs = append(errs, fmt.Errorf("mcp %s: %w", cfg.Name, err))
			continue
		}
		srv.setOnChange(c.fireHook)
		c.mu.Lock()
		c.servers = append(c.servers, srv)
		c.mu.Unlock()
	}
	return errs
}

// serverEnv builds the environment an MCP server subprocess inherits: the
// same allowlist the shell path applies (R2-16) plus the env entries the
// server's config block names, which come last and win. A third-party MCP
// server is an untrusted process — it must not receive the daemon's
// environment with every provider key in it.
func serverEnv(cfg ServerConfig) []string {
	return append(tools.AllowlistedProcessEnv(), cfg.Env...)
}

func connectOne(ctx context.Context, cfg ServerConfig) (*Server, error) {
	if cfg.Command == "" && cfg.URL == "" {
		return nil, fmt.Errorf("no command or url configured")
	}
	if cfg.Command != "" && cfg.URL != "" {
		return nil, fmt.Errorf("set command or url, not both")
	}

	srv := &Server{Name: cfg.Name, timeout: callTimeoutOf(cfg), cfg: cfg, done: make(chan struct{})}
	ctx, cancel := context.WithTimeout(ctx, ConnectTimeout)
	defer cancel()
	if err := srv.attach(ctx); err != nil {
		return nil, err
	}
	return srv, nil
}

// attach opens a session from cfg, loads its tools, and marks the server
// connected. It is the one place a session comes to life, shared by the
// initial connect and by reconnects.
func (s *Server) attach(ctx context.Context) error {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return fmt.Errorf("mcp server %s is closed", s.Name)
	}

	client := sdk.NewClient(&sdk.Implementation{Name: "evilcode", Version: "0.1.0"}, &sdk.ClientOptions{
		// A server that adds or removes tools mid-session (login flows,
		// dynamic registries) must not stay stale until restart. The handler
		// runs on the SDK's notification goroutine, so it only schedules.
		ToolListChangedHandler: func(ctx context.Context, req *sdk.ToolListChangedRequest) {
			s.scheduleReload()
		},
	})

	var transport sdk.Transport
	if s.cfg.URL != "" {
		// Hosted and remote servers speak streamable HTTP. The SDK's own
		// reconnect-with-backoff covers a dropped stream; a server that is
		// simply absent fails the connect and is reported like any other.
		transport = &sdk.StreamableClientTransport{Endpoint: s.cfg.URL}
	} else {
		if _, err := exec.LookPath(s.cfg.Command); err != nil {
			return fmt.Errorf("%s is not installed", s.cfg.Command)
		}
		cmd := exec.Command(s.cfg.Command, s.cfg.Args...)
		cmd.Env = serverEnv(s.cfg)
		transport = &sdk.CommandTransport{Command: cmd}
	}

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return err
	}
	if err := s.loadToolsWith(ctx, session); err != nil {
		_ = session.Close()
		return err
	}
	s.mu.Lock()
	s.Session = session
	s.connected = true
	s.lastConnErr = nil
	s.mu.Unlock()
	go s.monitor()
	return nil
}

// reconnectBackoff spaces the bounded background reconnect attempts after a
// transport dies. Atomic because tests tune it while a live session's
// monitor goroutine reads it.
var reconnectBackoff = func() *atomic.Pointer[[]time.Duration] {
	p := new(atomic.Pointer[[]time.Duration])
	p.Store(&[]time.Duration{time.Second, 2 * time.Second, 4 * time.Second})
	return p
}()

// currentBackoff snapshots the active backoff schedule.
func currentBackoff() []time.Duration { return *reconnectBackoff.Load() }

// monitor watches the session's transport. When it dies — and the death was
// not an intentional Close — it attempts a bounded reconnect with backoff so
// a server that died mid-session is not dead for the rest of the session.
// Exhausted attempts leave the server disconnected with the error on the
// status surface; the next tool call makes one more lazy attempt.
func (s *Server) monitor() {
	session := s.currentSession()
	err := session.Wait()

	s.mu.Lock()
	intentional := s.closed
	s.connected = false
	switch {
	case intentional:
		// The user closed it; a close is not a fault for the status surface.
		s.lastConnErr = nil
	case err != nil:
		s.lastConnErr = err
	default:
		s.lastConnErr = fmt.Errorf("the server closed the connection")
	}
	s.mu.Unlock()
	if intentional {
		return
	}

	for _, backoff := range currentBackoff() {
		select {
		case <-time.After(backoff):
		case <-s.done:
			return
		}
		if rerr := s.reattach(); rerr == nil {
			return // reattach started the next monitor
		}
	}
}

// reattach brings a dead server back: one bounded dial plus a tool reload,
// serialized so concurrent discoverers produce one session. It is a no-op
// reporting success when someone else already reattached.
func (s *Server) reattach() error {
	s.connMu.Lock()
	defer s.connMu.Unlock()

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("mcp server %s is closed", s.Name)
	}
	if s.connected {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), ConnectTimeout)
	defer cancel()
	old := s.currentSession()
	if err := s.attach(ctx); err != nil {
		s.mu.Lock()
		s.lastConnErr = err
		s.mu.Unlock()
		return err
	}
	if old != nil {
		_ = old.Close()
	}
	return nil
}

// currentSession snapshots the live session pointer.
func (s *Server) currentSession() *sdk.ClientSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Session
}

// loadTools adapts the server's tool list into evilcode's tool struct,
// following the list's pagination cursor: MCP list operations are paginated,
// so a single call exposes only the first page and tools past it do not exist
// as far as the model is concerned. The page and tool-count bounds are hard
// errors rather than silent truncation — a catalog cut in half is exactly the
// bug this closes.
func (s *Server) loadTools(ctx context.Context) error {
	return s.loadToolsWith(ctx, s.currentSession())
}

// loadToolsWith is loadTools against an explicit session, so a reconnect can
// reload from the new session before it becomes current.
func (s *Server) loadToolsWith(ctx context.Context, session *sdk.ClientSession) error {
	var out []tools.Tool
	cursor := ""
	for page := 1; ; page++ {
		if page > maxListPages {
			return fmt.Errorf("tool catalog exceeds %d pages; refusing to expose a truncated catalog", maxListPages)
		}
		res, err := session.ListTools(ctx, &sdk.ListToolsParams{Cursor: cursor})
		if err != nil {
			return err
		}

		for _, t := range res.Tools {
			remote := t.Name
			schema := json.RawMessage(`{"type":"object","properties":{}}`)
			if t.InputSchema != nil {
				if raw, err := json.Marshal(t.InputSchema); err == nil {
					schema = raw
				}
			}

			out = append(out, tools.Tool{
				// Namespaced so an MCP server can never shadow a built-in. A
				// server that ships its own `read` must not silently replace the
				// one the model has been taught to trust.
				Name:   s.Name + "__" + remote,
				Desc:   t.Description,
				Schema: schema,
				Run: func(ctx context.Context, args json.RawMessage) (tools.Result, error) {
					return s.call(ctx, remote, args)
				},
			})
			if len(out) > maxListTools {
				return fmt.Errorf("tool catalog exceeds %d tools; refusing to expose a truncated catalog", maxListTools)
			}
		}

		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}

	s.mu.Lock()
	s.tools = out
	s.mu.Unlock()
	return nil
}

// toolsSnapshot returns a copy of the loaded tool set, for tests and change
// consumers that must not race a reload.
func (s *Server) toolsSnapshot() []tools.Tool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.tools)
}

// setOnChange registers the consumer notified after a reload changes the set.
func (s *Server) setOnChange(fn func(tools.Set)) {
	s.mu.Lock()
	s.onChange = fn
	s.mu.Unlock()
}

// reloadDelay coalesces bursts of tools/list_changed notifications before
// the reload runs. Atomic because tests tune it while a live session's
// goroutine reads it.
var reloadDelay = func() *atomic.Int64 {
	d := new(atomic.Int64)
	d.Store(int64(250 * time.Millisecond))
	return d
}()

// scheduleReload starts one single-flight reload after a tools/list_changed
// notification. It runs on the SDK's notification goroutine: everything here
// is scheduling, the reload itself happens on its own goroutine with a fresh
// bounded context.
func (s *Server) scheduleReload() {
	s.mu.Lock()
	if s.reloading {
		s.mu.Unlock()
		return
	}
	s.reloading = true
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			s.reloading = false
			s.mu.Unlock()
		}()
		time.Sleep(time.Duration(reloadDelay.Load()))

		ctx, cancel := context.WithTimeout(context.Background(), ConnectTimeout)
		defer cancel()
		before := s.toolNames()
		if err := s.loadTools(ctx); err != nil {
			// Keep the loaded set — a failed refresh must not blank the
			// server's tools — and hold the error for the status surface.
			// The next notification retries.
			s.mu.Lock()
			s.lastReloadErr = err
			s.mu.Unlock()
			return
		}
		after := s.toolNames()
		s.mu.Lock()
		s.lastReloadErr = nil
		onChange := s.onChange
		s.mu.Unlock()
		if !slices.Equal(before, after) && onChange != nil {
			s.mu.Lock()
			changed := slices.Clone(s.tools)
			s.mu.Unlock()
			onChange(changed)
		}
	}()
}

// toolNames snapshots the loaded tool names, for change detection.
func (s *Server) toolNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.tools))
	for i, t := range s.tools {
		out[i] = t.Name
	}
	return out
}

func (s *Server) call(ctx context.Context, name string, args json.RawMessage) (tools.Result, error) {
	var parsed map[string]any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &parsed); err != nil {
			return tools.Result{}, fmt.Errorf("bad arguments: %w", err)
		}
	}

	// Bound the call so a wedged server costs one tool result, not the turn.
	// The smaller of the server's own timeout and any deadline the caller
	// already set wins; the error names the bound that actually applied.
	bound := s.timeout
	if d, ok := ctx.Deadline(); ok {
		if remaining := time.Until(d); remaining < bound {
			bound = remaining
		}
	}
	// A server the monitor has not yet marked dead gets one lazy reconnect
	// attempt here, so a session does not stay unusable while a background
	// backoff is still counting down.
	if !s.isConnected() {
		if err := s.reattach(); err != nil {
			return tools.Result{}, fmt.Errorf("mcp server %s is not connected: %w", s.Name, err)
		}
	}

	ctx, cancel := context.WithTimeout(ctx, bound)
	defer cancel()

	session := s.currentSession()
	res, err := session.CallTool(ctx, &sdk.CallToolParams{
		Name:      name,
		Arguments: parsed,
	})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return tools.Result{}, fmt.Errorf(
				"mcp tool %s__%s: no response in %s; the call was abandoned — the server may be wedged, retry or narrow it",
				s.Name, name, bound)
		}
		// A protocol-level error (unknown tool, bad params) rides a live
		// transport, so a cheap ping separates "the server said no" from "the
		// transport died". Only a dead transport earns a reconnect, and the
		// retry happens once within the same bound.
		if perr := s.ping(); perr != nil {
			if rerr := s.reattach(); rerr == nil {
				res, err = s.currentSession().CallTool(ctx, &sdk.CallToolParams{
					Name:      name,
					Arguments: parsed,
				})
			}
		}
		if err != nil {
			return tools.Result{}, err
		}
	}

	out, err := s.mapResult(name, res)
	if err != nil {
		return tools.Result{}, err
	}
	if res.IsError {
		// An MCP error is information the model can act on, so it comes back
		// as a tool error rather than a transport failure.
		if out.Output == "" {
			out.Output = "the server reported an error"
		}
		return out, fmt.Errorf("%s", out.Output)
	}
	return out, nil
}

// mapResult converts one MCP CallToolResult into evilcode's tools.Result.
//
// Every content type the protocol allows is mapped: text becomes output,
// images ride tools.Result.Images so a vision model and the UI can use them,
// audio and opaque blobs are named rather than silently dropped, and a
// content type this SDK build does not know is a hard error — a server that
// half-answers must not read as a success.
func (s *Server) mapResult(name string, res *sdk.CallToolResult) (tools.Result, error) {
	var b strings.Builder
	var images [][]byte
	for _, content := range res.Content {
		switch c := content.(type) {
		case nil:
			continue
		case *sdk.TextContent:
			b.WriteString(c.Text)
			b.WriteByte('\n')
		case *sdk.ImageContent:
			images = append(images, c.Data)
			fmt.Fprintf(&b, "[image #%d attached: %s, %d bytes]\n", len(images), c.MIMEType, len(c.Data))
		case *sdk.AudioContent:
			fmt.Fprintf(&b, "[audio attachment: %s, %d bytes — audio cannot be played in this harness]\n",
				c.MIMEType, len(c.Data))
		case *sdk.ResourceLink:
			title := c.Title
			if title == "" {
				title = c.Name
			}
			fmt.Fprintf(&b, "[resource link] %s — %s\n", title, c.URI)
		case *sdk.EmbeddedResource:
			if c.Resource == nil {
				continue
			}
			if c.Resource.Text != "" {
				b.WriteString(c.Resource.Text)
				b.WriteByte('\n')
				continue
			}
			if len(c.Resource.Blob) > 0 && strings.HasPrefix(c.Resource.MIMEType, "image/") {
				images = append(images, c.Resource.Blob)
				fmt.Fprintf(&b, "[image #%d attached: %s, %d bytes]\n", len(images), c.Resource.MIMEType, len(c.Resource.Blob))
				continue
			}
			fmt.Fprintf(&b, "[embedded resource: %s (%s), %d bytes]\n",
				c.Resource.URI, c.Resource.MIMEType, len(c.Resource.Blob))
		default:
			return tools.Result{}, fmt.Errorf(
				"mcp tool %s__%s returned content of unsupported type %T",
				s.Name, name, content)
		}
	}
	out := strings.TrimRight(b.String(), "\n")

	// Structured content is the machine-readable twin of the text (SEP-2106:
	// the two should mirror each other). When the server sent no text at all,
	// the JSON becomes the output; when both arrived, the text is what the
	// model reads and the JSON rides as display metadata instead of being
	// paid for twice in the context window.
	if res.StructuredContent != nil {
		raw, err := json.Marshal(res.StructuredContent)
		if err != nil {
			return tools.Result{}, fmt.Errorf("mcp tool %s__%s: structured content is not representable as JSON: %w",
				s.Name, name, err)
		}
		if out == "" {
			out = string(raw)
		} else {
			return tools.Result{Output: out, Images: images, Intent: s.Name + " · " + name, Display: raw}, nil
		}
	}
	return tools.Result{Output: out, Images: images, Intent: s.Name + " · " + name}, nil
}

// Tools returns every connected server's tools.
func (c *Client) Tools() tools.Set {
	c.mu.Lock()
	defer c.mu.Unlock()

	var out tools.Set
	for _, s := range c.servers {
		s.mu.Lock()
		out = append(out, s.tools...)
		s.mu.Unlock()
	}
	return out
}

// Summary describes the connected servers for the header line.
type Summary struct {
	Name      string
	Tools     int
	Connected bool
	LastError string
}

// Summaries reports what is connected, including per-server status: whether
// the transport is alive, how many tools it currently exposes, and the last
// connection- or refresh-level failure (empty when healthy).
func (c *Client) Summaries() []Summary {
	c.mu.Lock()
	defer c.mu.Unlock()

	out := make([]Summary, 0, len(c.servers))
	for _, s := range c.servers {
		out = append(out, s.status())
	}
	return out
}

// status snapshots one server's surface state.
func (s *Server) status() Summary {
	s.mu.Lock()
	defer s.mu.Unlock()

	sm := Summary{Name: s.Name, Tools: len(s.tools), Connected: s.connected}
	switch {
	case !s.connected && s.lastConnErr != nil:
		sm.LastError = s.lastConnErr.Error()
	case s.lastReloadErr != nil:
		sm.LastError = "tools refresh failed: " + s.lastReloadErr.Error()
	}
	return sm
}

// isConnected snapshots the liveness flag.
func (s *Server) isConnected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connected
}

// pingTimeout bounds the transport liveness probe.
const pingTimeout = 2 * time.Second

// ping probes the transport with a short bounded round trip. A live transport
// makes a CallTool error a protocol-level answer the model can act on; a
// failed ping means the connection itself is gone.
func (s *Server) ping() error {
	ctx, cancel := context.WithTimeout(context.Background(), pingTimeout)
	defer cancel()
	return s.currentSession().Ping(ctx, nil)
}

// Close shuts every server down.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.servers {
		s.Close()
	}
	c.servers = nil
}

// Close marks the server closed so no monitor or lazy call tries to bring it
// back, then shuts the transport down.
func (s *Server) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.connected = false
	done := s.done
	session := s.Session
	s.mu.Unlock()
	if done != nil {
		close(done)
	}
	if session != nil {
		_ = session.Close()
	}
}
