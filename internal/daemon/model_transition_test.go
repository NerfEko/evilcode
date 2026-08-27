package daemon

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"evilcode/internal/config"
	"evilcode/internal/provider"
)

// transitionServer builds a daemon server over a config the test may mutate,
// mirroring testServer's hermetic environment.
func transitionServer(t *testing.T, mutate func(*config.Config)) *Server {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("HOME", home)
	t.Setenv("EVILCODE_PROVIDER", "mock")
	t.Setenv("EVILCODE_SCENARIO", "chat")
	t.Setenv("EVILCODE_CONFIG", filepath.Join(home, "nonexistent.toml"))

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(cfg)
	}
	srv := NewServer(cfg, t.TempDir(), "")
	t.Cleanup(srv.Close)
	return srv
}

// A switch whose reasoning heuristic misses asks the provider's /api/show for
// the model's advertised efforts. That lookup is network-shaped: an endpoint
// that never answers must delay one bounded switch, not hold controlMu and
// stall every model control operation and snapshot with it (R2-13). The
// cleanup releases the parked handlers, since Server.Close waits for
// outstanding requests the canceled clients no longer answer for.
func TestModelSwitchSurvivesAnUnresponsiveOllamaEndpoint(t *testing.T) {
	release := make(chan struct{})
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	t.Cleanup(func() { close(release); hang.Close() })

	srv := transitionServer(t, func(cfg *config.Config) {
		cfg.Providers = append(cfg.Providers, config.ProviderConfig{
			Name:    "ollama-hang",
			Kind:    config.KindOllama,
			BaseURL: hang.URL,
		})
	})

	sess, err := srv.OpenWithOptions("", OpenOptions{Model: "mock-small@mock"})
	if err != nil {
		t.Fatal(err)
	}
	// A model name outside every known thinking family, so the switch must
	// consult the (hung) endpoint for reasoning levels.
	start := time.Now()
	if err := sess.SetModel("plainbird-7b@ollama-hang"); err != nil {
		t.Fatalf("switch against a hung endpoint failed: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("switch took %s; the /api/show lookup is not bounded", elapsed)
	}
	if sess.Model != "plainbird-7b" || sess.built.Agent.Provider.Name() != "ollama-hang" {
		t.Fatalf("switch did not commit: model %q provider %q", sess.Model, sess.built.Agent.Provider.Name())
	}
}

// A dedicated embedding model is a build-time decision (R2-11): switching the
// chat model must not re-couple semantic memory or compaction to the chat
// provider, and the vector-space identity must survive the switch unchanged.
func TestModelSwitchKeepsTheDedicatedEmbeddingBackend(t *testing.T) {
	srv := transitionServer(t, func(cfg *config.Config) {
		cfg.Features.EmbeddingModel = "embedder@other"
		cfg.Providers = append(cfg.Providers, config.ProviderConfig{Name: "other", Kind: config.KindMock})
	})

	sess, err := srv.OpenWithOptions("", OpenOptions{Model: "mock-small@mock"})
	if err != nil {
		t.Fatal(err)
	}
	if sess.built.Memory == nil {
		t.Fatal("no memory manager was built")
	}
	if got := sess.built.Memory.EmbeddingModel; got != "embedder@other" {
		t.Fatalf("build identity = %q, want embedder@other", got)
	}
	if err := sess.SetModel("mock-large@mock"); err != nil {
		t.Fatal(err)
	}
	if got := sess.built.Memory.EmbeddingModel; got != "embedder@other" {
		t.Errorf("post-switch identity = %q, want embedder@other; the switch re-coupled semantic memory to the chat choice", got)
	}
	ep, ok := sess.built.Memory.Embedder.(provider.Provider)
	if !ok || ep.Name() != "other" {
		t.Errorf("post-switch embedder = %T, want the dedicated embedding backend", sess.built.Memory.Embedder)
	}
	c := sess.built.Agent.Compactor
	if c == nil || c.Embedding == nil {
		t.Fatal("compaction lost its embedder across the switch")
	}
	if cp, ok := c.Embedding.(provider.Provider); !ok || cp.Name() != "other" {
		t.Errorf("compactor embedder = %T, want the dedicated backend, not the chat provider", c.Embedding)
	}
}

// Build wiring stamps per-model overrides onto the agent; a runtime switch has
// to carry them too, or the new model runs with the previous model's tool-parse
// and context limits (R2-13).
func TestModelSwitchCarriesThePerModelOverrides(t *testing.T) {
	srv := transitionServer(t, func(cfg *config.Config) {
		// Load seeds a default block for mock-large@mock; the test edits that
		// block the way a [[model]] table in config.toml would.
		for i := range cfg.Models {
			if cfg.Models[i].Name == "mock-large@mock" {
				cfg.Models[i].ContextWindow = 12345
				cfg.Models[i].LenientToolParse = true
			}
		}
	})

	sess, err := srv.OpenWithOptions("", OpenOptions{Model: "mock-small@mock"})
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.SetModel("mock-large@mock"); err != nil {
		t.Fatal(err)
	}
	if !sess.built.Agent.LenientToolParse {
		t.Error("LenientToolParse override did not follow the switch")
	}
	if sess.built.Agent.NumCtx != 12345 {
		t.Errorf("NumCtx = %d, want the override 12345", sess.built.Agent.NumCtx)
	}
}
