package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDiscoverCodexAuthAt(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "auth.json")
	const access = "access-token"
	const refresh = "refresh-token"
	const account = "account-id"
	writeCodexAuthFile(t, path, map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"access_token":  access,
			"refresh_token": refresh,
			"account_id":    account,
			"id_token":      "not-a-secret-for-tests",
		},
	})
	auth, err := DiscoverCodexAuthAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if auth.AccessToken != access || auth.RefreshToken != refresh || auth.AccountID != account {
		t.Fatalf("auth = %+v, want the discovered credential fields", auth)
	}
	if auth.AuthFile != path {
		t.Errorf("auth file = %q, want %q", auth.AuthFile, path)
	}

	wrongMode := filepath.Join(root, "wrong-mode.json")
	writeCodexAuthFile(t, wrongMode, map[string]any{
		"auth_mode": "apiKey",
		"tokens":    map[string]any{"access_token": access, "account_id": account},
	})
	if _, err := DiscoverCodexAuthAt(wrongMode); !errors.Is(err, ErrCodexAuthNotFound) {
		t.Errorf("wrong auth mode error = %v, want ErrCodexAuthNotFound", err)
	}
	if _, err := DiscoverCodexAuthAt(filepath.Join(root, "missing.json")); !errors.Is(err, ErrCodexAuthNotFound) {
		t.Errorf("missing auth error = %v, want ErrCodexAuthNotFound", err)
	}

	tooLarge := filepath.Join(root, "too-large.json")
	if err := os.WriteFile(tooLarge, make([]byte, codexAuthMaxBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := DiscoverCodexAuthAt(tooLarge); err == nil {
		t.Fatal("oversized auth file unexpectedly accepted")
	}
}

func TestDiscoverCodexAuthAccountIDFallbacks(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "id-token.json")
	idToken := testJWT(map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "from-id-token"},
	})
	writeCodexAuthFile(t, path, map[string]any{
		"auth_mode": "chatgptAuthTokens",
		"tokens": map[string]any{
			"access_token":  "opaque-access",
			"refresh_token": "opaque-refresh",
			"id_token":      idToken,
		},
	})
	auth, err := DiscoverCodexAuthAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if auth.AccountID != "from-id-token" {
		t.Errorf("account id = %q, want id-token claim", auth.AccountID)
	}

	accessPath := filepath.Join(root, "access-token.json")
	access := testJWT(map[string]any{"chatgpt_account_id": "from-access-token"})
	writeCodexAuthFile(t, accessPath, map[string]any{
		"tokens": map[string]any{"access_token": access, "refresh_token": "refresh"},
	})
	auth, err = DiscoverCodexAuthAt(accessPath)
	if err != nil {
		t.Fatal(err)
	}
	if auth.AccountID != "from-access-token" {
		t.Errorf("account id = %q, want access-token claim", auth.AccountID)
	}
}

func TestNormalizeCodexBaseURL(t *testing.T) {
	for _, tc := range []struct {
		input, want string
	}{
		{"https://chatgpt.com", DefaultCodexBaseURL},
		{"https://chatgpt.com/backend-api", DefaultCodexBaseURL},
		{"https://chatgpt.com/codex", DefaultCodexBaseURL},
		{"https://chatgpt.com/backend-api/codex/", DefaultCodexBaseURL},
		{"https://chatgpt.com.evil", "https://chatgpt.com.evil"},
		{"http://127.0.0.1:12345/custom", "http://127.0.0.1:12345/custom"},
	} {
		if got := normalizeCodexBaseURL(tc.input); got != tc.want {
			t.Errorf("normalizeCodexBaseURL(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestCodexChatStreamMapsResponsesAndSSE(t *testing.T) {
	const token = "access-secret"
	const account = "account-secret"
	var mu sync.Mutex
	var requestBody map[string]any
	var requestHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		mu.Lock()
		_ = json.Unmarshal(body, &requestBody)
		requestHeaders = r.Header.Clone()
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"id\":\"item_1\",\"call_id\":\"call_1\",\"name\":\"read\",\"arguments\":\"\"}}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"item_1\",\"delta\":\"{\\\"path\\\":\\\"x\\\"}\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.reasoning_summary_text.delta\",\"delta\":\"think\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"id\":\"item_1\",\"call_id\":\"call_1\",\"name\":\"read\",\"arguments\":\"{\\\"path\\\":\\\"x\\\"}\"}}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"id\":\"item_2\",\"call_id\":\"call_2\",\"name\":\"write\",\"arguments\":\"\"}}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.function_call_arguments.done\",\"item_id\":\"item_2\",\"call_id\":\"call_2\",\"name\":\"write\",\"arguments\":\"{\\\"ok\\\":true}\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":12,\"output_tokens\":4,\"input_tokens_details\":{\"cached_tokens\":3}}}}\n\n")
	}))
	defer server.Close()

	c := NewCodex("codex", server.URL, CodexAuthInfo{AccessToken: token, AccountID: account})
	c.HTTP = server.Client()
	stream, err := c.ChatStream(context.Background(), Req{
		Model: "gpt-5.3-codex",
		Messages: []Message{
			{Role: RoleSystem, Content: "be concise"},
			{Role: RoleUser, Content: "read x"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "old-call", Name: "read", Args: json.RawMessage(`{"path":"old"}`)}}},
			{Role: RoleTool, ToolCallID: "old-call", Content: "file contents"},
		},
		Tools: []ToolDef{{Name: "read", Desc: "read a file", Schema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var text, reasoning string
	var done Chunk
	for chunk := range stream {
		if chunk.Err != nil {
			t.Fatalf("stream error: %v", chunk.Err)
		}
		text += chunk.Text
		reasoning += chunk.Reasoning
		if chunk.Done {
			done = chunk
		}
	}
	if text != "hello" || reasoning != "think" {
		t.Errorf("text/reasoning = %q/%q", text, reasoning)
	}
	if !done.Done || len(done.ToolCalls) != 2 || string(done.ToolCalls[0].Args) != `{"path":"x"}` || string(done.ToolCalls[1].Args) != `{"ok":true}` {
		t.Fatalf("done = %+v, want the assembled function calls", done)
	}
	if done.ToolCalls[0].ID != "call_1" || done.ToolCalls[0].Name != "read" {
		t.Errorf("tool call = %+v", done.ToolCalls[0])
	}
	if done.ToolCalls[1].ID != "call_2" || done.ToolCalls[1].Name != "write" {
		t.Errorf("second tool call = %+v", done.ToolCalls[1])
	}
	if done.Usage == nil || done.Usage.PromptTokens != 12 || done.Usage.CompletionTokens != 4 || done.Usage.CacheReadTokens != 3 {
		t.Errorf("usage = %+v", done.Usage)
	}

	mu.Lock()
	body := requestBody
	headers := requestHeaders
	mu.Unlock()
	if headers.Get("Authorization") != "Bearer "+token {
		t.Errorf("authorization header = %q", headers.Get("Authorization"))
	}
	if headers.Get("ChatGPT-Account-Id") != account || headers.Get("OAI-Product-Sku") != "codex" || headers.Get("Originator") != "codex_cli_rs" {
		t.Errorf("Codex headers = account %q, sku %q, originator %q", headers.Get("ChatGPT-Account-Id"), headers.Get("OAI-Product-Sku"), headers.Get("Originator"))
	}
	if body["model"] != "gpt-5.3-codex" || body["instructions"] != "be concise" || body["stream"] != true {
		t.Errorf("request envelope = %+v", body)
	}
	input, ok := body["input"].([]any)
	if !ok || len(input) != 3 {
		t.Fatalf("input = %#v, want three mapped items plus instructions", body["input"])
	}
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %#v", body["tools"])
	}
}

func TestCodexModelsAndRefresh(t *testing.T) {
	const account = "account-id"
	const refresh = "refresh-secret"
	const rotated = "rotated-refresh"
	const fresh = "fresh-access"
	expired := testJWT(map[string]any{"exp": time.Now().Add(-time.Hour).Unix()})
	var refreshBody map[string]any
	var modelHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &refreshBody)
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"access_token":"fresh-access","refresh_token":"rotated-refresh"}`)
		case "/models":
			modelHeaders = r.Header.Clone()
			if r.URL.Query().Get("client_version") != codexClientVersion {
				t.Errorf("client_version = %q", r.URL.Query().Get("client_version"))
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"models":[{"slug":"gpt-5.3-codex","context_window":131072,"input_modalities":["text","image"]},{"id":"fallback","context_window":null}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("CODEX_REFRESH_TOKEN_URL_OVERRIDE", server.URL+"/oauth/token")
	c := NewCodex("codex", server.URL, CodexAuthInfo{AccessToken: expired, RefreshToken: refresh, AccountID: account})
	c.HTTP = server.Client()
	models, err := c.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].Name != "gpt-5.3-codex" || !models[0].Vision || models[0].ContextWindow != 131072 || models[1].Name != "fallback" {
		t.Fatalf("models = %+v", models)
	}
	if refreshBody["grant_type"] != "refresh_token" || refreshBody["refresh_token"] != refresh || refreshBody["client_id"] != defaultCodexClientID {
		t.Errorf("refresh body = %+v", refreshBody)
	}
	if modelHeaders.Get("Authorization") != "Bearer "+fresh {
		t.Errorf("models authorization = %q", modelHeaders.Get("Authorization"))
	}
	if c.refreshToken != rotated {
		t.Errorf("refresh token = %q, want rotated token", c.refreshToken)
	}
}

func writeCodexAuthFile(t *testing.T, path string, value map[string]any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func testJWT(claims map[string]any) string {
	encode := func(value []byte) string { return base64.RawURLEncoding.EncodeToString(value) }
	header, _ := json.Marshal(map[string]string{"alg": "none", "typ": "JWT"})
	payload, _ := json.Marshal(claims)
	return strings.Join([]string{encode(header), encode(payload), "signature"}, ".")
}
