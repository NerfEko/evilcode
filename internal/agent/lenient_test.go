package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"evilcode/internal/provider"
	"evilcode/internal/tools"
)

// A6: the documented [[model]] lenient_tool_parse setting must actually work —
// a small model that emits a tool call as JSON in prose gets it dispatched
// when the flag is on, and the JSON is stripped from the transcript.
func TestLenientToolParseExtractsProseToolCall(t *testing.T) {
	p := &proseProvider{}
	read := tools.Tool{
		Name:   "read",
		Desc:   "read a file",
		Schema: json.RawMessage(`{"type":"object"}`),
		Run: func(context.Context, json.RawMessage) (tools.Result, error) {
			return tools.Result{Output: "file contents"}, nil
		},
	}
	a := newTestAgent(t, p, tools.Set{read})
	a.LenientToolParse = true

	evs, err := collect(t, a, func() error { return a.Run(context.Background(), "hi") })
	if err != nil {
		t.Fatal(err)
	}
	if !hasToolStart(evs, "read") {
		t.Error("the prose JSON tool call was not dispatched")
	}
	if last := evs[len(evs)-1]; last.Reason != EndComplete {
		t.Errorf("reason = %v, want complete", last.Reason)
	}
	msgs := a.Conv.Messages()
	var called, clean int
	for _, m := range msgs {
		if m.Role != provider.RoleAssistant {
			continue
		}
		if len(m.ToolCalls) == 1 && m.ToolCalls[0].Name == "read" {
			called++
		}
		if len(m.ToolCalls) == 0 && strings.TrimSpace(m.Content) != "" {
			clean++
		}
		if strings.Contains(m.Content, `"tool"`) {
			t.Errorf("the JSON tool call was not stripped from the transcript: %q", m.Content)
		}
	}
	if called != 1 {
		t.Errorf("assistant messages carrying the extracted read call = %d, want 1", called)
	}
	if clean != 1 {
		t.Errorf("clean follow-up assistant messages = %d, want 1 (the prose around the call)", clean)
	}
}

// The flag is opt-in: with it off, the same prose is left alone and no tool
// runs.
func TestLenientToolParseOffLeavesProseAlone(t *testing.T) {
	p := &proseProvider{}
	a := newTestAgent(t, p, tools.Set{{
		Name:   "read",
		Desc:   "read a file",
		Schema: json.RawMessage(`{"type":"object"}`),
		Run: func(context.Context, json.RawMessage) (tools.Result, error) {
			return tools.Result{Output: "file contents"}, nil
		},
	}})
	// LenientToolParse defaults to false.

	evs, err := collect(t, a, func() error { return a.Run(context.Background(), "hi") })
	if err != nil {
		t.Fatal(err)
	}
	if hasToolStart(evs, "read") {
		t.Error("a tool ran without the opt-in flag")
	}
}

// Unknown tool names, non-object arguments, and non-function type claims must
// be rejected: the strictness is what keeps the fallback from misfiring on
// ordinary prose.
func TestLenientToolParseRejectsBadShapes(t *testing.T) {
	ts := tools.Set{{
		Name:   "read",
		Desc:   "read a file",
		Schema: json.RawMessage(`{"type":"object"}`),
	}}
	cases := []struct {
		name    string
		content string
	}{
		{"unknown tool", `I will call {"tool": "rm_rf", "args": {"path": "/"}} now.`},
		{"non-object args", `{"tool": "read", "args": "the file"}`},
		{"typed as something else", `{"type": "quote", "tool": "read", "args": {}}`},
		{"no tool key", `{"args": {}}`},
		{"array args", `{"tool": "read", "args": [1, 2]}`},
	}
	for _, tc := range cases {
		calls, stripped, ok := parseLenientToolCalls(tc.content, ts)
		if ok {
			t.Errorf("%s: accepted %q -> calls %+v, stripped %q", tc.name, tc.content, calls, stripped)
		}
	}
}

func TestParseLenientToolCallsAcceptsShapes(t *testing.T) {
	ts := tools.Set{{
		Name:   "read",
		Desc:   "read a file",
		Schema: json.RawMessage(`{"type":"object"}`),
	}}
	cases := []string{
		`{"tool": "read", "args": {"path": "main.go"}}`,
		`{"name": "read", "arguments": {"path": "main.go"}}`,
		`{"function": "read", "args": {"path": "main.go"}}`,
		"Before this:\n```json\n{\"tool\": \"read\", \"args\": {\"path\": \"main.go\"}}\n```\nAfter that.",
		`{"type": "function", "function": {"name": "read", "arguments": {"path": "main.go"}}}`,
	}
	for _, content := range cases {
		calls, _, ok := parseLenientToolCalls(content, ts)
		if !ok {
			t.Errorf("rejected %q", content)
			continue
		}
		if len(calls) != 1 || calls[0].Name != "read" {
			t.Errorf("%q: calls = %+v", content, calls)
		}
		var args struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(calls[0].Args, &args); err != nil || args.Path != "main.go" {
			t.Errorf("%q: args = %s (%v)", content, calls[0].Args, err)
		}
	}
}

func hasToolStart(evs []Event, name string) bool {
	for _, e := range evs {
		if e.Kind == EventToolStart && e.Call != nil && e.Call.Name == name {
			return true
		}
	}
	return false
}

// proseProvider returns a canned assistant message whose content contains a
// JSON tool call written as prose, with no structured tool_calls. The prose
// appears only in the first response; later rounds answer plainly, like a real
// model that learned the call worked.
type proseProvider struct {
	served bool
}

func (p *proseProvider) Name() string { return "prose" }
func (p *proseProvider) Embed(context.Context, []string) ([][]float32, error) {
	return nil, nil
}
func (p *proseProvider) Models(context.Context) ([]provider.ModelInfo, error) {
	return nil, nil
}
func (p *proseProvider) ChatStream(context.Context, provider.Req) (<-chan provider.Chunk, error) {
	ch := make(chan provider.Chunk, 3)
	if !p.served {
		p.served = true
		ch <- provider.Chunk{Text: "Let me check that file. {\"tool\": \"read\", \"args\": {\"path\": \"main.go\"}} One moment."}
	} else {
		ch <- provider.Chunk{Text: "There it is."}
	}
	ch <- provider.Chunk{Done: true}
	close(ch)
	return ch, nil
}
