package provider

// Codex speaks the Responses API through the first-party ChatGPT backend using
// the OAuth session already managed by the Codex CLI. It deliberately keeps
// this separate from OpenAI: a ChatGPT access token is not an OpenAI API key,
// and the backend requires an account header and product header in addition to
// the bearer token.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	DefaultCodexBaseURL  = "https://chatgpt.com/backend-api/codex"
	defaultCodexTokenURL = "https://auth.openai.com/oauth/token"
	defaultCodexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	// The Codex catalog filters entries by the advertised client version. 0.1.0
	// is an Evilcode build version, but it predates every current Codex model and
	// makes the endpoint return an empty catalog with HTTP 200. Keep this at a
	// recent compatible Codex protocol version rather than coupling discovery to
	// Evilcode's own release stamp.
	codexClientVersion  = "0.147.0"
	codexAuthMaxBytes   = 1 << 20
	codexStreamMaxBytes = 8 << 20
	codexRefreshWindow  = 5 * time.Minute
)

var ErrCodexAuthNotFound = errors.New("codex: ChatGPT OAuth account not found")

// CodexAuthInfo is the non-persistent credential snapshot read from the Codex
// CLI's auth.json. The fields contain secrets and must never be logged.
type CodexAuthInfo struct {
	AccessToken  string
	RefreshToken string
	AccountID    string
	AuthFile     string
}

// Codex is a Responses API provider authenticated by a ChatGPT OAuth account.
// BaseURL and HTTP are exported to make a local test transport possible; normal
// callers should construct it through config.ProviderConfig.Build.
type Codex struct {
	name    string
	BaseURL string
	HTTP    *http.Client

	TokenEndpoint string
	ClientID      string

	authMu       sync.Mutex
	accessToken  string
	refreshToken string
	accountID    string
	authFile     string
	expiresAt    time.Time
}

// NewCodex constructs a provider from an already discovered auth snapshot.
func NewCodex(name, baseURL string, auth CodexAuthInfo) *Codex {
	if name == "" {
		name = "codex"
	}
	if baseURL == "" {
		baseURL = DefaultCodexBaseURL
	}
	return &Codex{
		name:          name,
		BaseURL:       normalizeCodexBaseURL(baseURL),
		HTTP:          &http.Client{},
		TokenEndpoint: defaultCodexTokenURL,
		ClientID:      defaultCodexClientID,
		accessToken:   auth.AccessToken,
		refreshToken:  auth.RefreshToken,
		accountID:     auth.AccountID,
		authFile:      auth.AuthFile,
		expiresAt:     jwtExpiry(auth.AccessToken),
	}
}

// NewCodexFromAuthFile discovers a Codex CLI account at authFile. An empty
// path uses CODEX_HOME/auth.json, or ~/.codex/auth.json when CODEX_HOME is not
// set.
func NewCodexFromAuthFile(name, baseURL, authFile string) (*Codex, error) {
	auth, err := DiscoverCodexAuthAt(authFile)
	if err != nil {
		return nil, err
	}
	return NewCodex(name, baseURL, auth), nil
}

func (c *Codex) Name() string { return c.name }

// DiscoverCodexAuth reads the same auth file used by the Codex CLI without
// printing or persisting any credential material.
func DiscoverCodexAuth() (CodexAuthInfo, error) {
	return DiscoverCodexAuthAt("")
}

// DiscoverCodexAuthAt is split out for deterministic tests and for callers
// that explicitly configure a Codex home. It accepts only ChatGPT OAuth modes;
// OPENAI_API_KEY-only auth belongs to the ordinary OpenAI provider.
func DiscoverCodexAuthAt(authFile string) (CodexAuthInfo, error) {
	if authFile == "" {
		authFile = codexAuthFile()
	}
	data, err := readBoundedFile(authFile, codexAuthMaxBytes)
	if err != nil {
		if os.IsNotExist(err) {
			return CodexAuthInfo{}, ErrCodexAuthNotFound
		}
		return CodexAuthInfo{}, fmt.Errorf("codex: read auth file: %w", err)
	}
	var raw struct {
		AuthMode  string `json:"auth_mode"`
		AccountID string `json:"account_id"`
		Tokens    *struct {
			AccessToken  string          `json:"access_token"`
			RefreshToken string          `json:"refresh_token"`
			AccountID    string          `json:"account_id"`
			IDToken      json.RawMessage `json:"id_token"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return CodexAuthInfo{}, fmt.Errorf("codex: parse auth file: %w", err)
	}
	mode := strings.ToLower(strings.TrimSpace(raw.AuthMode))
	if mode != "" && mode != "chatgpt" && mode != "chatgptauthtokens" {
		return CodexAuthInfo{}, ErrCodexAuthNotFound
	}
	if raw.Tokens == nil {
		return CodexAuthInfo{}, ErrCodexAuthNotFound
	}
	access := strings.TrimSpace(raw.Tokens.AccessToken)
	refresh := strings.TrimSpace(raw.Tokens.RefreshToken)
	account := strings.TrimSpace(raw.Tokens.AccountID)
	if account == "" {
		account = strings.TrimSpace(raw.AccountID)
	}
	if account == "" {
		account = codexAccountIDFromJWT(raw.Tokens.IDToken)
	}
	if account == "" {
		// Older auth files have occasionally omitted account_id from tokens while
		// retaining it in the access-token claims. This is only a local fallback;
		// the account header is still required before a request is sent.
		account = codexAccountIDFromToken(access)
	}
	if access == "" || account == "" || !validCredential(access) || !validCredential(account) {
		return CodexAuthInfo{}, ErrCodexAuthNotFound
	}
	if refresh != "" && !validCredential(refresh) {
		return CodexAuthInfo{}, ErrCodexAuthNotFound
	}
	return CodexAuthInfo{
		AccessToken:  access,
		RefreshToken: refresh,
		AccountID:    account,
		AuthFile:     authFile,
	}, nil
}

func codexAuthFile() string {
	home := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(h, ".codex")
		}
	}
	if home == "" {
		return filepath.Join(".codex", "auth.json")
	}
	return filepath.Join(home, "auth.json")
}

func readBoundedFile(path string, max int64) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("file is not a regular file")
	}
	if info.Size() < 0 || info.Size() > max {
		return nil, fmt.Errorf("file is too large")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("file is too large")
	}
	return data, nil
}

func validCredential(value string) bool {
	if value == "" || len(value) > codexAuthMaxBytes {
		return false
	}
	for _, r := range value {
		if r <= ' ' || r == 0x7f || r == '\r' || r == '\n' {
			return false
		}
	}
	return true
}

// codexAccountIDFromJWT extracts only the account claim used by the backend.
// Codex has stored id_token both as a raw JWT string and as a flattened claims
// object across releases, so discovery accepts both without retaining the
// token or exposing any other claim data.
func codexAccountIDFromJWT(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}
	var token string
	if json.Unmarshal(trimmed, &token) == nil {
		return codexAccountIDFromToken(token)
	}
	var claims struct {
		AccountID string `json:"chatgpt_account_id"`
		RawJWT    string `json:"raw_jwt"`
		Auth      struct {
			AccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if json.Unmarshal(trimmed, &claims) != nil {
		return ""
	}
	if strings.TrimSpace(claims.AccountID) != "" {
		return strings.TrimSpace(claims.AccountID)
	}
	if strings.TrimSpace(claims.Auth.AccountID) != "" {
		return strings.TrimSpace(claims.Auth.AccountID)
	}
	return codexAccountIDFromToken(claims.RawJWT)
}

func codexAccountIDFromToken(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		if payload, err = base64.URLEncoding.DecodeString(parts[1]); err != nil {
			return ""
		}
	}
	var claims struct {
		AccountID string `json:"chatgpt_account_id"`
		Auth      struct {
			AccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	if strings.TrimSpace(claims.AccountID) != "" {
		return strings.TrimSpace(claims.AccountID)
	}
	return strings.TrimSpace(claims.Auth.AccountID)
}

func normalizeCodexBaseURL(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return DefaultCodexBaseURL
	}
	for _, host := range []string{"https://chatgpt.com", "https://chat.openai.com", "https://chatgpt-staging.com"} {
		lower := strings.ToLower(baseURL)
		switch lower {
		case host, host + "/backend-api", host + "/codex":
			return host + "/backend-api/codex"
		case host + "/backend-api/codex":
			return baseURL
		}
	}
	return baseURL
}

func codexURL(base, path string) string {
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path, "/")
}

func (c *Codex) bearerToken(ctx context.Context) (string, error) {
	c.authMu.Lock()
	defer c.authMu.Unlock()

	if c.accessToken == "" || !validCredential(c.accessToken) {
		return "", ErrCodexAuthNotFound
	}
	if !validCredential(c.accountID) {
		return "", fmt.Errorf("codex: ChatGPT account id unavailable; run `codex login`")
	}
	if c.expiresAt.IsZero() || time.Until(c.expiresAt) > codexRefreshWindow {
		return c.accessToken, nil
	}
	if c.refreshToken == "" {
		return "", fmt.Errorf("codex: ChatGPT access token expired; run `codex login`")
	}

	endpoint := c.TokenEndpoint
	if override := strings.TrimSpace(os.Getenv("CODEX_REFRESH_TOKEN_URL_OVERRIDE")); override != "" {
		endpoint = override
	}
	if endpoint == "" {
		endpoint = defaultCodexTokenURL
	}
	clientID := c.ClientID
	if override := strings.TrimSpace(os.Getenv("CODEX_APP_SERVER_LOGIN_CLIENT_ID")); override != "" {
		clientID = override
	}
	if clientID == "" {
		clientID = defaultCodexClientID
	}
	body, err := json.Marshal(struct {
		ClientID     string `json:"client_id"`
		GrantType    string `json:"grant_type"`
		RefreshToken string `json:"refresh_token"`
	}{clientID, "refresh_token", c.refreshToken})
	if err != nil {
		return "", fmt.Errorf("codex: encode token refresh: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("codex: create token refresh: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("codex: refresh token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", httpError(resp.StatusCode, io.LimitReader(resp.Body, 2048))
	}
	var refreshed struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, codexAuthMaxBytes)).Decode(&refreshed); err != nil {
		return "", fmt.Errorf("codex: parse token refresh: %w", err)
	}
	refreshed.AccessToken = strings.TrimSpace(refreshed.AccessToken)
	refreshed.RefreshToken = strings.TrimSpace(refreshed.RefreshToken)
	if !validCredential(refreshed.AccessToken) {
		return "", fmt.Errorf("codex: token refresh returned no access token")
	}
	oldRefresh := c.refreshToken
	c.accessToken = refreshed.AccessToken
	if validCredential(refreshed.RefreshToken) {
		c.refreshToken = refreshed.RefreshToken
	}
	c.expiresAt = jwtExpiry(c.accessToken)
	// Persistence is best effort. The current process has the new credentials;
	// failure to update the CLI's file must not turn a successful refresh into a
	// failed model request. Only access/refresh tokens are changed, preserving
	// the CLI's id_token object and any future fields.
	if c.authFile != "" {
		_ = persistCodexTokens(c.authFile, c.accessToken, c.refreshToken, oldRefresh)
	}
	return c.accessToken, nil
}

func (c *Codex) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Codex) requestHeaders(token string) http.Header {
	h := make(http.Header)
	h.Set("Authorization", "Bearer "+token)
	h.Set("ChatGPT-Account-Id", c.accountID)
	h.Set("OAI-Product-Sku", "codex")
	h.Set("Originator", "codex_cli_rs")
	h.Set("User-Agent", "evilcode-codex/"+codexClientVersion)
	return h
}

type codexRequest struct {
	Model             string            `json:"model"`
	Instructions      string            `json:"instructions,omitempty"`
	Input             []json.RawMessage `json:"input"`
	Tools             []json.RawMessage `json:"tools,omitempty"`
	ToolChoice        string            `json:"tool_choice"`
	ParallelToolCalls bool              `json:"parallel_tool_calls"`
	Reasoning         *codexReasoning   `json:"reasoning,omitempty"`
	Store             bool              `json:"store"`
	Stream            bool              `json:"stream"`
	Include           []string          `json:"include,omitempty"`
}

type codexReasoning struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

func (c *Codex) ChatStream(ctx context.Context, req Req) (<-chan Chunk, error) {
	if strings.TrimSpace(req.Model) == "" {
		return nil, fmt.Errorf("codex: model is required")
	}
	token, err := c.bearerToken(ctx)
	if err != nil {
		return nil, err
	}
	instructions, input, err := toCodexInput(req.Messages)
	if err != nil {
		return nil, err
	}
	tools, err := toCodexTools(req.Tools)
	if err != nil {
		return nil, err
	}
	effort := req.ReasoningEffort
	if !effort.Valid() {
		effort = DefaultReasoningEffort
	}
	body := codexRequest{
		Model:             req.Model,
		Instructions:      instructions,
		Input:             input,
		Tools:             tools,
		ToolChoice:        "auto",
		ParallelToolCalls: true,
		Reasoning:         &codexReasoning{Effort: string(effort), Summary: "auto"},
		Store:             false,
		Stream:            true,
		Include:           []string{"reasoning.encrypted_content"},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("codex: encode request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		codexURL(c.BaseURL, "responses"), bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("codex: create request: %w", err)
	}
	httpReq.Header = c.requestHeaders(token)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	resp, err := c.httpClient().Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return nil, httpError(resp.StatusCode, resp.Body)
	}

	ch := make(chan Chunk)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		streamCodexSSE(ctx, resp.Body, ch)
	}()
	return ch, nil
}

func toCodexInput(msgs []Message) (string, []json.RawMessage, error) {
	var instructions []string
	input := make([]json.RawMessage, 0, len(msgs))
	for _, msg := range msgs {
		switch msg.Role {
		case RoleSystem:
			if msg.Content != "" {
				instructions = append(instructions, msg.Content)
			}
		case RoleUser:
			content := codexContent(msg.Content, msg.Images, "input")
			item := map[string]any{"type": "message", "role": "user", "content": content}
			raw, err := json.Marshal(item)
			if err != nil {
				return "", nil, fmt.Errorf("codex: encode user message: %w", err)
			}
			input = append(input, raw)
		case RoleAssistant:
			if msg.Content != "" || len(msg.Images) > 0 {
				item := map[string]any{"type": "message", "role": "assistant",
					"content": codexContent(msg.Content, msg.Images, "output")}
				raw, err := json.Marshal(item)
				if err != nil {
					return "", nil, fmt.Errorf("codex: encode assistant message: %w", err)
				}
				input = append(input, raw)
			}
			for _, call := range msg.ToolCalls {
				id := strings.TrimSpace(call.ID)
				if id == "" {
					return "", nil, fmt.Errorf("codex: assistant tool call %q has no id", call.Name)
				}
				args := string(call.Args)
				if strings.TrimSpace(args) == "" {
					args = "{}"
				}
				item := map[string]any{"type": "function_call", "call_id": id,
					"name": call.Name, "arguments": args}
				raw, err := json.Marshal(item)
				if err != nil {
					return "", nil, fmt.Errorf("codex: encode function call: %w", err)
				}
				input = append(input, raw)
			}
		case RoleTool:
			id := strings.TrimSpace(msg.ToolCallID)
			if id == "" {
				return "", nil, fmt.Errorf("codex: tool result has no call id")
			}
			output := any(msg.Content)
			if len(msg.Images) > 0 {
				output = codexContent(msg.Content, msg.Images, "input")
			}
			item := map[string]any{"type": "function_call_output", "call_id": id, "output": output}
			raw, err := json.Marshal(item)
			if err != nil {
				return "", nil, fmt.Errorf("codex: encode tool result: %w", err)
			}
			input = append(input, raw)
		default:
			return "", nil, fmt.Errorf("codex: unsupported message role %q", msg.Role)
		}
	}
	return strings.Join(instructions, "\n\n"), input, nil
}

func codexContent(text string, images [][]byte, mode string) []map[string]any {
	content := make([]map[string]any, 0, len(images)+1)
	if text != "" {
		kind := "input_text"
		if mode == "output" {
			kind = "output_text"
		}
		content = append(content, map[string]any{"type": kind, "text": text})
	}
	for _, image := range images {
		content = append(content, map[string]any{
			"type":      "input_image",
			"image_url": "data:" + DetectImageMIME(image) + ";base64," + base64.StdEncoding.EncodeToString(image),
		})
	}
	return content
}

func toCodexTools(tools []ToolDef) ([]json.RawMessage, error) {
	if len(tools) == 0 {
		return nil, nil
	}
	out := make([]json.RawMessage, 0, len(tools))
	for _, tool := range tools {
		if strings.TrimSpace(tool.Name) == "" {
			return nil, fmt.Errorf("codex: tool name is empty")
		}
		schema := tool.Schema
		if len(bytes.TrimSpace(schema)) == 0 {
			schema = json.RawMessage(`{"type":"object"}`)
		}
		if !json.Valid(schema) {
			return nil, fmt.Errorf("codex: tool %q has invalid JSON schema", tool.Name)
		}
		item := map[string]any{"type": "function", "name": tool.Name,
			"description": tool.Desc, "strict": false, "parameters": schema}
		raw, err := json.Marshal(item)
		if err != nil {
			return nil, fmt.Errorf("codex: encode tool %q: %w", tool.Name, err)
		}
		out = append(out, raw)
	}
	return out, nil
}

type codexCallAccum struct {
	order []string
	byKey map[string]*codexCall
}

type codexCall struct {
	Key    string
	ID     string
	CallID string
	Name   string
	Args   strings.Builder
}

func newCodexCallAccum() *codexCallAccum {
	return &codexCallAccum{byKey: make(map[string]*codexCall)}
}

func (a *codexCallAccum) get(item map[string]any) *codexCall {
	callID, _ := item["call_id"].(string)
	itemID, _ := item["item_id"].(string)
	if itemID == "" {
		itemID, _ = item["id"].(string)
	}
	callID = strings.TrimSpace(callID)
	itemID = strings.TrimSpace(itemID)
	var call *codexCall
	for _, key := range []string{callID, itemID} {
		if key == "" {
			continue
		}
		if existing, ok := a.byKey[key]; ok {
			if call == nil {
				call = existing
			} else if call != existing {
				call = a.merge(call, existing)
			}
		}
	}
	if call == nil {
		key := callID
		if key == "" {
			key = itemID
		}
		if key == "" {
			key = fmt.Sprintf("call_%d", len(a.order)+1)
		}
		call = &codexCall{Key: key, ID: itemID, CallID: callID}
		a.order = append(a.order, key)
	}
	if callID != "" {
		a.byKey[callID] = call
	}
	if itemID != "" {
		a.byKey[itemID] = call
	}
	return call
}

func (a *codexCallAccum) merge(primary, secondary *codexCall) *codexCall {
	if primary == secondary {
		return primary
	}
	if primary.ID == "" {
		primary.ID = secondary.ID
	}
	if primary.CallID == "" {
		primary.CallID = secondary.CallID
	}
	if primary.Name == "" {
		primary.Name = secondary.Name
	}
	if primary.Args.Len() == 0 {
		primary.Args.WriteString(secondary.Args.String())
	}
	for key, call := range a.byKey {
		if call == secondary {
			a.byKey[key] = primary
		}
	}
	for i, key := range a.order {
		if key == secondary.Key {
			a.order = append(a.order[:i], a.order[i+1:]...)
			break
		}
	}
	return primary
}

func (a *codexCallAccum) addItem(item map[string]any) {
	if typ, _ := item["type"].(string); typ != "function_call" {
		return
	}
	call := a.get(item)
	if value, ok := item["id"].(string); ok && value != "" {
		call.ID = value
	}
	if value, ok := item["call_id"].(string); ok && value != "" {
		call.CallID = value
	}
	if value, ok := item["name"].(string); ok && value != "" {
		call.Name = value
	}
	if value, ok := item["arguments"].(string); ok && value != "" {
		call.Args.Reset()
		call.Args.WriteString(value)
	}
}

func (a *codexCallAccum) addDelta(item map[string]any, delta string) {
	call := a.get(item)
	call.Args.WriteString(delta)
}

func (a *codexCallAccum) setArguments(item map[string]any, arguments string) {
	call := a.get(item)
	if name, ok := item["name"].(string); ok && strings.TrimSpace(name) != "" {
		call.Name = name
	}
	call.Args.Reset()
	call.Args.WriteString(arguments)
}

func (a *codexCallAccum) finish() []ToolCall {
	out := make([]ToolCall, 0, len(a.order))
	for i, key := range a.order {
		call := a.byKey[key]
		id := call.CallID
		if id == "" {
			id = call.ID
		}
		if id == "" {
			id = fmt.Sprintf("call_%d", i+1)
		}
		args := strings.TrimSpace(call.Args.String())
		if args == "" {
			args = "{}"
		}
		out = append(out, ToolCall{ID: id, Name: call.Name, Args: json.RawMessage(args)})
	}
	return out
}

// streamCodexSSE decodes Responses API events and emits text/reasoning live,
// then emits complete function calls and usage on one terminal Done chunk.
func streamCodexSSE(ctx context.Context, r io.Reader, ch chan<- Chunk) {
	sc := bufio.NewScanner(io.LimitReader(r, codexStreamMaxBytes))
	sc.Buffer(make([]byte, 0, 64*1024), codexStreamMaxBytes)
	dataLines := make([]string, 0, 2)
	calls := newCodexCallAccum()
	completed := false
	terminal := false

	send := func(chunk Chunk) bool {
		select {
		case ch <- chunk:
			return true
		case <-ctx.Done():
			return false
		}
	}
	process := func(data string) bool {
		data = strings.TrimSpace(data)
		if data == "" || data == "[DONE]" {
			return true
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			send(Chunk{Err: fmt.Errorf("codex: bad SSE payload: %w", err)})
			terminal = true
			return false
		}
		kind, _ := event["type"].(string)
		switch kind {
		case "response.output_text.delta", "response.reasoning_summary_text.delta":
			delta, _ := event["delta"].(string)
			if delta == "" {
				return true
			}
			chunk := Chunk{}
			if kind == "response.output_text.delta" {
				chunk.Text = delta
			} else {
				chunk.Reasoning = delta
			}
			return send(chunk)
		case "response.output_item.added", "response.output_item.done":
			if item, ok := event["item"].(map[string]any); ok {
				calls.addItem(item)
			}
		case "response.function_call_arguments.delta":
			delta, _ := event["delta"].(string)
			item := map[string]any{
				"item_id": event["item_id"],
				"call_id": event["call_id"],
			}
			calls.addDelta(item, delta)
		case "response.function_call_arguments.done":
			arguments, _ := event["arguments"].(string)
			item := map[string]any{
				"item_id": event["item_id"],
				"call_id": event["call_id"],
				"name":    event["name"],
			}
			calls.setArguments(item, arguments)
		case "response.completed":
			if response, ok := event["response"].(map[string]any); ok {
				if output, ok := response["output"].([]any); ok {
					for _, value := range output {
						if item, ok := value.(map[string]any); ok {
							calls.addItem(item)
						}
					}
				}
			}
			usage := codexUsage(event)
			completed = true
			terminal = true
			return send(Chunk{ToolCalls: calls.finish(), Usage: usage, Done: true})
		case "response.failed", "response.incomplete", "error":
			message := codexEventError(event, kind)
			send(Chunk{Err: fmt.Errorf("codex: %s", message)})
			terminal = true
			return false
		}
		return true
	}
	flush := func() bool {
		if len(dataLines) == 0 {
			return true
		}
		data := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		return process(data)
	}

	for sc.Scan() {
		line := strings.TrimSuffix(sc.Text(), "\r")
		if line == "" {
			if !flush() || terminal {
				return
			}
			continue
		}
		if strings.HasPrefix(line, ":") || strings.HasPrefix(line, "event:") || strings.HasPrefix(line, "id:") {
			continue
		}
		if data, ok := strings.CutPrefix(line, "data:"); ok {
			dataLines = append(dataLines, strings.TrimPrefix(data, " "))
		}
	}
	if terminal || ctx.Err() != nil {
		return
	}
	if !flush() {
		return
	}
	if err := sc.Err(); err != nil {
		send(Chunk{Err: fmt.Errorf("codex: read SSE stream: %w", err)})
		return
	}
	if !completed {
		send(Chunk{Err: fmt.Errorf("codex: stream closed before response.completed")})
	}
}

func codexEventError(event map[string]any, kind string) string {
	for _, candidate := range []any{event["error"], event["response"]} {
		if value, ok := candidate.(map[string]any); ok {
			if nested, ok := value["error"].(map[string]any); ok {
				if msg, _ := nested["message"].(string); msg != "" {
					return msg
				}
			}
			if msg, _ := value["message"].(string); msg != "" {
				return msg
			}
		}
		if msg, ok := candidate.(string); ok && msg != "" {
			return msg
		}
	}
	if kind == "response.incomplete" {
		return "response incomplete"
	}
	return "response failed"
}

func codexUsage(event map[string]any) *Usage {
	response, _ := event["response"].(map[string]any)
	usage, _ := response["usage"].(map[string]any)
	if usage == nil {
		return nil
	}
	return &Usage{
		PromptTokens:     codexJSONInt(usage["input_tokens"]),
		CompletionTokens: codexJSONInt(usage["output_tokens"]),
		CacheReadTokens:  codexJSONIntNested(usage, "input_tokens_details", "cached_tokens"),
		CacheWriteTokens: codexJSONIntNested(usage, "input_tokens_details", "cache_write_tokens"),
	}
}

func codexJSONInt(value any) int {
	switch n := value.(type) {
	case float64:
		return int(n)
	case json.Number:
		i, _ := strconv.Atoi(string(n))
		return i
	case int:
		return n
	}
	return 0
}

func codexJSONIntNested(value map[string]any, outer, inner string) int {
	nested, _ := value[outer].(map[string]any)
	return codexJSONInt(nested[inner])
}

type codexModelsResponse struct {
	Models []codexModel `json:"models"`
}

type codexModel struct {
	Slug                      string          `json:"slug"`
	ID                        string          `json:"id"`
	DisplayName               string          `json:"display_name"`
	Visibility                string          `json:"visibility"`
	ContextWindow             json.RawMessage `json:"context_window"`
	InputModalities           []string        `json:"input_modalities"`
	ReasoningEfforts          []string        `json:"reasoning_efforts"`
	SupportedReasoningEfforts []string        `json:"supported_reasoning_efforts"`
	Reasoning                 json.RawMessage `json:"reasoning"`
}

func (c *Codex) reasoningEffortLevelsForModel(model string) []ReasoningEffort {
	if strings.Contains(strings.ToLower(strings.TrimSpace(model)), "gpt-5.6") {
		return OpenAIGPT56ReasoningEfforts()
	}
	return CodexReasoningEfforts()
}

func (c *Codex) Models(ctx context.Context) ([]ModelInfo, error) {
	token, err := c.bearerToken(ctx)
	if err != nil {
		return nil, err
	}
	url := codexURL(c.BaseURL, "models") + "?client_version=" + codexClientVersion
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("codex: create models request: %w", err)
	}
	req.Header = c.requestHeaders(token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, httpError(resp.StatusCode, resp.Body)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, codexAuthMaxBytes))
	if err != nil {
		return nil, fmt.Errorf("codex: read models response: %w", err)
	}
	var wrapped codexModelsResponse
	if err := json.Unmarshal(data, &wrapped); err != nil {
		return nil, fmt.Errorf("codex: parse models response: %w", err)
	}
	models := make([]ModelInfo, 0, len(wrapped.Models))
	for _, model := range wrapped.Models {
		// The backend includes internal presets (for example the hidden
		// auto-review and worker variants) in the same response. The official
		// Codex picker only exposes visibility=list entries. Older responses did
		// not carry visibility, so an omitted field remains compatible.
		if model.Visibility != "" && !strings.EqualFold(model.Visibility, "list") {
			continue
		}
		name := strings.TrimSpace(model.Slug)
		if name == "" {
			name = strings.TrimSpace(model.ID)
		}
		if name == "" {
			continue
		}
		contextWindow := 0
		if len(model.ContextWindow) > 0 && string(model.ContextWindow) != "null" {
			var n int
			if json.Unmarshal(model.ContextWindow, &n) == nil {
				contextWindow = n
			}
		}
		vision := false
		for _, modality := range model.InputModalities {
			if strings.EqualFold(modality, "image") {
				vision = true
				break
			}
		}
		metadata := map[string]any{
			"reasoning_efforts":           model.ReasoningEfforts,
			"supported_reasoning_efforts": model.SupportedReasoningEfforts,
			"reasoning":                   model.Reasoning,
		}
		levels := reasoningEffortsFromMetadata(metadata)
		if len(levels) == 0 {
			levels = c.reasoningEffortLevelsForModel(name)
		}
		models = append(models, ModelInfo{Name: name, ContextWindow: contextWindow,
			Vision: vision, ReasoningEfforts: levels})
	}
	return models, nil
}

func (c *Codex) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return nil, fmt.Errorf("codex: embeddings are not available through the ChatGPT backend")
}

func jwtExpiry(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		Exp json.Number `json:"exp"`
	}
	if err := json.Unmarshal(data, &claims); err != nil {
		return time.Time{}
	}
	seconds, err := claims.Exp.Int64()
	if err != nil || seconds <= 0 {
		return time.Time{}
	}
	return time.Unix(seconds, 0)
}

// persistCodexTokens updates only the rotating OAuth fields in auth.json. The
// final parameter is the previous refresh token and is used to avoid a write
// when the response did not rotate anything.
func persistCodexTokens(path, access, refresh, previousRefresh string) error {
	if path == "" || (access == "" && refresh == previousRefresh) {
		return nil
	}
	data, err := readBoundedFile(path, codexAuthMaxBytes)
	if err != nil {
		return err
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return err
	}
	var tokens map[string]json.RawMessage
	if raw := root["tokens"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &tokens); err != nil {
			return err
		}
	}
	if tokens == nil {
		tokens = make(map[string]json.RawMessage)
	}
	if access != "" {
		tokens["access_token"], _ = json.Marshal(access)
	}
	if refresh != "" && refresh != previousRefresh {
		tokens["refresh_token"], _ = json.Marshal(refresh)
	}
	root["tokens"], _ = json.Marshal(tokens)
	root["last_refresh"], _ = json.Marshal(time.Now().UTC().Format(time.RFC3339Nano))
	updated, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	updated = append(updated, '\n')
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".auth.json.*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(updated); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
