package provider

import (
	"context"
	"fmt"
	"strings"
)

// OpenCode Go is opencode's subscription gateway for open coding models,
// exposed as an OpenAI-compatible chat completions API at
// https://opencode.ai/zen/go/v1. It reuses the shared OpenAI-compatible
// transport and adds what the gateway asks its clients to do: identify with a
// real user agent and send a stable session id in x-opencode-session so
// routing and prompt caching follow the conversation.
//
// Its model list is merged from two sources: the gateway's own /v1/models,
// which is what is actually routable today, and a bundled metadata table
// generated from models.dev (opencode_models.go), which supplies the context
// windows, vision support, and per-model reasoning vocabularies the wire
// listing does not carry.
type OpenCodeGo struct {
	*OpenAI
}

// DefaultOpenCodeGoBaseURL is the gateway's API root. It deliberately omits
// the /v1 suffix that the gateway's own docs publish: the shared OpenAI
// transport appends /v1/chat/completions and /v1/models itself, and every
// other evilcode base URL follows the same no-suffix convention.
const DefaultOpenCodeGoBaseURL = "https://opencode.ai/zen/go"

// openCodeGoUserAgent follows the gateway's client guidance: identify with the
// real coding-agent name rather than a generic HTTP-library default.
const openCodeGoUserAgent = "evilcode/1.0"

// openCodeGoSessionHeader is the conversation header OpenCode Go recognizes.
const openCodeGoSessionHeader = "x-opencode-session"

func NewOpenCodeGo(name, baseURL, apiKey string) *OpenCodeGo {
	if baseURL == "" {
		baseURL = DefaultOpenCodeGoBaseURL
	}
	return &OpenCodeGo{
		OpenAI: NewOpenAI(name, baseURL, apiKey).
			WithSessionHeader(openCodeGoSessionHeader).
			WithUserAgent(openCodeGoUserAgent),
	}
}

// opencodeGoSpecFor looks up a model's bundled metadata.
func opencodeGoSpecFor(id string) (opencodeGoSpec, bool) {
	for _, s := range opencodeGoCatalogue {
		if s.id == id {
			return s, true
		}
	}
	return opencodeGoSpec{}, false
}

// OpenCodeGoContextWindow returns a model's bundled context window, or 0 when
// the catalogue does not know it. Exported for config, which resolves context
// limits per concrete provider type without widening the Provider interface.
func OpenCodeGoContextWindow(model string) int {
	if spec, ok := opencodeGoSpecFor(strings.TrimSpace(model)); ok {
		return spec.context
	}
	return 0
}

// opencodeGoEfforts returns a model's advertised reasoning levels, or nil
// when the catalogue does not advertise any. There is deliberately no
// model-name heuristic here: the gateway serves many families whose thinking
// control is a toggle the OpenAI effort vocabulary cannot express, and
// guessing would offer a value the request would reject.
func (o *OpenCodeGo) reasoningEffortLevelsForModel(model string) []ReasoningEffort {
	if spec, ok := opencodeGoSpecFor(model); ok {
		return NormalizeReasoningEfforts(spec.efforts)
	}
	return nil
}

// ChatStream clamps the reasoning effort to what the model's catalogue entry
// advertises before delegating to the OpenAI-compatible transport. A saved
// preference can outlive the catalogue (a model re-served without its effort
// vocabulary, or with a toggle instead of levels), and sending an
// unadvertised reasoning_effort risks a 400 from the gateway; omitting the
// field always leaves the model's own default. "none" is gated like any other
// value: models whose catalogue lists it (gpt-5.6-luna, hy3, hy4-preview)
// accept it on the wire, and for the rest there is no reliable way to turn
// thinking off, so the default stands.
func (o *OpenCodeGo) ChatStream(ctx context.Context, req Req) (<-chan Chunk, error) {
	if req.ReasoningEffort != "" {
		advertised, _ := opencodeGoSpecFor(req.Model)
		if !containsEffort(NormalizeReasoningEfforts(advertised.efforts), req.ReasoningEffort) {
			req.ReasoningEffort = ""
		}
	}
	// The gateway serves part of its catalogue only through the Responses API:
	// muse-spark and gpt-5.6 ids return 500 on chat completions and grok-4.6 is
	// rejected with "not supported for format oa-compat", while /responses
	// streams normally for them (verified live 2026-09-15).
	if openCodeGoWantsResponses(req.Model) {
		return o.OpenAI.chatStreamResponses(ctx, req)
	}
	return o.OpenAI.ChatStream(ctx, req)
}

// openCodeGoWantsResponses matches ids the gateway does not serve over chat
// completions (verified live 2026-09-15): the muse-spark contributor pair and
// gpt-5.6-luna return 500, grok-4.6 is rejected as "not supported for format
// oa-compat". The grok-4.5 sibling is routed too so it starts working the
// moment its upstream recovers, on the wire the gateway publishes it under
// (the Zen gateway's endpoint table also routes grok and muse ids to
// /responses). Everything else stays on chat completions.
func openCodeGoWantsResponses(model string) bool {
	lower := strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(lower, "muse-spark") ||
		strings.HasPrefix(lower, "grok-") ||
		strings.HasPrefix(lower, "gpt-5.6")
}

func containsEffort(levels []ReasoningEffort, effort ReasoningEffort) bool {
	for _, level := range levels {
		if level == effort {
			return true
		}
	}
	return false
}

// Models merges the gateway's live listing with the bundled metadata. The
// live list decides which models are routable right now; the bundled table
// fills in what it does not carry. When the gateway is unreachable (offline,
// or before a key has been entered) the bundled catalogue keeps the model
// picker working instead of collapsing to just the current model.
func (o *OpenCodeGo) Models(ctx context.Context) ([]ModelInfo, error) {
	live, err := o.OpenAI.Models(ctx)
	if err != nil || len(live) == 0 {
		return opencodeGoCatalogueModels(), nil
	}
	for i := range live {
		spec, ok := opencodeGoSpecFor(live[i].Name)
		if !ok {
			continue
		}
		live[i].ContextWindow = spec.context
		live[i].Vision = spec.vision
		live[i].ReasoningEfforts = NormalizeReasoningEfforts(spec.efforts)
	}
	return live, nil
}

// opencodeGoCatalogueModels renders the bundled table as a picker listing.
// Order is the catalogue's (models.dev order), not a sort, so it matches the
// published catalogue.
func opencodeGoCatalogueModels() []ModelInfo {
	out := make([]ModelInfo, 0, len(opencodeGoCatalogue))
	for _, spec := range opencodeGoCatalogue {
		out = append(out, ModelInfo{
			Name:             spec.id,
			ContextWindow:    spec.context,
			Vision:           spec.vision,
			ReasoningEfforts: NormalizeReasoningEfforts(spec.efforts),
		})
	}
	return out
}

func (o *OpenCodeGo) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return nil, fmt.Errorf("opencode-go: embeddings not wired for provider %q", o.name)
}
