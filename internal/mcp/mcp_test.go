package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// newInProcessClient wires an *sdk.Server into a Client over the SDK's
// in-memory transports, exercising the same loadTools/call path a real
// subprocess goes through without spawning anything.
func newInProcessClient(t *testing.T, server *sdk.Server, cfg ServerConfig, clientOpts *sdk.ClientOptions) (*Client, *Server) {
	t.Helper()

	if clientOpts == nil {
		clientOpts = &sdk.ClientOptions{}
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

	srv := &Server{Name: cfg.Name, Session: clientSession, timeout: callTimeoutOf(cfg)}
	if err := srv.loadTools(context.Background()); err != nil {
		t.Fatalf("loadTools: %v", err)
	}
	cl := &Client{}
	cl.servers = append(cl.servers, srv)
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
