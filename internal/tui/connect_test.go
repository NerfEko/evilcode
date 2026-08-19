package tui

import (
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"evilcode/internal/config"
	"evilcode/internal/tools"
)

func TestConnectCommandRegistersHelpAndSystemSection(t *testing.T) {
	cmd, ok := FindCommand("connect")
	if !ok {
		t.Fatal("connect command not registered")
	}
	for _, want := range []string{"/connect brave", "/connect brave status"} {
		if !strings.Contains(cmd.Long, want) {
			t.Errorf("connect help missing %q:\n%s", want, cmd.Long)
		}
	}
	for _, section := range HelpSections {
		for _, name := range section.Names {
			if name == "connect" {
				return
			}
		}
	}
	t.Fatal("connect is not listed in a help section")
}

func TestConnectBraveSavesKeyAndActivatesLiveTool(t *testing.T) {
	path := writeLoginConfig(t)
	t.Setenv(config.EnvBraveSearchKey, "")
	t.Setenv(config.EnvBraveKey, "")
	search := tools.NewBraveSearch("")
	m := NewModel(nil, HeaderState{SessionName: "s", Model: "m"}).WithBraveSearch(search)

	m.runCommandWithArg("connect", "brave")
	if !m.loginMode || m.loginProvider != "brave" {
		t.Fatalf("connect entry state: mode=%v target=%q", m.loginMode, m.loginProvider)
	}
	secret := "brave-test-key"
	m.editor.Text = secret
	m.editor.Cursor = len([]rune(secret))
	m.handleLoginKey("enter", tea.KeyPressMsg{})

	if m.loginMode || m.editor.Text != "" || m.loginProvider != "" {
		t.Fatalf("connect did not clear state: mode=%v text=%q target=%q", m.loginMode, m.editor.Text, m.loginProvider)
	}
	if search.APIKey != secret {
		t.Fatalf("live Brave key = %q, want %q", search.APIKey, secret)
	}
	if strings.Contains(m.notice, secret) || len(m.blocks) != 0 {
		t.Fatalf("connect leaked the secret into UI state: notice=%q blocks=%v", m.notice, m.blocks)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "brave_api_key = \""+secret+"\"") {
		t.Fatalf("connect did not persist the key: %s", data)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.BraveSearchAPIKey(); got != secret {
		t.Fatalf("reloaded Brave key = %q, want %q", got, secret)
	}
}

func TestConnectBraveStatusNeverPrintsKey(t *testing.T) {
	writeLoginConfig(t)
	t.Setenv(config.EnvBraveSearchKey, "")
	t.Setenv(config.EnvBraveKey, "")
	m := &Model{}
	m.runCommandWithArg("connect", "status")
	if !strings.Contains(m.notice, "brave") || !strings.Contains(m.notice, "no API key") {
		t.Fatalf("status without key = %q", m.notice)
	}

	t.Setenv(config.EnvBraveSearchKey, "brave-env-secret")
	m.runCommandWithArg("connect", "brave status")
	if !strings.Contains(m.notice, "API key present") || strings.Contains(m.notice, "brave-env-secret") {
		t.Fatalf("status with key leaked or omitted presence: %q", m.notice)
	}
}
