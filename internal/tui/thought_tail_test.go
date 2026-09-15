package tui

import (
	"testing"

	"evilcode/internal/agent"
)

// The last word of a thought must land in the thought, not after the reply.
// A provider chunk can carry the final thinking token and the first answer
// token together; the TUI collapses the trace when the answer starts, so a
// reasoning delta that arrives after that first text delta must still belong
// to the open trace, not to a fresh block under the reply.
func TestTrailingReasoningDeltaStaysInTheThought(t *testing.T) {
	m := newTestModel(t)
	m.applyEvent(agent.Event{Kind: agent.EventReasoningDelta, Text: "weighing it up, the answer is"})
	traceIdx := m.reasoningIdx
	if traceIdx < 0 {
		t.Fatal("no reasoning block was opened")
	}

	m.applyEvent(agent.Event{Kind: agent.EventReasoningDelta, Text: " the final word"})
	if m.reasoningIdx != traceIdx {
		t.Fatalf("the final thought word opened a new block at %d, trace at %d", m.reasoningIdx, traceIdx)
	}

	m.applyEvent(agent.Event{Kind: agent.EventTextDelta, Text: "The answer is 4."})
	if len(m.blocks) != 2 {
		t.Fatalf("blocks = %d, want trace then reply", len(m.blocks))
	}
	if m.blocks[0].Kind != BlockReasoning || m.blocks[1].Kind != BlockAssistant {
		t.Fatalf("block order = %v then %v, want reasoning then assistant", m.blocks[0].Kind, m.blocks[1].Kind)
	}
	if m.blocks[0].Text != "weighing it up, the answer is the final word" {
		t.Errorf("trace text = %q, want the final word inside the thought", m.blocks[0].Text)
	}
}
