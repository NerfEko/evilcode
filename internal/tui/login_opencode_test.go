package tui

import (
	"os"
	"testing"

	tea "charm.land/bubbletea/v2"

	"evilcode/internal/agent"
	"evilcode/internal/config"
	"evilcode/internal/provider"
)

// The default config carries an opencode-go provider, so /login must offer it
// in the selector and accept `/login opencode-go` straight away: enter the
// key, and the saved key must reach both the config file and the live client
// without a restart.
func TestLoginOpenCodeGoDefaultProvider(t *testing.T) {
	// A config path that does not exist means Load() falls back to the pure
	// defaults, which include opencode-go with its key env var.
	path := os.TempDir() + "/evilcode-login-opencode-absent.toml"
	_ = os.Remove(path)
	t.Cleanup(func() { _ = os.Remove(path) })
	t.Setenv(config.EnvConfigPath, path)
	t.Setenv(config.EnvOllamaKey, "")
	t.Setenv(config.EnvOpenCodeGoKey, "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	pc := cfg.FindProvider("opencode-go")
	if pc == nil {
		t.Fatal("defaults have no opencode-go provider")
	}
	live, err := pc.Build()
	if err != nil {
		t.Fatal(err)
	}
	a := agent.New("s", live, "glm-5.3-flash", nil, agent.NewConversation("system"))
	m := NewModel(a, HeaderState{SessionName: "s", Model: "glm-5.3-flash", Provider: "opencode-go"})

	if _, cmd := m.runCommandWithArg("login", "opencode-go"); cmd != nil {
		t.Fatal("/login opencode-go should not return a command")
	}
	if !m.loginMode || m.loginProvider != "opencode-go" {
		t.Fatalf("loginMode=%v loginProvider=%q, want opencode-go", m.loginMode, m.loginProvider)
	}

	secret := "sk-opencode-live"
	m.editor.Text = secret
	m.editor.Cursor = len([]rune(secret))
	m.handleLoginKey("enter", tea.KeyPressMsg{})

	if m.loginMode || m.editor.Text != "" {
		t.Fatalf("login did not clear state: mode=%v text=%q", m.loginMode, m.editor.Text)
	}
	if got := live.(*provider.OpenCodeGo).OpenAI.APIKey; got != secret {
		t.Errorf("live client APIKey = %q, want %q", got, secret)
	}

	reloaded, err := config.Load()
	if err != nil {
		t.Fatalf("saved config does not load: %v", err)
	}
	saved := reloaded.FindProvider("opencode-go")
	if saved == nil || saved.APIKeyValue() != secret {
		t.Fatalf("reloaded opencode-go = %+v, want the saved key", saved)
	}
}
