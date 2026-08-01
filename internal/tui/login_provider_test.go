package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"evilcode/internal/agent"
	"evilcode/internal/config"
	"evilcode/internal/provider"
)

// writeLoginConfig writes a config with an ollama-cloud and a deepseek
// provider, returns its path. Both keys come from the environment, which the
// caller clears so the "no key" state is deterministic.
func writeLoginConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(`default_model = "deepseek-chat@deepseek"

[[provider]]
name = "ollama-local"
kind = "ollama"
base_url = "http://localhost:11434"

[[provider]]
name = "ollama-cloud"
kind = "ollama"
base_url = "https://ollama.com"
api_key_env = "OLLAMA_API_KEY"

[[provider]]
name = "deepseek"
kind = "deepseek"
base_url = "https://api.deepseek.com"
api_key_env = "DEEPSEEK_API_KEY"
`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvConfigPath, path)
	t.Setenv(config.EnvOllamaKey, "")
	t.Setenv(config.EnvDeepSeekKey, "")
	return path
}

// /login deepseek targets the deepseek provider, persists the entered key, and
// updates the live *provider.OpenAI client's APIKey so the next turn uses it
// without a restart.
func TestLoginProviderArgPersistsAndUpdatesOpenAIProvider(t *testing.T) {
	path := writeLoginConfig(t)
	prov := provider.NewOpenAI("deepseek", "https://api.deepseek.com", "")
	a := agent.New("s", prov, "deepseek-chat", nil, agent.NewConversation("system"))
	m := NewModel(a, HeaderState{SessionName: "s", Model: "deepseek-chat", Provider: "deepseek"})

	if _, cmd := m.runCommandWithArg("login", "deepseek"); cmd != nil {
		t.Fatal("/login deepseek should not return a command")
	}
	if !m.loginMode || m.loginProvider != "deepseek" {
		t.Fatalf("loginMode=%v loginProvider=%q, want deepseek", m.loginMode, m.loginProvider)
	}
	if !strings.Contains(m.notice, "deepseek") {
		t.Fatalf("notice = %q, want it to name deepseek", m.notice)
	}

	secret := "sk-deepseek-live"
	m.editor.Text = secret
	m.editor.Cursor = len([]rune(secret))
	m.handleLoginKey("enter", tea.KeyPressMsg{})

	if m.loginMode || m.editor.Text != "" || m.loginProvider != "" {
		t.Fatalf("login did not clear state: mode=%v text=%q provider=%q",
			m.loginMode, m.editor.Text, m.loginProvider)
	}
	if strings.Contains(m.notice, secret) {
		t.Fatalf("notice leaked the secret: %q", m.notice)
	}

	// Persisted to the deepseek block in the config file.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), secret) {
		t.Fatal("login did not persist the key to the config file")
	}

	// Live OpenAI provider updated in place.
	if got := prov.APIKey; got != secret {
		t.Errorf("live provider APIKey = %q, want %q", got, secret)
	}

	// And reloading the config resolves the saved key.
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	pc := cfg.FindProvider("deepseek")
	if pc == nil || pc.APIKeyValue() != secret {
		t.Errorf("reloaded deepseek key = %q, want %q", pc, secret)
	}
}

// /login status <provider> reports presence without printing the key.
func TestLoginStatusForNamedProvider(t *testing.T) {
	writeLoginConfig(t)
	m := &Model{}
	m.runCommandWithArg("login", "status deepseek")
	if !strings.Contains(m.notice, "deepseek") || strings.Contains(m.notice, "no key") == false {
		t.Fatalf("status with no key = %q, want deepseek + no key configured", m.notice)
	}

	t.Setenv(config.EnvDeepSeekKey, "sk-env")
	m.runCommandWithArg("login", "status deepseek")
	if !strings.Contains(m.notice, "key present") {
		t.Fatalf("status with a key = %q, want key present", m.notice)
	}
	if strings.Contains(m.notice, "sk-env") {
		t.Fatalf("status printed the secret: %q", m.notice)
	}
}

// Saving a key through /login must reach the in-memory provider list the model
// picker fetches from, so the DeepSeek catalog shows up without a restart.
func TestLoginSavesKeyToInMemoryProviders(t *testing.T) {
	writeLoginConfig(t)
	m := &Model{providers: []config.ProviderConfig{
		{Name: "deepseek", Kind: config.KindDeepSeek, BaseURL: "https://api.deepseek.com", APIKeyEnv: config.EnvDeepSeekKey},
	}}
	if got := m.providers[0].APIKeyValue(); got != "" {
		t.Fatalf("initial deepseek key = %q, want empty", got)
	}

	m.loginProvider = "deepseek"
	m.editor.Text = "sk-new"
	m.editor.Cursor = len([]rune(m.editor.Text))
	m.handleLoginKey("enter", tea.KeyPressMsg{})

	if got := m.providers[0].APIKeyValue(); got != "sk-new" {
		t.Fatalf("in-memory deepseek key after /login = %q, want %q", got, "sk-new")
	}
}

// /login with an unknown provider name is rejected without entering login mode.
func TestLoginRejectsUnknownProvider(t *testing.T) {
	writeLoginConfig(t)
	m := &Model{}
	m.runCommandWithArg("login", "ghost")
	if m.loginMode {
		t.Fatal("/login ghost entered login mode for a non-existent provider")
	}
	if !strings.Contains(m.notice, "unknown provider") && !strings.Contains(m.notice, "usage") {
		t.Fatalf("notice = %q, want a usage/unknown-provider message", m.notice)
	}
}

// /login with no argument opens the provider selector rather than silently
// defaulting to ollama-cloud, so the user can choose which key they are
// entering. `/login <provider>` still goes straight into key entry.
func TestLoginNoArgOpensProviderSelector(t *testing.T) {
	writeLoginConfig(t)
	m := &Model{}
	m.runCommandWithArg("login", "")
	if m.loginMode {
		t.Fatalf("loginMode=%v, want false until a provider is picked", m.loginMode)
	}
	if !m.loginPickerOpen {
		t.Fatal("/login with no arg did not open the provider selector")
	}
	names := make(map[string]bool)
	for _, e := range m.loginPicker.Entries {
		names[e.Name] = true
	}
	if !names["ollama-cloud"] || !names["deepseek"] {
		t.Fatalf("selector entries = %v, want both ollama-cloud and deepseek", m.loginPicker.Entries)
	}
	if names["ollama-local"] {
		t.Fatal("ollama-local (no api_key_env) should be filtered out of the login selector")
	}

	// Selecting deepseek transitions into masked key entry for it.
	m.loginPicker.Selected = 0
	for i, e := range m.loginPicker.Entries {
		if e.Name == "deepseek" {
			m.loginPicker.Selected = i
			break
		}
	}
	m.handleLoginPickerKey("enter")
	if !m.loginMode || m.loginProvider != "deepseek" {
		t.Fatalf("after selecting deepseek: mode=%v provider=%q, want deepseek", m.loginMode, m.loginProvider)
	}
	if m.loginPickerOpen {
		t.Fatal("selector stayed open after a selection")
	}
}
