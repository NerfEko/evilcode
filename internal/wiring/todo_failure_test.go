package wiring

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"evilcode/internal/config"
)

// blockTodoStore makes todo.NewStore fail deterministically: it needs to
// MkdirAll "<dataDir>/todos", and a plain file sitting where that directory
// belongs turns the mkdir into ENOTDIR regardless of the runtime's uid.
func blockTodoStore(t *testing.T, dataDir string) {
	t.Helper()
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "todos"), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	fn()

	w.Close()
	var buf bytes.Buffer
	io.Copy(&buf, r)
	return buf.String()
}

// H5.19: a failed todo.NewStore was swallowed unconditionally, so a daemon or
// headless session silently had no todo tool and auto-poke read empty state
// — worse for a swarm, where the namespace was explicitly configured to be
// shared coordination state, not an optional extra.
func TestBuildFailsWhenAnExplicitTodoNamespaceCannotOpen(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("HOME", home)
	blockTodoStore(t, filepath.Join(home, "data", "evilcode"))

	cfg := config.Default()
	cfg.Providers = append(cfg.Providers, config.ProviderConfig{Name: "mock", Kind: config.KindMock})
	cfg.DefaultModel = "m@mock"

	repo := t.TempDir()
	_, err := Build(cfg, Options{Cwd: repo, NoTools: true, TodoNamespace: "swarm"})
	if err == nil {
		t.Fatal("expected Build to fail when the explicitly-configured swarm namespace cannot open")
	}
}

// A private, per-session store (no namespace configured) is an enhancement,
// not a prerequisite — the same trade the memory bank already gets.
func TestBuildContinuesWithoutTodosWhenNoNamespaceIsConfigured(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("HOME", home)
	blockTodoStore(t, filepath.Join(home, "data", "evilcode"))

	cfg := config.Default()
	cfg.Providers = append(cfg.Providers, config.ProviderConfig{Name: "mock", Kind: config.KindMock})
	cfg.DefaultModel = "m@mock"

	repo := t.TempDir()
	var sess *Session
	var err error
	stderr := captureStderr(t, func() {
		sess, err = Build(cfg, Options{Cwd: repo, NoTools: true})
	})
	if err != nil {
		t.Fatalf("a private todo store failing to open must not fail the build: %v", err)
	}
	defer sess.Close()
	if sess.Todos != nil {
		t.Error("Todos should be nil when the store could not open")
	}
	if stderr == "" {
		t.Error("a swallowed todo store failure must at least be logged")
	}
}
