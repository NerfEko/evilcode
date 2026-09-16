package compact

import (
	"context"
	"strings"
	"testing"

	"evilcode/internal/provider"
)

// goalOK is a summary that passes every gate for the goal "fix the login bug
// in auth.go".
const goalOK = `## Goal
Fix the auth.go login bug.

## Progress

### Done
- [x] patched auth.go

## Next Steps
1. run tests`

// chatTurn mimics the Toad-22 failure: the summarizer answered the
// transcript instead of summarizing it.
const chatTurn = `Great question! Would you like me to continue reading the file next?`

func summarizeConst(s string) Summarizer {
	return func(context.Context, ModelRef, string, string) (string, error) { return s, nil }
}

func oneCandidate() []ModelInfoLite { return []ModelInfoLite{{Ref: "m@p", ContextWindow: 100000}} }

func TestValidateSummaryGates(t *testing.T) {
	goal := "fix the login bug in auth.go"
	if err := ValidateSummary("", goal); err == nil {
		t.Error("empty summary passed")
	}
	if err := ValidateSummary(strings.Repeat("x", SUMMARIZATION_MAX_BYTES+10), goal); err == nil {
		t.Error("oversized summary passed")
	}
	if err := ValidateSummary("just some prose without structure", goal); err == nil {
		t.Error("unstructured summary passed")
	}
	if err := ValidateSummary("## Goal\nunrelated topic entirely about widgets", goal); err == nil {
		t.Error("summary that dropped the goal passed")
	}
	if err := ValidateSummary(chatTurn, goal); err == nil {
		t.Error("summary that answers the transcript (trailing question) passed")
	}
	if err := ValidateSummary("## Goal\nfix login in auth.go\n\nWant me to continue reading next?", goal); err == nil {
		t.Error("sectioned summary ending in a question passed")
	}
	if err := ValidateSummary(goalOK, goal); err != nil {
		t.Errorf("well-formed summary rejected: %v", err)
	}
}

func TestCompactRunsFileOpsAndShortSummary(t *testing.T) {
	prep := &Preparation{
		MessagesToSummarize: []provider.Message{
			userMsg("fix the login bug in auth.go"),
			callMsg("c1", "read", `{"path":"auth.go"}`),
			toolMsg("c1", "read", "contents"),
			asstMsg("patched"),
		},
		RecentMessages: []provider.Message{userMsg("thanks")},
		TokensBefore:   1000,
	}
	var shortPromptSeen bool
	summarizer := func(_ context.Context, _, system, user string) (string, error) {
		if strings.Contains(user, CompactionShortSummaryPrompt) {
			shortPromptSeen = true
			return "I fixed the login bug.", nil
		}
		return goalOK, nil
	}
	res, _, err := Compact(context.Background(), prep, summarizer, oneCandidate(), "", SummaryOptions{}, "fix the login bug in auth.go")
	if err != nil {
		t.Fatalf("Compact = %v", err)
	}
	if !shortPromptSeen {
		t.Error("short summary side-call never ran")
	}
	if !strings.Contains(res.Summary, "<files>") || !strings.Contains(res.Summary, "auth.go") {
		t.Errorf("mechanical file injection missing:\n%s", res.Summary)
	}
	if res.ShortSummary != "I fixed the login bug." {
		t.Errorf("ShortSummary = %q", res.ShortSummary)
	}
}

func TestCompactRetriesRejectedSummaryThenNextModel(t *testing.T) {
	prep := &Preparation{
		MessagesToSummarize: []provider.Message{
			userMsg("fix the login bug in auth.go"),
			asstMsg("done"),
		},
		RecentMessages: []provider.Message{userMsg("ok")},
	}
	calls := 0
	summarizer := func(_ context.Context, _, _, user string) (string, error) {
		calls++
		if strings.Contains(user, "failed validation") {
			// The corrective retry fixed it.
			return goalOK, nil
		}
		return chatTurn, nil
	}
	res, _, err := Compact(context.Background(), prep, summarizer, oneCandidate(), "", SummaryOptions{}, "fix the login bug in auth.go")
	if err != nil {
		t.Fatalf("corrective retry did not rescue the summary: %v", err)
	}
	if calls < 3 { // summary + short + corrected summary (+ short again)
		t.Errorf("calls = %d", calls)
	}
	if !strings.Contains(res.Summary, "## Goal") {
		t.Error("retry result not adopted")
	}
}

func TestCompactFallsBackToNextCandidate(t *testing.T) {
	prep := &Preparation{
		MessagesToSummarize: []provider.Message{userMsg("fix the login bug in auth.go")},
		RecentMessages:      []provider.Message{userMsg("tail")},
	}
	summarizer := func(_ context.Context, model ModelRef, _, _ string) (string, error) {
		if model == "weak@local" {
			return "", context.DeadlineExceeded
		}
		return goalOK, nil
	}
	candidates := []ModelInfoLite{{Ref: "weak@local"}, {Ref: "strong@cloud"}}
	res, used, err := Compact(context.Background(), prep, summarizer, candidates, "", SummaryOptions{}, "fix the login bug in auth.go")
	if err != nil {
		t.Fatalf("candidate fallback failed: %v", err)
	}
	if used != "strong@cloud" {
		t.Errorf("used model = %q, want the second candidate", used)
	}
	if res == nil || !strings.Contains(res.Summary, "## Goal") {
		t.Error("no summary from fallback model")
	}
}

func TestCompactSplitTurnMergesPrefixSummary(t *testing.T) {
	prep := &Preparation{
		MessagesToSummarize: []provider.Message{userMsg("fix the login bug in auth.go")},
		TurnPrefixMessages:  []provider.Message{userMsg("first half of work")},
		IsSplitTurn:         true,
		RecentMessages:      []provider.Message{asstMsg("tail")},
	}
	summarizer := func(_ context.Context, _, _, user string) (string, error) {
		if strings.Contains(user, CompactionTurnPrefixPrompt) {
			return "## Original Request\nfirst half of work\n\n## Early Progress\n- started", nil
		}
		if strings.Contains(user, CompactionShortSummaryPrompt) {
			return "short", nil
		}
		return goalOK, nil
	}
	res, _, err := Compact(context.Background(), prep, summarizer, oneCandidate(), "", SummaryOptions{}, "fix the login bug in auth.go")
	if err != nil {
		t.Fatalf("Compact = %v", err)
	}
	if !strings.Contains(res.Summary, "**Turn Context (split turn):**") {
		t.Errorf("split-turn merge missing:\n%s", res.Summary)
	}
}
