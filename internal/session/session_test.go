package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"unicode/utf8"

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
		{Role: provider.RoleAssistant, Content: "hello",
			ProviderItems: []json.RawMessage{
				json.RawMessage(`{"type":"reasoning","id":"rs_1","encrypted_content":"opaque"}`),
				json.RawMessage(`{"type":"function_call","id":"fc_1","call_id":"c1","name":"read","arguments":"{\"path\":\"a.go\"}"}`),
			},
			ToolCalls: []provider.ToolCall{
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
	if len(got[1].ProviderItems) != 2 || !strings.Contains(string(got[1].ProviderItems[0]), "encrypted_content") {
		t.Errorf("provider items did not survive session replay: %s", got[1].ProviderItems)
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

func TestStoreRefusesAppendsAfterCloseStarts(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir, "bat")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteMessage(provider.Message{Role: provider.RoleUser, Content: "too late"}); err == nil {
		t.Fatal("an append succeeded after the clean-exit marker")
	}
	msgs, err := Messages(st.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Fatalf("message was written after close: %+v", msgs)
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

func TestCrashAfterResumeIsDetected(t *testing.T) {
	dir := t.TempDir()

	st, _ := Open(dir, "phantom")
	st.WriteMessage(provider.Message{Role: provider.RoleUser, Content: "first"})
	st.Close() // clean_exit #1: the first run ended cleanly.

	st2, _, err := Resume(dir, "phantom")
	if err != nil {
		t.Fatal(err)
	}
	st2.WriteMessage(provider.Message{Role: provider.RoleUser, Content: "second"})
	// No Close: this run crashed after the resume, despite the earlier clean exit.

	info, err := Describe(dir, "phantom")
	if err != nil {
		t.Fatal(err)
	}
	if !info.Crashed {
		t.Error("a crash after resume must not be masked by an earlier clean-exit marker")
	}
}

// TestCloseAlwaysReleasesTheDescriptor forces the clean-exit meta-write inside
// Close to fail (by pointing the store's fd at /dev/full, so the write
// syscall returns ENOSPC) and checks the descriptor is still released. Before
// H5.12, Close returned on that error without ever reaching closeFile, so the
// fd stayed open — checkable on Linux via /proc/self/fd.
func TestCloseAlwaysReleasesTheDescriptor(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir, "bat")
	if err != nil {
		t.Fatal(err)
	}

	full, err := os.OpenFile("/dev/full", os.O_WRONLY, 0)
	if err != nil {
		t.Skipf("/dev/full unavailable: %v", err)
	}
	defer full.Close()

	fd := int(st.file.Fd())
	if err := syscall.Dup2(int(full.Fd()), fd); err != nil {
		t.Fatalf("dup2: %v", err)
	}
	procPath := fmt.Sprintf("/proc/self/fd/%d", fd)
	if _, err := os.Lstat(procPath); err != nil {
		t.Skipf("no /proc/self/fd support: %v", err)
	}

	if err := st.Close(); err == nil {
		t.Fatal("want an error from a write that hit /dev/full")
	}

	if _, err := os.Lstat(procPath); err == nil {
		t.Errorf("fd %d still open after Close failed on the meta write — descriptor leaked", fd)
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

// A session that was opened and quit without a prompt leaves a log with only
// lifecycle markers — nothing to resume. The picker must not list it, and
// neither must completions or name allocation, all of which go through List.
func TestListOmitsEmptySessions(t *testing.T) {
	dir := t.TempDir()

	// An empty session: created (writes a start marker), closed, never said.
	empty, err := CreateNamed(dir, "ghost")
	if err != nil {
		t.Fatal(err)
	}
	if err := empty.Close(); err != nil {
		t.Fatal(err)
	}

	// A real session with one message, for contrast.
	real, err := CreateNamed(dir, "bat")
	if err != nil {
		t.Fatal(err)
	}
	real.WriteMessage(provider.Message{Role: provider.RoleUser, Content: "hi"})
	real.Close()

	list, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("list = %d sessions, want only the non-empty one: %+v", len(list), list)
	}
	if list[0].Name != "bat" {
		t.Errorf("listed %q, want bat", list[0].Name)
	}
}

// Name allocation must not propose a name whose empty log is on disk, even
// though List hides that log from the resume picker. The name is still claimed
// at the O_EXCL layer CreateNamed guards with, and the daemon's claimName can
// only advance past names its in-memory set knows — handing it a disk-claimed
// name makes it retry the same name until it gives up.
func TestCreateSkipsNamesClaimedByEmptyFiles(t *testing.T) {
	t.Setenv("EVILCODE_DETERMINISTIC", "")
	dir := t.TempDir()

	empty, err := CreateNamed(dir, "raven")
	if err != nil {
		t.Fatal(err)
	}
	empty.Close() // leaves a 0-message raven.jsonl

	for range 40 {
		st, err := Create(dir)
		if err != nil {
			t.Fatalf("Create failed: %v", err)
		}
		if st.Name == "raven" {
			t.Fatalf("Create reused the disk-claimed empty name %q", st.Name)
		}
		st.Close()
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

// TestRememberedModel records model switches and checks that Describe reports
// the last one, which is what /resume resolves against. Last-write-wins is the
// point: a session that switched mid-run resumes on the model it ended on.
func TestRememberedModel(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir, "bat")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.WriteModel("llama2:7b@ollama-local"); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteModel("deepseek-chat@deepseek"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	info, err := Describe(dir, "bat")
	if err != nil {
		t.Fatal(err)
	}
	if info.Model != "deepseek-chat@deepseek" {
		t.Errorf("Model = %q, want the last-written deepseek-chat@deepseek", info.Model)
	}
}

// TestRememberedModelEmptyForOldSessions confirms a session with no model meta
// leaves Model empty, so the resume path falls through to the config default
// rather than resolving against garbage.
func TestRememberedModelEmptyForOldSessions(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir, "bat")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.WriteMessage(provider.Message{Role: provider.RoleUser, Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	info, err := Describe(dir, "bat")
	if err != nil {
		t.Fatal(err)
	}
	if info.Model != "" {
		t.Errorf("Model = %q, want empty for a session with no model meta", info.Model)
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

func TestHistoryDoesNotKeepAnUnpersistedPrompt(t *testing.T) {
	h, _ := OpenHistory(t.TempDir())
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.Path = filepath.Join(blocked, "history.jsonl")
	if err := h.Add("must not stick"); err == nil {
		t.Fatal("writing below a regular file unexpectedly succeeded")
	}
	if h.Len() != 0 {
		t.Fatalf("history retained an unpersisted prompt: %v", h.All())
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

func TestCheckpointAndRewindPoints(t *testing.T) {
	dir := t.TempDir()
	st, _ := Open(dir, "bat")
	st.WriteMessage(provider.Message{Role: provider.RoleUser, Content: "first task"})
	st.WriteMessage(provider.Message{Role: provider.RoleAssistant, Content: "done"})
	st.WriteCheckpoint("after-first")
	st.WriteMessage(provider.Message{Role: provider.RoleUser, Content: "second task"})
	st.Close()

	cps, err := Checkpoints(st.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cps) != 1 || cps[0].Name != "after-first" {
		t.Fatalf("checkpoints = %+v", cps)
	}

	points, err := RewindPoints(st.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 {
		t.Fatalf("rewind points = %+v, want 2", points)
	}
	if points[0].Index != 1 || points[1].Index != 2 {
		t.Errorf("points should be numbered from 1: %+v", points)
	}
}

func TestRewindPointsSkipHarnessMessages(t *testing.T) {
	// An automated continuation is not a point a person would think of
	// rewinding to.
	dir := t.TempDir()
	st, _ := Open(dir, "bat")
	st.WriteMessage(provider.Message{Role: provider.RoleUser, Content: "real prompt"})
	st.WriteMessage(provider.Message{
		Role: provider.RoleUser, Content: "[automated todo completion gate] keep going",
	})
	st.Close()

	points, _ := RewindPoints(st.Path)
	if len(points) != 1 || points[0].Prompt != "real prompt" {
		t.Errorf("points = %+v, want only the real prompt", points)
	}
}

func TestRewindTruncatesAndBacksUp(t *testing.T) {
	dir := t.TempDir()
	st, _ := Open(dir, "bat")
	st.WriteMessage(provider.Message{Role: provider.RoleUser, Content: "one"})
	st.WriteMessage(provider.Message{Role: provider.RoleAssistant, Content: "a"})
	st.WriteMessage(provider.Message{Role: provider.RoleUser, Content: "two"})
	st.WriteMessage(provider.Message{Role: provider.RoleAssistant, Content: "b"})
	st.Close()

	points, _ := RewindPoints(st.Path)
	if len(points) != 2 {
		t.Fatalf("points = %+v", points)
	}

	kept, err := Rewind(dir, "bat", points[1].Entry)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range kept {
		if m.Content == "two" || m.Content == "b" {
			t.Errorf("rewind kept a message it should have pruned: %q", m.Content)
		}
	}
	// A mistaken rewind must be recoverable.
	if _, err := os.Stat(st.Path + ".bak"); err != nil {
		t.Error("rewind should leave a backup")
	}
}

func TestCollapseSummaryDescribesWhatWasLost(t *testing.T) {
	got := CollapseSummary([]provider.Message{
		{Role: provider.RoleUser, Content: "do the thing"},
		{Role: provider.RoleTool, Content: "output"},
		{Role: provider.RoleAssistant, Content: "I changed three files."},
	})
	for _, want := range []string{"1 prompt", "1 tool call", "I changed three files", "still changed"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary is missing %q:\n%s", want, got)
		}
	}
	if got := CollapseSummary(nil); got != "" {
		t.Errorf("an empty prune should produce no summary, got %q", got)
	}
}

func TestCollapseSummaryDoesNotSplitUTF8(t *testing.T) {
	got := CollapseSummary([]provider.Message{
		{Role: provider.RoleAssistant, Content: strings.Repeat("🔥", 401)},
	})
	if !strings.Contains(got, "…") || !strings.HasSuffix(got, "memories were kept.") {
		t.Fatalf("summary was not formed: %q", got)
	}
	if !strings.Contains(got, strings.Repeat("🔥", 400)) {
		t.Fatal("summary lost or split a complete rune")
	}
}

func TestSaveMarksTheSession(t *testing.T) {
	dir := t.TempDir()
	st, _ := Open(dir, "bat")
	st.WriteMessage(provider.Message{Role: provider.RoleUser, Content: "x"})
	st.Close()

	if err := Save(dir, "bat", true); err != nil {
		t.Fatal(err)
	}
	info, _ := Describe(dir, "bat")
	if !info.Saved {
		t.Error("session should be pinned")
	}

	Save(dir, "bat", false)
	info, _ = Describe(dir, "bat")
	if info.Saved {
		t.Error("session should be unpinned")
	}
}

func TestSaveDoesNotMaskACrash(t *testing.T) {
	dir := t.TempDir()
	st, _ := Open(dir, "bat")
	st.WriteMessage(provider.Message{Role: provider.RoleUser, Content: "x"})
	// No Close: the session is still open elsewhere, same as a real pin/unpin
	// issued from the TUI mid-turn.

	if err := Save(dir, "bat", true); err != nil {
		t.Fatal(err)
	}

	info, err := Describe(dir, "bat")
	if err != nil {
		t.Fatal(err)
	}
	if !info.Saved {
		t.Error("session should be pinned")
	}
	if !info.Crashed {
		t.Error("pinning a still-open session must not fake a clean exit")
	}
}

func TestRenameRefusesCollisionsAndBadNames(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"bat", "wolf"} {
		st, _ := Open(dir, n)
		st.Close()
	}
	if err := Rename(dir, "bat", "wolf"); err == nil {
		t.Error("renaming onto an existing session must be refused")
	}
	// The name becomes a filename, so it has to stay safe.
	for _, bad := range []string{"", "a/b", "with space"} {
		if err := Rename(dir, "bat", bad); err == nil {
			t.Errorf("rename to %q should be refused", bad)
		}
	}
	if err := Rename(dir, "bat", "raven"); err != nil {
		t.Errorf("a valid rename failed: %v", err)
	}
}

func TestStoreRenameRollsBackWhenAttachmentDestinationExists(t *testing.T) {
	dir := t.TempDir()
	st, err := CreateNamed(dir, "old")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := os.Mkdir(blobDir(st.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(blobDir(filepath.Join(Dir(dir), "new.jsonl")), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := st.Rename(dir, "new"); err == nil {
		t.Fatal("rename succeeded despite an existing attachment destination")
	}
	if st.Name != "old" || st.Path != filepath.Join(Dir(dir), "old.jsonl") {
		t.Fatalf("live identity changed after failed rename: %q %q", st.Name, st.Path)
	}
	if _, err := os.Stat(st.Path); err != nil {
		t.Fatalf("source log disappeared after failed rename: %v", err)
	}
}

func TestDeriveTitlePrefersWhatTheAgentUnderstood(t *testing.T) {
	// The list should be labeled by what the agent understood you wanted,
	// which is more useful than the first thing you happened to type.
	if got := DeriveTitle("auth flow", "ship the gate", "read the handler", "hi"); got != "auth flow" {
		t.Errorf("got %q, want the active group", got)
	}
	if got := DeriveTitle("", "ship the gate", "read the handler", "hi"); got != "ship the gate" {
		t.Errorf("got %q, want the stated intention", got)
	}
	if got := DeriveTitle("", "", "read the handler", "hi"); got != "read the handler" {
		t.Errorf("got %q, want the first todo", got)
	}
	if got := DeriveTitle("", "", "", "hi"); got != "hi" {
		t.Errorf("got %q, want the prompt fallback", got)
	}
	if got := DeriveTitle("", "", "", ""); got != "" {
		t.Errorf("got %q, want empty", got)
	}
	unicodeTitle := strings.Repeat("é", 31)
	if got := DeriveTitle(unicodeTitle, "", "", ""); !utf8.ValidString(got) {
		t.Errorf("title split a UTF-8 sequence: %q", got)
	}
}

func TestTransferCarriesASummary(t *testing.T) {
	dir := t.TempDir()
	old, _ := Open(dir, "bat")
	old.Close()

	if err := Transfer(dir, "bat", "raven", "we wired the auth flow"); err != nil {
		t.Fatal(err)
	}
	msgs, _ := Messages(filepath.Join(Dir(dir), "raven.jsonl"))
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content, "we wired the auth flow") {
		t.Errorf("transferred messages = %+v", msgs)
	}
	if err := Transfer(dir, "bat", "raven", "again"); err == nil {
		t.Error("transferring onto an existing session must be refused")
	}
}

func TestReadErrorsOnMidLogCorruption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bat.jsonl")
	body := `{"ts":"2026-01-01T00:00:00Z","type":"user","data":{"role":"user","content":"first"}}` + "\n" +
		`{not valid json at all` + "\n" +
		`{"ts":"2026-01-01T00:00:01Z","type":"user","data":{"role":"user","content":"third"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Read(path); err == nil {
		t.Fatal("expected an error for a malformed line in the middle of the log")
	} else if !strings.Contains(err.Error(), ":2:") {
		t.Errorf("error should name the line number, got: %v", err)
	}
}

func TestReadTakesTruncatedFinalLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bat.jsonl")
	body := `{"ts":"2026-01-01T00:00:00Z","type":"user","data":{"role":"user","content":"first"}}` + "\n" +
		`{"ts":"2026-01-01T00:00:01Z","type":"user","data":{"role":"user` // truncated, no closing braces/newline
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	entries, err := Read(path)
	if err != nil {
		t.Fatalf("a truncated final line must be tolerated, got: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected only the surviving first entry, got %d", len(entries))
	}
}

func TestReadSalvagesEntriesGluedToTornTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bat.jsonl")
	body := `{"ts":"2026-01-01T00:00:00Z","type":"user","data":{"role":"user","content":"first"}}` + "\n" +
		`{"ts":"2026-01-01T00:00:01Z","type":"assistant","data":{"role":"assistant","content":"torn` +
		`{"ts":"2026-01-01T00:00:02Z","type":"user","data":{"role":"user","content":"second"}}` +
		`{"ts":"2026-01-01T00:00:03Z","type":"assistant","data":{"role":"assistant","content":"third"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), FilePerm); err != nil {
		t.Fatal(err)
	}

	entries, err := Read(path)
	if err != nil {
		t.Fatalf("glued tail should be recoverable: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("recovered %d entries, want first plus two complete tail entries", len(entries))
	}
	if got := string(entries[1].Data); !strings.Contains(got, `"second"`) {
		t.Errorf("first salvaged entry = %s, want second message", got)
	}
	if got := string(entries[2].Data); !strings.Contains(got, `"third"`) {
		t.Errorf("second salvaged entry = %s, want third message", got)
	}

	// The repair is durable: a second replay must not need the torn prefix to
	// rediscover the recovered entries.
	repaired, err := Read(path)
	if err != nil {
		t.Fatalf("repaired session should replay cleanly: %v", err)
	}
	if len(repaired) != 3 {
		t.Fatalf("repaired session has %d entries, want 3", len(repaired))
	}
	if data, err := os.ReadFile(path); err != nil {
		t.Fatal(err)
	} else if strings.Contains(string(data), "torn") {
		t.Errorf("repair retained torn bytes: %q", data)
	}
}

func TestReadSalvagesEntryAfterTornOuterObject(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bat.jsonl")
	body := `{"ts":"2026-01-01T00:00:01Z","type":"assistant","data":` +
		`{"ts":"2026-01-01T00:00:02Z","type":"user","data":{"role":"user","content":"second"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), FilePerm); err != nil {
		t.Fatal(err)
	}

	entries, err := Read(path)
	if err != nil {
		t.Fatalf("structural glued tail should be recoverable: %v", err)
	}
	if len(entries) != 1 || !strings.Contains(string(entries[0].Data), `"second"`) {
		t.Fatalf("recovered entries = %#v, want the appended user envelope", entries)
	}
}

func TestReadDoesNotSalvageArrayElement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bat.jsonl")
	body := `[` +
		`{"ts":"2026-01-01T00:00:02Z","type":"user","data":{"role":"user","content":"array"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), FilePerm); err != nil {
		t.Fatal(err)
	}

	entries, err := Read(path)
	if err != nil {
		t.Fatalf("malformed array tail should be tolerated: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("recovered %d array elements, want none", len(entries))
	}
}

func TestReadDoesNotSalvageTornStringInsideArray(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bat.jsonl")
	body := `[{"ts":"2026-01-01T00:00:01Z","type":"assistant","data":{"role":"assistant","content":"torn` +
		`{"ts":"2026-01-01T00:00:02Z","type":"user","data":{"role":"user","content":"ghost"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), FilePerm); err != nil {
		t.Fatal(err)
	}

	entries, err := Read(path)
	if err != nil {
		t.Fatalf("array string tail should be tolerated: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("recovered %d entries from an array string, want none", len(entries))
	}
}

func TestReadResynchronizesAfterMismatchedArrayCloser(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bat.jsonl")
	body := `[{"ts":"2026-01-01T00:00:01Z","type":"assistant","data":{"role":"assistant","content":` +
		`]` +
		`{"ts":"2026-01-01T00:00:02Z","type":"user","data":{"role":"user","content":"second"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), FilePerm); err != nil {
		t.Fatal(err)
	}

	entries, err := Read(path)
	if err != nil {
		t.Fatalf("mismatched array closer should permit resynchronization: %v", err)
	}
	if len(entries) != 1 || !strings.Contains(string(entries[0].Data), `"second"`) {
		t.Fatalf("recovered entries = %#v, want the post-array user envelope", entries)
	}
}

func TestReadDoesNotSalvageNestedEnvelope(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bat.jsonl")
	// The outer record is torn after its data object. The nested object has the
	// envelope-shaped keys, but it is payload and must not become a message.
	body := `{"ts":"2026-01-01T00:00:00Z","type":"assistant","data":{"role":"assistant","tool_calls":[{"id":"c1","name":"tool","args":{"ts":"2026-01-01T00:00:01Z","type":"user","data":{"role":"user","content":"ghost"}}}]}`
	if err := os.WriteFile(path, []byte(body), FilePerm); err != nil {
		t.Fatal(err)
	}

	entries, err := Read(path)
	if err != nil {
		t.Fatalf("nested payload should be treated as a torn tail: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("recovered %d nested entries, want none", len(entries))
	}
}

func TestReadRemovesTruncatedTailBeforeTheNextAppend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(Dir(dir), "bat.jsonl")
	if err := os.MkdirAll(Dir(dir), DirPerm); err != nil {
		t.Fatal(err)
	}
	body := `{"ts":"2026-01-01T00:00:00Z","type":"user","data":{"role":"user","content":"first"}}` + "\n" +
		`{"ts":"2026-01-01T00:00:01Z","type":"user","data":{"role":"user`
	if err := os.WriteFile(path, []byte(body), FilePerm); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); err != nil {
		t.Fatal(err)
	}
	st, err := Open(dir, "bat")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.WriteMessage(provider.Message{Role: provider.RoleUser, Content: "second"}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	entries, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("appending after a crash tail left %d entries, want 3 including clean exit", len(entries))
	}
}

func TestMessagesErrorsOnMidLogCorruption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bat.jsonl")
	body := `{"ts":"2026-01-01T00:00:00Z","type":"user","data":{"role":"user","content":"first"}}` + "\n" +
		`{"ts":"2026-01-01T00:00:01Z","type":"user","data":42}` + "\n" +
		`{"ts":"2026-01-01T00:00:02Z","type":"user","data":{"role":"user","content":"third"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Messages(path); err == nil {
		t.Fatal("expected an error for a mid-log record that fails to decode as a message")
	}
}

func TestMessagesTakesUndecodableFinalRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bat.jsonl")
	body := `{"ts":"2026-01-01T00:00:00Z","type":"user","data":{"role":"user","content":"first"}}` + "\n" +
		`{"ts":"2026-01-01T00:00:01Z","type":"user","data":42}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	msgs, err := Messages(path)
	if err != nil {
		t.Fatalf("an undecodable final record must be tolerated, got: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Content != "first" {
		t.Fatalf("expected only the surviving first message, got %+v", msgs)
	}
}

// H5.22: a log can end with an assistant tool_call that has no adjacent
// result — a crash or daemon shutdown mid-round, or corruption predating
// H1.2/H1.3's live guarantee. Replaying it as-is reproduces the exact 400
// those fixed, on the very next request after resume.
func TestMessagesStubsAToolCallWithNoResultAtEndOfLog(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir, "bat")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.WriteMessage(provider.Message{Role: provider.RoleUser, Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteMessage(provider.Message{
		Role: provider.RoleAssistant, Content: "one sec",
		ToolCalls: []provider.ToolCall{{ID: "c1", Name: "read", Args: json.RawMessage(`{}`)}},
	}); err != nil {
		t.Fatal(err)
	}
	// No tool result: the process ended here, same shape as a crash mid-round.
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	msgs, err := Messages(st.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 {
		t.Fatalf("expected a stubbed result appended, got %d messages: %+v", len(msgs), msgs)
	}
	stub := msgs[2]
	if stub.Role != provider.RoleTool || stub.ToolCallID != "c1" {
		t.Errorf("stub = %+v, want a tool result for c1", stub)
	}
}

// A batch of several calls with only some answered must get exactly the
// missing ones stubbed, in no particular order relative to the real result.
func TestMessagesStubsOnlyTheUnansweredCallInABatch(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir, "bat")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.WriteMessage(provider.Message{
		Role: provider.RoleAssistant, Content: "on it",
		ToolCalls: []provider.ToolCall{
			{ID: "c1", Name: "read", Args: json.RawMessage(`{}`)},
			{ID: "c2", Name: "read", Args: json.RawMessage(`{}`)},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteMessage(provider.Message{
		Role: provider.RoleTool, Content: "package main", ToolCallID: "c1", ToolName: "read",
	}); err != nil {
		t.Fatal(err)
	}
	// c2's result never arrived.
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	msgs, err := Messages(st.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 {
		t.Fatalf("got %d messages, want 3 (assistant, c1's real result, c2's stub): %+v", len(msgs), msgs)
	}
	byID := map[string]provider.Message{}
	for _, m := range msgs[1:] {
		byID[m.ToolCallID] = m
	}
	if byID["c1"].Content != "package main" {
		t.Errorf("c1's real result was altered: %+v", byID["c1"])
	}
	if byID["c2"].Role != provider.RoleTool {
		t.Errorf("c2 was not stubbed: %+v", byID["c2"])
	}
}
