// Package provider talks to model backends. It owns its own message and
// tool-call structs and maps to each wire format at the provider edge, so the
// agent core never sees a vendor's JSON shape (plan.md §16).
package provider

import (
	"context"
	"encoding/json"
	"strings"
)

// ReasoningEffort controls how much deliberate reasoning a reasoning model
// spends before producing its answer. The shared type contains the vocabulary
// used by the providers Evilcode can translate at its wire boundary; each
// model advertises the subset it actually accepts.
type ReasoningEffort string

const (
	ReasoningEffortNone    ReasoningEffort = "none"
	ReasoningEffortMinimal ReasoningEffort = "minimal"
	ReasoningEffortLow     ReasoningEffort = "low"
	ReasoningEffortMedium  ReasoningEffort = "medium"
	ReasoningEffortHigh    ReasoningEffort = "high"
	ReasoningEffortXHigh   ReasoningEffort = "xhigh"
	ReasoningEffortMax     ReasoningEffort = "max"

	DefaultReasoningEffort = ReasoningEffortMedium
)

var standardReasoningEffortLevels = [...]ReasoningEffort{
	ReasoningEffortNone,
	ReasoningEffortMinimal,
	ReasoningEffortLow,
	ReasoningEffortMedium,
	ReasoningEffortHigh,
	ReasoningEffortXHigh,
	ReasoningEffortMax,
}

var codexReasoningEffortLevels = [...]ReasoningEffort{
	ReasoningEffortLow,
	ReasoningEffortMedium,
	ReasoningEffortHigh,
	ReasoningEffortXHigh,
}

var openAIReasoningEffortLevels = [...]ReasoningEffort{
	ReasoningEffortNone,
	ReasoningEffortMinimal,
	ReasoningEffortLow,
	ReasoningEffortMedium,
	ReasoningEffortHigh,
	ReasoningEffortXHigh,
}

// GPT-5.6 Sol, Terra, and Luna expose the full current OpenAI reasoning
// vocabulary, including max.
var gpt56ReasoningEffortLevels = [...]ReasoningEffort{
	ReasoningEffortNone,
	ReasoningEffortLow,
	ReasoningEffortMedium,
	ReasoningEffortHigh,
	ReasoningEffortXHigh,
	ReasoningEffortMax,
}

var deepSeekReasoningEffortLevels = [...]ReasoningEffort{
	ReasoningEffortNone,
	ReasoningEffortHigh,
	ReasoningEffortMax,
}

var ollamaReasoningEffortLevels = [...]ReasoningEffort{
	ReasoningEffortNone,
	ReasoningEffortLow,
	ReasoningEffortMedium,
	ReasoningEffortHigh,
	ReasoningEffortMax,
}

var ollamaThinkingReasoningEffortLevels = [...]ReasoningEffort{
	ReasoningEffortLow,
	ReasoningEffortMedium,
	ReasoningEffortHigh,
}

// Valid reports whether e is a supported effort level.
func (e ReasoningEffort) Valid() bool {
	for _, level := range standardReasoningEffortLevels {
		if e == level {
			return true
		}
	}
	return false
}

// NormalizeReasoningEfforts canonicalizes, de-duplicates, and preserves the
// advertised order of a model's levels. Unknown values are ignored so a
// provider can add metadata without making the picker offer a value its wire
// adapter cannot translate yet.
func NormalizeReasoningEfforts(levels []ReasoningEffort) []ReasoningEffort {
	seen := make(map[ReasoningEffort]struct{}, len(levels))
	out := make([]ReasoningEffort, 0, len(levels))
	for _, raw := range levels {
		effort, ok := ParseReasoningEffort(string(raw))
		if !ok {
			continue
		}
		if _, ok := seen[effort]; ok {
			continue
		}
		seen[effort] = struct{}{}
		out = append(out, effort)
	}
	return out
}

func copyReasoningEfforts(levels []ReasoningEffort) []ReasoningEffort {
	return append([]ReasoningEffort(nil), levels...)
}

// CodexReasoningEfforts returns the known Codex model levels. The Codex
// catalogue currently exposes these four values even when it does not include
// an explicit per-model capability field.
func CodexReasoningEfforts() []ReasoningEffort {
	return copyReasoningEfforts(codexReasoningEffortLevels[:])
}

// OpenAIReasoningEfforts returns the broad OpenAI reasoning vocabulary. A
// model-specific catalogue entry should take precedence when one is present.
func OpenAIReasoningEfforts() []ReasoningEffort {
	return copyReasoningEfforts(openAIReasoningEffortLevels[:])
}

// OpenAIGPT56ReasoningEfforts returns the levels supported by GPT-5.6 Sol,
// Terra, and Luna. It is separate from the older GPT-5 fallback because max
// is not a safe assumption for every older OpenAI-compatible model.
func OpenAIGPT56ReasoningEfforts() []ReasoningEffort {
	return copyReasoningEfforts(gpt56ReasoningEffortLevels[:])
}

// DeepSeekReasoningEfforts returns DeepSeek's toggle plus its two translated
// effort values.
func DeepSeekReasoningEfforts() []ReasoningEffort {
	return copyReasoningEfforts(deepSeekReasoningEffortLevels[:])
}

// OllamaReasoningEfforts returns the common level-based thinking shape. The
// Ollama API accepts these levels for most thinking-capable models, including
// max; GPT-OSS advertises its narrower shape separately.
func OllamaReasoningEfforts() []ReasoningEffort {
	return copyReasoningEfforts(ollamaReasoningEffortLevels[:])
}

// OllamaThinkingReasoningEfforts returns the level-based Ollama thinking
// vocabulary used by GPT-OSS, which accepts think: "low|medium|high" and
// cannot fully disable its reasoning trace.
func OllamaThinkingReasoningEfforts() []ReasoningEffort {
	return copyReasoningEfforts(ollamaThinkingReasoningEffortLevels[:])
}

// ParseReasoningEffort normalizes a user-facing effort value.
func ParseReasoningEffort(value string) (ReasoningEffort, bool) {
	effort := ReasoningEffort(strings.ToLower(strings.TrimSpace(value)))
	return effort, effort.Valid()
}

// Next advances through the shared effort levels, wrapping after max.
func (e ReasoningEffort) Next() ReasoningEffort {
	return e.NextIn(standardReasoningEffortLevels[:])
}

// NextIn advances through a model's advertised levels. If the current value
// is not in that list, cycling starts at its first level rather than silently
// selecting a provider-incompatible default.
func (e ReasoningEffort) NextIn(levels []ReasoningEffort) ReasoningEffort {
	levels = NormalizeReasoningEfforts(levels)
	if len(levels) == 0 {
		return DefaultReasoningEffort
	}
	for i, level := range levels {
		if e == level {
			return levels[(i+1)%len(levels)]
		}
	}
	return levels[0]
}

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

	// Held marks a tool result stopped by a safety gate. It is persisted so a
	// resumed or attached transcript keeps the warning distinct from failure.
	Held bool `json:"held,omitempty"`

	// Images are attachments for a vision model, as raw bytes.
	//
	// Raw rather than encoded, because the two wire formats disagree: Ollama
	// wants bare base64 and OpenAI wants a data URI with a MIME type. Encoding
	// at the provider edge keeps either one from imposing its format on the
	// shared type. `encoding/json` base64s a []byte for free, so the session
	// log round-trips without a store change.
	Images [][]byte `json:"images,omitempty"`

	// Repairs names the argument rewrites a tool call received before its
	// strict decode (an aliased field, a string-wrapped number coerced). It is
	// display metadata: it lives on the message so a resumed or attached
	// session's tool rows show the same repair suffix as the live one did.
	Repairs []string `json:"repairs,omitempty"`
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

	// ReasoningEffort is sent only by providers whose wire protocol supports
	// it. An empty value leaves a provider's own default untouched.
	ReasoningEffort ReasoningEffort

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

	// CacheReadTokens are prompt tokens served from the provider's KV cache
	// this request — DeepSeek's prompt_cache_hit_tokens. Zero when the
	// provider does not report caching, so a non-cache provider just leaves
	// both at zero and the widget stays away.
	CacheReadTokens int

	// CacheWriteTokens are prompt tokens written to the cache this request
	// — DeepSeek's prompt_cache_miss_tokens. The cache is filled from the
	// prefix that did not hit, so read+write is the full prompt the cache saw.
	CacheWriteTokens int
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

	// Vision reports that the model accepts images. Nothing infers it from a
	// name — sending bytes to a text-only model fails deep inside the provider
	// with a message that explains nothing, so this is configured rather than
	// guessed.
	Vision bool
	// Size is a human-readable parameter count or file size when known.
	Size string

	// ReasoningEfforts is the ordered set advertised by the provider for this
	// model. An empty slice means the catalogue did not expose capabilities;
	// the UI may use a provider-specific fallback for known model families.
	ReasoningEfforts []ReasoningEffort
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

// SupportsReasoningEffort reports whether p has a wire-level effort control.
// The provider interface stays intentionally small; most implementations are
// not reasoning-aware, and widening it would force every mock and local
// backend to grow a meaningless method.
func SupportsReasoningEffort(p Provider) bool {
	switch p := p.(type) {
	case *Codex:
		return true
	case *OpenAI:
		return p.supportsReasoningEffort
	case *Ollama:
		return true
	default:
		return false
	}
}

// ReasoningEffortLevelsForProvider returns a provider fallback when the model
// catalogue did not provide a per-model list. It is deliberately conservative
// for Ollama and OpenAI-compatible providers: model-name inference below only
// enables controls for families known to expose thinking.
func ReasoningEffortLevelsForProvider(p Provider, model string) []ReasoningEffort {
	switch p := p.(type) {
	case *Codex:
		return p.reasoningEffortLevelsForModel(model)
	case *OpenAI:
		return p.reasoningEffortLevelsForModel(model)
	case *Ollama:
		return p.reasoningEffortLevelsForModel(model)
	default:
		return nil
	}
}

// reasoningEffortsFromMetadata extracts common capability field spellings
// used by OpenAI-compatible gateways and the Codex catalogue. It intentionally
// accepts only effort-shaped keys, not every string in a model object.
func reasoningEffortsFromMetadata(fields map[string]any) []ReasoningEffort {
	for _, key := range []string{
		"reasoning_efforts", "supported_reasoning_efforts", "reasoning_effort",
		"efforts", "effort", "levels",
	} {
		if levels := reasoningEffortsFromValue(fields[key]); len(levels) > 0 {
			return levels
		}
	}
	for _, key := range []string{"reasoning", "thinking"} {
		if nested, ok := fields[key].(map[string]any); ok {
			if levels := reasoningEffortsFromMetadata(nested); len(levels) > 0 {
				return levels
			}
		}
	}
	return nil
}

func reasoningEffortsFromValue(value any) []ReasoningEffort {
	switch value := value.(type) {
	case string:
		if effort, ok := ParseReasoningEffort(value); ok {
			return []ReasoningEffort{effort}
		}
	case []string:
		levels := make([]ReasoningEffort, 0, len(value))
		for _, level := range value {
			levels = append(levels, ReasoningEffort(level))
		}
		return NormalizeReasoningEfforts(levels)
	case []any:
		levels := make([]ReasoningEffort, 0, len(value))
		for _, item := range value {
			if level, ok := item.(string); ok {
				levels = append(levels, ReasoningEffort(level))
			}
		}
		return NormalizeReasoningEfforts(levels)
	case map[string]any:
		return reasoningEffortsFromMetadata(value)
	case json.RawMessage:
		var decoded any
		if json.Unmarshal(value, &decoded) == nil {
			return reasoningEffortsFromValue(decoded)
		}
	}
	return nil
}

// DetectImageMIME sniffs an image's type from its magic bytes.
//
// By content rather than by file extension: an attachment can arrive from a
// clipboard with no name at all, and a wrong MIME is rejected by the API with an
// error that says nothing about which file caused it.
func DetectImageMIME(data []byte) string {
	switch {
	case len(data) >= 8 && string(data[:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png"
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return "image/jpeg"
	case len(data) >= 6 && string(data[:6]) == "GIF89a":
		return "image/gif"
	case len(data) >= 6 && string(data[:6]) == "GIF87a":
		return "image/gif"
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp"
	case len(data) >= 2 && data[0] == 'B' && data[1] == 'M':
		return "image/bmp"
	default:
		// PNG is the safest guess: it is what the render path produces and what
		// every vision endpoint accepts.
		return "image/png"
	}
}
