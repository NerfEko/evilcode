package daemon

import (
	"strings"
	"testing"

	"evilcode/internal/config"
)

// R2-12: a credential was persisted before it was validated — an unknown
// provider name landed in the user's config.toml, a key that failed to build a
// client was reported as saved, busy sessions kept their old provider instance
// indefinitely while the UI said "saved", and rebuild errors were swallowed.

func TestSetCredentialRefusesUnknownProviderWithoutPersisting(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()

	sess, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.setCredential("no-such-provider", "k"); err == nil {
		t.Fatal("an unknown provider was accepted")
	} else if !strings.Contains(err.Error(), "no provider named") {
		t.Fatalf("err = %v, want a named refusal", err)
	}
	// The refusal happens before anything reaches the config or the live map.
	if sess.built.Config.Providers[0].APIKey == "k" {
		t.Fatal("the key leaked into the session config")
	}
}

func TestSetCredentialReportsABrokenProviderInsteadOfSaving(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()

	// A provider whose client cannot be built — Codex without its auth file —
	// must refuse the credential instead of saving it.
	srv.Cfg.Providers = append(srv.Cfg.Providers, config.ProviderConfig{
		Name: "broken", Kind: config.KindCodex, AuthFile: "/nonexistent/codex-auth",
	})
	sess, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.setCredential("broken", "k"); err == nil {
		t.Fatal("a provider that cannot build reported the key as saved")
	}
	// The daemon config kept the old (empty) key: nothing was committed.
	if got := srv.Cfg.Providers[len(srv.Cfg.Providers)-1].APIKey; got == "k" {
		t.Fatal("a rejected credential was persisted")
	}
}

func TestBusySessionRefreshesItsCredentialAtTheNextTurnBoundary(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()

	sess, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	target := sess.built.Agent.Provider.Name()

	// A busy session defers the swap instead of silently keeping the old key.
	sess.pendingCredential = target
	before := sess.built.Agent.Provider
	sess.applyPendingCredential()
	if sess.built.Agent.Provider == before {
		t.Fatal("the pending credential was not applied at the boundary")
	}
	if sess.pendingCredential != "" {
		t.Fatal("the pending credential was not consumed")
	}

	// A missing provider is reported rather than applied silently.
	sess.pendingCredential = "gone"
	provider := sess.built.Agent.Provider
	sess.applyPendingCredential()
	if sess.built.Agent.Provider != provider {
		t.Fatal("a missing provider replaced the live one")
	}
	if sess.pendingCredential != "" {
		t.Error("the pending marker survived a failed apply")
	}
}
