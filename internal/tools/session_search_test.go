package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSearchSession(t *testing.T, dir, name, body string) {
	t.Helper()
	sessions := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	content := strings.Join([]string{
		`{"ts":"2026-08-01T10:00:00Z","type":"meta","data":{"kind":"start"}}`,
		`{"ts":"2026-08-01T10:00:01Z","type":"message","data":{"role":"user","content":"` + body + `"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(sessions, name+".jsonl"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSessionSearchToolReturnsRoleDateAndExcerpt(t *testing.T) {
	dir := t.TempDir()
	writeSearchSession(t, dir, "anchor", "I need the anchor parser repaired")

	tool := NewSessionSearch(dir, "current")
	result, err := tool.Run(context.Background(), json.RawMessage(`{"query":"anchor parser","role":"user"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Output, "anchor") || !strings.Contains(result.Output, "user") || !strings.Contains(result.Output, "anchor parser") {
		t.Fatalf("session_search output = %q, want name, role, and excerpt", result.Output)
	}
}

func TestSessionSearchIndexReusesUnchangedFiles(t *testing.T) {
	dir := t.TempDir()
	writeSearchSession(t, dir, "anchor", "the anchor parser is recoverable")
	index := newSessionSearchIndex()

	if _, err := index.search(context.Background(), dir, "current", "anchor parser", "any", 10); err != nil {
		t.Fatal(err)
	}
	files, reads := index.stats()
	if files != 1 || reads != 1 {
		t.Fatalf("initial index stats = files %d reads %d, want 1/1", files, reads)
	}
	if _, err := index.search(context.Background(), dir, "current", "parser", "any", 10); err != nil {
		t.Fatal(err)
	}
	_, reads = index.stats()
	if reads != 1 {
		t.Fatalf("repeated search reads = %d, want unchanged file reuse", reads)
	}
}

func TestSessionSearchSalvagesGluedTail(t *testing.T) {
	dir := t.TempDir()
	sessions := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"ts":"2026-08-01T10:00:00Z","type":"assistant","data":{"role":"assistant","content":"torn` +
		`{"ts":"2026-08-01T10:00:01Z","type":"user","data":{"role":"user","content":"recover the glued transcript"}}` + "\n"
	if err := os.WriteFile(filepath.Join(sessions, "damaged.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := NewSessionSearch(dir, "current").Run(context.Background(), json.RawMessage(`{"query":"glued transcript","role":"user"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Output, "damaged") {
		t.Fatalf("search missed a complete record after a torn prefix: %q", result.Output)
	}
}

func TestSessionSearchUsesLiveCurrentName(t *testing.T) {
	dir := t.TempDir()
	writeSearchSession(t, dir, "current", "the active session should stay private")
	name := "current"
	tool := NewSessionSearchWithCurrentName(dir, func() string { return name })

	result, err := tool.Run(context.Background(), json.RawMessage(`{"query":"active session"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Output, "current") {
		t.Fatalf("active session appeared before rename: %q", result.Output)
	}
	if err := os.Rename(filepath.Join(dir, "sessions", "current.jsonl"), filepath.Join(dir, "sessions", "renamed.jsonl")); err != nil {
		t.Fatal(err)
	}
	name = "renamed"
	result, err = tool.Run(context.Background(), json.RawMessage(`{"query":"active session"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Output, "renamed") {
		t.Fatalf("active session appeared after rename: %q", result.Output)
	}
}

func TestSessionSearchToolNameMatchesNonEmptyToolResult(t *testing.T) {
	dir := t.TempDir()
	sessions := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"ts":"2026-08-01T10:00:00Z","type":"tool","data":{"role":"tool","tool_name":"grep","content":"matched 3 lines"}}` + "\n"
	if err := os.WriteFile(filepath.Join(sessions, "tools.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := NewSessionSearch(dir, "current").Run(context.Background(), json.RawMessage(`{"query":"grep","role":"tool"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Output, "tools") {
		t.Fatalf("tool name was not searchable in a non-empty result: %q", result.Output)
	}
}

func TestSessionSearchBoundsRetainedMessageText(t *testing.T) {
	dir := t.TempDir()
	writeSearchSession(t, dir, "large", strings.Repeat("anchor ", maxIndexedMessageBytes))
	index := newSessionSearchIndex()
	if _, err := index.search(context.Background(), dir, "current", "anchor", "any", 10); err != nil {
		t.Fatal(err)
	}
	for _, file := range index.files {
		if len(file.messages) == 0 || len(file.messages[0].Text) > maxIndexedMessageBytes {
			t.Fatalf("indexed message text is unbounded: %+v", file)
		}
	}
}

func TestSessionSearchHonorsCancellation(t *testing.T) {
	dir := t.TempDir()
	writeSearchSession(t, dir, "anchor", "the anchor parser is recoverable")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewSessionSearch(dir, "current").Run(ctx, json.RawMessage(`{"query":"anchor"}`))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled search error = %v, want context.Canceled", err)
	}
}

func TestSessionSearchRetainsTailOfLargeSession(t *testing.T) {
	dir := t.TempDir()
	sessions := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	var body strings.Builder
	for i := 0; i < 6000; i++ {
		term := "old filler"
		if i == 5999 {
			term = "recent tail marker"
		}
		fmt.Fprintf(&body, `{"ts":"2026-08-01T10:00:00Z","type":"message","data":{"role":"user","content":"%s %s"}}`+"\n", term, strings.Repeat("x", 180))
	}
	if err := os.WriteFile(filepath.Join(sessions, "long.jsonl"), []byte(body.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := NewSessionSearch(dir, "current").Run(context.Background(), json.RawMessage(`{"query":"tail marker","role":"user"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Output, "long") {
		t.Fatalf("search missed the retained tail of a large session: %q", result.Output)
	}
}

func TestSessionSearchFallsBackToEarlyMessagesEvictedFromBoundedIndex(t *testing.T) {
	dir := t.TempDir()
	sessions := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	var body strings.Builder
	for i := 0; i < 6000; i++ {
		term := "ordinary filler"
		if i == 0 {
			term = "early coverage marker"
		}
		fmt.Fprintf(&body, `{"ts":"2026-08-01T10:00:00Z","type":"message","data":{"role":"user","content":"%s %s"}}`+"\n", term, strings.Repeat("x", 180))
	}
	if err := os.WriteFile(filepath.Join(sessions, "long-early.jsonl"), []byte(body.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := NewSessionSearch(dir, "current").Run(context.Background(), json.RawMessage(`{"query":"early coverage marker","role":"user"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Output, "long-early [") {
		t.Fatalf("search missed an early message evicted by the bounded index: %q", result.Output)
	}
}

func TestSessionSearchFallsBackPastPerMessageTermCeiling(t *testing.T) {
	dir := t.TempDir()
	var content strings.Builder
	for i := 0; i < maxIndexedTermsPerMsg+20; i++ {
		fmt.Fprintf(&content, "term%d ", i)
	}
	content.WriteString("late vocabulary marker")
	writeSearchSession(t, dir, "wide-vocabulary", content.String())
	result, err := NewSessionSearch(dir, "current").Run(context.Background(), json.RawMessage(`{"query":"late vocabulary marker","role":"user"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Output, "wide-vocabulary [") {
		t.Fatalf("search missed terms beyond the per-message dictionary ceiling: %q", result.Output)
	}
}

func TestSessionSearchCentersExcerptForLongMessage(t *testing.T) {
	dir := t.TempDir()
	sessions := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	content := strings.Repeat("head ", 5000) + "needleunique " + strings.Repeat("tail ", 5000)
	data, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	line := fmt.Sprintf(`{"ts":"2026-08-01T10:00:00Z","type":"message","data":{"role":"user","content":%s}}`+"\n", data)
	if err := os.WriteFile(filepath.Join(sessions, "wide.jsonl"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := NewSessionSearch(dir, "current").Run(context.Background(), json.RawMessage(`{"query":"needleunique","role":"user"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Output, "needleunique") {
		t.Fatalf("long-message excerpt omitted the matching text: %q", result.Output)
	}
}

func TestSessionSearchCentersEachMatchingLongMessage(t *testing.T) {
	dir := t.TempDir()
	sessions := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	makeContent := func(prefix string) string {
		return strings.Repeat(prefix+" ", 5000) + "needleunique " + strings.Repeat("tail ", 5000)
	}
	first, _ := json.Marshal(makeContent("first"))
	second, _ := json.Marshal(makeContent("second"))
	body := fmt.Sprintf(
		`{"ts":"2026-08-01T10:00:00Z","type":"message","data":{"role":"user","content":%s}}`+"\n"+`{"ts":"2026-08-02T10:00:00Z","type":"message","data":{"role":"user","content":%s}}`+"\n",
		first, second)
	if err := os.WriteFile(filepath.Join(sessions, "two-wide.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := NewSessionSearch(dir, "current").Run(context.Background(), json.RawMessage(`{"query":"needleunique","role":"user","limit":10}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Output, "first") || !strings.Contains(result.Output, "second") {
		t.Fatalf("matching long messages did not retain their own excerpts: %q", result.Output)
	}
}
