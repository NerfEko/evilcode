package agent

import (
	"strings"
	"testing"

	"evilcode/internal/provider"
)

func TestEffectiveCompactionWindowReservesCodexResponseHeadroom(t *testing.T) {
	if got := EffectiveCompactionWindow(400_000, 272_000, 128_000); got != 252_000 {
		t.Fatalf("effective compaction window = %d, want 252000", got)
	}
	if got := EffectiveCompactionWindow(100_000, 0, 0); got != 100_000 {
		t.Fatalf("provider without separate input limit = %d, want the context window", got)
	}
}

func TestMessagesForModelClearsOldToolOutputOnlyInTheProjection(t *testing.T) {
	old := strings.Repeat("old ", 100_000)
	previous := strings.Repeat("previous ", 100_000)
	current := strings.Repeat("current ", 100_000)

	conv := NewConversation("system")
	conv.Append(
		provider.Message{Role: provider.RoleUser, Content: "first request"},
		provider.Message{Role: provider.RoleTool, ToolName: "read", ToolCallID: "one", Content: old},
		provider.Message{Role: provider.RoleUser, Content: "second request"},
		provider.Message{Role: provider.RoleTool, ToolName: "read", ToolCallID: "two", Content: previous},
		provider.Message{Role: provider.RoleUser, Content: "current request"},
		provider.Message{Role: provider.RoleTool, ToolName: "read", ToolCallID: "three", Content: current},
	)

	projected := conv.MessagesForModel()
	if got := projected[2].Content; got != modelToolOutputPlaceholder {
		t.Fatalf("old tool output = %q, want the projection placeholder", got)
	}
	if projected[4].Content != previous || projected[6].Content != current {
		t.Fatal("the newest tool results were pruned from the model projection")
	}

	full := conv.Messages()
	if full[2].Content != old {
		t.Fatal("MessagesForModel rewrote the durable conversation")
	}
}

func TestMessagesForModelDoesNotPruneDuringTheFirstUserTurn(t *testing.T) {
	large := strings.Repeat("output ", 200_000)
	conv := NewConversation("")
	conv.Append(
		provider.Message{Role: provider.RoleUser, Content: "one request"},
		provider.Message{Role: provider.RoleTool, ToolName: "read", ToolCallID: "one", Content: large},
		provider.Message{Role: provider.RoleTool, ToolName: "read", ToolCallID: "two", Content: large},
	)

	projected := conv.MessagesForModel()
	if projected[1].Content != large || projected[2].Content != large {
		t.Fatal("the active first turn was pruned before a newer user turn existed")
	}
}

func TestMessagesForModelProtectsSkillOutput(t *testing.T) {
	large := strings.Repeat("skill instruction ", 200_000)
	conv := NewConversation("")
	conv.Append(
		provider.Message{Role: provider.RoleUser, Content: "first request"},
		provider.Message{Role: provider.RoleTool, ToolName: "skill", ToolCallID: "skill", Content: large},
		provider.Message{Role: provider.RoleUser, Content: "second request"},
		provider.Message{Role: provider.RoleTool, ToolName: "read", ToolCallID: "read", Content: "small"},
	)

	projected := conv.MessagesForModel()
	if projected[1].Content != large {
		t.Fatal("skill output was pruned from the model projection")
	}
}
