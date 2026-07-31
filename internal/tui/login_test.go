package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"evilcode/internal/config"
	"evilcode/internal/theme"
)

func TestMaskedComposerDoesNotRenderLoginKey(t *testing.T) {
	secret := "sk-never-on-screen"
	r := NewRenderer(theme.Dracula(), 80)
	rows := r.RenderComposer(ComposerState{Text: secret, Masked: true})
	frame := strings.Join(rows, "\n")
	if strings.Contains(frame, secret) || strings.Contains(frame, "sk-") {
		t.Fatalf("masked composer rendered the secret: %q", frame)
	}
	if !strings.Contains(frame, "•") {
		t.Fatalf("masked composer did not show a mask: %q", frame)
	}
}

func TestLoginClearsSecretWithoutTranscriptBlock(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.toml")
	if err := os.WriteFile(path, []byte(`[[provider]]
name = "ollama-cloud"
kind = "ollama"
`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvConfigPath, path)
	t.Setenv(config.EnvOllamaKey, "")
	secret := "sk-never-in-transcript"
	m := &Model{}
	m.loginMode = true
	m.editor.Text = secret
	m.editor.Cursor = len([]rune(secret))
	m.handleLoginKey("enter", tea.KeyPressMsg{})
	if m.loginMode || m.editor.Text != "" {
		t.Fatal("login left secret in the active editor")
	}
	if len(m.blocks) != 0 || strings.Contains(m.notice, secret) {
		t.Fatalf("login leaked secret into UI state: blocks=%v notice=%q", m.blocks, m.notice)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), secret) {
		t.Fatal("login did not persist the key")
	}
}
