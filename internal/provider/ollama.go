package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Ollama speaks the native Ollama API. Ollama Cloud is just a remote Ollama
// host — same endpoints, same shapes, only BaseURL and a bearer token differ —
// so one client serves both localhost and ollama.com (plan.md §1.2).
type Ollama struct {
	name    string
	BaseURL string
	APIKey  string
	HTTP    *http.Client
}

// NewOllama builds a client. An empty base URL means a local daemon.
func NewOllama(name, baseURL, apiKey string) *Ollama {
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	return &Ollama{
		name:    name,
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		// No client timeout: a long turn is not a hung turn. Cancellation is
		// the context's job.
		HTTP: &http.Client{},
	}
}

func (o *Ollama) Name() string { return o.name }

func (o *Ollama) post(ctx context.Context, path string, body any) (*http.Response, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.BaseURL+path, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if o.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.APIKey)
	}
	return o.HTTP.Do(req)
}

// ollamaMessage is the wire shape of one message.
type ollamaMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	Thinking  string           `json:"thinking,omitempty"`
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
	ToolName  string           `json:"tool_name,omitempty"`

	// Images is Ollama's attachment field: bare base64, no data URI, no MIME.
	Images []string `json:"images,omitempty"`
}

type ollamaToolCall struct {
	Function struct {
		Name string `json:"name"`
		// Ollama sends arguments as a JSON object, not a string.
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

type ollamaChatReq struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Tools    []ollamaTool    `json:"tools,omitempty"`
	Stream   bool            `json:"stream"`
	Options  map[string]any  `json:"options,omitempty"`
}

type ollamaTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type ollamaChatResp struct {
	Message         ollamaMessage `json:"message"`
	Done            bool          `json:"done"`
	Error           string        `json:"error"`
	PromptEvalCount int           `json:"prompt_eval_count"`
	EvalCount       int           `json:"eval_count"`
}

func toOllamaMessages(msgs []Message) []ollamaMessage {
	out := make([]ollamaMessage, 0, len(msgs))
	for _, m := range msgs {
		om := ollamaMessage{
			Role:     string(m.Role),
			Content:  m.Content,
			Thinking: m.Reasoning,
			ToolName: m.ToolName,
		}
		for _, tc := range m.ToolCalls {
			var otc ollamaToolCall
			otc.Function.Name = tc.Name
			otc.Function.Arguments = tc.Args
			om.ToolCalls = append(om.ToolCalls, otc)
		}
		for _, img := range m.Images {
			om.Images = append(om.Images, base64.StdEncoding.EncodeToString(img))
		}
		out = append(out, om)
	}
	return out
}

func toOllamaTools(tools []ToolDef) []ollamaTool {
	out := make([]ollamaTool, 0, len(tools))
	for _, t := range tools {
		var ot ollamaTool
		ot.Type = "function"
		ot.Function.Name = t.Name
		ot.Function.Description = t.Desc
		ot.Function.Parameters = t.Schema
		out = append(out, ot)
	}
	return out
}

func (o *Ollama) ChatStream(ctx context.Context, req Req) (<-chan Chunk, error) {
	body := ollamaChatReq{
		Model:    req.Model,
		Messages: toOllamaMessages(req.Messages),
		Tools:    toOllamaTools(req.Tools),
		Stream:   true,
	}
	if req.NumCtx > 0 || req.Temperature != nil {
		body.Options = map[string]any{}
		if req.NumCtx > 0 {
			body.Options["num_ctx"] = req.NumCtx
		}
		if req.Temperature != nil {
			body.Options["temperature"] = *req.Temperature
		}
	}

	resp, err := o.post(ctx, "/api/chat", body)
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
		streamOllamaNDJSON(ctx, resp.Body, ch)
	}()
	return ch, nil
}

// streamOllamaNDJSON decodes one NDJSON response body onto ch. Split out from
// ChatStream so parsing is testable without a server.
func streamOllamaNDJSON(ctx context.Context, r io.Reader, ch chan<- Chunk) {
	sc := bufio.NewScanner(r)
	// Model output lines can be long; the default 64KB limit is not enough for
	// a tool call carrying a whole file.
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	var callSeq int
	send := func(c Chunk) bool {
		select {
		case ch <- c:
			return true
		case <-ctx.Done():
			return false
		}
	}

	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var resp ollamaChatResp
		if err := json.Unmarshal(line, &resp); err != nil {
			// A malformed line is worth surfacing rather than silently
			// dropping: it usually means a proxy injected something.
			if !send(Chunk{Err: fmt.Errorf("ollama: bad NDJSON line: %w", err)}) {
				return
			}
			continue
		}
		if resp.Error != "" {
			send(Chunk{Err: fmt.Errorf("ollama: %s", resp.Error)})
			return
		}

		var chunk Chunk
		chunk.Text = resp.Message.Content
		chunk.Reasoning = resp.Message.Thinking
		for _, tc := range resp.Message.ToolCalls {
			callSeq++
			chunk.ToolCalls = append(chunk.ToolCalls, ToolCall{
				// Ollama does not issue call IDs, so synthesize stable ones.
				ID:   fmt.Sprintf("call_%d", callSeq),
				Name: tc.Function.Name,
				Args: tc.Function.Arguments,
			})
		}
		if resp.Done {
			chunk.Done = true
			chunk.Usage = &Usage{
				PromptTokens:     resp.PromptEvalCount,
				CompletionTokens: resp.EvalCount,
			}
		}
		if chunk.Text == "" && chunk.Reasoning == "" && len(chunk.ToolCalls) == 0 && !chunk.Done {
			continue
		}
		if !send(chunk) {
			return
		}
		if resp.Done {
			return
		}
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		send(Chunk{Err: err})
	}
}

type ollamaEmbedResp struct {
	Embeddings [][]float32 `json:"embeddings"`
	Error      string      `json:"error"`
}

func (o *Ollama) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return o.EmbedModel(ctx, "nomic-embed-text", texts)
}

// EmbedModel embeds with an explicit model. Embedding runs against a local
// daemon by default because cloud embedding availability is thin (plan.md §1.2).
func (o *Ollama) EmbedModel(ctx context.Context, model string, texts []string) ([][]float32, error) {
	resp, err := o.post(ctx, "/api/embed", map[string]any{
		"model": model,
		"input": texts,
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, httpError(resp.StatusCode, resp.Body)
	}
	var out ollamaEmbedResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.Error != "" {
		return nil, fmt.Errorf("ollama: %s", out.Error)
	}
	return out.Embeddings, nil
}

type ollamaTagsResp struct {
	Models []struct {
		Name    string `json:"name"`
		Size    int64  `json:"size"`
		Details struct {
			ParameterSize string `json:"parameter_size"`
		} `json:"details"`
	} `json:"models"`
}

func (o *Ollama) Models(ctx context.Context) ([]ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.BaseURL+"/api/tags", nil)
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
	var tags ollamaTagsResp
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return nil, err
	}
	out := make([]ModelInfo, 0, len(tags.Models))
	for _, m := range tags.Models {
		out = append(out, ModelInfo{Name: m.Name, Size: m.Details.ParameterSize})
	}
	return out, nil
}

// httpError turns a non-200 into an error carrying enough of the body to be
// diagnosable, without dumping an entire HTML error page into the TUI.
func httpError(status int, body io.Reader) error {
	b, _ := io.ReadAll(io.LimitReader(body, 2048))
	msg := strings.TrimSpace(string(b))
	// Providers wrap their message in {"error": ...} often enough to be worth
	// unwrapping for a readable notice.
	var wrapped struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(b, &wrapped) == nil && len(wrapped.Error) > 0 {
		var s string
		if json.Unmarshal(wrapped.Error, &s) == nil {
			msg = s
		} else {
			var obj struct {
				Message string `json:"message"`
			}
			if json.Unmarshal(wrapped.Error, &obj) == nil && obj.Message != "" {
				msg = obj.Message
			}
		}
	}
	if msg == "" {
		msg = http.StatusText(status)
	}
	return &HTTPError{Status: status, Message: msg}
}

// HTTPError carries the status code so retry policy can distinguish a
// rate limit from a bad API key (plan.md §15).
type HTTPError struct {
	Status  int
	Message string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("http %d: %s", e.Status, e.Message)
}

// Retryable reports whether resending the identical request could plausibly
// succeed. Auth and bad-model errors are deterministic and must stop the loop
// rather than spin it (plan.md §12.6).
func (e *HTTPError) Retryable() bool {
	switch e.Status {
	case http.StatusTooManyRequests, http.StatusRequestTimeout:
		return true
	}
	return e.Status >= 500
}
