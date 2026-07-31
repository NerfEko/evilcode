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
}

// ConnectTimeout bounds the handshake. A server that does not answer promptly
// must not hold up the session — the harness is usable without it.
const ConnectTimeout = 10 * time.Second

// Server is a live connection.
type Server struct {
	Name    string
	Session *sdk.ClientSession

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

	srv := &Server{Name: cfg.Name, Session: session}
	if err := srv.loadTools(ctx); err != nil {
		session.Close()
		return nil, err
	}
	return srv, nil
}

// loadTools adapts the server's tool list into evilcode's tool struct.
func (s *Server) loadTools(ctx context.Context) error {
	res, err := s.Session.ListTools(ctx, nil)
	if err != nil {
		return err
	}

	var out []tools.Tool
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

	res, err := s.Session.CallTool(ctx, &sdk.CallToolParams{
		Name:      name,
		Arguments: parsed,
	})
	if err != nil {
		return tools.Result{}, err
	}

	var b strings.Builder
	for _, content := range res.Content {
		if text, ok := content.(*sdk.TextContent); ok {
			b.WriteString(text.Text)
			b.WriteByte('\n')
		}
	}
	out := strings.TrimRight(b.String(), "\n")

	if res.IsError {
		// An MCP error is information the model can act on, so it comes back
		// as a tool error rather than a transport failure.
		if out == "" {
			out = "the server reported an error"
		}
		return tools.Result{Output: out}, fmt.Errorf("%s", out)
	}
	return tools.Result{Output: out, Intent: s.Name + " · " + name}, nil
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
