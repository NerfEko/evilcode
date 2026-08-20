package agent

import (
	"testing"

	"evilcode/internal/provider"
)

func TestConversationSyncKeepsRemoteHistoryAndEpoch(t *testing.T) {
	c := NewConversation("")
	c.Append(provider.Message{Role: provider.RoleUser, Content: "old"})
	c.Sync([]provider.Message{
		{Role: provider.RoleUser, Content: "new", Hidden: true},
		{Role: provider.RoleAssistant, Content: "answer"},
	}, 4)
	if got := c.Len(); got != 2 {
		t.Fatalf("Len = %d, want 2", got)
	}
	if got := c.Epoch(); got != 4 {
		t.Fatalf("Epoch = %d, want 4", got)
	}
	msgs := c.Messages()
	if !msgs[0].Hidden || msgs[0].Content != "new" {
		t.Fatalf("synced messages = %+v", msgs)
	}
}
