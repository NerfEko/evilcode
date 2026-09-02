package config

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"evilcode/internal/provider"
)

func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadMissingFileUsesDefaults(t *testing.T) {
	t.Setenv(EnvOllamaKey, "")
	path := filepath.Join(t.TempDir(), "absent.toml")
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("a missing config file must not be an error: %v", err)
	}
	if len(cfg.Providers) != 3 {
		t.Errorf("providers = %d, want the three defaults", len(cfg.Providers))
	}
	if cfg.Path != path {
		t.Errorf("Path = %q, want %q — the path must be recorded even for a missing file, so a daemon can start refreshing last_model once the file appears (A4)", cfg.Path, path)
	}
}

func TestDefaultModelFollowsCloudKey(t *testing.T) {
	t.Setenv(EnvOllamaKey, "")
	if got := Default().DefaultModel; got != "deepseek-v4-flash:0731@ollama-local" {
		t.Errorf("without a key, default = %q, want the local model", got)
	}
	t.Setenv(EnvOllamaKey, "sk-test")
	if got := Default().DefaultModel; got != "deepseek-v4-flash:0731@ollama-cloud" {
		t.Errorf("with a key, default = %q, want the cloud model", got)
	}
}

func TestBraveSearchAPIKeyPrefersDedicatedEnvironmentName(t *testing.T) {
	t.Setenv(EnvBraveSearchKey, "search-key")
	t.Setenv(EnvBraveKey, "short-key")
	if got := BraveSearchAPIKey(); got != "search-key" {
		t.Errorf("BraveSearchAPIKey() = %q, want the dedicated key", got)
	}
}

func TestBraveSearchAPIKeyAcceptsShortAlias(t *testing.T) {
	t.Setenv(EnvBraveSearchKey, "")
	t.Setenv(EnvBraveKey, "short-key")
	if got := BraveSearchAPIKey(); got != "short-key" {
		t.Errorf("BraveSearchAPIKey() = %q, want the short alias", got)
	}
}

func TestConfigBraveSearchAPIKeyPrefersEnvironmentOverSavedValue(t *testing.T) {
	t.Setenv(EnvBraveSearchKey, "env-key")
	t.Setenv(EnvBraveKey, "")
	cfg := &Config{Web: WebConfig{BraveAPIKey: "file-key"}}
	if got := cfg.BraveSearchAPIKey(); got != "env-key" {
		t.Errorf("Config.BraveSearchAPIKey() = %q, want the environment key", got)
	}

	t.Setenv(EnvBraveSearchKey, "")
	if got := cfg.BraveSearchAPIKey(); got != "file-key" {
		t.Errorf("Config.BraveSearchAPIKey() = %q, want the saved key", got)
	}
}

func TestConfigOllamaSessionCookiePrefersEnvironmentOverSavedValue(t *testing.T) {
	t.Setenv(EnvOllamaSessionCookie, "env-cookie")
	cfg := &Config{Web: WebConfig{OllamaSessionCookie: "file-cookie"}}
	if got := cfg.OllamaSessionCookie(); got != "env-cookie" {
		t.Errorf("Config.OllamaSessionCookie() = %q, want the environment cookie", got)
	}

	t.Setenv(EnvOllamaSessionCookie, "")
	if got := cfg.OllamaSessionCookie(); got != "file-cookie" {
		t.Errorf("Config.OllamaSessionCookie() = %q, want the saved cookie", got)
	}
}

func TestPartialConfigKeepsDefaults(t *testing.T) {
	// The regression this guards: booleans that default to true must survive a
	// config file that never mentions them.
	t.Setenv(EnvOllamaKey, "")
	path := write(t, `default_model = "mistral@ollama-local"`)
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultModel != "mistral@ollama-local" {
		t.Errorf("DefaultModel = %q", cfg.DefaultModel)
	}
	if !cfg.Display.KeybindingHints {
		t.Error("keybinding_hints defaults to true and was not set in the file")
	}
	if !cfg.Display.IdleAnimation {
		t.Error("idle_animation defaults to true and was not set in the file")
	}
	if !cfg.Features.AutoPoke {
		t.Error("auto_poke defaults to true and was not set in the file")
	}
	if cfg.Features.SkillRetrieval {
		t.Error("skill_retrieval defaults to false and was not set in the file")
	}
	if cfg.Display.Theme != "catppuccin-frappe" {
		t.Errorf("theme = %q, want the default", cfg.Display.Theme)
	}
	if len(cfg.Providers) != 3 {
		t.Errorf("providers = %d, want the defaults kept", len(cfg.Providers))
	}
}

func TestExplicitFalseIsHonored(t *testing.T) {
	t.Setenv(EnvOllamaKey, "")
	path := write(t, `
default_model = "m@ollama-local"
[display]
keybinding_hints = false
[features]
auto_poke = false
skill_retrieval = true
`)
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Display.KeybindingHints {
		t.Error("an explicit false must win over the default true")
	}
	if cfg.Features.AutoPoke {
		t.Error("an explicit false must win over the default true")
	}
	if !cfg.Features.SkillRetrieval {
		t.Error("an explicit skill_retrieval true must be honored")
	}
}

func TestProvidersReplaceDefaults(t *testing.T) {
	t.Setenv(EnvOllamaKey, "")
	path := write(t, `
default_model = "gpt-4o@openrouter"

[[provider]]
name = "openrouter"
kind = "openai"
base_url = "https://openrouter.ai/api"
api_key_env = "OPENROUTER_KEY"
`)
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Providers) != 1 || cfg.Providers[0].Name != "openrouter" {
		t.Fatalf("providers = %+v, want only the configured one", cfg.Providers)
	}
	if cfg.Providers[0].Kind != KindOpenAI {
		t.Errorf("kind = %q", cfg.Providers[0].Kind)
	}
}

func TestEnvModelOverridesFile(t *testing.T) {
	t.Setenv(EnvOllamaKey, "")
	path := write(t, `default_model = "from-file@ollama-local"`)
	t.Setenv(EnvModel, "from-env@ollama-local")
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultModel != "from-env@ollama-local" {
		t.Errorf("DefaultModel = %q, want the env override to win", cfg.DefaultModel)
	}
}

func TestMockProviderEnvRegistersItself(t *testing.T) {
	t.Setenv(EnvOllamaKey, "")
	t.Setenv(EnvProvider, "mock")
	cfg, err := LoadFrom(filepath.Join(t.TempDir(), "absent.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.findProvider("mock") == nil {
		t.Fatal("EVILCODE_PROVIDER=mock must register a mock provider")
	}
	p, model, err := cfg.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "mock" || model != "mock-large" {
		t.Errorf("resolved to %s/%s", p.Name(), model)
	}
}

func TestSplitModelRef(t *testing.T) {
	tests := []struct{ ref, model, prov string }{
		{"qwen3-coder:480b-cloud@ollama-cloud", "qwen3-coder:480b-cloud", "ollama-cloud"},
		{"mistral", "mistral", ""},
		// A model name can itself contain @, so the split must take the last one.
		{"org/model@v2@openrouter", "org/model@v2", "openrouter"},
	}
	for _, tt := range tests {
		model, prov := SplitModelRef(tt.ref)
		if model != tt.model || prov != tt.prov {
			t.Errorf("SplitModelRef(%q) = %q, %q; want %q, %q", tt.ref, model, prov, tt.model, tt.prov)
		}
	}
}

// TestModelRefRoundTrip checks that ModelRef rebuilds what SplitModelRef took
// apart, which is what makes a remembered model resolve back to the same
// provider on resume.
func TestModelRefRoundTrip(t *testing.T) {
	tests := []struct{ ref, model, prov string }{
		{"qwen3-coder:480b-cloud@ollama-cloud", "qwen3-coder:480b-cloud", "ollama-cloud"},
		{"org/model@v2@openrouter", "org/model@v2", "openrouter"},
		{"mistral", "mistral", ""},
	}
	for _, tt := range tests {
		got := ModelRef(tt.model, tt.prov)
		if got != tt.ref {
			t.Errorf("ModelRef(%q, %q) = %q, want %q", tt.model, tt.prov, got, tt.ref)
		}
		// A bare name with no provider round-trips to itself, not to "name@".
		if tt.prov == "" && got != tt.model {
			t.Errorf("ModelRef(%q, \"\") = %q, want the bare name", tt.model, got)
		}
	}
}

func TestResolveBuildsTheRightClient(t *testing.T) {
	t.Setenv(EnvOllamaKey, "sk-cloud")
	cfg := Default()
	p, model, err := cfg.Resolve("qwen3-coder:480b-cloud@ollama-cloud")
	if err != nil {
		t.Fatal(err)
	}
	if model != "qwen3-coder:480b-cloud" {
		t.Errorf("model = %q", model)
	}
	if p.Name() != "ollama-cloud" {
		t.Errorf("provider = %q", p.Name())
	}
}

func TestResolveBareRefUsesFirstProvider(t *testing.T) {
	t.Setenv(EnvOllamaKey, "")
	cfg := Default()
	p, model, err := cfg.Resolve("mistral")
	if err != nil {
		t.Fatal(err)
	}
	if model != "mistral" || p.Name() != cfg.Providers[0].Name {
		t.Errorf("resolved to %s/%s", p.Name(), model)
	}
}

func TestResolveUnknownProvider(t *testing.T) {
	t.Setenv(EnvOllamaKey, "")
	if _, _, err := Default().Resolve("m@nope"); err == nil {
		t.Error("want an error for an unknown provider")
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		ok   bool
	}{
		{"no providers", Config{DefaultModel: "m"}, false},
		{"unnamed provider", Config{DefaultModel: "m", Providers: []ProviderConfig{{Kind: KindOllama}}}, false},
		{"missing kind", Config{DefaultModel: "m", Providers: []ProviderConfig{{Name: "a"}}}, false},
		{"unknown kind", Config{DefaultModel: "m", Providers: []ProviderConfig{{Name: "a", Kind: "carrier-pigeon"}}}, false},
		{"duplicate names", Config{DefaultModel: "m", Providers: []ProviderConfig{
			{Name: "a", Kind: KindOllama}, {Name: "a", Kind: KindOllama},
		}}, false},
		{"no default model", Config{Providers: []ProviderConfig{{Name: "a", Kind: KindOllama}}}, false},
		{"default names unknown provider", Config{DefaultModel: "m@ghost", Providers: []ProviderConfig{
			{Name: "a", Kind: KindOllama},
		}}, false},
		{"valid", Config{DefaultModel: "m@a", Providers: []ProviderConfig{{Name: "a", Kind: KindOllama}}}, true},
		{"valid deepseek", Config{DefaultModel: "m@deepseek", Providers: []ProviderConfig{
			{Name: "deepseek", Kind: KindDeepSeek},
		}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.ok && err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
			if !tt.ok && err == nil {
				t.Error("Validate() = nil, want an error")
			}
		})
	}
}

func TestValidateAggregatesProblemsWithTOMLPaths(t *testing.T) {
	cfg := Config{
		DefaultModel: "@",
		LastModel:    "model@",
		Providers: []ProviderConfig{
			{Name: "", Kind: "", BaseURL: "not a url", APIKeyEnv: "BAD-NAME"},
			{Name: "a", Kind: KindOllama, BaseURL: "https://"},
			{Name: "a", Kind: KindCodex, APIKeyEnv: "CODEX_KEY"},
		},
		Models: []ModelConfig{
			{Name: "m@a", ContextWindow: -1},
			{Name: "m@a", ContextWindow: maxConfigContextWindow + 1},
		},
		Roles:            Roles{Smol: []string{"@a", "x@ghost"}},
		ReasoningEfforts: map[string]string{"m": "bogus"},
		Display: Display{
			Theme:           "unknown",
			Overscroll:      "unknown",
			ThinkingDisplay: "unknown",
			ThinkingLines:   -1,
		},
		Features: Features{
			MaxSteps:       -1,
			EnvPassthrough: []string{"BAD-NAME", "PATH", "PATH"},
		},
		Keybindings: map[string]string{
			"not_an_action": "ctrl+q",
			"scroll_up":     "ctrl+j",
		},
		MCP: []MCPServer{
			{Name: "server", Command: "echo"},
			{Name: "server", Command: " "},
		},
		LSP: map[string][]string{"go": {" "}},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want aggregate diagnostics")
	}
	validation, ok := err.(*ValidationErrors)
	if !ok {
		t.Fatalf("Validate() error = %T, want *ValidationErrors: %v", err, err)
	}
	if len(validation.Problems) < 15 {
		t.Fatalf("got %d validation problems, want the independent invalid fields collected: %v", len(validation.Problems), validation.Problems)
	}
	for _, path := range []string{
		"default_model", "last_model", "provider[0].base_url", "provider[0].api_key_env",
		"provider[1].base_url", "provider[2].name", "provider[2]", "model[0].context_window",
		"model[1].name", "model[1].context_window", "roles.smol[0]", "roles.smol[1]",
		"reasoning_efforts.m", "display.theme", "display.overscroll", "display.thinking_display",
		"display.thinking_lines", "features.env_passthrough[0]", "features.env_passthrough[2]",
		"features.max_steps", "keybindings.not_an_action", "keybindings.scroll_up", "mcp[1].name",
		"mcp[1].command", "lsp.go",
	} {
		if !strings.Contains(err.Error(), path) {
			t.Errorf("aggregate error does not name %s: %v", path, err)
		}
	}
}

func TestValidateAcceptsACompleteConfig(t *testing.T) {
	cfg := Config{
		DefaultModel: "model@openai",
		Providers: []ProviderConfig{{
			Name: "openai", Kind: KindOpenAI, BaseURL: "https://api.example.test/v1", APIKeyEnv: "OPENAI_API_KEY",
		}},
		Models:           []ModelConfig{{Name: "model@openai", ContextWindow: 4096}},
		Roles:            Roles{Default: []string{"model@openai"}, Smol: []string{"small@openai"}},
		ReasoningEfforts: map[string]string{"model@openai": "high"},
		Display:          Display{Theme: "catppuccin-frappe", Overscroll: "overscroll", ThinkingDisplay: "current", ThinkingLines: 6},
		Features:         Features{EnvPassthrough: []string{"PATH", "HOME"}, MaxSteps: 10},
		Keybindings:      map[string]string{"scroll_up": "ctrl+shift+k"},
		MCP:              []MCPServer{{Name: "docs", Command: "mcp-docs", Env: []string{"MODE=stdio"}}},
		LSP:              map[string][]string{"go": {"gopls"}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("complete config rejected: %v", err)
	}
}

func TestValidateAllowsStaleConvenienceReferences(t *testing.T) {
	cfg := Config{
		DefaultModel:   "live@provider",
		LastModel:      "old@removed-provider",
		FavoriteModels: []string{"old@removed-provider"},
		Models:         []ModelConfig{{Name: "old@removed-provider", ContextWindow: 4096}},
		Providers:      []ProviderConfig{{Name: "provider", Kind: KindMock}},
		Features:       Features{EmbeddingModel: "embed@removed-provider"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("stale convenience references must not block startup: %v", err)
	}
}

func TestValidateAllowsIntentionalIntegrationDisables(t *testing.T) {
	cfg := Config{
		DefaultModel: "m@mock",
		Providers:    []ProviderConfig{{Name: "mock", Kind: KindMock}},
		Keybindings:  map[string]string{"scroll_up": ""},
		LSP:          map[string][]string{"go": nil},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("intentional key/LSP disables must validate: %v", err)
	}
}

func TestValidateProviderURLs(t *testing.T) {
	for _, value := range []string{
		"http://localhost", "https://example.test/api/", "http://[::1]:11434",
	} {
		if err := validateProviderURL(value); err != "" {
			t.Errorf("validateProviderURL(%q) = %q, want valid", value, err)
		}
	}
	for _, value := range []string{
		"localhost:11434", "ftp://example.test", "https://", "https://user:pass@example.test",
		"https://example.test:0", "https://example.test:65536", "https://example.test?token=x",
		"https://example.test#fragment", "https://example.test:bad",
	} {
		if err := validateProviderURL(value); err == "" {
			t.Errorf("validateProviderURL(%q) = empty, want invalid", value)
		}
	}
}

func TestLoadRepoOverridesValidatesTheEffectiveConfig(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, RepoConfigName), []byte(`
[roles]
smol = ["small@missing"]
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Default().LoadRepoOverrides(root); err == nil || !strings.Contains(err.Error(), "roles.smol[0]") {
		t.Fatalf("repo override validation error = %v, want the role path", err)
	}
}

func TestMCPTimeoutSecondsRoundTripsAndValidates(t *testing.T) {
	path := write(t, `
default_model = "m@a"

[[provider]]
name = "a"
kind = "ollama"
base_url = "http://localhost:11434"

[[mcp]]
name = "docs"
command = "mcp-docs"
timeout_seconds = 45
`)
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.MCP) != 1 || cfg.MCP[0].TimeoutSeconds != 45 {
		t.Fatalf("TimeoutSeconds = %+v, want 45", cfg.MCP)
	}

	cfg.MCP[0].TimeoutSeconds = -1
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "mcp[0].timeout_seconds") {
		t.Errorf("negative timeout_seconds accepted: %v", err)
	}
	cfg.MCP[0].TimeoutSeconds = maxMCPCallSeconds + 1
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "mcp[0].timeout_seconds") {
		t.Errorf("oversized timeout_seconds accepted: %v", err)
	}
	cfg.MCP[0].TimeoutSeconds = maxMCPCallSeconds
	if err := cfg.Validate(); err != nil {
		t.Errorf("valid timeout_seconds rejected: %v", err)
	}
}

func TestBadTOMLIsAnError(t *testing.T) {
	path := write(t, "default_model = [unclosed")
	if _, err := LoadFrom(path); err == nil {
		t.Error("want a parse error")
	}
}

func TestAPIKeyPrefersEnv(t *testing.T) {
	t.Setenv("MY_KEY", "from-env")
	p := ProviderConfig{APIKeyEnv: "MY_KEY", APIKey: "from-file"}
	if got := p.APIKeyValue(); got != "from-env" {
		t.Errorf("APIKeyValue() = %q, want the env value", got)
	}

	t.Setenv("MY_KEY", "")
	if got := p.APIKeyValue(); got != "from-file" {
		t.Errorf("APIKeyValue() = %q, want the file value when the env var is empty", got)
	}
}

func TestModelOverrides(t *testing.T) {
	cfg := &Config{Models: []ModelConfig{
		{Name: "qwen3-coder:480b-cloud", ContextWindow: 262144, AnchorEdits: true},
		{Name: "tinyllama@ollama-local", LenientToolParse: true},
	}}
	if got := cfg.ModelOverrides("qwen3-coder:480b-cloud@ollama-cloud"); got.ContextWindow != 262144 || !got.AnchorEdits {
		t.Errorf("overrides = %+v, want the bare name to match a ref carrying a provider", got)
	}
	if got := cfg.ModelOverrides("tinyllama@ollama-local"); !got.LenientToolParse {
		t.Errorf("overrides = %+v", got)
	}
	if got := cfg.ModelOverrides("unknown@x"); got.ContextWindow != 0 || got.AnchorEdits {
		t.Errorf("overrides = %+v, want the zero value", got)
	}
}

func TestRoleChainFallsBackToDefaultModel(t *testing.T) {
	cfg := &Config{
		DefaultModel: "big@p",
		Roles:        Roles{Smol: []string{"small@p", "backup@p"}},
	}
	if got := cfg.RoleChain("smol"); len(got) != 2 || got[0] != "small@p" {
		t.Errorf("smol chain = %v", got)
	}
	for _, role := range []string{"plan", "commit", "default", "anything-else"} {
		got := cfg.RoleChain(role)
		if len(got) != 1 || got[0] != "big@p" {
			t.Errorf("%s chain = %v, want the default model", role, got)
		}
	}
}

func TestXDGPaths(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg/cfg")
	t.Setenv("XDG_DATA_HOME", "/xdg/data")
	if got := ConfigDir(); got != "/xdg/cfg/evilcode" {
		t.Errorf("ConfigDir() = %q", got)
	}
	if got := DataDir(); got != "/xdg/data/evilcode" {
		t.Errorf("DataDir() = %q", got)
	}

	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", "/home/tester")
	if got := ConfigDir(); got != "/home/tester/.config/evilcode" {
		t.Errorf("ConfigDir() = %q", got)
	}
	if got := DataDir(); got != "/home/tester/.local/share/evilcode" {
		t.Errorf("DataDir() = %q", got)
	}
}

func TestFullConfigRoundTrips(t *testing.T) {
	// A config exercising every block, as a check that the struct tags match
	// the documented TOML spelling.
	t.Setenv(EnvOllamaKey, "")
	path := write(t, `
default_model = "qwen3-coder:480b-cloud@ollama-cloud"

[[provider]]
name = "ollama-cloud"
kind = "ollama"
base_url = "https://ollama.com"
api_key_env = "OLLAMA_API_KEY"

[[model]]
name = "qwen3-coder:480b-cloud"
context_window = 262144
anchor_edits = true

[roles]
smol = ["qwen3:8b@ollama-cloud"]
plan = ["deepseek-v3.1:671b-cloud@ollama-cloud"]

[display]
centered = true
theme = "nosferatu"
overscroll = "off"

[features]
auto_poke = true
memory = true

[keybindings]
scroll_up = "ctrl+shift+k"
`)
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Display.Theme != "nosferatu" || !cfg.Display.Centered || cfg.Display.Overscroll != "off" {
		t.Errorf("display = %+v", cfg.Display)
	}
	if !cfg.Features.Memory || !cfg.Features.AutoPoke {
		t.Errorf("features = %+v", cfg.Features)
	}
	if got := cfg.RoleChain("smol"); len(got) != 1 || got[0] != "qwen3:8b@ollama-cloud" {
		t.Errorf("smol = %v", got)
	}
	if cfg.Keybindings["scroll_up"] != "ctrl+shift+k" {
		t.Errorf("keybindings = %v", cfg.Keybindings)
	}
	if o := cfg.ModelOverrides("qwen3-coder:480b-cloud@ollama-cloud"); o.ContextWindow != 262144 {
		t.Errorf("model overrides = %+v", o)
	}
	if cfg.Path != path {
		t.Errorf("Path = %q, want %q", cfg.Path, path)
	}
}

func TestRepoModelBlocksOverrideGlobalEntries(t *testing.T) {
	// R2-09: a repo [[model]] block with the same name must replace the
	// global one — the resolution order (first match) used to hand the win to
	// the global entry merely because it was declared first.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, RepoConfigName), []byte(`
[[model]]
name = "glm-5.3@ollama-cloud"
context_window = 123456
anchor_edits = true
`), 0o644)

	cfg := Default()
	cfg.Models = append(cfg.Models, ModelConfig{Name: "glm-5.3@ollama-cloud", ContextWindow: 4096})
	if err := cfg.LoadRepoOverrides(dir); err != nil {
		t.Fatal(err)
	}
	got := cfg.ModelOverrides("glm-5.3@ollama-cloud")
	if got.ContextWindow != 123456 || !got.AnchorEdits {
		t.Fatalf("overrides = %+v, want the repo block to replace the global one", got)
	}

	// A new name appends, and the qualified-beats-bare rule is untouched.
	os.WriteFile(filepath.Join(dir, RepoConfigName), []byte(`
[[model]]
name = "repo-only"
lenient_tool_parse = true
`), 0o644)
	cfg2 := Default()
	cfg2.Models = append(cfg2.Models, ModelConfig{Name: "glm-5.3@ollama-cloud", ContextWindow: 4096})
	if err := cfg2.LoadRepoOverrides(dir); err != nil {
		t.Fatal(err)
	}
	if got := cfg2.ModelOverrides("repo-only@ollama-cloud"); !got.LenientToolParse {
		t.Errorf("a repo-only bare entry did not resolve: %+v", got)
	}
	if got := cfg2.ModelOverrides("glm-5.3@ollama-cloud"); got.ContextWindow != 4096 {
		t.Errorf("an unrelated global entry changed: %+v", got)
	}
}

func TestRepoOverridesAreNarrow(t *testing.T) {
	// A repo may pin its roles and default model, but checking out a
	// repository must never be able to redirect the user's API keys.
	t.Setenv(EnvOllamaKey, "")
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, RepoConfigName), []byte(`
default_model = "repo-model@ollama-local"

[roles]
smol = ["tiny@ollama-local"]

[[provider]]
name = "evil"
kind = "openai"
base_url = "https://attacker.example"
`), 0o644)

	cfg := Default()
	before := len(cfg.Providers)
	if err := cfg.LoadRepoOverrides(dir); err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultModel != "repo-model@ollama-local" {
		t.Errorf("DefaultModel = %q, want the repo override", cfg.DefaultModel)
	}
	if got := cfg.RoleChain("smol"); len(got) != 1 || got[0] != "tiny@ollama-local" {
		t.Errorf("smol chain = %v", got)
	}
	if len(cfg.Providers) != before {
		t.Errorf("providers = %d, want %d — a repo must not add providers",
			len(cfg.Providers), before)
	}
	for _, p := range cfg.Providers {
		if p.Name == "evil" {
			t.Fatal("a repo config injected a provider")
		}
	}
}

func TestRepoOverridesMissingFileIsFine(t *testing.T) {
	cfg := Default()
	if err := cfg.LoadRepoOverrides(t.TempDir()); err != nil {
		t.Errorf("a missing repo config is not an error: %v", err)
	}
	if err := cfg.LoadRepoOverrides(""); err != nil {
		t.Errorf("an empty root is not an error: %v", err)
	}
}

func TestRouterFallsThroughTheChain(t *testing.T) {
	t.Setenv(EnvOllamaKey, "")
	cfg := &Config{
		DefaultModel: "big@real",
		Providers:    []ProviderConfig{{Name: "real", Kind: KindMock}},
		// The first entry names a provider that does not exist; the router
		// should degrade to the next rather than failing the call.
		Roles: Roles{Smol: []string{"tiny@ghost", "small@real"}},
	}
	got, err := cfg.Router().For(RoleSmol)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "small" || got.Provider.Name() != "real" {
		t.Errorf("resolved to %s@%s", got.Model, got.Provider.Name())
	}
}

func TestRouterUnusableChain(t *testing.T) {
	cfg := &Config{
		DefaultModel: "big@real",
		Providers:    []ProviderConfig{{Name: "real", Kind: KindMock}},
		Roles:        Roles{Smol: []string{"a@ghost", "b@phantom"}},
	}
	if _, err := cfg.Router().For(RoleSmol); err == nil {
		t.Error("want an error when nothing in the chain resolves")
	}
}

func TestRouterSideCall(t *testing.T) {
	cfg := &Config{
		DefaultModel: "mock-large@mock",
		Providers:    []ProviderConfig{{Name: "mock", Kind: KindMock}},
	}
	got, err := cfg.Router().SideCall(context.Background(), RoleSmol, "be terse", "summarize")
	if err != nil {
		t.Fatal(err)
	}
	if got == "" {
		t.Error("side call returned nothing")
	}
}

func TestOverridesApplyToTheResolvedModel(t *testing.T) {
	// The bug this guards: looking overrides up by the -m flag means a session
	// relying on default_model gets none of them, silently.
	t.Setenv(EnvOllamaKey, "")
	cfg := &Config{
		DefaultModel: "big:cloud@p",
		Providers:    []ProviderConfig{{Name: "p", Kind: KindMock}},
		Models:       []ModelConfig{{Name: "big:cloud", AnchorEdits: true, ContextWindow: 262144}},
	}

	_, modelName, err := cfg.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.ModelOverrides(modelName); !got.AnchorEdits || got.ContextWindow != 262144 {
		t.Errorf("overrides for the resolved model = %+v, want them applied", got)
	}
	// The empty flag is what used to be passed, and finds nothing.
	if got := cfg.ModelOverrides(""); got.AnchorEdits {
		t.Error("an empty ref should not match an override; that is the bug")
	}
}

func TestSaveProviderAPIKeyPreservesUnknownTOMLAndUses0600(t *testing.T) {
	path := write(t, `default_model = "glm@ollama-cloud"
unknown_setting = "keep me"

[[provider]]
name = "ollama-cloud"
kind = "ollama"
base_url = "https://ollama.com"
api_key_env = "OLLAMA_API_KEY"
api_key = "old"

[[provider]]
name = "future"
kind = "mock"
future_setting = "also keep"
`)
	t.Setenv(EnvConfigPath, path)
	t.Setenv(EnvOllamaKey, "")
	key := "sk-test-secret"
	if err := SaveProviderAPIKey("ollama-cloud", key); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !containsAll(text, "unknown_setting = \"keep me\"", "future_setting = \"also keep\"") {
		t.Fatalf("writer dropped unknown TOML: %s", text)
	}
	if !containsAll(text, "api_key = \""+key+"\"") {
		t.Fatalf("writer did not update provider key: %s", text)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("config mode = %o, want 0600", info.Mode().Perm())
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Providers[0].APIKeyValue(); got != key {
		t.Fatalf("loaded key = %q, want saved key", got)
	}
}

func TestSaveBraveSearchAPIKeyPreservesUnknownTOMLAndUses0600(t *testing.T) {
	path := write(t, `default_model = "m@ollama-local"
unknown_setting = "keep me"

[[provider]]
name = "ollama-local"
kind = "ollama"
base_url = "http://localhost:11434"

[web]
brave_api_key = "old"

[future]
future_setting = "also keep"
`)
	t.Setenv(EnvConfigPath, path)
	t.Setenv(EnvBraveSearchKey, "")
	t.Setenv(EnvBraveKey, "")
	key := "brave-test-secret"
	if err := SaveBraveSearchAPIKey(key); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !containsAll(text, "unknown_setting = \"keep me\"", "future_setting = \"also keep\"", "brave_api_key = \""+key+"\"") {
		t.Fatalf("writer dropped or failed to update TOML: %s", text)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("config mode = %o, want 0600", info.Mode().Perm())
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.BraveSearchAPIKey(); got != key {
		t.Fatalf("loaded Brave key = %q, want saved key", got)
	}
}

func TestSaveOllamaSessionCookiePreservesUnknownTOMLAndUses0600(t *testing.T) {
	path := write(t, `default_model = "m@ollama-local"
unknown_setting = "keep me"

[[provider]]
name = "ollama-local"
kind = "ollama"
base_url = "http://localhost:11434"

[web]
brave_api_key = "keep-me-too"

[future]
future_setting = "also keep"
`)
	t.Setenv(EnvConfigPath, path)
	t.Setenv(EnvOllamaSessionCookie, "")
	cookie := "session-secret"
	if err := SaveOllamaSessionCookie(cookie); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !containsAll(text, "unknown_setting = \"keep me\"", "future_setting = \"also keep\"",
		"brave_api_key = \"keep-me-too\"", "ollama_session_cookie = \""+cookie+"\"") {
		t.Fatalf("writer dropped or failed to update TOML: %s", text)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("config mode = %o, want 0600", info.Mode().Perm())
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.OllamaSessionCookie(); got != cookie {
		t.Fatalf("loaded cookie = %q, want saved cookie", got)
	}
}

func TestSaveProviderAPIKeyOnAFreshMachineStillLoads(t *testing.T) {
	// /login on a machine with no config file used to write a lone
	// [[provider]] stub. An explicit provider array replaces the defaults, so
	// the next launch died on `default_model names unknown provider
	// "ollama-local"` — the login bricked the install.
	path := filepath.Join(t.TempDir(), "config.toml")
	t.Setenv(EnvConfigPath, path)
	t.Setenv(EnvOllamaKey, "")
	if err := SaveProviderAPIKey("ollama-cloud", "sk-fresh"); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("config written by /login does not load: %v", err)
	}
	if len(cfg.Providers) != len(Default().Providers) {
		t.Fatalf("providers = %+v, want the defaults kept", cfg.Providers)
	}
	i := providerIndex(cfg.Providers, "ollama-cloud")
	if i < 0 {
		t.Fatal("ollama-cloud missing")
	}
	if got := cfg.Providers[i].APIKeyValue(); got != "sk-fresh" {
		t.Errorf("key = %q, want the saved one", got)
	}
	if cfg.Providers[i].BaseURL != Default().Providers[1].BaseURL {
		t.Errorf("base_url = %q, want the default seeded in", cfg.Providers[i].BaseURL)
	}
}

func TestSaveProviderAPIKeyLeavesAnExplicitProviderListAlone(t *testing.T) {
	// A file that already names providers is replacing the defaults on purpose;
	// adding one must not drag the defaults back in behind the user's back.
	path := write(t, `default_model = "m@mine"

[[provider]]
name = "mine"
kind = "ollama"
base_url = "http://localhost:1"
`)
	t.Setenv(EnvConfigPath, path)
	t.Setenv(EnvOllamaKey, "")
	if err := SaveProviderAPIKey("ollama-cloud", "sk-added"); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Providers) != 2 || providerIndex(cfg.Providers, "mine") < 0 {
		t.Fatalf("providers = %+v, want only mine + ollama-cloud", cfg.Providers)
	}
}

func TestSaveModelPrefsReplacesAndPreserves(t *testing.T) {
	path := write(t, `# my config
unknown_setting = "keep me"

default_model = "old@ollama-local"

[[provider]]
name = "ollama-local"
kind = "ollama"
base_url = "http://localhost:11434"

[[provider]]
name = "ollama-cloud"
kind = "ollama"
base_url = "https://ollama.com"
api_key_env = "OLLAMA_API_KEY"
`)
	t.Setenv(EnvConfigPath, path)
	t.Setenv(EnvOllamaKey, "")
	if err := SaveModelPrefs([]string{"m1@ollama-local", "m2@ollama-cloud"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !containsAll(text, "unknown_setting = \"keep me\"", "# my config",
		"name = \"ollama-local\"", "api_key_env = \"OLLAMA_API_KEY\"") {
		t.Fatalf("writer dropped unrelated TOML: %s", text)
	}
	if !containsAll(text, "favorite_models = [\"m1@ollama-local\", \"m2@ollama-cloud\"]") {
		t.Fatalf("writer did not update the favorites: %s", text)
	}
	// The picker no longer writes default_model (Ctrl+O is gone): the existing
	// line must survive byte for byte as the deep fallback it was.
	if !containsAll(text, "default_model = \"old@ollama-local\"") {
		t.Fatalf("writer clobbered the existing default_model: %s", text)
	}
	// The replaced key must not linger as a duplicate inside the tables.
	if strings.Count(text, "favorite_models") != 1 {
		t.Fatalf("old keys not removed: %s", text)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultModel != "old@ollama-local" {
		t.Errorf("default = %q, want the untouched old@ollama-local", cfg.DefaultModel)
	}
	if len(cfg.FavoriteModels) != 2 || cfg.FavoriteModels[1] != "m2@ollama-cloud" {
		t.Errorf("favorites = %v, want the saved two", cfg.FavoriteModels)
	}
}

func TestSaveModelPrefsOnAFreshMachineStillLoads(t *testing.T) {
	// Ctrl+N on a machine with no config file must write a file the next launch
	// can load, and must not drag the defaults in (there is nothing to replace).
	// With no default_model written, a fresh launch still resolves a model from
	// Default()'s own cloud/local choice — the picker no longer pins one.
	path := filepath.Join(t.TempDir(), "config.toml")
	t.Setenv(EnvConfigPath, path)
	t.Setenv(EnvOllamaKey, "")
	if err := SaveModelPrefs([]string{"glm-5.2:cloud@ollama-cloud"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("config written by the picker does not load: %v", err)
	}
	if len(cfg.FavoriteModels) != 1 || cfg.FavoriteModels[0] != "glm-5.2:cloud@ollama-cloud" {
		t.Errorf("favorites = %v, want the saved one", cfg.FavoriteModels)
	}
}

func TestSavePersistentModelStatePreservesAndReloads(t *testing.T) {
	path := write(t, `# keep this comment
default_model = "mistral@ollama-local"
unknown_setting = "keep me"

[[provider]]
name = "ollama-local"
kind = "ollama"
base_url = "http://localhost:11434"
`)
	t.Setenv(EnvConfigPath, path)
	t.Setenv(EnvOllamaKey, "")

	if err := SaveLastModel("gpt-5.6-luna@openai"); err != nil {
		t.Fatal(err)
	}
	if err := SaveReasoningEffort("gpt-5.6-luna@openai", provider.ReasoningEffortMax); err != nil {
		t.Fatal(err)
	}
	if err := SaveReasoningEffort("mistral@ollama-local", provider.ReasoningEffortLow); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !containsAll(text, "# keep this comment", "unknown_setting = \"keep me\"",
		"last_model = \"gpt-5.6-luna@openai\"",
		"reasoning_efforts = {\"gpt-5.6-luna@openai\" = \"max\", \"mistral@ollama-local\" = \"low\"}") {
		t.Fatalf("persistent state writer dropped or missed config: %s", text)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LastModel != "gpt-5.6-luna@openai" {
		t.Errorf("last model = %q, want gpt-5.6-luna@openai", cfg.LastModel)
	}
	if got := cfg.ReasoningEffortFor("gpt-5.6-luna@openai"); got != provider.ReasoningEffortMax {
		t.Errorf("Luna effort = %q, want max", got)
	}
	if got := cfg.ReasoningEffortFor("mistral@ollama-local"); got != provider.ReasoningEffortLow {
		t.Errorf("Mistral effort = %q, want low", got)
	}
}

func TestConfigCloneCopiesReasoningEfforts(t *testing.T) {
	original := &Config{ReasoningEfforts: map[string]string{"m@mock": "high"}}
	clone := original.Clone()
	clone.ReasoningEfforts["m@mock"] = "low"
	if original.ReasoningEfforts["m@mock"] != "high" {
		t.Fatal("Clone shares the reasoning effort map with the original")
	}
}

func TestSaveModelPrefsEmptyFavoritesRemovesTheKey(t *testing.T) {
	path := write(t, `default_model = "a@ollama-local"
favorite_models = ["a@ollama-local", "b@ollama-local"]
`)
	t.Setenv(EnvConfigPath, path)
	t.Setenv(EnvOllamaKey, "")
	if err := SaveModelPrefs(nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "favorite_models") {
		t.Fatalf("empty favorites should drop the key, got: %s", data)
	}
	if !strings.Contains(string(data), "default_model = \"a@ollama-local\"") {
		t.Fatalf("empty favorites must leave default_model alone, got: %s", data)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.FavoriteModels) != 0 {
		t.Errorf("favorites = %v, want none after clearing", cfg.FavoriteModels)
	}
	if cfg.DefaultModel != "a@ollama-local" {
		t.Errorf("default = %q, want the untouched a@ollama-local", cfg.DefaultModel)
	}
}

func TestConfiguredCloudKeyRoutesToTheCloud(t *testing.T) {
	// The symptom this guards: /login saves the key into the config file, but
	// Default() picks the route before that file is decoded and could only read
	// the environment — so logging in still sent every turn through the local
	// daemon.
	t.Setenv(EnvOllamaKey, "")
	path := write(t, `
[[provider]]
name = "ollama-local"
kind = "ollama"
base_url = "http://localhost:11434"

[[provider]]
name = "ollama-cloud"
kind = "ollama"
base_url = "https://ollama.com"
api_key = "sk-in-the-file"
`)
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultModel != DefaultCloudModel {
		t.Errorf("default_model = %q, want %q", cfg.DefaultModel, DefaultCloudModel)
	}
}

func TestNoKeyAnywhereStaysLocal(t *testing.T) {
	t.Setenv(EnvOllamaKey, "")
	cfg, err := LoadFrom(filepath.Join(t.TempDir(), "absent.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultModel != DefaultLocalModel {
		t.Errorf("default_model = %q, want %q — ollama.com is unreachable without a key",
			cfg.DefaultModel, DefaultLocalModel)
	}
}

func TestFileDefaultModelBeatsTheKeyHeuristic(t *testing.T) {
	// Re-deciding the route must only fill in a choice the file did not make.
	t.Setenv(EnvOllamaKey, "sk-env")
	path := write(t, `default_model = "gpt-oss:20b@ollama-local"`)
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultModel != "gpt-oss:20b@ollama-local" {
		t.Errorf("default_model = %q, want the file's own choice", cfg.DefaultModel)
	}
}

func TestDeepSeekIsAFirstClassKind(t *testing.T) {
	// DeepSeek is a listed provider with its own kind, not a bare
	// OpenAI-compatible key. The wire protocol is OpenAI's, so Build serves it
	// with the OpenAI client — but the config kind, base URL, and key env are
	// DeepSeek's own.
	t.Setenv(EnvOllamaKey, "")
	d := Default()
	i := providerIndex(d.Providers, "deepseek")
	if i < 0 {
		t.Fatal("default config has no deepseek provider")
	}
	p := d.Providers[i]
	if p.Kind != KindDeepSeek {
		t.Errorf("deepseek kind = %q, want %q", p.Kind, KindDeepSeek)
	}
	if p.BaseURL != "https://api.deepseek.com" || p.APIKeyEnv != EnvDeepSeekKey {
		t.Errorf("deepseek = %+v, want the deepseek base URL and key env", p)
	}
	if err := d.Validate(); err != nil {
		t.Fatalf("default config does not validate: %v", err)
	}
	built, err := p.Build()
	if err != nil {
		t.Fatal(err)
	}
	if built.Name() != "deepseek" {
		t.Errorf("built provider name = %q", built.Name())
	}
	if _, ok := built.(*provider.OpenAI); !ok {
		t.Errorf("deepseek built as %T, want the OpenAI client (same wire protocol)", built)
	}
}

func TestCodexDiscoveryAddsProviderOnce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	authPath := filepath.Join(home, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"access","refresh_token":"refresh","account_id":"account"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Default()
	before := len(cfg.Providers)
	if !cfg.AddDiscoveredCodex() {
		t.Fatal("AddDiscoveredCodex() = false, want a discovered account")
	}
	if len(cfg.Providers) != before+1 {
		t.Fatalf("providers = %d, want %d", len(cfg.Providers), before+1)
	}
	pc := cfg.FindProvider("codex")
	if pc == nil || pc.Kind != KindCodex || pc.AuthFile != authPath || pc.BaseURL != provider.DefaultCodexBaseURL {
		t.Fatalf("discovered provider = %+v", pc)
	}
	if !cfg.AddDiscoveredCodex() || len(cfg.Providers) != before+1 {
		t.Fatalf("second discovery changed provider list: %+v", cfg.Providers)
	}
}

func TestLoadDiscoversCodexForDefaultModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"access","refresh_token":"refresh","account_id":"account"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	path := write(t, `default_model = "gpt-5.3-codex@codex"`)
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FindProvider("codex") == nil {
		t.Fatal("@codex default was validated before discovery")
	}
	prov, model, err := cfg.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if model != "gpt-5.3-codex" || prov.Name() != "codex" {
		t.Errorf("resolved to %s@%s", model, prov.Name())
	}
}

func TestCodexProviderRejectsAPIKeyConfig(t *testing.T) {
	cfg := Config{
		DefaultModel: "gpt-5.3-codex@codex",
		Providers:    []ProviderConfig{{Name: "codex", Kind: KindCodex, APIKeyEnv: "CODEX_KEY"}},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Codex API-key configuration unexpectedly validated")
	}
}

// A config written before DeepSeek became its own kind used kind = "openai".
// It must keep loading and building, not break an existing setup.
func TestLegacyDeepSeekAsOpenAIKindStillWorks(t *testing.T) {
	t.Setenv(EnvOllamaKey, "")
	path := write(t, `default_model = "deepseek-chat@deepseek"

[[provider]]
name = "deepseek"
kind = "openai"
base_url = "https://api.deepseek.com"
api_key_env = "DEEPSEEK_API_KEY"
`)
	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatal(err)
	}
	pc := cfg.FindProvider("deepseek")
	if pc == nil {
		t.Fatal("deepseek provider missing")
	}
	if _, err := pc.Build(); err != nil {
		t.Errorf("legacy openai-kind deepseek does not build: %v", err)
	}
}

func TestContextWindowForPrefersAnExplicitOverride(t *testing.T) {
	// A hand-written [[model]] context_window is a deliberate correction of what
	// the provider claims, so discovery must not undo it — and must not spend a
	// request finding that out.
	if got := ContextWindowFor(nil, "any", 4096); got != 4096 {
		t.Errorf("ContextWindowFor = %d, want the override", got)
	}
}

func TestContextLimitsForClampsCodexOAuthGPT56CatalogWindow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-5.6-luna","context_window":1050000}]}`))
	}))
	defer server.Close()

	codex := provider.NewCodex("codex", server.URL, provider.CodexAuthInfo{
		AccessToken: "access-token",
		AccountID:   "account-id",
	})
	codex.HTTP = server.Client()
	limits := ContextLimitsFor(codex, "gpt-5.6-luna", 0)
	if limits.ContextWindow != 400_000 || limits.InputLimit != 272_000 || limits.OutputLimit != 128_000 {
		t.Errorf("ContextLimitsFor = %+v, want OpenCode's Codex OAuth limits", limits)
	}
	if got := ContextWindowFor(codex, "gpt-5.6-luna", 0); got != 400_000 {
		t.Errorf("ContextWindowFor = %d, want the OpenCode Codex OAuth window", got)
	}
}

func containsAll(text string, values ...string) bool {
	for _, value := range values {
		if !strings.Contains(text, value) {
			return false
		}
	}
	return true
}
