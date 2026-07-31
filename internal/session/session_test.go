package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"evilcode/internal/provider"
)

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir, "bat")
	if err != nil {
		t.Fatal(err)
	}
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "hi"},
		{Role: provider.RoleAssistant, Content: "hello", ToolCalls: []provider.ToolCall{
			{ID: "c1", Name: "read", Args: json.RawMessage(`{"path":"a.go"}`)},
		}},
		{Role: provider.RoleTool, Content: "package main", ToolCallID: "c1", ToolName: "read"},
	}
	for _, m := range msgs {
		if err := st.WriteMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := Messages(st.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("messages = %d, want 3", len(got))
	}
	if got[0].Content != "hi" || got[0].Role != provider.RoleUser {
		t.Errorf("message 0 = %+v", got[0])
	}
	if len(got[1].ToolCalls) != 1 || got[1].ToolCalls[0].Name != "read" {
		t.Errorf("tool calls did not survive: %+v", got[1])
	}
	if got[2].ToolCallID != "c1" {
		t.Errorf("tool result lost its call ID: %+v", got[2])
	}
}

func TestResumeAppends(t *testing.T) {
	dir := t.TempDir()
	st, _ := Open(dir, "bat")
	st.WriteMessage(provider.Message{Role: provider.RoleUser, Content: "first"})
	st.Close()

	st2, msgs, err := Resume(dir, "bat")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Content != "first" {
		t.Fatalf("resumed messages = %+v", msgs)
	}
	st2.WriteMessage(provider.Message{Role: provider.RoleUser, Content: "second"})
	st2.Close()

	final, _ := Messages(st2.Path)
	if len(final) != 2 || final[1].Content != "second" {
		t.Errorf("final messages = %+v, want the resume appended", final)
	}
}

func TestResumeUnknownSession(t *testing.T) {
	if _, _, err := Resume(t.TempDir(), "ghost"); err == nil {
		t.Error("want an error for an unknown session")
	}
}

func TestCrashDetection(t *testing.T) {
	dir := t.TempDir()

	clean, _ := Open(dir, "bat")
	clean.WriteMessage(provider.Message{Role: provider.RoleUser, Content: "x"})
	clean.Close()

	crashed, _ := Open(dir, "wraith")
	crashed.WriteMessage(provider.Message{Role: provider.RoleUser, Content: "x"})
	// No Close: this is what a kill -9 leaves behind.

	cleanInfo, err := Describe(dir, "bat")
	if err != nil {
		t.Fatal(err)
	}
	if cleanInfo.Crashed {
		t.Error("a cleanly closed session must not be reported as crashed")
	}

	crashInfo, err := Describe(dir, "wraith")
	if err != nil {
		t.Fatal(err)
	}
	if !crashInfo.Crashed {
		t.Error("a session with no clean-exit marker must be reported as crashed")
	}
}

func TestTruncatedFinalLineIsSkipped(t *testing.T) {
	// A hard kill mid-write leaves a partial JSON line. Recovering what
	// survived is the whole point of resuming.
	dir := t.TempDir()
	st, _ := Open(dir, "bat")
	st.WriteMessage(provider.Message{Role: provider.RoleUser, Content: "survived"})
	st.w.Flush()
	st.file.Close()

	f, _ := os.OpenFile(st.Path, os.O_WRONLY|os.O_APPEND, 0o644)
	f.WriteString(`{"ts":"2026-07-30T00:00:00Z","type":"user","data":{"role":"us`)
	f.Close()

	msgs, err := Messages(st.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Content != "survived" {
		t.Errorf("messages = %+v, want the intact entry recovered", msgs)
	}
}

func TestListSortsByRecency(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"bat", "wolf", "imp"} {
		st, _ := Open(dir, name)
		st.WriteMessage(provider.Message{Role: provider.RoleUser, Content: "x"})
		st.Close()
	}
	list, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("sessions = %d", len(list))
	}
	for i := 1; i < len(list); i++ {
		if list[i].Modified.After(list[i-1].Modified) {
			t.Error("sessions must be listed most recent first")
		}
	}
	for _, info := range list {
		if info.Emoji == "" {
			t.Errorf("%s has no emoji", info.Name)
		}
		if info.Messages != 1 {
			t.Errorf("%s counted %d messages, want 1", info.Name, info.Messages)
		}
	}
}

func TestListMissingDirectory(t *testing.T) {
	list, err := List(filepath.Join(t.TempDir(), "nothing"))
	if err != nil {
		t.Fatalf("a missing sessions directory is not an error: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("list = %v", list)
	}
}

func TestCreateIsDeterministicUnderTestMode(t *testing.T) {
	t.Setenv("EVILCODE_DETERMINISTIC", "1")
	dir := t.TempDir()
	st, err := Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if st.Name != "dracula" {
		t.Errorf("name = %q, want the fixed deterministic name", st.Name)
	}
}

func TestCreateAvoidsExistingNames(t *testing.T) {
	t.Setenv("EVILCODE_DETERMINISTIC", "")
	dir := t.TempDir()
	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		st, err := Create(dir)
		if err != nil {
			t.Fatal(err)
		}
		if seen[st.Name] {
			t.Fatalf("Create reused the name %q", st.Name)
		}
		seen[st.Name] = true
		st.Close()
	}
}

func TestFork(t *testing.T) {
	dir := t.TempDir()
	st, _ := Open(dir, "bat")
	st.WriteMessage(provider.Message{Role: provider.RoleUser, Content: "original"})
	st.Close()

	if err := Fork(dir, "bat", "bat-fork"); err != nil {
		t.Fatal(err)
	}
	msgs, err := Messages(filepath.Join(Dir(dir), "bat-fork.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Content != "original" {
		t.Errorf("forked messages = %+v", msgs)
	}
	if err := Fork(dir, "bat", "bat-fork"); err == nil {
		t.Error("forking onto an existing name must be refused")
	}
}

func TestTitleFromMeta(t *testing.T) {
	dir := t.TempDir()
	st, _ := Open(dir, "bat")
	st.WriteMeta(Meta{Kind: MetaTitle, Note: "wire the auth flow"})
	st.Close()

	info, err := Describe(dir, "bat")
	if err != nil {
		t.Fatal(err)
	}
	if info.Title != "wire the auth flow" {
		t.Errorf("title = %q", info.Title)
	}
}

func TestHistoryPersistsAcrossOpens(t *testing.T) {
	dir := t.TempDir()
	h, err := OpenHistory(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"first prompt", "second prompt"} {
		if err := h.Add(p); err != nil {
			t.Fatal(err)
		}
	}

	// A new session must see the earlier session's prompts (plan.md §5.2).
	h2, err := OpenHistory(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := h2.All()
	if len(got) != 2 || got[0] != "first prompt" {
		t.Errorf("history = %v", got)
	}
}

func TestHistorySkipsEmptyAndHugePrompts(t *testing.T) {
	dir := t.TempDir()
	h, _ := OpenHistory(dir)
	h.Add("")
	h.Add("   \n  ")
	h.Add(strings.Repeat("x", MaxPromptLen+1))
	if got := h.Len(); got != 0 {
		t.Errorf("history = %d entries, want none recorded", got)
	}

	h.Add(strings.Repeat("x", MaxPromptLen))
	if got := h.Len(); got != 1 {
		t.Errorf("a prompt exactly at the limit should be recorded, got %d", got)
	}
}

func TestHistorySkipsConsecutiveDuplicates(t *testing.T) {
	dir := t.TempDir()
	h, _ := OpenHistory(dir)
	h.Add("same")
	h.Add("same")
	h.Add("different")
	h.Add("same")
	got := h.All()
	if len(got) != 3 {
		t.Fatalf("history = %v, want consecutive duplicates collapsed but a later repeat kept", got)
	}
}

func TestHistoryCompaction(t *testing.T) {
	dir := t.TempDir()
	h, _ := OpenHistory(dir)
	for i := 0; i < CompactAt+10; i++ {
		if err := h.Add(strings.Repeat("p", 1) + string(rune('a'+i%26)) + strings.Repeat("q", i%7)); err != nil {
			t.Fatal(err)
		}
	}
	if got := h.Len(); got > HistoryCap {
		t.Errorf("history holds %d entries, want compaction down to %d", got, HistoryCap)
	}

	// The compacted file must be what a fresh open sees.
	h2, err := OpenHistory(dir)
	if err != nil {
		t.Fatal(err)
	}
	if h2.Len() != h.Len() {
		t.Errorf("reloaded %d entries, in-memory has %d", h2.Len(), h.Len())
	}
	if _, err := os.Stat(h.Path + ".tmp"); err == nil {
		t.Error("compaction left its temp file behind")
	}
}

func TestDedupeKeepsLatest(t *testing.T) {
	in := []string{"a", "b", "a", "c", "b"}
	got := dedupeKeepingLatest(in)
	want := []string{"a", "c", "b"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestHistorySearch(t *testing.T) {
	dir := t.TempDir()
	h, _ := OpenHistory(dir)
	for _, p := range []string{
		"fix the auth redirect loop",
		"fix the auth token refresh",
		"add a test for the parser",
		"refactor the scroll math",
	} {
		h.Add(p)
	}

	// An empty query matches nothing, as readline does.
	if got := h.Search("", 10); len(got) != 0 {
		t.Errorf("empty query returned %v, want nothing", got)
	}

	got := h.Search("auth", 10)
	if len(got) != 2 {
		t.Fatalf("search(auth) = %v, want both auth prompts", got)
	}
	for _, g := range got {
		if !strings.Contains(g, "auth") {
			t.Errorf("unexpected match %q", g)
		}
	}

	// Free-form fuzzy: characters in order, anywhere — not anchored.
	if got := h.Search("scrl", 10); len(got) != 1 || !strings.Contains(got[0], "scroll") {
		t.Errorf("search(scrl) = %v, want the scroll prompt", got)
	}
	if got := h.Search("zzzz", 10); len(got) != 0 {
		t.Errorf("search(zzzz) = %v, want nothing", got)
	}
}

func TestHistorySearchRespectsLimit(t *testing.T) {
	dir := t.TempDir()
	h, _ := OpenHistory(dir)
	for i := 0; i < 30; i++ {
		h.Add("prompt about auth number " + string(rune('a'+i)))
	}
	if got := h.Search("auth", 5); len(got) != 5 {
		t.Errorf("search returned %d, want the limit of 5", len(got))
	}
}

func TestFuzzyScorePrefersAdjacency(t *testing.T) {
	tight, ok1 := fuzzyScore("auth", "auth flow")
	scattered, ok2 := fuzzyScore("auth", "a very ugly thing here")
	if !ok1 || !ok2 {
		t.Fatalf("both should match: %v %v", ok1, ok2)
	}
	if tight <= scattered {
		t.Errorf("adjacent match scored %d, scattered scored %d — adjacency must win", tight, scattered)
	}
}
