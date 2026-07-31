package memory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// vec builds a small unit-ish vector. The tests do not need real embeddings —
// they need vectors whose distances are known, which hand-written ones are and
// a model's are not.
func vec(xs ...float32) []float32 { return xs }

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestAddAndSearchRanksByCosine(t *testing.T) {
	s := openTemp(t)
	now := time.Now()

	// Deliberately far apart: anything closer than 0.95 to another memory is a
	// duplicate by definition, and would merge rather than rank.
	s.Add("the user prefers tabs", KindPreference, "sess", vec(1, 0, 0), now)
	s.Add("the build uses bazel", KindProject, "sess", vec(0, 0, 1), now)
	s.Add("tests live beside the code", KindFact, "sess", vec(0.6, 0.8, 0), now)

	hits := s.Search("", vec(1, 0, 0), 3, 0.5)
	if len(hits) != 2 {
		t.Fatalf("hits = %d, want the two vectors near the query", len(hits))
	}
	if hits[0].Text != "the user prefers tabs" {
		t.Errorf("top hit = %q", hits[0].Text)
	}
	// The orthogonal memory must not appear at all: a recall that injects
	// everything is the same as a recall that injects nothing.
	for _, h := range hits {
		if strings.Contains(h.Text, "bazel") {
			t.Errorf("an orthogonal memory scored %.2f", h.Score)
		}
	}
}

func TestKindWeightBreaksTiesTowardPreferences(t *testing.T) {
	// Two memories equally close to the query. A stated preference is more
	// useful to inject than something that merely happened.
	s := openTemp(t)
	now := time.Now()
	s.Add("it happened once", KindEpisode, "sess", vec(1, 0), now)
	s.Add("do it this way", KindPreference, "sess", vec(0, 1), now)

	// Equidistant from both, so only the kind weight can order them.
	hits := s.Search("", vec(1, 1), 2, 0.5)
	if len(hits) != 2 {
		t.Fatalf("hits = %d", len(hits))
	}
	if hits[0].Kind != KindPreference {
		t.Errorf("top hit is %s; a preference should outrank an episode at equal distance", hits[0].Kind)
	}
}

func TestSearchFallsBackToSubstringWithoutEmbeddings(t *testing.T) {
	// An embedder that is down must degrade recall, not disable it.
	s := openTemp(t)
	s.Add("the deploy script needs sudo", KindFact, "sess", nil, time.Now())
	s.Add("unrelated note about coffee", KindFact, "sess", nil, time.Now())

	hits := s.Search("what does deploy need", nil, 4, RecallThreshold)
	if len(hits) != 1 {
		t.Fatalf("hits = %d, want the lexical match", len(hits))
	}
	if !strings.Contains(hits[0].Text, "deploy") {
		t.Errorf("hit = %q", hits[0].Text)
	}
}

func TestMismatchedDimensionsScoreZero(t *testing.T) {
	// Switching embedding models leaves old vectors behind. They should stop
	// matching rather than crash or match arbitrarily.
	s := openTemp(t)
	s.Add("old model vector", KindFact, "sess", vec(1, 0, 0), time.Now())
	if hits := s.Search("", vec(1, 0), 4, 0.1); len(hits) != 0 {
		t.Errorf("a 3-dim memory matched a 2-dim query: %v", hits)
	}
}

func TestAddMergesNearDuplicates(t *testing.T) {
	s := openTemp(t)
	now := time.Now()
	first, merged, err := s.Add("the user prefers tabs", KindPreference, "a", vec(1, 0), now)
	if err != nil || merged {
		t.Fatalf("first add: merged=%v err=%v", merged, err)
	}

	// Same direction, so cosine 1.0 — the same fact restated.
	second, merged, err := s.Add("the user prefers tabs over spaces", KindPreference, "b", vec(2, 0), now)
	if err != nil {
		t.Fatal(err)
	}
	if !merged {
		t.Fatal("a restatement should merge rather than duplicate")
	}
	if second.ID != first.ID {
		t.Errorf("merged into #%d, want #%d", second.ID, first.ID)
	}
	if s.Len() != 1 {
		t.Errorf("bank holds %d memories, want 1", s.Len())
	}
	// The newer phrasing wins: a restated fact is usually a corrected one.
	if got := s.All()[0].Text; got != "the user prefers tabs over spaces" {
		t.Errorf("stored text = %q, want the newer phrasing", got)
	}
}

// H5.14: Add and Forget must not mutate in-memory state ahead of the durable
// append. A failed append used to leave live state serving a fact a restart
// would never replay.
func TestAddDoesNotMutateMemoryWhenAppendFails(t *testing.T) {
	s := openTemp(t)
	if _, _, err := s.Add("first", KindFact, "a", vec(1, 0), time.Now()); err != nil {
		t.Fatal(err)
	}
	before := s.Len()
	beforeNextID := s.nextID

	s.file.Close() // every subsequent append now fails on flush

	if _, _, err := s.Add("second", KindFact, "a", vec(0, 1), time.Now()); err == nil {
		t.Fatal("expected the append to fail against a closed file")
	}
	if s.Len() != before {
		t.Errorf("failed add must not grow the in-memory bank: had %d, now %d", before, s.Len())
	}
	if s.nextID != beforeNextID {
		t.Errorf("failed add must not consume an ID: had %d, now %d", beforeNextID, s.nextID)
	}
}

func TestAddMergeDoesNotMutateMemoryWhenAppendFails(t *testing.T) {
	s := openTemp(t)
	first, _, err := s.Add("the user prefers tabs", KindPreference, "a", vec(1, 0), time.Now())
	if err != nil {
		t.Fatal(err)
	}

	s.file.Close()

	if _, _, err := s.Add("the user prefers tabs over spaces", KindPreference, "b", vec(2, 0), time.Now()); err == nil {
		t.Fatal("expected the append to fail against a closed file")
	}
	if got := s.All()[0].Text; got != first.Text {
		t.Errorf("failed merge must not overwrite the stored text: got %q, want %q", got, first.Text)
	}
}

func TestForgetDoesNotMutateMemoryWhenAppendFails(t *testing.T) {
	s := openTemp(t)
	rec, _, err := s.Add("first", KindFact, "a", vec(1, 0), time.Now())
	if err != nil {
		t.Fatal(err)
	}

	s.file.Close()

	if _, err := s.Forget(rec.ID); err == nil {
		t.Fatal("expected the append to fail against a closed file")
	}
	if s.All()[0].Deleted {
		t.Error("a failed tombstone append must not mark the record deleted in memory")
	}
}

func TestIdenticalTextMergesWithoutEmbeddings(t *testing.T) {
	// `remember` must stay idempotent when the embedder is down, or a model
	// that re-states a fact each turn fills the bank with copies.
	s := openTemp(t)
	now := time.Now()
	s.Add("go vet must pass", KindProject, "a", nil, now)
	if _, merged, _ := s.Add("Go Vet Must Pass", KindProject, "a", nil, now); !merged {
		t.Error("identical text should merge regardless of case")
	}
	if s.Len() != 1 {
		t.Errorf("bank holds %d, want 1", s.Len())
	}
}

func TestForgetTombstonesAndSurvivesReload(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	rec, _, _ := s.Add("temporary thought", KindFact, "a", vec(1, 0), time.Now())
	found, err := s.Forget(rec.ID)
	if err != nil || !found {
		t.Fatalf("forget: found=%v err=%v", found, err)
	}
	s.Close()

	// The file is append-only, so forgetting is a tombstone. Reload has to
	// honor it or a deleted memory comes back on the next boot.
	again, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if again.Len() != 0 {
		t.Errorf("reloaded %d memories, want the tombstone honored", again.Len())
	}
}

func TestReloadSkipsCorruptLines(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	s.Add("keep me", KindFact, "a", vec(1, 0), time.Now())
	s.Close()

	// A process killed mid-write leaves a truncated final line. Losing it is
	// fine; refusing to start is not.
	f, _ := os.OpenFile(filepath.Join(dir, FileName), os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"id":2,"text":"truncated`)
	f.Close()

	again, err := Open(dir)
	if err != nil {
		t.Fatalf("a truncated line must not fail the load: %v", err)
	}
	defer again.Close()
	if again.Len() != 1 {
		t.Errorf("loaded %d memories, want the intact one", again.Len())
	}
}

func TestReloadErrorsOnMidLogCorruption(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	s.Add("first", KindFact, "a", vec(1, 0), time.Now())
	s.Add("third", KindFact, "a", vec(0, 1), time.Now())
	s.Close()

	// Splice a malformed line between the two real records — corruption in the
	// middle of the log, not a crash-truncated tail.
	path := filepath.Join(dir, FileName)
	data, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 records before splicing, got %d", len(lines))
	}
	spliced := lines[0] + "\n{not valid json\n" + lines[1] + "\n"
	if err := os.WriteFile(path, []byte(spliced), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Open(dir); err == nil {
		t.Fatal("expected an error for a malformed line in the middle of the log")
	} else if !strings.Contains(err.Error(), ":2:") {
		t.Errorf("error should name the line number, got: %v", err)
	}
}

// stubEmbedder returns a fixed vector per text, so a test can pin distances.
type stubEmbedder struct {
	vecs map[string][]float32
	err  error
	// calls counts embeds, which is how a test proves recall did not run.
	calls int
}

func (s *stubEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v, ok := s.vecs[t]
		if !ok {
			// Distinct per text, not a shared default: two different facts
			// sharing one vector are duplicates to the store, and every test
			// that stores two of them would silently exercise merging instead
			// of what it meant to test.
			v = unitFor(t)
		}
		out[i] = v
	}
	return out, nil
}

// unitFor maps a string to its own direction in a wide space, so two distinct
// texts are never near-duplicates of each other.
func unitFor(s string) []float32 {
	const dims = 64
	v := make([]float32, dims)
	h := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		h = (h ^ uint32(s[i])) * 16777619
	}
	v[h%dims] = 1
	return v
}

// stubRouter records side-calls and replies with a canned answer.
type stubRouter struct {
	reply string
	err   error
	role  string
	user  string
}

func (s *stubRouter) SideCall(_ context.Context, role, _, user string) (string, error) {
	s.role, s.user = role, user
	return s.reply, s.err
}

func TestRecallInjectsAboveThresholdOnly(t *testing.T) {
	store := openTemp(t)
	now := time.Now()
	store.Add("the user prefers tabs", KindPreference, "a", vec(1, 0, 0), now)
	store.Add("nothing to do with it", KindFact, "a", vec(0, 1, 0), now)

	emb := &stubEmbedder{vecs: map[string][]float32{"how should I indent?": vec(1, 0, 0)}}
	m := NewManager(store, emb, nil, "a", true)

	tail, hits := m.Recall(context.Background(), "how should I indent?")
	if len(hits) != 1 {
		t.Fatalf("hits = %d, want only the relevant memory", len(hits))
	}
	if !strings.Contains(tail, "<memories>") || !strings.Contains(tail, "prefers tabs") {
		t.Errorf("tail message = %q", tail)
	}
	if strings.Contains(tail, "nothing to do with it") {
		t.Error("an irrelevant memory was injected")
	}
}

func TestRecallIsSilentWhenDisabled(t *testing.T) {
	store := openTemp(t)
	store.Add("the user prefers tabs", KindPreference, "a", vec(1, 0, 0), time.Now())
	emb := &stubEmbedder{}
	m := NewManager(store, emb, nil, "a", false)

	if tail, hits := m.Recall(context.Background(), "indent?"); tail != "" || hits != nil {
		t.Errorf("disabled memory recalled %q", tail)
	}
	if emb.calls != 0 {
		t.Errorf("disabled memory made %d embed calls; off should mean off", emb.calls)
	}
}

func TestRecallSurvivesADeadEmbedder(t *testing.T) {
	// The invariant that matters most: memory never breaks the turn (§19).
	store := openTemp(t)
	store.Add("the deploy script needs sudo", KindFact, "a", vec(1, 0), time.Now())
	m := NewManager(store, &stubEmbedder{err: errors.New("connection refused")}, nil, "a", true)

	tail, _ := m.Recall(context.Background(), "what does deploy need")
	if !strings.Contains(tail, "deploy") {
		t.Errorf("tail = %q; a dead embedder should degrade to lexical recall", tail)
	}
	if got := m.Activity().Failed; got == "" {
		t.Error("the failure should be recorded for the widget")
	}
}

func TestRecallEmptyBankInjectsNothing(t *testing.T) {
	m := NewManager(openTemp(t), &stubEmbedder{}, nil, "a", true)
	if tail, _ := m.Recall(context.Background(), "anything"); tail != "" {
		t.Errorf("an empty bank produced %q", tail)
	}
}

func TestObserveTurnFiresEveryEighthTurn(t *testing.T) {
	m := NewManager(openTemp(t), nil, &stubRouter{reply: "[]"}, "a", true)
	for i := 1; i <= ExtractEvery*2; i++ {
		due := m.ObserveTurn(fmt.Sprintf("turn %d", i))
		want := i%ExtractEvery == 0
		if due != want {
			t.Errorf("turn %d: due = %v, want %v", i, due, want)
		}
	}
}

func TestObserveTurnNeedsTranscript(t *testing.T) {
	// Eight empty turns is not eight turns worth of conversation to mine.
	m := NewManager(openTemp(t), nil, &stubRouter{}, "a", true)
	for i := 0; i < ExtractEvery; i++ {
		if m.ObserveTurn("   ") {
			t.Fatal("extraction fired on empty turns")
		}
	}
}

func TestExtractStoresFactsAndUsesSmol(t *testing.T) {
	store := openTemp(t)
	router := &stubRouter{reply: `Sure! Here you go:
[{"text": "the user deploys with make release", "kind": "project"},
 {"text": "the user prefers tabs", "kind": "preference"}]`}
	m := NewManager(store, &stubEmbedder{}, router, "a", true)

	m.ObserveTurn("a conversation")
	n, err := m.Extract(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("saved %d facts, want 2", n)
	}
	if router.role != "smol" {
		t.Errorf("extraction used the %q role; ambient work must use smol (§16)", router.role)
	}
	if store.Len() != 2 {
		t.Errorf("bank holds %d", store.Len())
	}
}

func TestExtractToleratesProse(t *testing.T) {
	// Small models answer in prose no matter what the prompt says. That is a
	// normal outcome, not an error worth surfacing.
	m := NewManager(openTemp(t), nil, &stubRouter{reply: "I don't think anything here is worth remembering."}, "a", true)
	m.ObserveTurn("a conversation")
	n, err := m.Extract(context.Background())
	if n != 0 || err != nil {
		t.Errorf("n=%d err=%v, want a quiet zero", n, err)
	}
}

func TestExtractDrainsItsTranscript(t *testing.T) {
	// The same exchange must not be mined twice: re-extraction would restate
	// every fact it already found and lean on merge to clean up after it.
	router := &stubRouter{reply: "[]"}
	m := NewManager(openTemp(t), nil, router, "a", true)
	m.ObserveTurn("first exchange")
	m.Extract(context.Background())
	if !strings.Contains(router.user, "first exchange") {
		t.Fatalf("extraction saw %q", router.user)
	}
	router.user = ""
	if _, err := m.Extract(context.Background()); err != nil {
		t.Fatal(err)
	}
	if router.user != "" {
		t.Errorf("a second extraction re-read %q", router.user)
	}
}

// H5.15: a failed provider call or an unparsable reply must not lose the
// turns that were queued for it — there is no second copy to extract from.
func TestExtractKeepsTranscriptOnProviderError(t *testing.T) {
	router := &stubRouter{err: errors.New("network blip")}
	m := NewManager(openTemp(t), nil, router, "a", true)
	m.ObserveTurn("the user deploys with make release")

	if _, err := m.Extract(context.Background()); err == nil {
		t.Fatal("expected the provider error to surface")
	}
	if text, _ := m.peekTranscript(); !strings.Contains(text, "the user deploys with make release") {
		t.Errorf("a failed provider call must not drain the transcript, got %q", text)
	}
}

func TestExtractKeepsTranscriptOnUnparsableReply(t *testing.T) {
	router := &stubRouter{reply: "I don't think anything here is worth remembering."}
	m := NewManager(openTemp(t), nil, router, "a", true)
	m.ObserveTurn("the user deploys with make release")

	if _, err := m.Extract(context.Background()); err != nil {
		t.Fatal(err)
	}
	if text, _ := m.peekTranscript(); !strings.Contains(text, "the user deploys with make release") {
		t.Errorf("an unparsable reply must not drain the transcript, got %q", text)
	}
}

func TestExtractKeepsTurnsQueuedDuringAnInFlightCall(t *testing.T) {
	// A turn observed while an extraction is in flight must survive that
	// extraction's clear — clearTranscript only drops what it actually sent.
	m := NewManager(openTemp(t), nil, &stubRouter{reply: "[]"}, "a", true)
	m.ObserveTurn("first exchange")
	text, n := m.peekTranscript()
	if !strings.Contains(text, "first exchange") || n != 1 {
		t.Fatalf("peekTranscript = %q, %d", text, n)
	}
	m.ObserveTurn("second exchange, arrived mid-flight")
	m.clearTranscript(n)
	remaining, _ := m.peekTranscript()
	if remaining != "second exchange, arrived mid-flight" {
		t.Errorf("remaining transcript = %q, want only the mid-flight turn", remaining)
	}
}

func TestConsolidateStoresASearchableEpisode(t *testing.T) {
	store := openTemp(t)
	router := &stubRouter{reply: "Wired the auth redirect loop and fixed the token refresh."}
	m := NewManager(store, &stubEmbedder{}, router, "bat", true)

	rec, err := m.Consolidate(context.Background(), "user: fix auth\nassistant: done")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Kind != KindEpisode {
		t.Errorf("kind = %s, want episode", rec.Kind)
	}
	if rec.Session != "bat" {
		t.Errorf("session = %q; session RAG needs the name to resume by", rec.Session)
	}
}

func TestSearchSessionsReturnsOnlyEpisodes(t *testing.T) {
	store := openTemp(t)
	now := time.Now()
	store.Add("worked on the auth redirect loop", KindEpisode, "bat", vec(1, 0), now)
	store.Add("the user prefers tabs", KindPreference, "bat", vec(0, 1), now)

	emb := &stubEmbedder{vecs: map[string][]float32{"auth work": vec(1, 0)}}
	m := NewManager(store, emb, nil, "bat", true)

	hits := m.SearchSessions(context.Background(), "auth work", 5)
	if len(hits) != 1 {
		t.Fatalf("hits = %d, want only the episode", len(hits))
	}
	if hits[0].Session != "bat" {
		t.Errorf("session = %q", hits[0].Session)
	}
}

func TestReflectSynthesizesOverTheBank(t *testing.T) {
	store := openTemp(t)
	now := time.Now()
	store.Add("errors are wrapped with %w", KindProject, "a", vec(1, 0), now)
	store.Add("no panics outside main", KindProject, "a", vec(0.6, 0.8), now)

	router := &stubRouter{reply: "Wrap errors; never panic outside main."}
	emb := &stubEmbedder{vecs: map[string][]float32{"how are errors handled?": vec(1, 0)}}
	m := NewManager(store, emb, router, "a", true)

	answer, err := m.Reflect(context.Background(), "how are errors handled?")
	if err != nil {
		t.Fatal(err)
	}
	if answer != router.reply {
		t.Errorf("answer = %q", answer)
	}
	// Both memories must reach the model, or reflection is just recall.
	if !strings.Contains(router.user, "%w") || !strings.Contains(router.user, "panics") {
		t.Errorf("the smol call saw only %q", router.user)
	}
}

func TestReflectWithoutMatchesSaysSo(t *testing.T) {
	m := NewManager(openTemp(t), &stubEmbedder{}, &stubRouter{}, "a", true)
	if _, err := m.Reflect(context.Background(), "anything"); err == nil {
		t.Error("reflect over an empty bank should report having nothing, not answer")
	}
}

func TestNilManagerIsInert(t *testing.T) {
	// `/memory off` and an unconfigured bank both produce this. Every path has
	// to survive it, because the alternative is a panic on a feature the user
	// deliberately turned off.
	var m *Manager
	if m.Enabled() {
		t.Error("a nil manager reports enabled")
	}
	if tail, hits := m.Recall(context.Background(), "x"); tail != "" || hits != nil {
		t.Error("a nil manager recalled something")
	}
	if m.ObserveTurn("x") {
		t.Error("a nil manager scheduled extraction")
	}
	if n, err := m.Extract(context.Background()); n != 0 || err != nil {
		t.Errorf("Extract on nil: %d %v", n, err)
	}
	if hits := m.SearchSessions(context.Background(), "x", 3); hits != nil {
		t.Error("a nil manager searched sessions")
	}
	m.SetEnabled(true)
	if got := m.Activity(); got.Stage != StageIdle {
		t.Errorf("activity = %v", got)
	}
}

func TestFormatMemoriesIsTaggedAndAttributed(t *testing.T) {
	hits := []Hit{{Record: Record{Text: "prefers tabs", Kind: KindPreference}, Score: 0.9}}
	got := FormatMemories(hits)
	for _, want := range []string{"<memories>", "</memories>", "(preference)", "prefers tabs"} {
		if !strings.Contains(got, want) {
			t.Errorf("message is missing %q:\n%s", want, got)
		}
	}
	if FormatMemories(nil) != "" {
		t.Error("no hits should produce no message")
	}
}

func TestExtractJSONFindsTheArray(t *testing.T) {
	cases := map[string]string{
		`[{"text":"a"}]`:                    `[{"text":"a"}]`,
		"```json\n[{\"text\":\"a\"}]\n```":  `[{"text":"a"}]`,
		"Here you go:\n[{\"text\":\"a\"}]!": `[{"text":"a"}]`,
	}
	for in, want := range cases {
		if got := ExtractJSON(in); got != want {
			t.Errorf("ExtractJSON(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncateKeepsRunesIntact(t *testing.T) {
	got := Truncate("héllo wörld", 3)
	if !strings.HasPrefix(got, "h") {
		t.Errorf("got %q", got)
	}
	for _, r := range got {
		if r == '\uFFFD' {
			t.Errorf("truncation split a rune: %q", got)
		}
	}
	if Truncate("short", 100) != "short" {
		t.Error("text under the cap should pass through unchanged")
	}
}
