// Package provider talks to model backends. It owns its own message and
// tool-call structs and maps to each wire format at the provider edge, so the
// agent core never sees a vendor's JSON shape (plan.md §16).
package provider

import (
	"context"
	"encoding/json"
)

// Role is a message author. Kept as a string type rather than an enum because
// every wire format spells these the same way.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ToolCall is one model-requested tool invocation.
type ToolCall struct {
	// ID correlates a call with its result. Some providers supply one; for
	// those that do not (Ollama), the client synthesizes a stable one.
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// Message is one entry in the conversation.
type Message struct {
	Role    Role   `json:"role"`
	Content string `json:"content,omitempty"`

	// Reasoning holds thinking-model traces. It is kept separate from Content
	// so the TUI can render and garbage-collect it independently (§9.7, §4.6).
	Reasoning string `json:"reasoning,omitempty"`

	// ToolCalls is set on assistant messages that requested tools.
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`

	// ToolCallID is set on tool-result messages, naming the call they answer.
	ToolCallID string `json:"tool_call_id,omitempty"`

	// ToolName is the tool that produced a result; some APIs require it.
	ToolName string `json:"tool_name,omitempty"`

	// IsError marks a tool result as a failure, including the interrupt stubs
	// written at safe point C (§6.3).
	IsError bool `json:"is_error,omitempty"`
}

// ToolDef describes a tool to the model.
type ToolDef struct {
	Name   string          `json:"name"`
	Desc   string          `json:"description"`
	Schema json.RawMessage `json:"schema"`
}

// Req is one chat completion request.
type Req struct {
	Model    string
	Messages []Message
	Tools    []ToolDef

	// NumCtx requests a context window size. Zero leaves the server's default,
	// which matters for Ollama where the default is often far below the
	// model's real capacity.
	NumCtx int

	// Temperature is applied only when non-nil, so "unset" stays distinct from
	// "deliberately zero".
	Temperature *float64
}

// Usage reports token accounting for a turn.
type Usage struct {
	PromptTokens     int
	CompletionTokens int

	// ContextMax is the model's window when the provider reports it.
	ContextMax int
}

// Chunk is one piece of a streamed response. Exactly one of the content fields
// is normally set, but a provider may deliver text and tool calls together, and
// some models buffer the entire tool call before emitting anything — never
// assume text deltas arrive first (plan.md Part V).
type Chunk struct {
	Text      string
	Reasoning string
	ToolCalls []ToolCall
	Usage     *Usage
	Done      bool
	Err       error
}

// ModelInfo describes an available model.
type ModelInfo struct {
	Name          string
	ContextWindow int
	// Size is a human-readable parameter count or file size when known.
	Size string
}

// Provider is a model backend. Implementations must close the returned channel
// exactly once, and must respect ctx cancellation promptly — an interrupt that
// does not stop the stream leaves the UI lying about what the model is doing.
type Provider interface {
	// Name identifies the configured provider instance (not the vendor).
	Name() string
	ChatStream(ctx context.Context, req Req) (<-chan Chunk, error)
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	Models(ctx context.Context) ([]ModelInfo, error)
}
