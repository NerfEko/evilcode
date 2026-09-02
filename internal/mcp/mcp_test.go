package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"evilcode/internal/tools"
)

// newInProcessClient wires an *sdk.Server into a Client over the SDK's
// in-memory transports, exercising the same loadTools/call path a real
// subprocess goes through without spawning anything.
func newInProcessClient(t *testing.T, server *sdk.Server, cfg ServerConfig, clientOpts *sdk.ClientOptions) (*Client, *Server) {
	t.Helper()

	if clientOpts == nil {
		clientOpts = &sdk.ClientOptions{}
	}
	srv := &Server{Name: cfg.Name, timeout: callTimeoutOf(cfg)}
	// Mirror connectOne's notification wiring: a test client would otherwise
	// silently never see tools/list_changed.
	clientOpts.ToolListChangedHandler = func(ctx context.Context, req *sdk.ToolListChangedRequest) {
		srv.scheduleReload()
	}
	serverTransport, clientTransport := sdk.NewInMemoryTransports()
	client := sdk.NewClient(&sdk.Implementation{Name: "evilcode-test", Version: "test"}, clientOpts)

	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	clientSession, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })

	srv.Session = clientSession
	if err := srv.loadTools(context.Background()); err != nil {
		t.Fatalf("loadTools: %v", err)
	}
	cl := &Client{}
	cl.servers = append(cl.servers, srv)
	srv.setOnChange(cl.fireHook)
	return cl, srv
}

// newTestServer builds a server with one tool registered by raw handler, so a
// test controls the result content exactly.
func newTestServer(t *testing.T, name string, handler sdk.ToolHandler, opts *sdk.ServerOptions) *sdk.Server {
	t.Helper()
	server := sdk.NewServer(&sdk.Implementation{Name: name, Version: "1"}, opts)
	if handler != nil {
		server.AddTool(&sdk.Tool{Name: "echo", InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}}, handler)
	}
	return server
}

func TestCallWithoutTimeoutSucceeds(t *testing.T) {
	server := newTestServer(t, "slow", func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "ok"}}}, nil
	}, nil)
	_, srv := newInProcessClient(t, server, ServerConfig{Name: "srv"}, nil)

	res, err := srv.call(context.Background(), "echo", nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res.Output != "ok" {
		t.Errorf("Output = %q, want %q", res.Output, "ok")
	}
}

func TestCallTimesOutOnAWedgedServer(t *testing.T) {
	server := newTestServer(t, "wedged", func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "late"}}}, nil
		}
	}, nil)
	_, srv := newInProcessClient(t, server, ServerConfig{Name: "srv", Timeout: 50 * time.Millisecond}, nil)

	done := make(chan struct{})
	var err error
	go func() {
		_, err = srv.call(context.Background(), "echo", nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("call did not return within the configured timeout")
	}
	if err == nil {
		t.Fatal("call succeeded, want a timeout error")
	}
	if !strings.Contains(err.Error(), "no response in 50ms") {
		t.Errorf("error does not name the bound that applied: %v", err)
	}
	if !strings.Contains(err.Error(), "srv__echo") {
		t.Errorf("error does not name the tool: %v", err)
	}
}

func TestCallTimeoutRespectsACallerDeadline(t *testing.T) {
	// The caller's deadline is smaller than the server's own timeout, so it is
	// the bound that actually applied — the error must name it, not ours.
	server := newTestServer(t, "wedged", func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "late"}}}, nil
		}
	}, nil)
	_, srv := newInProcessClient(t, server, ServerConfig{Name: "srv", Timeout: time.Minute}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := srv.call(ctx, "echo", nil)
	if err == nil {
		t.Fatal("call succeeded, want a deadline error")
	}
	// The caller's 30ms deadline fired, not the server's one-minute timeout:
	// the message must name the bound that actually applied.
	if strings.Contains(err.Error(), "1m0s") {
		t.Errorf("error names the server timeout, not the caller's deadline: %v", err)
	}
	if !strings.Contains(err.Error(), "no response in 2") {
		t.Errorf("error does not name the caller's bound: %v", err)
	}
}

func TestCallTimeoutOfDefaultsAndOverrides(t *testing.T) {
	if got := callTimeoutOf(ServerConfig{Name: "a"}); got != CallTimeout {
		t.Errorf("callTimeoutOf(default) = %s, want %s", got, CallTimeout)
	}
	if got := callTimeoutOf(ServerConfig{Name: "a", Timeout: 3 * time.Second}); got != 3*time.Second {
		t.Errorf("callTimeoutOf(3s) = %s, want 3s", got)
	}
}

func TestCallArgumentsReachTheServer(t *testing.T) {
	var got map[string]any
	server := newTestServer(t, "args", func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		if err := json.Unmarshal(req.Params.Arguments, &got); err != nil {
			return nil, err
		}
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "seen"}}}, nil
	}, nil)
	_, srv := newInProcessClient(t, server, ServerConfig{Name: "srv"}, nil)

	if _, err := srv.call(context.Background(), "echo", json.RawMessage(`{"x":1}`)); err != nil {
		t.Fatalf("call: %v", err)
	}
	if got["x"] != float64(1) {
		t.Errorf("server saw arguments %v, want x=1", got)
	}
}

func TestImageContentAttachesToTheResult(t *testing.T) {
	png := []byte("pretend-png-bytes")
	server := newTestServer(t, "img", func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{
			&sdk.ImageContent{Data: png, MIMEType: "image/png"},
		}}, nil
	}, nil)
	_, srv := newInProcessClient(t, server, ServerConfig{Name: "srv"}, nil)

	res, err := srv.call(context.Background(), "echo", nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if len(res.Images) != 1 || string(res.Images[0]) != string(png) {
		t.Fatalf("Images = %v, want the raw decoded bytes", res.Images)
	}
	if !strings.Contains(res.Output, "image #1 attached") || !strings.Contains(res.Output, "image/png") {
		t.Errorf("Output = %q, want a note naming the attachment", res.Output)
	}
}

func TestStructuredOnlyResultBecomesTheOutput(t *testing.T) {
	server := newTestServer(t, "structured", func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{StructuredContent: map[string]any{"sum": float64(3)}}, nil
	}, nil)
	_, srv := newInProcessClient(t, server, ServerConfig{Name: "srv"}, nil)

	res, err := srv.call(context.Background(), "echo", nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res.Output != `{"sum":3}` {
		t.Errorf("Output = %q, want the structured JSON", res.Output)
	}
}

func TestStructuredContentWithTextRidesAsDisplay(t *testing.T) {
	server := newTestServer(t, "both", func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{
			Content:           []sdk.Content{&sdk.TextContent{Text: "3 items"}},
			StructuredContent: map[string]any{"count": float64(3)},
		}, nil
	}, nil)
	_, srv := newInProcessClient(t, server, ServerConfig{Name: "srv"}, nil)

	res, err := srv.call(context.Background(), "echo", nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res.Output != "3 items" {
		t.Errorf("Output = %q, want the text unmodified", res.Output)
	}
	if string(res.Display.([]byte)) != `{"count":3}` {
		t.Errorf("Display = %v, want the structured JSON", res.Display)
	}
}

func TestAudioAndResourceLinksAreNamedNotDropped(t *testing.T) {
	server := newTestServer(t, "misc", func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{
			&sdk.AudioContent{Data: []byte("wav-bytes"), MIMEType: "audio/wav"},
			&sdk.ResourceLink{URI: "file:///tmp/x", Name: "x"},
		}}, nil
	}, nil)
	_, srv := newInProcessClient(t, server, ServerConfig{Name: "srv"}, nil)

	res, err := srv.call(context.Background(), "echo", nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !strings.Contains(res.Output, "audio attachment") || !strings.Contains(res.Output, "audio/wav") {
		t.Errorf("Output = %q, want the audio named", res.Output)
	}
	if !strings.Contains(res.Output, "file:///tmp/x") {
		t.Errorf("Output = %q, want the resource link", res.Output)
	}
}

func TestEmbeddedResourceTextAndBlobsMap(t *testing.T) {
	server := newTestServer(t, "embedded", func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{
			&sdk.EmbeddedResource{Resource: &sdk.ResourceContents{URI: "file:///a.txt", Text: "hello"}},
			&sdk.EmbeddedResource{Resource: &sdk.ResourceContents{
				URI: "file:///a.png", MIMEType: "image/png", Blob: []byte("png-bytes")}},
			&sdk.EmbeddedResource{Resource: &sdk.ResourceContents{
				URI: "file:///a.bin", MIMEType: "application/octet-stream", Blob: []byte("opaque")}},
		}}, nil
	}, nil)
	_, srv := newInProcessClient(t, server, ServerConfig{Name: "srv"}, nil)

	res, err := srv.call(context.Background(), "echo", nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !strings.Contains(res.Output, "hello") {
		t.Errorf("Output = %q, want the embedded text", res.Output)
	}
	if len(res.Images) != 1 || string(res.Images[0]) != "png-bytes" {
		t.Errorf("Images = %v, want the image blob attached", res.Images)
	}
	if !strings.Contains(res.Output, "file:///a.bin") || !strings.Contains(res.Output, "application/octet-stream") {
		t.Errorf("Output = %q, want the opaque blob named", res.Output)
	}
}

func TestEmptyResultIsEmptySuccess(t *testing.T) {
	server := newTestServer(t, "empty", func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{}, nil
	}, nil)
	_, srv := newInProcessClient(t, server, ServerConfig{Name: "srv"}, nil)

	res, err := srv.call(context.Background(), "echo", nil)
	if err != nil {
		t.Fatalf("an empty success must not error, got: %v", err)
	}
	if res.Output != "" {
		t.Errorf("Output = %q, want empty", res.Output)
	}
}

func TestUnsupportedContentTypeFailsLoudly(t *testing.T) {
	server := newTestServer(t, "weird", func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{
			&sdk.ToolUseContent{ID: "x", Name: "y"},
		}}, nil
	}, nil)
	_, srv := newInProcessClient(t, server, ServerConfig{Name: "srv"}, nil)

	_, err := srv.call(context.Background(), "echo", nil)
	if err == nil {
		t.Fatal("unsupported content silently succeeded")
	}
	if !strings.Contains(err.Error(), "unsupported type") || !strings.Contains(err.Error(), "srv__echo") {
		t.Errorf("error does not name the tool and type: %v", err)
	}
}

func TestLoadToolsFollowsPagination(t *testing.T) {
	// The SDK server paginates automatically at ServerOptions.PageSize; five
	// tools at two per page means the old single ListTools call saw only two.
	server := sdk.NewServer(&sdk.Implementation{Name: "paged", Version: "1"},
		&sdk.ServerOptions{PageSize: 2})
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		n := name
		server.AddTool(&sdk.Tool{Name: n, InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: n}}}, nil
		})
	}
	_, srv := newInProcessClient(t, server, ServerConfig{Name: "srv"}, nil)

	got := map[string]bool{}
	for _, tool := range srv.tools {
		got[tool.Name] = true
	}
	for _, want := range []string{"srv__a", "srv__b", "srv__c", "srv__d", "srv__e"} {
		if !got[want] {
			t.Errorf("tool %s missing; only %v loaded", want, got)
		}
	}
}

func TestLoadToolsRefusesAToolCountBeyondTheBound(t *testing.T) {
	server := sdk.NewServer(&sdk.Implementation{Name: "many", Version: "1"}, nil)
	for i := range maxListTools + 1 {
		server.AddTool(&sdk.Tool{Name: fmt.Sprintf("t%d", i), InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{}, nil
		})
	}
	serverTransport, clientTransport := sdk.NewInMemoryTransports()
	client := sdk.NewClient(&sdk.Implementation{Name: "evilcode-test", Version: "test"}, nil)
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer serverSession.Close()
	clientSession, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer clientSession.Close()

	srv := &Server{Name: "srv", Session: clientSession, timeout: time.Minute}
	err = srv.loadTools(context.Background())
	if err == nil {
		t.Fatal("oversized catalog loaded without error")
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("exceeds %d tools", maxListTools)) {
		t.Errorf("error does not name the bound: %v", err)
	}
}

func TestLoadToolsRefusesUnboundedPagination(t *testing.T) {
	// One tool per page and more pages than maxListPages: the old loop could
	// never terminate here, and silent truncation is not acceptable either.
	server := sdk.NewServer(&sdk.Implementation{Name: "endless", Version: "1"},
		&sdk.ServerOptions{PageSize: 1})
	for i := range maxListPages + 5 {
		server.AddTool(&sdk.Tool{Name: fmt.Sprintf("t%d", i), InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			return &sdk.CallToolResult{}, nil
		})
	}
	serverTransport, clientTransport := sdk.NewInMemoryTransports()
	client := sdk.NewClient(&sdk.Implementation{Name: "evilcode-test", Version: "test"}, nil)
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer serverSession.Close()
	clientSession, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer clientSession.Close()

	srv := &Server{Name: "srv", Session: clientSession, timeout: time.Minute}
	err = srv.loadTools(context.Background())
	if err == nil {
		t.Fatal("an endless catalog loaded without error")
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("exceeds %d pages", maxListPages)) {
		t.Errorf("error does not name the bound: %v", err)
	}
}

// Gap 6: an MCP server subprocess sees the allowlist plus its configured env
// entries — never the daemon's full environment.
func TestServerEnvIsAllowlisted(t *testing.T) {
	t.Setenv("MCP_GAP6_SECRET", "leak-me")
	t.Setenv("PATH", "/usr/bin")

	env := serverEnv(ServerConfig{Name: "srv", Env: []string{"FOO=bar"}})
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "MCP_GAP6_SECRET") {
		t.Errorf("daemon environment leaked into the server env: %q", joined)
	}
	if !strings.Contains(joined, "PATH=/usr/bin") {
		t.Errorf("allowlisted PATH missing: %q", joined)
	}
	if !strings.Contains(joined, "FOO=bar") {
		t.Errorf("configured env entry missing: %q", joined)
	}
}

// Gap 5: a tools/list_changed notification rebuilds the set and pushes it to
// the registered consumer.
func TestListChangedRefreshesToolsAndFiresTheHook(t *testing.T) {
	oldDelay := reloadDelay.Load()
	reloadDelay.Store(int64(10 * time.Millisecond))
	defer func() { reloadDelay.Store(oldDelay) }()

	server := sdk.NewServer(&sdk.Implementation{Name: "dyn", Version: "1"},
		&sdk.ServerOptions{PageSize: 1})
	server.AddTool(&sdk.Tool{Name: "a", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{}, nil
	})

	var pushed []string
	cl, srv := newInProcessClient(t, server, ServerConfig{Name: "srv"}, &sdk.ClientOptions{})
	cl.OnToolsChanged(func(ts tools.Set) {
		pushed = toolsSetNames(ts)
	})

	server.RemoveTools("a")
	server.AddTool(&sdk.Tool{Name: "b", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{}, nil
	})
	// No explicit scheduleReload: RemoveTools/AddTool fire tools/list_changed
	// on the wire, and the registered handler must turn that into a reload.

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		names := toolsSetNames(srv.toolsSnapshot())
		if len(names) == 1 && names[0] == "srv__b" && len(pushed) == 1 && pushed[0] == "srv__b" {
			return // both the reload and the push landed
		}
		time.Sleep(5 * time.Millisecond)
	}
	srv.mu.Lock()
	reloadErr := srv.lastReloadErr
	srv.mu.Unlock()
	t.Fatalf("refresh did not land: server=%v pushed=%v lastReloadErr=%v",
		toolsSetNames(srv.toolsSnapshot()), pushed, reloadErr)
}

// A changed-set reload without a registered consumer must not panic, and an
// unchanged set must not fire the hook.
func TestListChangedWithoutConsumerIsSafe(t *testing.T) {
	oldDelay := reloadDelay.Load()
	reloadDelay.Store(int64(10 * time.Millisecond))
	defer func() { reloadDelay.Store(oldDelay) }()

	server := sdk.NewServer(&sdk.Implementation{Name: "dyn", Version: "1"}, nil)
	server.AddTool(&sdk.Tool{Name: "a", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{}, nil
	})
	_, srv := newInProcessClient(t, server, ServerConfig{Name: "srv"}, &sdk.ClientOptions{})

	srv.scheduleReload() // no change, no consumer
	time.Sleep(300 * time.Millisecond)
	srv.mu.Lock()
	reloadErr := srv.lastReloadErr
	srv.mu.Unlock()
	if reloadErr != nil {
		t.Fatalf("reload failed: %v", reloadErr)
	}
}

func toolsSetNames(ts tools.Set) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Name
	}
	return out
}
