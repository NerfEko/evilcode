package compact

import (
	"context"
	"strings"
	"testing"

	"evilcode/internal/provider"
)

// Regression fixture for a real compaction failure: a session was compacted
// early by the old projection trigger; the weak summarizer answered the
// transcript's trailing question instead of summarizing, invented APIs, and
// the user's goal vanished — the session then chased phantom code.
//
// This file pins every defense the omp port added against that class of
// failure.

// regressionGoal is the session's actual first user message.
const regressionGoal = "sometimes evilcode will use agents and orchestrates even when the user doesnt ask. it should only happen when the user says the keyword or when specifically asked"

// regressionChatTurn mimics the bad summarizer output that passed all the old
// gates: emoji headers, a trailing question, invented code.
const regressionChatTurn = "**🎯 Interrupt Analysis & Continuation Guide**\n\n### **Next Steps:**\n1. Want me to read through hub.go lines ~250-400 next?\n\n```go\n// Likely around offset ~350:\nfunc (s *Server) SubmitTask(taskName string) { ... }\n```\n\n**What do you want to continue reading or analyze next?** 🔍"

// regressionOK is what a correct summary looks like for the same session.
const regressionGood = "## Goal\nThe user wants the agents/orchestrates keyword gating fixed: agents should run only when the user says the keyword or explicitly asks.\n\n## Progress\n\n### Done\n- [x] located NewOrchestrateHook in internal/agent/orchestrate.go\n\n## Next Steps\n1. gate the hook on the keyword"

// regressionTranscript builds the shape that killed the session: a 50KB
// renderer-noise tool result and a trailing question in the last turn.
func regressionTranscript() []provider.Message {
	noise := strings.Repeat("internal/agent/agent.go:495 — shown above\n", 2400)
	return []provider.Message{
		{Role: provider.RoleUser, Content: regressionGoal},
		{Role: provider.RoleAssistant, Content: "**Inspecting repo for system prompts**", ToolCalls: []provider.ToolCall{{ID: "c1", Name: "grep", Args: []byte(`{"pattern":"NewOrchestrateHook"}`)}}},
		{Role: provider.RoleTool, ToolCallID: "c1", ToolName: "grep", Content: noise},
		{Role: provider.RoleUser, Content: "keep going"},
		{Role: provider.RoleAssistant, Content: "**Reading agent.go**", ToolCalls: []provider.ToolCall{{ID: "c2", Name: "read", Args: []byte(`{"path":"internal/agent/agent.go:90-520"}`)}}},
		{Role: provider.RoleTool, ToolCallID: "c2", ToolName: "read", Content: noise},
	}
}

// TestSummaryRejectionTriggerDoesNotFireEarly: omp compacts only near the real window.
// The old engine's projection (2 samples × 15-turn lookahead) compacted
// a session at 3.5 minutes of age with the context nowhere near full. On the
// omp rule, a context 40% into the window must NOT compact.
func TestSummaryRejectionTriggerDoesNotFireEarly(t *testing.T) {
	msgs := regressionTranscript()
	est := EstimateConversation(msgs) // ~3-4k tokens
	s := DefaultSettings()
	if ShouldCompact(est, 100000, s) {
		t.Fatalf("compacted at %d tokens of a 100k window (threshold %d)", est, ResolveThresholdTokens(100000, s))
	}
}

// TestSummaryRejectionSummaryCannotAnswerTheTranscript pins the anti-continuation
// gates: the mimicked chat turn fails every one.
func TestSummaryRejectionSummaryCannotBeAChatTurn(t *testing.T) {
	if err := ValidateSummary(regressionChatTurn, regressionGoal); err == nil {
		t.Fatal("the chat-turn summary passed validation")
	}
}

// TestSummaryRejectionSummaryMustPreserveGoal pins the goal-echo gate on a good
// summary.
func TestSummaryRejectionSummaryPreservesGoal(t *testing.T) {
	if err := ValidateSummary(regressionGood, regressionGoal); err != nil {
		t.Fatalf("the well-formed goal-preserving summary was rejected: %v", err)
	}
}

// TestSummaryRejectionFullFlow: the log's newest stretch is one fat tool
// result — no valid cut point at or after the budget crossing, so omp's
// prepare returns undefined exactly like ours. omp recovers that shape
// through the overflow path (CompactWhole): the request fails, recovery
// folds everything with no tail. The flow test drives it.
func TestSummaryRejectionFullFlow(t *testing.T) {
	msgs := regressionTranscript()
	est := EstimateConversation(msgs)
	prep := PrepareCompaction(msgs, DefaultSettings(), est, est)
	if prep == nil {
		// omp-faithful: no summarizable prefix at the budget → the overflow
		// path is the one that rescues this shape. Build the whole-mode prep
		// directly: every message summarized, no tail.
		prep = &Preparation{
			MessagesToSummarize: msgs,
			TokensBefore:        est,
		}
	}
	res, _, err := Compact(context.Background(), prep,
		func(_ context.Context, _, system, user string) (string, error) {
			if strings.Contains(user, CompactionShortSummaryPrompt) {
				return "I fixed the keyword gating.", nil
			}
			return regressionGood, nil
		},
		oneCandidate(), "", SummaryOptions{}, regressionGoal)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if !strings.Contains(res.Summary, "keyword") {
		t.Errorf("summary dropped the goal: %q", res.Summary)
	}
	if !strings.Contains(res.Summary, "<files>") {
		t.Error("mechanical file injection missing")
	}
	if strings.HasSuffix(strings.TrimRight(res.Summary, " \n\t"), "?") {
		t.Error("summary ends in a question — it answered the transcript")
	}
	if len(res.Summary) > SUMMARIZATION_MAX_BYTES {
		t.Error("summary over the size cap")
	}
	// The 50KB noise result is truncated in any serialized transcript.
	serialized := Serialize(msgs)
	if len(serialized) > 100000 {
		t.Errorf("serialized transcript = %d bytes; the tool-result cap failed", len(serialized))
	}
}
