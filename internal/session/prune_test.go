package session

import (
	"encoding/json"
	"os"
	"strings"

	"evilcode/internal/agent/compact"
	"evilcode/internal/provider"
	"testing"
)

// TestPruneSupersededReadsBlanksTheOlderLog writes a session with two reads
// of one path and asserts the rewrite blanks only the older one, keeps
// call/result pairing, and leaves meta entries intact.
func TestPruneSupersededReadsBlanksTheOlderLog(t *testing.T) {
	dir := t.TempDir()
	name := "prune"
	store, err := CreateNamed(dir, name)
	if err != nil {
		t.Fatal(err)
	}
	fat := strings.Repeat("auth source ", 400)
	for _, m := range []provider.Message{
		{Role: provider.RoleUser, Content: "explore"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "c1", Name: "read", Args: []byte(`{"path":"auth.go"}`)}}},
		{Role: provider.RoleTool, ToolCallID: "c1", ToolName: "read", Content: fat},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "c2", Name: "read", Args: []byte(`{"path":"auth.go"}`)}}},
		{Role: provider.RoleTool, ToolCallID: "c2", ToolName: "read", Content: fat},
	} {
		if err := store.WriteMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	store.Close()

	pruned, saved, err := PruneSupersededReads(dir, name)
	if err != nil {
		t.Fatal(err)
	}
	if pruned != 1 {
		t.Fatalf("pruned = %d, want 1", pruned)
	}
	if saved <= 0 {
		t.Errorf("tokensSaved = %d, want positive", saved)
	}

	entries, err := Read(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	var sawNotice, sawFresh int
	for _, e := range entries {
		if e.Type != TypeTool {
			continue
		}
		var m provider.Message
		if err := json.Unmarshal(e.Data, &m); err != nil {
			t.Fatal(err)
		}
		if m.Content == compact.SUPERSEDED_NOTICE {
			sawNotice++
		}
		if strings.HasPrefix(m.Content, "auth source ") {
			sawFresh++
		}
	}
	if sawNotice != 1 || sawFresh != 1 {
		t.Errorf("notice=%d fresh=%d, want 1/1 — the newest read must survive", sawNotice, sawFresh)
	}
	if _, err := os.Stat(store.Path + ".bak"); err != nil {
		t.Error("prune should leave a backup like every rewrite")
	}
}

// TestPruneSupersededReadsLeavesUnprunableLogsAlone: a log with no superseded
// reads must not be rewritten — no .bak churn, no content change.
func TestPruneSupersededReadsLeavesUnprunableLogsAlone(t *testing.T) {
	dir := t.TempDir()
	name := "clean"
	store, err := CreateNamed(dir, name)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []provider.Message{
		{Role: provider.RoleUser, Content: "one read only"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "c1", Name: "read", Args: []byte(`{"path":"main.go"}`)}}},
		{Role: provider.RoleTool, ToolCallID: "c1", ToolName: "read", Content: strings.Repeat("src ", 400)},
	} {
		if err := store.WriteMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	store.Close()
	before, err := os.ReadFile(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := PruneSupersededReads(dir, name); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("an unprunable log was rewritten")
	}
	if _, err := os.Stat(store.Path + ".bak"); err == nil {
		t.Error("no-op prune churned a backup")
	}
}
