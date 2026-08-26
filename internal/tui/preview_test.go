package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"evilcode/internal/provider"
	"evilcode/internal/session"
)

func TestSessionPreviewShowsTheConversation(t *testing.T) {
	// It used to show the name, message count, modified age and title — all of
	// which are already on the row beside it, and the title was always empty
	// because nothing ever wrote one. A preview that previews nothing spends
	// 60% of the screen saying so.
	r := testRenderer(120)
	rows := []SessionRow{{
		Info: session.Info{Name: "venom", Messages: 2, Modified: time.Now()},
		Preview: BlocksFromMessages([]provider.Message{
			{Role: provider.RoleUser, Content: "wire the auth flow"},
			{Role: provider.RoleAssistant, Content: "Done — the refresh path is wired."},
		}, ""),
	}}

	got := strings.Join(plainLines(r.sessionPreview(rows, 0, 70, 20)), "\n")
	if !strings.Contains(got, "wire the auth flow") {
		t.Errorf("preview is missing the prompt:\n%s", got)
	}
	if !strings.Contains(got, "refresh path is wired") {
		t.Errorf("preview is missing the reply:\n%s", got)
	}
	if !strings.Contains(got, "venom") {
		t.Errorf("preview is missing the session name:\n%s", got)
	}
}

func TestSessionPreviewAlwaysClosesItsBox(t *testing.T) {
	// With no sessions it used to return a top border and nothing else — no
	// sides, no bottom.
	r := testRenderer(120)
	got := plainLines(r.sessionPreview(nil, 0, 70, 12))
	if len(got) != 12 {
		t.Fatalf("drew %d rows, want the full box height", len(got))
	}
	if !strings.Contains(got[len(got)-1], "╰") {
		t.Errorf("box has no bottom border: %q", got[len(got)-1])
	}
}

func TestSessionPreviewTailsALongConversation(t *testing.T) {
	// The most recent context is what tells you whether this is the session you
	// meant, so the box fills from the end.
	r := testRenderer(120)
	var msgs []provider.Message
	for i := 0; i < 30; i++ {
		msgs = append(msgs, provider.Message{
			Role: provider.RoleUser, Content: "prompt number " + string(rune('a'+i%26)),
		})
	}
	msgs = append(msgs, provider.Message{
		Role: provider.RoleAssistant, Content: "THE FINAL ANSWER",
	})
	rows := []SessionRow{{
		Info:    session.Info{Name: "long", Messages: len(msgs)},
		Preview: BlocksFromMessages(msgs, ""),
	}}

	got := strings.Join(plainLines(r.sessionPreview(rows, 0, 70, 14)), "\n")
	if !strings.Contains(got, "THE FINAL ANSWER") {
		t.Errorf("preview dropped the newest message:\n%s", got)
	}
}

func TestDeriveTitlePrefersTheWorkOverThePrompt(t *testing.T) {
	// §5.4: the list is labelled by what the agent understood you wanted.
	m := newTestModel(t)
	m.blocks = []Block{{Kind: BlockUser, Text: "do something vague"}}
	if got := m.deriveTitle(); got != "do something vague" {
		t.Errorf("with no todos the first prompt should be the title, got %q", got)
	}
}

func TestBlocksFromMessagesNumbersPromptsFromOne(t *testing.T) {
	got := BlocksFromMessages([]provider.Message{
		{Role: provider.RoleUser, Content: "first"},
		{Role: provider.RoleAssistant, Content: "reply"},
		{Role: provider.RoleUser, Content: "second"},
	}, "")
	var nums []int
	for _, b := range got {
		if b.Kind == BlockUser {
			nums = append(nums, b.Number)
		}
	}
	if len(nums) != 2 || nums[0] != 1 || nums[1] != 2 {
		t.Errorf("prompt numbers = %v, want 1 then 2", nums)
	}
}

func TestBlocksFromMessagesRebuildsToolRows(t *testing.T) {
	// A resumed session must show bash commands and their outputs, not a bare
	// "bash" row: the command lives on the assistant message's tool call and
	// the output on the tool-result message.
	bashArgs := json.RawMessage(`{"cmd": "ls -la"}`)
	editArgs := json.RawMessage(`{"path": "main.go", "old": "x", "new": "y"}`)
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "inspect the repo"},
		{Role: provider.RoleAssistant, Content: "On it.",
			ToolCalls: []provider.ToolCall{
				{ID: "c1", Name: "bash", Args: bashArgs},
				{ID: "c2", Name: "edit", Args: editArgs},
			}},
		{Role: provider.RoleTool, ToolCallID: "c1", ToolName: "bash",
			Content: "total 4\ndrwxr-xr-x 2 eko eko 4096 Aug 26 10:00 .\n"},
		{Role: provider.RoleTool, ToolCallID: "c2", ToolName: "edit",
			Content: "updated", Diff: "@@ -1,1 +1,1 @@\n-x\n+y\n"},
	}

	blocks := BlocksFromMessages(msgs, "")
	if len(blocks) != 4 {
		t.Fatalf("got %d blocks, want 4: %+v", len(blocks), blocks)
	}
	bash := blocks[2]
	if bash.ToolName != "bash" || bash.ToolCommand != "ls -la" {
		t.Fatalf("bash row lost its command: %+v", bash)
	}
	if !strings.Contains(bash.ToolOutput, "total 4") {
		t.Fatalf("bash row lost its output: %+v", bash)
	}
	if bash.ToolTarget != "ls -la" {
		t.Fatalf("bash row lost its target: %+v", bash)
	}
	edit := blocks[3]
	if edit.ToolName != "edit" || edit.ToolPath != "main.go" {
		t.Fatalf("edit row lost its path: %+v", edit)
	}
	if edit.Diff == "" || !edit.HasDiff || edit.Added != 1 || edit.Removed != 1 {
		t.Fatalf("edit row lost its diff: %+v", edit)
	}
}
