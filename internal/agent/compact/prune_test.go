package compact

import (
	"encoding/json"
	"strings"
	"testing"

	"evilcode/internal/provider"
)

func readCall(id, path string) provider.Message {
	return provider.Message{
		Role:      provider.RoleAssistant,
		ToolCalls: []provider.ToolCall{{ID: id, Name: "read", Args: json.RawMessage(`{"path":"` + path + `"}`)}},
	}
}

func readResult(id string, content string) provider.Message {
	return provider.Message{Role: provider.RoleTool, ToolCallID: id, ToolName: "read", Content: content}
}

func TestFindSupersededReadsKeepsNewestPerPath(t *testing.T) {
	msgs := []provider.Message{
		userMsg("explore"),
		readCall("c1", "auth.go"),
		readResult("c1", strings.Repeat("old auth", 100)),
		readCall("c2", "hub.go"),
		readResult("c2", strings.Repeat("hub old", 400)),
		readCall("c3", "auth.go:50-200"), // same path, different selector
		readResult("c3", strings.Repeat("new auth", 400)),
	}
	candidates := FindSupersededReads(msgs, nil)
	if len(candidates) != 1 {
		t.Fatalf("candidates = %d, want 1 (c1 superseded by c3)", len(candidates))
	}
	if msgs[candidates[0].Index].ToolCallID != "c1" {
		t.Errorf("pruned the wrong read: index %d", candidates[0].Index)
	}
}

func TestFindSupersededReadsNeverPrunesTheLastRead(t *testing.T) {
	msgs := []provider.Message{
		readCall("c1", "auth.go"),
		readResult("c1", "auth contents"),
	}
	if candidates := FindSupersededReads(msgs, nil); len(candidates) != 0 {
		t.Fatalf("the only read of a path must survive; got %d", len(candidates))
	}
}

func TestFindSupersededReadsSkipsErrorsAndSmallResults(t *testing.T) {
	msgs := []provider.Message{
		readCall("c1", "auth.go"),
		{Role: provider.RoleTool, ToolCallID: "c1", ToolName: "read", IsError: true, Content: "not found"},
		readCall("c2", "auth.go"),
		readResult("c2", strings.Repeat("new", 400)),
	}
	// The error result is protected and the small final result survives.
	if candidates := FindSupersededReads(msgs, nil); len(candidates) != 0 {
		t.Fatalf("error results must never prune; got %d", len(candidates))
	}
}

func TestPruneMessagesBlanksContentKeepsPairing(t *testing.T) {
	msgs := []provider.Message{
		readCall("c1", "auth.go"),
		readResult("c1", strings.Repeat("long ", 200)),
		readCall("c2", "auth.go"),
		readResult("c2", strings.Repeat("newer ", 400)),
	}
	candidates := FindSupersededReads(msgs, nil)
	if len(candidates) != 1 {
		t.Fatalf("candidates = %d, want 1", len(candidates))
	}
	out := PruneMessages(msgs, candidates)
	if out[candidates[0].Index].Content != SUPERSEDED_NOTICE {
		t.Errorf("superseded content = %q", out[candidates[0].Index].Content)
	}
	if out[1].ToolCallID != "c1" || out[0].ToolCalls[0].ID != "c1" {
		t.Error("pruning disturbed the call/result pairing")
	}
	if out[3].Content == SUPERSEDED_NOTICE {
		t.Errorf("the newest read was blanked")
	}
}
