package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Ollama speaks the native Ollama API. Ollama Cloud is just a remote Ollama
// host — same endpoints, same shapes, only BaseURL and a bearer token differ —
// so one client serves both localhost and ollama.com (plan.md §1.2).
type Ollama struct {
	name    string
	BaseURL string
	APIKey  string
	HTTP    *http.Client

	// callSeq synthesizes tool_call IDs (Ollama issues none). It is scoped to
	// the provider instance, not one request, so IDs stay unique across every
	// turn of a session — a session resumed against an OpenAI-kind provider
	// depends on tool_call_id being unique, not just present.
	callSeq atomic.Int64

	// showCache memoizes /api/show per model. A model's context window and
	// capabilities do not change under a running session.
	showMu    sync.Mutex
	showCache map[string]ModelInfo
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
	// Think is false for disabled reasoning and a level string for enabled
	// reasoning. It is interface{} so false remains an explicit JSON value
	// rather than disappearing through omitempty.
	Think   any            `json:"think,omitempty"`
	Options map[string]any `json:"options,omitempty"`
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
	if req.ReasoningEffort.Valid() {
		body.Think = ollamaThinkValue(req.Model, req.ReasoningEffort)
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
		streamOllamaNDJSON(ctx, resp.Body, ch, &o.callSeq)
	}()
	return ch, nil
}

// streamOllamaNDJSON decodes one NDJSON response body onto ch. Split out from
// ChatStream so parsing is testable without a server. callSeq synthesizes
// tool_call IDs; callers share one counter across a session's requests (see
// Ollama.callSeq) so IDs stay unique across turns, not just within one.
func streamOllamaNDJSON(ctx context.Context, r io.Reader, ch chan<- Chunk, callSeq *atomic.Int64) {
	sc := bufio.NewScanner(r)
	// Model output lines can be long; the default 64KB limit is not enough for
	// a tool call carrying a whole file.
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

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
			// dropping: it usually means a proxy injected something. Terminal,
			// because the consumer returns on the first error chunk — carrying
			// on leaves this goroutine blocked on a send nobody will receive.
			send(Chunk{Err: fmt.Errorf("ollama: bad NDJSON line: %w", err)})
			return
		}
		if resp.Error != "" {
			send(Chunk{Err: fmt.Errorf("ollama: %s", resp.Error)})
			return
		}

		var chunk Chunk
		chunk.Text = resp.Message.Content
		chunk.Reasoning = resp.Message.Thinking
		for _, tc := range resp.Message.ToolCalls {
			id := callSeq.Add(1)
			chunk.ToolCalls = append(chunk.ToolCalls, ToolCall{
				// Ollama does not issue call IDs, so synthesize stable ones.
				ID:   fmt.Sprintf("call_%d", id),
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
		return
	}
	if ctx.Err() == nil {
		// A clean transport EOF is not a successful Ollama response unless a
		// protocol record said done. Treating a truncated proxy response as a final
		// answer silently commits partial text to the conversation.
		send(Chunk{Err: fmt.Errorf("ollama: %w", ErrStreamTruncated)})
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
	if err := json.NewDecoder(io.LimitReader(resp.Body, providerJSONMaxBytes)).Decode(&out); err != nil {
		return nil, err
	}
	if out.Error != "" {
		return nil, fmt.Errorf("ollama: %s", out.Error)
	}
	return out.Embeddings, nil
}

type ollamaTagsResp struct {
	Models []struct {
		Name         string   `json:"name"`
		Size         int64    `json:"size"`
		Capabilities []string `json:"capabilities"`
		Details      struct {
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
	if err := json.NewDecoder(io.LimitReader(resp.Body, providerJSONMaxBytes)).Decode(&tags); err != nil {
		return nil, err
	}
	out := make([]ModelInfo, len(tags.Models))
	for i, m := range tags.Models {
		out[i] = ModelInfo{
			Name:             m.Name,
			Size:             m.Details.ParameterSize,
			ReasoningEfforts: ollamaReasoningEffortsForCapabilities(m.Name, m.Capabilities),
		}
	}

	// /api/tags supplies names, sizes, and (on current Ollama versions)
	// capabilities. Ollama Cloud does not always fill parameter_size, and older
	// servers may omit capabilities, so /api/show still enriches each entry with
	// the authoritative per-model details.
	//
	// Bounded fan-out: one request per model, ShowConcurrency at a time, and the
	// caller's deadline governs. A model that does not answer keeps its listing
	// entry unenriched — a catalogue missing a context window is far better than
	// no catalogue.
	var wg sync.WaitGroup
	sem := make(chan struct{}, ShowConcurrency)
	for i := range out {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			detail, err := o.Show(ctx, out[i].Name)
			if err != nil {
				return
			}
			out[i].ContextWindow = detail.ContextWindow
			out[i].Vision = detail.Vision
			if len(detail.ReasoningEfforts) > 0 {
				out[i].ReasoningEfforts = detail.ReasoningEfforts
			}
			if detail.Size != "" {
				out[i].Size = detail.Size
			}
		}(i)
	}
	wg.Wait()
	return out, nil
}

// ShowConcurrency bounds the per-model detail fan-out. Model listings are
// short — a local daemon holds a handful, ollama.com under twenty — so this is
// about not opening a connection per model at once, not about throughput.
const ShowConcurrency = 8

type ollamaShowResp struct {
	Capabilities []string       `json:"capabilities"`
	ModelInfo    map[string]any `json:"model_info"`
	Details      struct {
		ParameterSize string `json:"parameter_size"`
	} `json:"details"`
}

func ollamaReasoningEffortsForCapabilities(model string, capabilities []string) []ReasoningEffort {
	for _, capability := range capabilities {
		if !strings.EqualFold(capability, "thinking") {
			continue
		}
		if strings.Contains(strings.ToLower(model), "gpt-oss") {
			return OllamaThinkingReasoningEfforts()
		}
		return OllamaReasoningEfforts()
	}
	return nil
}

// Show fetches one model's metadata, memoized for the life of the client: a
// model's context window does not change under a running session, and the model
// picker would otherwise re-fetch the whole catalogue every time it opens.
func (o *Ollama) Show(ctx context.Context, model string) (ModelInfo, error) {
	o.showMu.Lock()
	if o.showCache == nil {
		o.showCache = map[string]ModelInfo{}
	} else if hit, ok := o.showCache[model]; ok {
		o.showMu.Unlock()
		return hit, nil
	}
	o.showMu.Unlock()

	resp, err := o.post(ctx, "/api/show", map[string]string{"model": model})
	if err != nil {
		return ModelInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ModelInfo{}, httpError(resp.StatusCode, resp.Body)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, providerJSONMaxBytes))
	if err != nil {
		return ModelInfo{}, err
	}
	var show ollamaShowResp
	if err := json.Unmarshal(body, &show); err != nil {
		return ModelInfo{}, err
	}
	var raw map[string]any
	_ = json.Unmarshal(body, &raw)

	// A local daemon reports parameter_size as "8B"; cloud reports a bare count,
	// or "0" for a model it does not publish one for. Only the already-readable
	// form is taken verbatim.
	info := ModelInfo{Name: model}
	if _, bare := jsonInt(json.Number(show.Details.ParameterSize)); !bare {
		info.Size = show.Details.ParameterSize
	}
	// Ollama scopes the context length to the architecture — glm5.2.context_length,
	// llama.context_length, qwen3moe.context_length. The suffix identifies it;
	// no fixed key can, and the family is not knowable in advance.
	for key, value := range show.ModelInfo {
		if strings.HasSuffix(key, ".context_length") {
			if n, ok := jsonInt(value); ok && n > 0 {
				info.ContextWindow = n
			}
			break
		}
	}
	for _, c := range show.Capabilities {
		if strings.EqualFold(c, "vision") {
			info.Vision = true
		}
	}
	info.ReasoningEfforts = reasoningEffortsFromMetadata(raw)
	if len(info.ReasoningEfforts) == 0 {
		info.ReasoningEfforts = ollamaInfoEffortLevels(show.ModelInfo)
	}
	if len(info.ReasoningEfforts) == 0 {
		// The API has no metadata for this model. Ollama today only reports a
		// boolean "thinking" capability and accepts every think value for every
		// thinking model, so the generic vocabulary is the best the API can
		// express. If the server starts advertising per-model levels, the
		// metadata read above picks them up with no code change.
		info.ReasoningEfforts = ollamaReasoningEffortsForCapabilities(model, show.Capabilities)
	}
	// Cloud reports a bare parameter count where local reports "8B". Render the
	// count so the picker's detail column stays one kind of thing.
	if n, ok := jsonInt(show.ModelInfo["general.parameter_count"]); ok && n > 0 {
		info.Size = humanParams(n)
	}

	o.showMu.Lock()
	o.showCache[model] = info
	o.showMu.Unlock()
	return info, nil
}

// showEffortTimeout bounds the one /api/show lookup that enriches reasoning
// levels when the name heuristic misses. Show is memoized, so this is at most
// one request per model per client.
const showEffortTimeout = 3 * time.Second

// ollamaInfoEffortLevels scans a /api/show model_info map for effort-shaped
// fields. Ollama scopes per-model metadata under the architecture
// ("glm5_next.context_length"), so the scan matches on the key's field suffix
// rather than the whole key. Sorted iteration keeps the result deterministic.
func ollamaInfoEffortLevels(info map[string]any) []ReasoningEffort {
	keys := make([]string, 0, len(info))
	for key := range info {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		field := key
		if i := strings.LastIndex(field, "."); i >= 0 {
			field = field[i+1:]
		}
		switch field {
		case "reasoning_efforts", "supported_reasoning_efforts",
			"reasoning_effort", "efforts", "levels":
			if levels := reasoningEffortsFromValue(info[key]); len(levels) > 0 {
				return levels
			}
		}
	}
	return nil
}

func (o *Ollama) reasoningEffortLevelsForModel(model string) []ReasoningEffort {
	o.showMu.Lock()
	if info, ok := o.showCache[model]; ok && len(info.ReasoningEfforts) > 0 {
		levels := append([]ReasoningEffort(nil), info.ReasoningEfforts...)
		o.showMu.Unlock()
		return levels
	}
	o.showMu.Unlock()

	lower := strings.ToLower(strings.TrimSpace(model))
	if strings.Contains(lower, "gpt-oss") {
		return OllamaThinkingReasoningEfforts()
	}
	if strings.Contains(lower, "think") || strings.Contains(lower, "reason") ||
		strings.Contains(lower, "r1") || strings.Contains(lower, "qwen3") ||
		strings.Contains(lower, "glm") || strings.Contains(lower, "qwq") {
		return OllamaReasoningEfforts()
	}

	// The heuristic missed. The model may still advertise a "thinking"
	// capability over /api/show — deepseek-v4, kimi, minimax, nemotron are
	// not in the heuristic list — so ask the API before concluding the model
	// has no reasoning control. A failed lookup leaves the heuristic result
	// (nil) in place, so behavior is unchanged when the endpoint is
	// unreachable.
	ctx, cancel := context.WithTimeout(context.Background(), showEffortTimeout)
	defer cancel()
	if enriched, err := o.Show(ctx, model); err == nil {
		return append([]ReasoningEffort(nil), enriched.ReasoningEfforts...)
	}
	return nil
}

func ollamaThinkValue(model string, effort ReasoningEffort) any {
	if effort == ReasoningEffortNone {
		return false
	}
	if strings.Contains(strings.ToLower(model), "gpt-oss") {
		switch effort {
		case ReasoningEffortLow, ReasoningEffortMedium, ReasoningEffortHigh:
			return string(effort)
		default:
			return string(ReasoningEffortHigh)
		}
	}
	// Ollama's current thinking API accepts levels for most thinking models;
	// preserve the selected level instead of collapsing every enabled value to
	// the same boolean. The shared vocabulary has two values that Ollama does
	// not name directly, so translate them to the nearest supported level.
	switch effort {
	case ReasoningEffortMinimal:
		return string(ReasoningEffortLow)
	case ReasoningEffortLow, ReasoningEffortMedium, ReasoningEffortHigh, ReasoningEffortMax:
		return string(effort)
	case ReasoningEffortXHigh:
		return string(ReasoningEffortMax)
	default:
		return true
	}
}

// jsonInt reads a number that came through encoding/json as a float64 without
// losing a parameter count that does not fit one exactly.
func jsonInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		maxInt := int(^uint(0) >> 1)
		minInt := -maxInt - 1
		if math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n ||
			n >= float64(maxInt) || n <= float64(minInt) {
			return 0, false
		}
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		if err != nil || int64(int(i)) != i {
			return 0, false
		}
		return int(i), true
	}
	return 0, false
}

func humanParams(n int) string {
	switch {
	case n >= 1e12:
		return fmt.Sprintf("%.0fT", float64(n)/1e12)
	case n >= 1e9:
		return fmt.Sprintf("%.0fB", float64(n)/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%.0fM", float64(n)/1e6)
	}
	return fmt.Sprintf("%d", n)
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
