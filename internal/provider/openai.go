package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// OpenAI speaks the OpenAI-compatible chat completions API, which covers
// OpenRouter, DeepSeek, and most other hosted endpoints (plan.md §16).
type OpenAI struct {
	name    string
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

func NewOpenAI(name, baseURL, apiKey string) *OpenAI {
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}
	return &OpenAI{
		name:    name,
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		HTTP:    &http.Client{},
	}
}

func (o *OpenAI) Name() string { return o.name }

type oaiMessage struct {
	Role       string        `json:"role"`
	Content    string        `json:"content"`
	ToolCalls  []oaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	Name       string        `json:"name,omitempty"`
}

type oaiToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name string `json:"name"`
		// Unlike Ollama, arguments arrive as a JSON *string*, streamed in
		// fragments that must be concatenated before they parse.
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaiReq struct {
	Model       string       `json:"model"`
	Messages    []oaiMessage `json:"messages"`
	Tools       []oaiTool    `json:"tools,omitempty"`
	Stream      bool         `json:"stream"`
	Temperature *float64     `json:"temperature,omitempty"`
	StreamOpts  *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options,omitempty"`
}

type oaiTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type oaiStreamResp struct {
	Choices []struct {
		Delta struct {
			Content          string        `json:"content"`
			ReasoningContent string        `json:"reasoning_content"`
			Reasoning        string        `json:"reasoning"`
			ToolCalls        []oaiToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func toOAIMessages(msgs []Message) []oaiMessage {
	out := make([]oaiMessage, 0, len(msgs))
	for _, m := range msgs {
		om := oaiMessage{
			Role:       string(m.Role),
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
		}
		for i, tc := range m.ToolCalls {
			var otc oaiToolCall
			otc.Index = i
			otc.ID = tc.ID
			otc.Type = "function"
			otc.Function.Name = tc.Name
			otc.Function.Arguments = string(tc.Args)
			om.ToolCalls = append(om.ToolCalls, otc)
		}
		out = append(out, om)
	}
	return out
}

func toOAITools(tools []ToolDef) []oaiTool {
	out := make([]oaiTool, 0, len(tools))
	for _, t := range tools {
		var ot oaiTool
		ot.Type = "function"
		ot.Function.Name = t.Name
		ot.Function.Description = t.Desc
		ot.Function.Parameters = t.Schema
		out = append(out, ot)
	}
	return out
}

func (o *OpenAI) ChatStream(ctx context.Context, req Req) (<-chan Chunk, error) {
	body := oaiReq{
		Model:       req.Model,
		Messages:    toOAIMessages(req.Messages),
		Tools:       toOAITools(req.Tools),
		Stream:      true,
		Temperature: req.Temperature,
	}
	body.StreamOpts = &struct {
		IncludeUsage bool `json:"include_usage"`
	}{IncludeUsage: true}

	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		o.BaseURL+"/v1/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if o.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+o.APIKey)
	}

	resp, err := o.HTTP.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, httpError(resp.StatusCode, resp.Body)
	}

	ch := make(chan Chunk)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		streamOpenAISSE(ctx, resp.Body, ch)
	}()
	return ch, nil
}

// toolCallAccum reassembles tool calls that arrive as indexed fragments.
type toolCallAccum struct {
	order []int
	byIdx map[int]*ToolCall
}

func newToolCallAccum() *toolCallAccum {
	return &toolCallAccum{byIdx: map[int]*ToolCall{}}
}

func (a *toolCallAccum) add(tc oaiToolCall) {
	cur, ok := a.byIdx[tc.Index]
	if !ok {
		cur = &ToolCall{}
		a.byIdx[tc.Index] = cur
		a.order = append(a.order, tc.Index)
	}
	if tc.ID != "" {
		cur.ID = tc.ID
	}
	if tc.Function.Name != "" {
		cur.Name = tc.Function.Name
	}
	if tc.Function.Arguments != "" {
		cur.Args = append(cur.Args, tc.Function.Arguments...)
	}
}

func (a *toolCallAccum) finish() []ToolCall {
	out := make([]ToolCall, 0, len(a.order))
	for i, idx := range a.order {
		tc := *a.byIdx[idx]
		if tc.ID == "" {
			// Not every gateway issues IDs; the agent still needs one to pair
			// the result back to its call.
			tc.ID = fmt.Sprintf("call_%d", i+1)
		}
		if len(tc.Args) == 0 {
			tc.Args = json.RawMessage("{}")
		}
		out = append(out, tc)
	}
	return out
}

// streamOpenAISSE decodes an SSE body onto ch, accumulating tool-call
// fragments and emitting them once at the end of the stream.
func streamOpenAISSE(ctx context.Context, r io.Reader, ch chan<- Chunk) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	calls := newToolCallAccum()
	var usage *Usage

	send := func(c Chunk) bool {
		select {
		case ch <- c:
			return true
		case <-ctx.Done():
			return false
		}
	}

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			break
		}

		var resp oaiStreamResp
		if err := json.Unmarshal([]byte(data), &resp); err != nil {
			if !send(Chunk{Err: fmt.Errorf("openai: bad SSE payload: %w", err)}) {
				return
			}
			continue
		}
		if resp.Error != nil {
			send(Chunk{Err: fmt.Errorf("openai: %s", resp.Error.Message)})
			return
		}
		if resp.Usage != nil {
			usage = &Usage{
				PromptTokens:     resp.Usage.PromptTokens,
				CompletionTokens: resp.Usage.CompletionTokens,
			}
		}
		for _, choice := range resp.Choices {
			d := choice.Delta
			reasoning := d.ReasoningContent
			if reasoning == "" {
				reasoning = d.Reasoning
			}
			for _, tc := range d.ToolCalls {
				calls.add(tc)
			}
			if d.Content != "" || reasoning != "" {
				if !send(Chunk{Text: d.Content, Reasoning: reasoning}) {
					return
				}
			}
		}
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		send(Chunk{Err: err})
		return
	}
	send(Chunk{ToolCalls: calls.finish(), Usage: usage, Done: true})
}

func (o *OpenAI) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return nil, fmt.Errorf("openai: embeddings not wired for provider %q", o.name)
}

func (o *OpenAI) Models(ctx context.Context) ([]ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.BaseURL+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	if o.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.APIKey)
	}
	resp, err := o.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, httpError(resp.StatusCode, resp.Body)
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	models := make([]ModelInfo, 0, len(out.Data))
	for _, m := range out.Data {
		models = append(models, ModelInfo{Name: m.ID})
	}
	return models, nil
}
