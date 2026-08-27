package config

import (
	"context"
	"strings"
	"testing"
	"time"
)

// R2-10: a role's fallback chain was only a build-time fallback — SideCall
// made exactly one network attempt, so authentication failure, rate limits,
// model-not-found, timeouts, and mid-stream errors never tried the next
// configured entry. The chain is now a runtime fallback.

func TestSideCallFallsThroughToTheNextChainEntryAtRuntime(t *testing.T) {
	cfg := Default()
	// An unreachable endpoint: ChatStream fails at connection time.
	cfg.Providers = append(cfg.Providers, ProviderConfig{
		Name: "dead", Kind: "ollama", BaseURL: "http://127.0.0.1:1",
	})
	cfg.Providers = append(cfg.Providers, ProviderConfig{
		Name: "mock", Kind: "mock",
	})
	cfg.Roles.Smol = []string{"m1@unreachable", "m2@mock"}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	got, err := cfg.Router().SideCall(ctx, RoleSmol, "", "say something")
	if err != nil {
		t.Fatalf("the fallback chain did not reach its second entry: %v", err)
	}
	if got == "" {
		t.Fatal("the reachable entry produced no text")
	}
}

// Every tried entry is named in the diagnostic, and a spent context stops the
// walk instead of hammering the rest of the chain.
func TestSideCallAggregatesDiagnosticsAndHonorsTheContext(t *testing.T) {
	cfg := Default()
	cfg.Providers = append(cfg.Providers,
		ProviderConfig{Name: "unreachable", Kind: "ollama", BaseURL: "http://127.0.0.1:1"},
		ProviderConfig{Name: "mock", Kind: "mock"},
	)
	cfg.Roles.Smol = []string{"m1@unreachable", "m2@mock"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := cfg.Router().SideCall(ctx, RoleSmol, "", "hi")
	if err == nil {
		t.Fatal("a dead chain reported success")
	}
	if !strings.Contains(err.Error(), "m1@unreachable") {
		t.Errorf("the diagnostic does not name the first entry: %v", err)
	}
	if !strings.Contains(err.Error(), `role "smol" failed`) {
		t.Errorf("the diagnostic does not name the role: %v", err)
	}
}

func TestSideCallWithNoUsableModelNamesTheRole(t *testing.T) {
	cfg := Default()
	cfg.Providers = nil
	cfg.DefaultModel = ""
	cfg.Roles = Roles{}
	_, err := cfg.Router().SideCall(context.Background(), RoleSmol, "", "hi")
	// RoleChain always yields at least the default model ref, so an empty
	// config surfaces as per-entry diagnostics under the role name.
	if err == nil || !strings.Contains(err.Error(), `role "smol" failed`) {
		t.Fatalf("err = %v, want the role named", err)
	}
}
