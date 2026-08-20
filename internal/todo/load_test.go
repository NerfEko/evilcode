package todo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// H5.13: a corrupt or unreadable state file must not be mistaken for a fresh
// session's legitimate empty state.
func TestNewStoreErrorsOnCorruptFile(t *testing.T) {
	dir := t.TempDir()
	todos := filepath.Join(dir, "todos")
	if err := os.MkdirAll(todos, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(todos, "swarm.json"), []byte("{not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := NewStore(dir, "swarm"); err == nil {
		t.Fatal("expected an error for a corrupt state file, got none")
	}
}

func TestNewStoreErrorsOnUnreadableFile(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	dir := t.TempDir()
	todos := filepath.Join(dir, "todos")
	if err := os.MkdirAll(todos, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(todos, "swarm.json")
	if err := os.WriteFile(path, []byte(`[]`), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o600) })

	_, err := NewStore(dir, "swarm")
	if err == nil {
		t.Fatal("expected a permission error to surface, not silent empty state")
	}
	if !strings.Contains(err.Error(), "permission") {
		t.Errorf("expected a permission error, got: %v", err)
	}
}

func TestNewStoreRejectsOversizedStateFile(t *testing.T) {
	dir := t.TempDir()
	todos := filepath.Join(dir, "todos")
	if err := os.MkdirAll(todos, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(todos, "swarm.json")
	if err := os.WriteFile(path, []byte(`{"padding":"`+strings.Repeat("x", maxStateBytes)+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(dir, "swarm"); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized todo state error = %v", err)
	}
}
