package config

import (
	"context"
	"slices"
	"testing"

	"evilcode/internal/provider"
)

// OpenCode Go is a first-class kind like DeepSeek: its own kind name, base
// URL, key env var, and model catalogue, served over the OpenAI-compatible
// wire by the specialized transport.
func TestOpenCodeGoIsAFirstClassKind(t *testing.T) {
	t.Setenv(EnvOllamaKey, "")
	t.Setenv(EnvOpenCodeGoKey, "")
	d := Default()
	i := providerIndex(d.Providers, "opencode-go")
	if i < 0 {
		t.Fatal("default config has no opencode-go provider")
	}
	p := d.Providers[i]
	if p.Kind != KindOpenCodeGo {
		t.Errorf("opencode-go kind = %q, want %q", p.Kind, KindOpenCodeGo)
	}
	if p.BaseURL != provider.DefaultOpenCodeGoBaseURL || p.APIKeyEnv != EnvOpenCodeGoKey {
		t.Errorf("opencode-go = %+v, want the gateway base URL and key env", p)
	}
	if err := d.Validate(); err != nil {
		t.Fatalf("default config does not validate: %v", err)
	}
	built, err := p.Build()
	if err != nil {
		t.Fatal(err)
	}
	if built.Name() != "opencode-go" {
		t.Errorf("built provider name = %q", built.Name())
	}
	ocg, ok := built.(*provider.OpenCodeGo)
	if !ok {
		t.Fatalf("opencode-go built as %T, want the OpenCodeGo client", built)
	}
	if ocg.BaseURL != provider.DefaultOpenCodeGoBaseURL {
		t.Errorf("built base URL = %q, want %q", ocg.BaseURL, provider.DefaultOpenCodeGoBaseURL)
	}
	if ocg.SessionHeader != "x-opencode-session" || ocg.UserAgent == "" {
		t.Errorf("built transport = %+v, want the gateway's session header and user agent", ocg.OpenAI)
	}
}

// A configured opencode-go key is the only working cloud route, so the
// default model routes there; an ollama-cloud key keeps precedence it already
// had.
func TestPreferredDefaultModelRoutesToOpenCodeGo(t *testing.T) {
	t.Setenv(EnvOllamaKey, "")
	t.Setenv(EnvOpenCodeGoKey, "sk-opencode")

	cfg := Default()
	if got := cfg.DefaultModel; got != DefaultOpenCodeGoModel {
		t.Errorf("default model = %q, want %q", got, DefaultOpenCodeGoModel)
	}
	if got := cfg.ReasoningEffortFor(DefaultOpenCodeGoModel); got != provider.ReasoningEffortHigh {
		t.Errorf("opencode-go default effort = %q, want high", got)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("routed default does not validate: %v", err)
	}

	// The Ollama cloud route keeps its place when both keys exist.
	t.Setenv(EnvOllamaKey, "sk-ollama")
	cfg = Default()
	if got := cfg.DefaultModel; got != DefaultCloudModel {
		t.Errorf("default model with both keys = %q, want %q", got, DefaultCloudModel)
	}

	// Without either key the local route stands, exactly as before.
	t.Setenv(EnvOpenCodeGoKey, "")
	t.Setenv(EnvOllamaKey, "")
	cfg = Default()
	if got := cfg.DefaultModel; got != DefaultLocalModel {
		t.Errorf("default model without keys = %q, want %q", got, DefaultLocalModel)
	}

	// A user file may drop the provider; a key alone must not route to a
	// provider the file does not list.
	cfg = Default()
	cfg.Providers = slices.Delete(cfg.Providers, 1, len(cfg.Providers))[:1] // keep ollama-local only
	cfg.DefaultModel = ""
	if got := cfg.preferredDefaultModel(); got != DefaultLocalModel {
		t.Errorf("default model without the provider entry = %q, want %q", got, DefaultLocalModel)
	}
}

// The bundled catalogue resolves a model's window for the context meter
// without any discovery request.
func TestContextLimitsForOpenCodeGo(t *testing.T) {
	cfg := Default()
	p := cfg.Providers[providerIndex(cfg.Providers, "opencode-go")]
	prov, err := p.Build()
	if err != nil {
		t.Fatal(err)
	}
	if got := ContextLimitsFor(prov, "glm-5.3-flash", 0).ContextWindow; got != 1000000 {
		t.Errorf("glm-5.3-flash window = %d, want 1000000", got)
	}
	if got := ContextLimitsFor(prov, "brand-new-model", 0).ContextWindow; got != 0 {
		t.Errorf("unknown model window = %d, want 0", got)
	}
	// An explicit [[model]] override still wins.
	if got := ContextLimitsFor(prov, "glm-5.3-flash", 200000).ContextWindow; got != 200000 {
		t.Errorf("override window = %d, want 200000", got)
	}
}

// Saving a key through /login targets the default provider section, and the
// saved config loads and routes to the gateway.
func TestSaveOpenCodeGoKeyWritesLoadableConfig(t *testing.T) {
	path := t.TempDir() + "/config.toml"
	t.Setenv(EnvConfigPath, path)
	t.Setenv(EnvOllamaKey, "")
	t.Setenv(EnvOpenCodeGoKey, "")

	if err := SaveProviderAPIKey("opencode-go", "sk-test"); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("saved config does not load: %v", err)
	}
	if cfg.DefaultModel != DefaultOpenCodeGoModel {
		t.Errorf("default model after key = %q, want %q", cfg.DefaultModel, DefaultOpenCodeGoModel)
	}
	p := cfg.FindProvider("opencode-go")
	if p == nil || p.APIKey != "sk-test" {
		t.Fatalf("opencode-go provider = %+v, want the saved key", p)
	}
	if _, err := p.Build(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cfg.Resolve(cfg.DefaultModel); err != nil {
		t.Fatalf("default model does not resolve: %v", err)
	}
}

// The default model must resolve through Resolve the same way the agent does.
func TestResolveOpenCodeGoDefaultModel(t *testing.T) {
	t.Setenv(EnvOpenCodeGoKey, "sk-opencode")
	cfg := Default()
	prov, model, err := cfg.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if model != "glm-5.3-flash" {
		t.Errorf("resolved model = %q, want glm-5.3-flash", model)
	}
	if _, ok := prov.(*provider.OpenCodeGo); !ok {
		t.Fatalf("resolved provider = %T, want the OpenCodeGo client", prov)
	}
	infos, err := prov.Models(context.Background()) // live fetch will fail; bundled catalogue stands in
	if err != nil || len(infos) == 0 {
		t.Fatalf("bundled catalogue: %v (%d models)", err, len(infos))
	}
}
