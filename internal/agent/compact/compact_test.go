package compact

import (
	"encoding/json"
	"strings"
	"testing"

	"evilcode/internal/provider"
)

func userMsg(content string) provider.Message {
	return provider.Message{Role: provider.RoleUser, Content: content}
}

func asstMsg(content string) provider.Message {
	return provider.Message{Role: provider.RoleAssistant, Content: content}
}

func toolMsg(callID, name, content string) provider.Message {
	return provider.Message{Role: provider.RoleTool, ToolCallID: callID, ToolName: name, Content: content}
}

func callMsg(id, name, args string) provider.Message {
	return provider.Message{
		Role:      provider.RoleAssistant,
		ToolCalls: []provider.ToolCall{{ID: id, Name: name, Args: json.RawMessage(args)}},
	}
}

func TestCountTokensBasic(t *testing.T) {
	// cl100k tokenizes these to known counts (rough ranges, stable across the
	// embedded vocab).
	if got := countTokens(""); got != 0 {
		t.Errorf("countTokens(\"\") = %d, want 0", got)
	}
	if got := countTokens("hello world"); got < 1 || got > 4 {
		t.Errorf("countTokens(hello world) = %d, want 1-4", got)
	}
	// A code line tokenizes worse than bytes/4 would suggest.
	code := strings.Repeat("if err != nil {\n\treturn err\n}\n", 20)
	if got := countTokens(code); got < len(code)/8 {
		t.Errorf("countTokens(code) = %d, suspiciously small", got)
	}
}

func TestEstimateTokensImagesCostFixed(t *testing.T) {
	msg := provider.Message{Role: provider.RoleUser, Images: [][]byte{make([]byte, 8), make([]byte, 8)}}
	if got, want := estimateTokens(msg), 2*IMAGE_TOKEN_ESTIMATE; got != want {
		t.Errorf("estimateTokens = %d, want %d (fixed image estimate)", got, want)
	}
}

func TestSerializeConversationMarkers(t *testing.T) {
	msgs := []provider.Message{
		userMsg("fix the login bug"),
		callMsg("c1", "read", `{"path":"auth.go"}`),
		toolMsg("c1", "read", strings.Repeat("x", 3000)),
		asstMsg("fixed in auth.go"),
	}
	out := serializeConversation(msgs)
	for _, want := range []string{
		"[User]: fix the login bug",
		"[Tool Call]: read({\"path\":\"auth.go\"})",
		"[Tool Result (read)]: ",
		"[Assistant]: fixed in auth.go",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("serialized transcript missing %q:\n%s", want, out)
		}
	}
}

func TestSerializeConversationTruncatesToolResults(t *testing.T) {
	msgs := []provider.Message{
		toolMsg("c1", "read", strings.Repeat("a", TOOL_RESULT_MAX_CHARS+500)),
	}
	out := serializeConversation(msgs)
	if len(out) > TOOL_RESULT_MAX_CHARS+200 {
		t.Errorf("tool result not truncated: %d chars", len(out))
	}
	if !strings.Contains(out, "[truncated]") {
		t.Error("truncation marker missing")
	}
}

func TestSerializeConversationSkipsSystemAndHidden(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: "system prompt"},
		{Role: provider.RoleUser, Content: "hidden harness", Hidden: true},
		userMsg("visible"),
	}
	out := serializeConversation(msgs)
	if strings.Contains(out, "system prompt") || strings.Contains(out, "hidden harness") {
		t.Errorf("system/hidden leaked into transcript:\n%s", out)
	}
}

func TestFindValidCutPointsNeverToolResults(t *testing.T) {
	msgs := []provider.Message{
		userMsg("goal"),
		callMsg("c1", "read", "{}"),
		toolMsg("c1", "read", "out"),
		asstMsg("done"),
	}
	cuts := findValidCutPoints(msgs, 0, len(msgs))
	for _, c := range cuts {
		if msgs[c].Role == provider.RoleTool {
			t.Errorf("cut point at tool result index %d", c)
		}
	}
}

func TestFindCutPointKeepsRecentTokens(t *testing.T) {
	// 4 user turns, each a user message + assistant reply of known size.
	var msgs []provider.Message
	for i := 0; i < 4; i++ {
		msgs = append(msgs, userMsg(strings.Repeat("u", 2000)))
		msgs = append(msgs, asstMsg(strings.Repeat("a", 2000)))
	}
	// keepRecent ~ 2 messages worth of tokens.
	cut := FindCutPoint(msgs, 0, len(msgs), 1100)
	if cut.FirstKept <= len(msgs)-3 {
		t.Errorf("FirstKept = %d, want near the end", cut.FirstKept)
	}
	if cut.IsSplitTurn {
		t.Errorf("a clean user-boundary cut reported IsSplitTurn")
	}
}

func TestFindCutPointMidTurnSplits(t *testing.T) {
	// One user message, then a long assistant/tool stretch; budget lands in
	// the middle. The cut must fall on a valid boundary and report split.
	msgs := []provider.Message{
		userMsg("big task"),
		callMsg("c1", "read", "{}"),
		toolMsg("c1", "read", strings.Repeat("r", 8000)),
		asstMsg(strings.Repeat("m", 8000)),
	}
	cut := FindCutPoint(msgs, 0, len(msgs), estimateTokens(msgs[3])/2+estimateTokens(msgs[0]))
	if !cut.IsSplitTurn {
		t.Fatalf("want a split turn, got %+v", cut)
	}
	if cut.TurnStart != 0 {
		t.Errorf("TurnStart = %d, want 0", cut.TurnStart)
	}
}

func TestPrepareCompactionNoOpWhenNothingToSummarize(t *testing.T) {
	msgs := []provider.Message{userMsg("only turn")}
	if got := PrepareCompaction(msgs, DefaultSettings(), 100, 100); got != nil {
		t.Errorf("PrepareCompaction = %+v, want nil", got)
	}
}

func TestPrepareCompactionSkipsPriorCompactionRegion(t *testing.T) {
	msgs := []provider.Message{SummaryMessage("old summary")}
	for range 6 {
		msgs = append(msgs, userMsg("recent question "+strings.Repeat("q", 8000)))
		msgs = append(msgs, asstMsg(strings.Repeat("a", 8000)))
	}
	prep := PrepareCompaction(msgs, DefaultSettings(), 100, 0)
	if prep == nil {
		t.Fatal("PrepareCompaction = nil, want a preparation")
	}
	if prep.PreviousSummary != "old summary" {
		t.Errorf("PreviousSummary = %q", prep.PreviousSummary)
	}
	for _, m := range prep.MessagesToSummarize {
		if IsCompactionMarker(m) {
			t.Error("prior summary re-entered the summarization input")
		}
	}
}

func TestKeepRecentCalibratedShrinksOnDeflation(t *testing.T) {
	if got := keepRecentCalibrated(20000, 40000, 10000); got != 5000 {
		t.Errorf("ratio 4x must divide the budget; got %d", got)
	}
	if got := keepRecentCalibrated(20000, 5000, 10000); got != 20000 {
		t.Errorf("ratio <1 must not grow the budget; got %d", got)
	}
	if got := keepRecentCalibrated(20000, 0, 10000); got != 20000 {
		t.Errorf("no provider tokens must leave budget alone; got %d", got)
	}
}

func TestShouldCompactMatchesOmpThreshold(t *testing.T) {
	s := DefaultSettings()
	// 100k window, 16384 reserve → threshold at 83616.
	if got := ResolveThresholdTokens(100000, s); got != 83616 {
		t.Errorf("ResolveThresholdTokens = %d, want 83616", got)
	}
	if ShouldCompact(83616, 100000, s) {
		t.Error("compacted exactly at the threshold (omp requires strictly greater)")
	}
	if !ShouldCompact(83617, 100000, s) {
		t.Error("did not compact one token past the threshold")
	}
	if ShouldCompact(99999, 100000, Settings{Enabled: false}) {
		t.Error("disabled settings still compacted")
	}
}

func TestCompactionContextTokensTakesTheFloor(t *testing.T) {
	if got := CompactionContextTokens(100, 500); got != 500 {
		t.Errorf("local estimate must floor the decision; got %d", got)
	}
	if got := CompactionContextTokens(500, 100); got != 500 {
		t.Errorf("provider usage must win when larger; got %d", got)
	}
}

func TestSplitReadSelector(t *testing.T) {
	cases := [][2]string{
		{"src/main.go:50", "src/main.go"},
		{"src/main.go:50-200", "src/main.go"},
		{"src/main.go:5-16,960-973", "src/main.go"},
		{"src/main.go:raw", "src/main.go"},
		{"src/main.go:conflicts", "src/main.go"},
		{"weird:file.md", "weird:file.md"},    // not a selector shape
		{"C:/x/y", "C:/x/y"},                  // colon at 1: not a selector
		{"src/main.go:50+150", "src/main.go"}, // omp's +N chunk
		{"src/main.go", "src/main.go"},        // no selector
		{"src/main.go:50bad", "src/main.go:50bad"},
	}
	for _, c := range cases {
		if got := SplitReadSelector(c[0]); got != c[1] {
			t.Errorf("SplitReadSelector(%q) = %q, want %q", c[0], got, c[1])
		}
	}
}

func TestFileOpsExtractionAndUpsert(t *testing.T) {
	f := newFileOps()
	f.ExtractFromMessage(callMsg("c1", "read", `{"path":"src/auth.go:50-200"}`))
	f.ExtractFromMessage(callMsg("c2", "edit", `{"path":"src/auth.go"}`))
	f.ExtractFromMessage(callMsg("c3", "write", `{"path":"new.go"}`))
	f.ExtractFromMessage(callMsg("c4", "read", `{"path":"https://example.com/x"}`))

	reads, modified := f.ComputeFileLists()
	if len(reads) != 0 {
		t.Errorf("reads = %v, auth.go moved to modified", reads)
	}
	if len(modified) != 2 || modified[0] != "new.go" {
		t.Errorf("modified = %v", modified)
	}

	summary := UpsertFileOperations("## Goal\nfix", reads, modified, nil)
	if !strings.Contains(summary, "<files>") || !strings.Contains(summary, "auth.go") {
		t.Errorf("files block missing:\n%s", summary)
	}
	// Upsert replaces, never accumulates.
	again := UpsertFileOperations(summary, []string{"other.go"}, nil, nil)
	if strings.Contains(again, "auth.go") {
		t.Errorf("stale files block survived upsert:\n%s", again)
	}
}
