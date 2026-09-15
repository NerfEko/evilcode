package runcmd

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"evilcode/internal/agent"
	"evilcode/internal/provider"
)

// Repairs remain in the event for diagnostics, but normal output stays quiet.
func TestToolLineHidesRepairs(t *testing.T) {
	e := agent.Event{
		Kind:    agent.EventToolResult,
		Call:    &provider.ToolCall{ID: "c1", Name: "read", Args: json.RawMessage(`{"file_path":"a.txt"}`)},
		Repairs: []string{"file_path→path"},
	}
	line := toolLine(e)
	if strings.Contains(line, "repaired:") || strings.Contains(line, "file_path→path") {
		t.Errorf("toolLine = %q, repair metadata leaked into normal output", line)
	}
}

func TestShortenKeepsUTF8Valid(t *testing.T) {
	got := shorten(strings.Repeat("é", 31))
	if !utf8.ValidString(got) {
		t.Fatalf("shorten split a UTF-8 sequence: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("shorten(%q) = %q, want a truncation marker", strings.Repeat("é", 31), got)
	}
}

func TestReadPipedPromptIsBounded(t *testing.T) {
	if _, err := readPipedPrompt(strings.NewReader(strings.Repeat("x", MaxPromptBytes+1))); err == nil {
		t.Fatal("accepted an unbounded prompt from stdin")
	}
	if got, err := readPipedPrompt(strings.NewReader("  hello\n")); err != nil || got != "hello" {
		t.Errorf("readPipedPrompt = %q, %v", got, err)
	}
}
