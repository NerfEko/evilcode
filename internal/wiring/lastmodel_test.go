package wiring

import (
	"os"
	"path/filepath"
	"testing"

	"evilcode/internal/config"
)

// The daemon loads its config once at serve start and keeps a shared in-memory
// copy, but the interactive client persists last_model to the file whenever a
// session resolves or the user picks a model. A long-lived daemon must honor
// the file's current value for the NEXT new session, or every picker switch is
// lost on relaunch.
func TestBuildHonorsTheFilesLastModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("HOME", home)

	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("last_model = \"remembered@mock\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The daemon's shared config: file-backed (Path set) but holding the stale
	// boot-time value — the situation after the user switched models.
	cfg := config.Default()
	cfg.Providers = append(cfg.Providers, config.ProviderConfig{Name: "mock", Kind: config.KindMock})
	cfg.DefaultModel = "unpinned-model@mock"
	cfg.LastModel = "boot-time-model@mock"
	cfg.Path = path

	sess, err := Build(cfg, Options{Cwd: dir, NoTools: true})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	if sess.Model != "remembered" {
		t.Errorf("resolved model = %q, want the file's last_model", sess.Model)
	}
}

// An in-memory config (Path empty) must not read any user file: tests and
// embedded callers stay hermetic, and the stale value is honored as-is.
func TestBuildKeepsLastModelForInMemoryConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("HOME", home)

	repo := t.TempDir()
	cfg := config.Default()
	cfg.Providers = append(cfg.Providers, config.ProviderConfig{Name: "mock", Kind: config.KindMock})
	cfg.DefaultModel = "unpinned-model@mock"
	cfg.LastModel = "boot-time-model@mock"

	sess, err := Build(cfg, Options{Cwd: repo, NoTools: true})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	if sess.Model != "boot-time-model" {
		t.Errorf("resolved model = %q, want the in-memory last model (no file refresh)", sess.Model)
	}
}