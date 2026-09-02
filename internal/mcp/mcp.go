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
	"strings"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"evilcode/internal/tools"
)

// ServerConfig is one configured MCP server.
type ServerConfig struct {
	Name    string   `toml:"name"`
	Command string   `toml:"command"`
	Args    []string `toml:"args"`
	Env     []string `toml:"env"`

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

	mu    sync.Mutex
	tools []tools.Tool
}

// Client holds every connected server.
type Client struct {
	mu      sync.Mutex
	servers []*Server
}

// New builds an empty client.
func New() *Client { return &Client{} }

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
		c.mu.Lock()
		c.servers = append(c.servers, srv)
		c.mu.Unlock()
	}
	return errs
}

func connectOne(ctx context.Context, cfg ServerConfig) (*Server, error) {
	if cfg.Command == "" {
		return nil, fmt.Errorf("no command configured")
	}
	if _, err := exec.LookPath(cfg.Command); err != nil {
		return nil, fmt.Errorf("%s is not installed", cfg.Command)
	}

	ctx, cancel := context.WithTimeout(ctx, ConnectTimeout)
	defer cancel()

	cmd := exec.Command(cfg.Command, cfg.Args...)
	if len(cfg.Env) > 0 {
		cmd.Env = append(cmd.Environ(), cfg.Env...)
	}

	client := sdk.NewClient(&sdk.Implementation{Name: "evilcode", Version: "0.1.0"}, nil)
	session, err := client.Connect(ctx, &sdk.CommandTransport{Command: cmd}, nil)
	if err != nil {
		return nil, err
	}

	srv := &Server{Name: cfg.Name, Session: session, timeout: callTimeoutOf(cfg)}
	if err := srv.loadTools(ctx); err != nil {
		session.Close()
		return nil, err
	}
	return srv, nil
}

// loadTools adapts the server's tool list into evilcode's tool struct,
// following the list's pagination cursor: MCP list operations are paginated,
// so a single call exposes only the first page and tools past it do not exist
// as far as the model is concerned. The page and tool-count bounds are hard
// errors rather than silent truncation — a catalog cut in half is exactly the
// bug this closes.
func (s *Server) loadTools(ctx context.Context) error {
	var out []tools.Tool
	cursor := ""
	for page := 1; ; page++ {
		if page > maxListPages {
			return fmt.Errorf("tool catalog exceeds %d pages; refusing to expose a truncated catalog", maxListPages)
		}
		res, err := s.Session.ListTools(ctx, &sdk.ListToolsParams{Cursor: cursor})
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
	ctx, cancel := context.WithTimeout(ctx, bound)
	defer cancel()

	res, err := s.Session.CallTool(ctx, &sdk.CallToolParams{
		Name:      name,
		Arguments: parsed,
	})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return tools.Result{}, fmt.Errorf(
				"mcp tool %s__%s: no response in %s; the call was abandoned — the server may be wedged, retry or narrow it",
				s.Name, name, bound)
		}
		return tools.Result{}, err
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
	Name  string
	Tools int
}

// Summaries reports what is connected.
func (c *Client) Summaries() []Summary {
	c.mu.Lock()
	defer c.mu.Unlock()

	out := make([]Summary, 0, len(c.servers))
	for _, s := range c.servers {
		s.mu.Lock()
		out = append(out, Summary{Name: s.Name, Tools: len(s.tools)})
		s.mu.Unlock()
	}
	return out
}

// Close shuts every server down.
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, s := range c.servers {
		_ = s.Session.Close()
	}
	c.servers = nil
}
