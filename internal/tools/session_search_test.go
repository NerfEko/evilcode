package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSearchSession(t *testing.T, dir, name, body string) {
	t.Helper()
	sessions := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	content := strings.Join([]string{
		`{"ts":"2026-08-01T10:00:00Z","type":"meta","data":{"kind":"start"}}`,
		`{"ts":"2026-08-01T10:00:01Z","type":"message","data":{"role":"user","content":"` + body + `"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(sessions, name+".jsonl"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSessionSearchToolReturnsRoleDateAndExcerpt(t *testing.T) {
	dir := t.TempDir()
	writeSearchSession(t, dir, "anchor", "I need the anchor parser repaired")

	tool := NewSessionSearch(dir, "current")
	result, err := tool.Run(context.Background(), json.RawMessage(`{"query":"anchor parser","role":"user"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Output, "anchor") || !strings.Contains(result.Output, "user") || !strings.Contains(result.Output, "anchor parser") {
		t.Fatalf("session_search output = %q, want name, role, and excerpt", result.Output)
	}
}

func TestSessionSearchIndexReusesUnchangedFiles(t *testing.T) {
	dir := t.TempDir()
	writeSearchSession(t, dir, "anchor", "the anchor parser is recoverable")
	index := newSessionSearchIndex()

	if _, err := index.search(dir, "current", "anchor parser", "any", 10); err != nil {
		t.Fatal(err)
	}
	files, reads := index.stats()
	if files != 1 || reads != 1 {
		t.Fatalf("initial index stats = files %d reads %d, want 1/1", files, reads)
	}
	if _, err := index.search(dir, "current", "parser", "any", 10); err != nil {
		t.Fatal(err)
	}
	_, reads = index.stats()
	if reads != 1 {
		t.Fatalf("repeated search reads = %d, want unchanged file reuse", reads)
	}
}
