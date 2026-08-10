package config

import (
	"context"
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
	cfg, err := LoadFrom(filepath.Join(t.TempDir(), "absent.toml"))
	if err != nil {
		t.Fatalf("a missing config file must not be an error: %v", err)
	}
	if len(cfg.Providers) != 3 {
		t.Errorf("providers = %d, want the three defaults", len(cfg.Providers))
	}
	if cfg.Path != "" {
		t.Errorf("Path = %q, want empty for defaults", cfg.Path)
	}
}

func TestDefaultModelFollowsCloudKey(t *testing.T) {
	t.Setenv(EnvOllamaKey, "")
	if got := Default().DefaultModel; got != "glm-5.2:cloud@ollama-local" {
		t.Errorf("without a key, default = %q, want the local model", got)
	}
	t.Setenv(EnvOllamaKey, "sk-test")
	if got := Default().DefaultModel; got != "glm-5.2:cloud@ollama-cloud" {
		t.Errorf("with a key, default = %q, want the cloud model", got)
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

func containsAll(text string, values ...string) bool {
	for _, value := range values {
		if !strings.Contains(text, value) {
			return false
		}
	}
	return true
}
