// Prompts and resources adaptation (mcp_gaps #8). Only tools surfaced before:
// a server exposing prompts or resources — git, filesystem, and most real
// servers — kept those features invisible to the model. When the server's
// initialize result declares the capabilities, they are adapted into
// namespaced tools: read_resource / list_resources for resources, get_prompt
// for prompts. A server's own tool of the same name always wins.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"evilcode/internal/tools"
)

// capabilityTools adapts one server's prompts and resources into tools. seen
// holds the remote tool names already loaded from the same server, so a
// server that ships its own read_resource is never shadowed by the adapter.
func (s *Server) capabilityTools(session *sdk.ClientSession, seen map[string]bool) []tools.Tool {
	var out []tools.Tool
	if session == nil {
		return nil
	}
	init := session.InitializeResult()
	if init == nil || init.Capabilities == nil {
		return nil
	}
	caps := init.Capabilities
	if caps.Resources != nil {
		out = append(out, s.readResourceTool(seen), s.listResourcesTool(seen))
	}
	if caps.Prompts != nil {
		out = append(out, s.getPromptTool(seen))
	}
	return out
}

// namespaced builds the adapter tool with the server's namespace, skipping
// names the server already uses itself.
func (s *Server) namespaced(remote, desc string, schema json.RawMessage, run func(context.Context, json.RawMessage) (tools.Result, error), seen map[string]bool) (tools.Tool, bool) {
	if seen[remote] {
		return tools.Tool{}, false
	}
	seen[remote] = true
	return tools.Tool{
		Name:   s.Name + "__" + remote,
		Desc:   desc,
		Schema: schema,
		// Resources and prompts are reads by spec: declaring it lets the
		// batch scheduler overlap them like every other read-only tool.
		Effect: tools.EffectReadOnly,
		Run:    run,
	}, true
}

func (s *Server) readResourceTool(seen map[string]bool) tools.Tool {
	run := func(ctx context.Context, args json.RawMessage) (tools.Result, error) {
		var params struct {
			URI string `json:"uri"`
		}
		if err := json.Unmarshal(args, &params); err != nil || strings.TrimSpace(params.URI) == "" {
			return tools.Result{}, fmt.Errorf("read_resource needs a uri (discover one with %s__list_resources)", s.Name)
		}
		res, err := s.currentSession().ReadResource(ctx, &sdk.ReadResourceParams{URI: params.URI})
		if err != nil {
			return tools.Result{}, err
		}
		out, images := resourceContentsText(res.Contents)
		return tools.Result{Output: out, Images: images, Intent: s.Name + " · read_resource"}, nil
	}
	tool, _ := s.namespaced("read_resource",
		"Read one resource from the "+s.Name+" MCP server by URI. Use "+s.Name+"__list_resources to discover URIs.",
		json.RawMessage(`{"type":"object","properties":{"uri":{"type":"string","description":"resource URI"}},"required":["uri"]}`),
		run, seen)
	return tool
}

func (s *Server) listResourcesTool(seen map[string]bool) tools.Tool {
	run := func(ctx context.Context, _ json.RawMessage) (tools.Result, error) {
		var b strings.Builder
		cursor := ""
		for page := 1; ; page++ {
			if page > maxListPages {
				return tools.Result{}, fmt.Errorf("resource list exceeds %d pages; refusing a truncated list", maxListPages)
			}
			res, err := s.currentSession().ListResources(ctx, &sdk.ListResourcesParams{Cursor: cursor})
			if err != nil {
				return tools.Result{}, err
			}
			for _, r := range res.Resources {
				fmt.Fprintf(&b, "%s — %s", r.URI, r.Name)
				if r.MIMEType != "" {
					fmt.Fprintf(&b, " (%s)", r.MIMEType)
				}
				if r.Description != "" {
					fmt.Fprintf(&b, ": %s", r.Description)
				}
				b.WriteByte('\n')
			}
			if len(res.Resources) == 0 && page == 1 && res.NextCursor == "" {
				b.WriteString("(the server lists no resources)\n")
			}
			if res.NextCursor == "" {
				break
			}
			cursor = res.NextCursor
		}
		return tools.Result{Output: strings.TrimRight(b.String(), "\n"), Intent: s.Name + " · list_resources"}, nil
	}
	tool, _ := s.namespaced("list_resources",
		"List the resources the "+s.Name+" MCP server offers, with their URIs for read_resource.",
		json.RawMessage(`{"type":"object","properties":{}`),
		run, seen)
	return tool
}

func (s *Server) getPromptTool(seen map[string]bool) tools.Tool {
	run := func(ctx context.Context, args json.RawMessage) (tools.Result, error) {
		var params struct {
			Name      string            `json:"name"`
			Arguments map[string]string `json:"arguments,omitempty"`
		}
		if err := json.Unmarshal(args, &params); err != nil || strings.TrimSpace(params.Name) == "" {
			return tools.Result{}, fmt.Errorf("get_prompt needs a prompt name")
		}
		res, err := s.currentSession().GetPrompt(ctx, &sdk.GetPromptParams{Name: params.Name, Arguments: params.Arguments})
		if err != nil {
			return tools.Result{}, err
		}
		var b strings.Builder
		for _, msg := range res.Messages {
			if msg == nil {
				continue
			}
			fmt.Fprintf(&b, "[%s] ", msg.Role)
			switch c := msg.Content.(type) {
			case *sdk.TextContent:
				b.WriteString(c.Text)
			case nil:
				b.WriteString("(empty message)")
			default:
				fmt.Fprintf(&b, "(%T content — not renderable as text)", msg.Content)
			}
			b.WriteByte('\n')
		}
		out := strings.TrimRight(b.String(), "\n")
		if out == "" {
			out = fmt.Sprintf("the prompt %q returned no messages", params.Name)
		}
		return tools.Result{Output: out, Intent: s.Name + " · get_prompt"}, nil
	}
	tool, _ := s.namespaced("get_prompt",
		"Render one prompt template from the "+s.Name+" MCP server. Arguments are the template's placeholders.",
		json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"},"arguments":{"type":"object","additionalProperties":{"type":"string"}}},"required":["name"]}`),
		run, seen)
	return tool
}

// resourceContentsText renders resource contents: text becomes output, image
// blobs attach like tool-result images, and anything opaque is named rather
// than silently dropped.
func resourceContentsText(contents []*sdk.ResourceContents) (string, [][]byte) {
	var b strings.Builder
	var images [][]byte
	for _, rc := range contents {
		if rc == nil {
			continue
		}
		switch {
		case rc.Text != "":
			b.WriteString(rc.Text)
			b.WriteByte('\n')
		case len(rc.Blob) > 0 && strings.HasPrefix(rc.MIMEType, "image/"):
			images = append(images, rc.Blob)
			fmt.Fprintf(&b, "[image #%d attached: %s, %d bytes]\n", len(images), rc.MIMEType, len(rc.Blob))
		default:
			fmt.Fprintf(&b, "[resource %s: %s, %d bytes of binary data]\n", rc.URI, rc.MIMEType, len(rc.Blob))
		}
	}
	return strings.TrimRight(b.String(), "\n"), images
}