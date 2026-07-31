package lsp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// H4.7: rename applies whatever paths the language server names. A server is a
// subprocess answering with whatever it likes — a compromised or simply buggy
// one can name files outside the workspace, and the write phase is trusted
// because phase one succeeded.
func TestRenameRefusesPathsOutsideTheWorkspace(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(base, "outside.go")
	if err := os.WriteFile(outside, []byte("package outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	c := &Client{Root: root}
	for _, path := range []string{
		outside,
		filepath.Join(root, "..", "outside.go"),
		"/etc/passwd",
	} {
		if err := c.insideRoot(path); err == nil {
			t.Errorf("a rename touching %q was accepted", path)
		} else if !strings.Contains(err.Error(), "outside the workspace") {
			t.Errorf("error for %q = %q", path, err)
		}
	}

	inside := filepath.Join(root, "main.go")
	if err := os.WriteFile(inside, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := c.insideRoot(inside); err != nil {
		t.Errorf("a file inside the workspace was refused: %v", err)
	}
}

// A workspace reached through a symlink must not reject its own files.
func TestRenameAcceptsAWorkspaceReachedThroughASymlink(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(real, "main.go")
	if err := os.WriteFile(file, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	c := &Client{Root: link}
	if err := c.insideRoot(file); err != nil {
		t.Errorf("a file in a symlinked workspace was refused: %v", err)
	}
}
