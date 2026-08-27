package wiring

import (
	"strings"
	"testing"

	"evilcode/internal/config"
)

// R2-11: semantic memory and compaction relevance used to ride whatever
// provider the CHAT model resolved to, so choosing a chat provider without an
// embeddings endpoint silently degraded memory to lexical-only while the UI
// presented semantic memory as enabled. A dedicated embedding model decouples
// the vector space from the chat choice.

func TestDedicatedEmbeddingModelResolvesIndependently(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = append(cfg.Providers, config.ProviderConfig{Name: "mock", Kind: config.KindMock})
	cfg.Features.EmbeddingModel = "embedder@mock"
	out, err := Build(cfg, Options{Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(out.Close)
	if out.EmbeddingProvider == nil {
		t.Fatal("the dedicated embedding provider was not resolved")
	}
	if out.EmbeddingID != "embedder@mock" {
		t.Fatalf("EmbeddingID = %q, want the configured reference", out.EmbeddingID)
	}
	// The memory manager's vector-space identity follows the actual embedder.
	if out.Memory != nil && !strings.Contains(out.Memory.EmbeddingModel, "embedder@mock") {
		t.Errorf("memory identity = %q, want the dedicated reference", out.Memory.EmbeddingModel)
	}
}

func TestUnknownEmbeddingModelDisablesSemanticsInsteadOfFailing(t *testing.T) {
	cfg := config.Default()
	cfg.Features.EmbeddingModel = "m@no-such-provider"
	out, err := Build(cfg, Options{Cwd: t.TempDir()})
	if err != nil {
		t.Fatalf("an unresolvable embedding model failed the build: %v", err)
	}
	t.Cleanup(out.Close)
	if out.EmbeddingProvider != nil {
		t.Fatal("an unresolvable embedding model produced an embedder anyway")
	}
}

// The default (no dedicated model) keeps the legacy coupling: the chat
// provider is the embedder and the identity names it.
func TestLegacyEmbeddingCouplingIsExplicit(t *testing.T) {
	cfg := config.Default()
	out, err := Build(cfg, Options{Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(out.Close)
	if out.EmbeddingProvider != nil || out.EmbeddingID != "" {
		t.Fatalf("embedding backend = %v/%q, want the chat-provider default",
			out.EmbeddingProvider, out.EmbeddingID)
	}
}
