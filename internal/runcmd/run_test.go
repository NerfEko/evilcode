package runcmd

import (
	"encoding/json"
	"strings"
	"testing"

	"evilcode/internal/agent"
	"evilcode/internal/provider"
)

// A repaired argument is visible in the headless tool row, matching the TUI.
func TestToolLineShowsRepairs(t *testing.T) {
	e := agent.Event{
		Kind: agent.EventToolResult,
		Call: &provider.ToolCall{ID: "c1", Name: "read", Args: json.RawMessage(`{"file_path":"a.txt"}`)},
		Repairs: []string{"file_path→path"},
	}
	line := toolLine(e)
	if !strings.Contains(line, "repaired: file_path→path") {
		t.Errorf("toolLine = %q, want the repair shown", line)
	}
}
