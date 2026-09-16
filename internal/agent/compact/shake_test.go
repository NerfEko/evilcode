package compact

import (
	"strings"
	"testing"

	"evilcode/internal/provider"
)

func TestCollectShakeRegionsToolResults(t *testing.T) {
	big := strings.Repeat("y", 8000)
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "go"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "c1", Name: "big", Args: []byte(`{}`)}}},
		{Role: provider.RoleTool, ToolCallID: "c1", ToolName: "big", Content: big},
		{Role: provider.RoleAssistant, Content: "done"},
	}
	// Aggressive: protect nothing.
	regions := CollectShakeRegions(msgs, AggressiveShakeConfig())
	if len(regions) != 1 || regions[0].Kind != "toolResult" {
		t.Fatalf("regions = %+v, want the tool result", regions)
	}
	if regions[0].MsgIndex != 2 {
		t.Errorf("region index = %d, want 2", regions[0].MsgIndex)
	}
	if regions[0].Label != "big" {
		t.Errorf("label = %q, want the tool name", regions[0].Label)
	}
}

func TestCollectShakeRegionsProtectsRecent(t *testing.T) {
	big := strings.Repeat("y", 8000)
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "go"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "c1", Name: "big", Args: []byte(`{}`)}}},
		{Role: provider.RoleTool, ToolCallID: "c1", ToolName: "big", Content: big},
		{Role: provider.RoleAssistant, Content: strings.Repeat("tail ", 6000)},
	}
	// Default: protect 16000 tokens of the recent tail — the big result is
	// inside it and must not be shaken.
	if regions := CollectShakeRegions(msgs, DefaultShakeConfig()); len(regions) != 0 {
		t.Fatalf("protected tail shaken: %+v", regions)
	}
}

func TestCollectShakeRegionsSavingsGate(t *testing.T) {
	big := strings.Repeat("y", 8000)
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "go"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "c1", Name: "big", Args: []byte(`{}`)}}},
		{Role: provider.RoleTool, ToolCallID: "c1", ToolName: "big", Content: big},
	}
	// minSavings above what one region reclaims → no shake.
	cfg := AggressiveShakeConfig()
	cfg.MinSavings = 100000
	if regions := CollectShakeRegions(msgs, cfg); len(regions) != 0 {
		t.Fatalf("savings gate ignored: %+v", regions)
	}
}

func TestCollectShakeRegionsFencedBlocks(t *testing.T) {
	code := "```go\n" + strings.Repeat("func f() { x() }\n", 200) + "```"
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "here is a big dump:\n" + code},
		{Role: provider.RoleAssistant, Content: strings.Repeat("ack ", 6000)},
	}
	regions := CollectShakeRegions(msgs, AggressiveShakeConfig())
	var found bool
	for _, r := range regions {
		if r.Kind == "block" && strings.Contains(r.Original, "func f()") {
			found = true
		}
	}
	if !found {
		t.Fatalf("fenced block not located: %+v", regions)
	}
}

func TestApplyShakeRegionsSplicesIndependentOffsets(t *testing.T) {
	big := strings.Repeat("y", 8000)
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "go"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "c1", Name: "big", Args: []byte(`{}`)}}},
		{Role: provider.RoleTool, ToolCallID: "c1", ToolName: "big", Content: big},
	}
	items := []ShakeItem{{
		Region:      ShakeRegion{Kind: "toolResult", MsgIndex: 2, Tokens: estimateTokens(msgs[2])},
		Replacement: "[shaken ~4000 tokens]",
	}}
	out := ApplyShakeRegions(msgs, items)
	if out[2].Content != "[shaken ~4000 tokens]" {
		t.Errorf("replacement = %q", out[2].Content)
	}
	if out[2].ToolCallID != "c1" {
		t.Error("shake disturbed tool pairing")
	}
	if msgs[2].Content != big {
		t.Error("the input slice was mutated")
	}
}

func TestShakeSkipsBeforeCompactionBoundary(t *testing.T) {
	big := strings.Repeat("y", 8000)
	msgs := []provider.Message{
		SummaryMessage("old"),
		{Role: provider.RoleUser, Content: CompactedRecentPrefix + "old context"},
		{Role: provider.RoleUser, Content: "go"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "c1", Name: "big", Args: []byte(`{}`)}}},
		{Role: provider.RoleTool, ToolCallID: "c1", ToolName: "big", Content: big},
	}
	cfg := AggressiveShakeConfig()
	cfg.SkipBefore = 2
	// The boundary region (already summarized) is skipped; the tool result
	// after it is still eligible.
	regions := CollectShakeRegions(msgs, cfg)
	if len(regions) != 1 || regions[0].MsgIndex != 4 {
		t.Fatalf("regions = %+v, want only index 4", regions)
	}
}
