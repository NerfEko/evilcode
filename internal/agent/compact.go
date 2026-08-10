package agent

import (
	"context"
	"fmt"
	"hash/fnv"
	"math"
	"strings"
	"sync"
	"time"

	"evilcode/internal/provider"
)

// CompactPrompt asks for a summary a fresh context window can work from.
const CompactPrompt = `Summarize this coding session for a fresh context window.

Keep decisions made, files changed, what is still outstanding, and anything the
next turn would otherwise have to rediscover. Drop pleasantries and dead ends.
Be dense.`

// CompactMessageCap bounds one message inside the transcript handed to the
// summariser, so a single pasted file cannot crowd out the conversation.
const CompactMessageCap = 2000

// RecentTurnsToKeep is the verbatim tail preserved by compaction. Keeping a
// real working window means a compaction that lands between a prompt and its
// answer does not make the model rediscover the task it is in the middle of.
// Ten matches the jcode compaction floor while remaining small enough to leave
// room for the fresh request.
const RecentTurnsToKeep = 10

// CompactThreshold is the fraction of the context window at which a turn
// compacts before dispatching when the projection has not fired first
// (plan.md §9.9).
//
// A constant rather than a config knob: it is the kind of setting nobody tunes
// and everybody would have to understand to tune correctly.
const CompactThreshold = 0.85

// CompactProjectionLookahead is how many future turns the token-growth
// projection covers. Fifteen matches jcode's proactive default: a long-running
// coding session gets time to summarize before the turn that fills the window.
const CompactProjectionLookahead = 15

// CompactEWMAAlpha controls how quickly the projected per-turn growth follows
// recent observations. A smaller value smooths one unusually large response
// without ignoring a sustained increase.
const CompactEWMAAlpha = 0.3

// CompactProjectionMinSamples is the number of context observations needed for
// one per-turn delta and therefore a meaningful projection.
const CompactProjectionMinSamples = 2

// CompactProjectionFloor avoids spending a summarizer call on a tiny context
// merely because an early request was unusually large. This mirrors jcode's
// proactive floor while the fixed threshold remains the safety fallback.
const CompactProjectionFloor = 0.40

// CompactEmbeddingMessageCap bounds the text sent to the embedding provider
// for one completed assistant turn. The beginning of a turn carries its topic
// cheaply; a pasted file should not become an embedding request the size of
// the conversation it is helping compact.
const CompactEmbeddingMessageCap = 512

// CompactEmbeddingHistoryWindow is the rolling semantic window used to spot
// a change from one topic to another.
const CompactEmbeddingHistoryWindow = 10

// CompactTopicShiftMinSnapshots is the minimum history needed to compare two
// non-empty halves instead of treating one pair as a topic boundary.
const CompactTopicShiftMinSnapshots = 4

// CompactTopicShiftThreshold matches jcode's semantic compaction default:
// below this cosine similarity, the older and newer halves represent different
// topics closely enough that the old one is a free compaction point.
const CompactTopicShiftThreshold = 0.45

// CompactEmbeddingTimeout bounds a detached semantic request. Embeddings are
// an enhancement; a provider that is down or slow must not hold the turn loop
// or leave a goroutine behind indefinitely.
const CompactEmbeddingTimeout = 5 * time.Second

// CompactRelevanceGoalMessages is the recent message window used to represent
// the work that is still active when choosing a semantic compaction boundary.
const CompactRelevanceGoalMessages = 5

// CompactRelevanceKeepThreshold matches jcode's default semantic relevance
// threshold. A message at or above this similarity is kept verbatim.
const CompactRelevanceKeepThreshold = 0.65

// CompactRelevanceBatchSize keeps each provider request small even when a
// session has a long history.
const CompactRelevanceBatchSize = 32

// CompactRelevanceWait is the small grace period used by the automatic path to
// consume a lookup that was already queued for the exact transcript snapshot.
// Compact itself never waits; a slow provider still falls back to recency.
const CompactRelevanceWait = 50 * time.Millisecond

// MaxAutoCompactions bounds automatic compaction for a session.
//
// Invariant 6. Without it, a summary that is itself over the threshold compacts
// forever and never sends a request — the model never speaks and the loop never
// ends, which is the worst shape of runaway because it looks like hanging.
const MaxAutoCompactions = 3

// Summarizer turns a transcript into a summary. It is a function rather than a
// router so this package keeps knowing nothing about config, and so `internal/
// agent` stays free of anything the TUI owns (invariant 1).
type Summarizer func(ctx context.Context, system, user string) (string, error)

// EmbeddingProvider is the small part of a model backend semantic compaction
// needs. Keeping it separate from provider.Provider lets the compactor remain
// useful with a test or local embedding service that does not implement chat.
type EmbeddingProvider interface {
	Embed(context.Context, []string) ([][]float32, error)
}

// Compactor collapses a conversation when it gets too long.
//
// It lives here rather than in the TUI because the TUI is not the only thing
// that runs a long conversation: a daemon session, an overnight run and a
// spawned worker all needed compaction and none of them could reach it.
type Compactor struct {
	// Summarize produces the summary. Nil disables compaction entirely.
	Summarize Summarizer

	// Embedding supplies optional per-turn vectors for semantic topic-shift and
	// relevance detection. RecordEmbeddingSnapshot and PrepareRelevance invoke
	// it in bounded background calls; ShouldCompact never calls the provider.
	Embedding EmbeddingProvider

	// Persist writes the compacted history to durable storage and returns what
	// a resume would replay. Nil means memory-only, which is what the TUI used
	// to do by accident — and why resuming a compacted session restored the
	// full history.
	Persist func(summary string) ([]provider.Message, error)

	// PersistWithTail is the durable form used by live sessions. It receives the
	// exact messages kept verbatim after the summary so a resume sees the same
	// compacted context as the in-memory conversation. Persist remains for small
	// callers that only need the legacy summary-only rewrite.
	PersistWithTail func(summary string, tail []provider.Message) ([]provider.Message, error)

	// OnCompaction resets session-local caches whose contents are no longer in
	// the model context (for example the tool exposure ledger).
	OnCompaction func()

	mu    sync.Mutex
	count int

	// Projection state is sampled once per turn by ShouldCompact. Keeping the
	// state on the compactor, rather than the agent, makes all frontends use the
	// same prediction and keeps it resettable after a successful rewrite.
	projectionWindow  int
	projectionLast    int
	projectionSamples int
	projectionEWMA    float64

	// Semantic state is protected by mu. A dimension change means the provider
	// changed vector spaces, so old and new vectors must never be compared.
	embeddingHistory  [][]float32
	embeddingDim      int
	embeddingInFlight bool
	embeddingEpoch    uint64

	// Relevance state is also protected by mu. Relevance is prepared in the
	// background before compaction; Compact only consumes a ready result and
	// otherwise falls back to the ordinary recency boundary.
	relevanceInFlight    bool
	relevanceReady       bool
	relevanceKey         uint64
	relevanceInFlightKey uint64
	relevanceEarliest    int
	relevanceRun         uint64
	relevanceCancel      context.CancelFunc
}

// Count is how many times this session has been compacted.
func (c *Compactor) Count() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count
}

// Enabled reports whether compaction is available.
func (c *Compactor) Enabled() bool { return c != nil && c.Summarize != nil }

// Transcript renders a conversation for the summariser.
//
// Exported so the manual `/compact` path and the automatic one cannot drift into
// summarising two different things.
func Transcript(msgs []provider.Message) string {
	var b strings.Builder
	for _, msg := range msgs {
		if msg.Role == provider.RoleSystem {
			continue
		}
		text := compactionMessageText(msg)
		if text == "" {
			continue
		}
		fmt.Fprintf(&b, "%s: %s\n\n", msg.Role, text)
	}
	return b.String()
}

// compactionMessageText preserves the operational facts carried outside
// Message.Content. Tool-call assistant rows are commonly content-empty; if the
// call name/arguments are omitted, the following result appears in the summary
// with no explanation of what action produced it. The complete representation
// remains subject to one fixed per-message budget.
func compactionMessageText(msg provider.Message) string {
	var b strings.Builder
	full := false
	appendChunk := func(chunk string) {
		if full || chunk == "" {
			return
		}
		remaining := CompactMessageCap - b.Len()
		if remaining <= 0 {
			full = true
			return
		}
		if len(chunk) <= remaining {
			b.WriteString(chunk)
			return
		}
		const marker = "..."
		if remaining <= len(marker) {
			b.WriteString(marker[:remaining])
		} else {
			b.WriteString(truncateAtRune(chunk, remaining-len(marker)))
			b.WriteString(marker)
		}
		full = true
	}

	content := strings.TrimSpace(msg.Content)
	if msg.Role == provider.RoleTool {
		appendChunk("[Result")
		if msg.ToolName != "" {
			appendChunk(": ")
			appendChunk(msg.ToolName)
		}
		appendChunk("]")
		if content != "" {
			appendChunk(" ")
			appendChunk(content)
		}
	} else {
		appendChunk(content)
	}
	for _, call := range msg.ToolCalls {
		if b.Len() > 0 {
			appendChunk("\n")
		}
		appendChunk("[Tool: ")
		appendChunk(call.Name)
		if len(call.Args) > 0 {
			appendChunk(" - ")
			appendChunk(string(call.Args))
		}
		appendChunk("]")
	}
	for range msg.Images {
		if b.Len() > 0 {
			appendChunk("\n")
		}
		appendChunk("[Image]")
	}
	return strings.TrimSpace(b.String())
}

// Compact summarises a conversation and replaces it with the summary.
//
// The order matters: the summary is written to storage *before* the in-memory
// history is replaced, so a failure to persist leaves the conversation intact
// rather than dropping it on the floor with nothing on disk to recover from.
func (c *Compactor) Compact(ctx context.Context, conv *Conversation) (string, error) {
	if !c.Enabled() {
		return "", fmt.Errorf("no summarizer is configured")
	}
	if conv.Len() == 0 {
		return "", fmt.Errorf("nothing to compact")
	}

	msgs := conv.Messages()
	cutoff := compactionCutoff(msgs, RecentTurnsToKeep)
	if cutoff == 0 {
		return "", fmt.Errorf("not enough history to compact while keeping the most recent %d turns", RecentTurnsToKeep)
	}
	cutoff = c.relevanceCutoff(ctx, msgs, cutoff)
	old := msgs[:cutoff]
	tail := cloneMessages(msgs[cutoff:])

	summary, err := c.Summarize(ctx, CompactPrompt, Transcript(old))
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(summary) == "" {
		return "", fmt.Errorf("the summarizer returned nothing")
	}

	replay := append([]provider.Message{CompactMessage(summary)}, tail...)
	var stored []provider.Message
	if c.PersistWithTail != nil {
		stored, err = c.PersistWithTail(summary, tail)
		if err != nil {
			return "", fmt.Errorf("compaction was not saved: %w", err)
		}
	} else if c.Persist != nil {
		stored, err = c.Persist(summary)
		if err != nil {
			return "", fmt.Errorf("compaction was not saved: %w", err)
		}
		if len(stored) > 0 {
			replay = stored
		}
	}
	conv.Reset(replay)
	if c.OnCompaction != nil {
		c.OnCompaction()
	}

	c.mu.Lock()
	c.count++
	c.resetProjectionLocked()
	c.resetEmbeddingHistoryLocked()
	c.mu.Unlock()
	return summary, nil
}

// PrepareRelevance starts a best-effort relevance lookup for a transcript. It
// never waits for the provider. Callers that know a compaction may be needed
// should call this after a turn so Compact can consume a ready result later.
func (c *Compactor) PrepareRelevance(ctx context.Context, msgs []provider.Message) {
	if c == nil {
		return
	}
	standardCutoff := compactionCutoff(msgs, RecentTurnsToKeep)
	if standardCutoff == 0 {
		return
	}
	request, ok := buildRelevanceRequest(msgs, standardCutoff)
	if !ok {
		return
	}
	c.queueRelevance(ctx, request)
}

// PrepareRelevanceIfNeeded starts relevance work only when the current
// context, projection, or topic-shift state says compaction may be imminent.
// It is the completed-turn prewarm hook; keeping the gate here prevents a
// large history from launching a full scan after every successful turn.
func (c *Compactor) PrepareRelevanceIfNeeded(
	ctx context.Context, used, window int, conv *Conversation,
) {
	if c == nil || conv == nil {
		return
	}
	msgs := conv.Messages()
	if !c.relevanceMayBeNeeded(used, window, msgs) {
		return
	}
	c.PrepareRelevance(ctx, msgs)
}

// WaitForRelevance gives a lookup already queued for this exact transcript a
// short chance to finish. It never starts provider work and never waits for the
// full embedding timeout; Compact itself remains non-blocking.
func (c *Compactor) WaitForRelevance(
	ctx context.Context, msgs []provider.Message, wait time.Duration,
) bool {
	if c == nil || wait <= 0 {
		return false
	}
	request, ok := buildRelevanceRequest(msgs, compactionCutoff(msgs, RecentTurnsToKeep))
	if !ok {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	// The relevance worker only changes state when its request completes; a
	// 1ms poll burns a core during every grace period. Ten milliseconds keeps
	// the 50ms wait responsive without turning compaction into a busy loop.
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		c.mu.Lock()
		ready := c.relevanceReady && c.relevanceKey == request.key
		inFlight := c.relevanceInFlight && c.relevanceInFlightKey == request.key
		c.mu.Unlock()
		if ready {
			return true
		}
		if !inFlight {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return false
		case <-ticker.C:
		}
	}
}

// relevanceCutoff consumes a ready relevance result. Embedding failure or a
// lookup that is still in flight is deliberately a no-op: the ordinary recency
// cutoff is safer than allowing an optional semantic service to delay or
// prevent compaction.
func (c *Compactor) relevanceCutoff(ctx context.Context, msgs []provider.Message, standardCutoff int) int {
	if standardCutoff <= 0 || standardCutoff > len(msgs) {
		return standardCutoff
	}
	request, ok := buildRelevanceRequest(msgs, standardCutoff)
	if !ok {
		return standardCutoff
	}

	c.mu.Lock()
	ready := c.relevanceReady && c.relevanceKey == request.key
	earliest := c.relevanceEarliest
	c.mu.Unlock()
	if !ready {
		c.queueRelevance(ctx, request)
		return standardCutoff
	}

	if earliest >= standardCutoff {
		return standardCutoff
	}
	return relevanceAdjustedCutoff(msgs, standardCutoff, earliest)
}

type relevanceCandidate struct {
	index int
	role  provider.Role
	text  string
}

type relevanceRequest struct {
	key            uint64
	goal           string
	standardCutoff int
	candidates     []relevanceCandidate
}

func buildRelevanceRequest(msgs []provider.Message, standardCutoff int) (relevanceRequest, bool) {
	request := relevanceRequest{standardCutoff: standardCutoff}
	request.goal = relevanceGoalText(msgs)
	if request.goal == "" {
		return relevanceRequest{}, false
	}

	for i := 0; i < standardCutoff; i++ {
		text := relevanceMessageText(msgs[i])
		if text == "" {
			continue
		}
		request.candidates = append(request.candidates, relevanceCandidate{
			index: i,
			role:  msgs[i].Role,
			text:  text,
		})
	}
	if len(request.candidates) == 0 {
		return relevanceRequest{}, false
	}

	h := fnv.New64a()
	_, _ = fmt.Fprintf(h, "%d\x00%s\x00", standardCutoff, request.goal)
	for _, candidate := range request.candidates {
		_, _ = fmt.Fprintf(h, "%d\x00%s\x00%s\x00", candidate.index, candidate.role, candidate.text)
	}
	request.key = h.Sum64()
	return request, true
}

func (c *Compactor) queueRelevance(ctx context.Context, request relevanceRequest) {
	if c == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = context.WithoutCancel(ctx)

	c.mu.Lock()
	if c.Embedding == nil || (c.relevanceReady && c.relevanceKey == request.key) {
		c.mu.Unlock()
		return
	}
	if c.relevanceInFlight && c.relevanceInFlightKey == request.key {
		c.mu.Unlock()
		return
	}
	if c.relevanceCancel != nil {
		c.relevanceCancel()
	}
	embedder := c.Embedding
	epoch := c.embeddingEpoch
	baseCtx, cancel := context.WithCancel(ctx)
	c.relevanceRun++
	run := c.relevanceRun
	c.relevanceInFlight = true
	c.relevanceInFlightKey = request.key
	c.relevanceCancel = cancel
	c.mu.Unlock()

	go func() {
		defer cancel()
		defer func() {
			c.mu.Lock()
			if c.relevanceRun == run {
				c.relevanceInFlight = false
				c.relevanceInFlightKey = 0
				c.relevanceCancel = nil
			}
			c.mu.Unlock()
		}()

		embedCtx, timeoutCancel := context.WithTimeout(baseCtx, CompactEmbeddingTimeout)
		defer timeoutCancel()
		goalVectors, err := embedder.Embed(embedCtx, []string{request.goal})
		if err != nil || len(goalVectors) == 0 {
			return
		}

		goalVector := goalVectors[0]
		earliest := request.standardCutoff
		for start := 0; start < len(request.candidates); start += CompactRelevanceBatchSize {
			end := min(start+CompactRelevanceBatchSize, len(request.candidates))
			texts := make([]string, end-start)
			for i, candidate := range request.candidates[start:end] {
				texts[i] = candidate.text
			}
			vectors, err := embedder.Embed(embedCtx, texts)
			if err != nil || len(vectors) < len(texts) {
				return
			}
			for i, candidate := range request.candidates[start:end] {
				similarity, ok := cosineSimilarity32(goalVector, vectors[i])
				if ok && similarity >= CompactRelevanceKeepThreshold {
					earliest = min(earliest, candidate.index)
				}
			}
		}

		c.mu.Lock()
		defer c.mu.Unlock()
		if c.embeddingEpoch != epoch || c.relevanceRun != run {
			return
		}
		c.relevanceKey = request.key
		c.relevanceEarliest = earliest
		c.relevanceReady = true
	}()
}

func (c *Compactor) relevanceMayBeNeeded(used, window int, msgs []provider.Message) bool {
	if !c.Enabled() || used <= 0 || window <= 0 ||
		compactionCutoff(msgs, RecentTurnsToKeep) == 0 {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Embedding == nil || c.count >= MaxAutoCompactions {
		return false
	}
	if float64(used) >= CompactThreshold*float64(window) {
		return true
	}
	if float64(used) < CompactProjectionFloor*float64(window) {
		return false
	}
	if c.topicShiftLocked(used, window) {
		return true
	}
	if c.projectionWindow != window || c.projectionSamples < CompactProjectionMinSamples {
		return false
	}
	projected := float64(used) + c.projectionEWMA*CompactProjectionLookahead
	return projected >= CompactThreshold*float64(window)
}

func relevanceAdjustedCutoff(msgs []provider.Message, standardCutoff, earliest int) int {
	// The cutoff is moved before the earliest relevant message. Re-run the
	// tool-boundary check because a relevant tool result must also retain its
	// assistant call. If that would leave fewer than two real messages to
	// summarize, keep the standard boundary so compaction remains meaningful.
	adjusted := safeToolBoundary(msgs, earliest)
	if adjusted <= 0 || adjusted >= standardCutoff || nonSystemMessageCount(msgs[:adjusted]) < 2 {
		return standardCutoff
	}
	return adjusted
}

// relevanceGoalText joins short excerpts from the latest messages. Tool
// results get a smaller excerpt, matching jcode's goal representation while
// keeping the embedding request bounded.
func relevanceGoalText(msgs []provider.Message) string {
	indices := make([]int, 0, CompactRelevanceGoalMessages)
	for i := len(msgs) - 1; i >= 0 && len(indices) < CompactRelevanceGoalMessages; i-- {
		if relevanceGoalExcerpt(msgs[i]) != "" {
			indices = append(indices, i)
		}
	}
	if len(indices) == 0 {
		return ""
	}

	var b strings.Builder
	for i := len(indices) - 1; i >= 0; i-- {
		if excerpt := relevanceGoalExcerpt(msgs[indices[i]]); excerpt != "" {
			if b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(excerpt)
		}
	}
	return b.String()
}

func relevanceGoalExcerpt(msg provider.Message) string {
	if msg.Role == provider.RoleSystem {
		return ""
	}
	cap := 200
	if msg.Role == provider.RoleTool {
		cap = 100
	}
	return relevanceExcerpt(msg.Content, cap)
}

func relevanceMessageText(msg provider.Message) string {
	if msg.Role == provider.RoleSystem {
		return ""
	}
	return relevanceExcerpt(msg.Content, CompactEmbeddingMessageCap)
}

func relevanceExcerpt(text string, cap int) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if len(text) > cap {
		return truncateAtRune(text, cap)
	}
	return text
}

func nonSystemMessageCount(msgs []provider.Message) int {
	count := 0
	for _, msg := range msgs {
		if msg.Role != provider.RoleSystem {
			count++
		}
	}
	return count
}

// AddEmbeddingSnapshot records one already-computed assistant-turn embedding.
// It is intentionally separate from RecordEmbeddingSnapshot so callers with a
// local vector can update the semantic window without starting a provider call.
func (c *Compactor) AddEmbeddingSnapshot(vector []float32) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.addEmbeddingSnapshotLocked(vector)
}

// SetEmbeddingProvider switches the semantic backend and starts a fresh vector
// epoch. A provider change can change the embedding space; a result already in
// flight from the old provider is ignored when it returns.
func (c *Compactor) SetEmbeddingProvider(embedder EmbeddingProvider) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.Embedding = embedder
	c.resetEmbeddingHistoryLocked()
	c.mu.Unlock()
}

// ResetSemanticHistory discards vectors for a conversation rewrite such as
// /rewind. The epoch invalidates a detached provider result from the discarded
// history as well as clearing the vectors already stored.
func (c *Compactor) ResetSemanticHistory() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.resetEmbeddingHistoryLocked()
	c.mu.Unlock()
}

// RecordEmbeddingSnapshot starts a best-effort semantic snapshot without
// making the caller wait for the embedder. At most one request is in flight;
// if it is still running, the next turn simply falls back to the predictive
// J6.2 path until a vector is available.
func (c *Compactor) RecordEmbeddingSnapshot(ctx context.Context, text string) {
	if c == nil {
		return
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if len(text) > CompactEmbeddingMessageCap {
		text = truncateAtRune(text, CompactEmbeddingMessageCap)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// A turn's context is normally canceled as soon as Agent.Run returns. The
	// snapshot is a detached best-effort side call, so its own timeout governs
	// cleanup instead of the turn's lifetime.
	ctx = context.WithoutCancel(ctx)

	c.mu.Lock()
	embedder := c.Embedding
	if embedder == nil || c.embeddingInFlight {
		c.mu.Unlock()
		return
	}
	c.embeddingInFlight = true
	epoch := c.embeddingEpoch
	c.mu.Unlock()

	go func() {
		defer func() {
			c.mu.Lock()
			c.embeddingInFlight = false
			c.mu.Unlock()
		}()

		embedCtx, cancel := context.WithTimeout(ctx, CompactEmbeddingTimeout)
		defer cancel()
		vectors, err := embedder.Embed(embedCtx, []string{text})
		if err != nil || len(vectors) == 0 {
			return
		}

		c.mu.Lock()
		defer c.mu.Unlock()
		// A successful compaction or context-window change starts a new
		// semantic epoch. Do not let a late result from the old transcript
		// become the first vector in the new one.
		if c.embeddingEpoch != epoch {
			return
		}
		c.addEmbeddingSnapshotLocked(vectors[0])
	}()
}

// cloneMessages copies the slice and each variable-length field that a
// compaction keeps. The conversation owns its message values; retaining the
// caller's tool-call or image backing arrays would let a later append mutate
// the exact tail we promised to preserve.
func cloneMessages(msgs []provider.Message) []provider.Message {
	out := make([]provider.Message, len(msgs))
	for i, msg := range msgs {
		out[i] = msg
		out[i].ToolCalls = append([]provider.ToolCall(nil), msg.ToolCalls...)
		for j, call := range msg.ToolCalls {
			out[i].ToolCalls[j].Args = append(call.Args[:0:0], call.Args...)
		}
		out[i].Images = make([][]byte, len(msg.Images))
		for j, image := range msg.Images {
			out[i].Images[j] = append([]byte(nil), image...)
		}
		out[i].Repairs = append([]string(nil), msg.Repairs...)
	}
	return out
}

// compactionCutoff returns the prefix that can be summarized while retaining
// the latest user turns. The cutoff is conservative around tool calls: if the
// requested boundary would leave a tool result without its assistant call, it
// moves backward to keep the whole pair in the live suffix. A malformed or
// unanswered pair aborts compaction rather than handing a strict provider an
// invalid transcript.
func compactionCutoff(msgs []provider.Message, keepTurns int) int {
	if keepTurns <= 0 {
		return 0
	}
	users := 0
	cutoff := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != provider.RoleUser {
			continue
		}
		users++
		if users == keepTurns {
			cutoff = i
			break
		}
	}
	if cutoff <= 0 {
		return 0
	}
	old := msgs[:cutoff]
	oldContent := false
	for _, msg := range old {
		if msg.Role != provider.RoleSystem {
			oldContent = true
			break
		}
	}
	if !oldContent {
		return 0
	}
	return safeToolBoundary(msgs, cutoff)
}

// safeToolBoundary keeps tool-call/result pairs on one side of the cutoff.
// Provider messages carry the call id on the assistant and result rows rather
// than a nested content-block tree, so the check is deliberately expressed in
// terms of those two fields.
func safeToolBoundary(msgs []provider.Message, initial int) int {
	cutoff := initial
	callAt := make(map[string]int)
	resultAt := make(map[string][]int)
	for i, msg := range msgs {
		if msg.Role == provider.RoleAssistant {
			for _, call := range msg.ToolCalls {
				if call.ID != "" {
					if _, exists := callAt[call.ID]; !exists {
						callAt[call.ID] = i
					}
				}
			}
		}
		if msg.Role == provider.RoleTool {
			if msg.ToolCallID == "" {
				return 0
			}
			resultAt[msg.ToolCallID] = append(resultAt[msg.ToolCallID], i)
		}
	}

	for id, positions := range resultAt {
		call, ok := callAt[id]
		if !ok {
			return 0
		}
		for _, result := range positions {
			if result >= cutoff && call < cutoff {
				// The result is in the kept suffix but its call is in the
				// summarized prefix. Re-run the check at the call boundary;
				// the whole assistant message and its results now survive.
				return safeToolBoundary(msgs, call)
			}
			if result < cutoff && call >= cutoff {
				return 0
			}
		}
	}

	// A tool call in the kept suffix must have at least one result in that
	// suffix. Live turns normally satisfy this invariant, but manual compaction
	// must fail closed if it is invoked mid-tool-call.
	for i := cutoff; i < len(msgs); i++ {
		if msgs[i].Role != provider.RoleAssistant {
			continue
		}
		for _, call := range msgs[i].ToolCalls {
			positions := resultAt[call.ID]
			answered := false
			for _, result := range positions {
				if result >= cutoff {
					answered = true
					break
				}
			}
			if !answered {
				return 0
			}
		}
	}
	return cutoff
}

// ShouldCompact reports whether a turn should compact before dispatching.
func (c *Compactor) ShouldCompact(used, window int) bool {
	if !c.Enabled() || window <= 0 || used <= 0 {
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.count >= MaxAutoCompactions {
		return false
	}

	// A provider/model switch can change the window between turns. An old
	// slope has no meaning in the new coordinate system, so start a fresh
	// projection rather than carrying it across the boundary.
	if c.projectionWindow != window {
		previousWindow := c.projectionWindow
		c.resetProjectionLocked()
		// projectionWindow == 0 is also the initial state. Do not discard
		// snapshots that arrived before the first usage observation; only a
		// real window change invalidates the semantic vector space here.
		if previousWindow != 0 {
			c.resetEmbeddingHistoryLocked()
		}
		c.projectionWindow = window
	}

	if c.projectionSamples > 0 {
		if used < c.projectionLast {
			// A drop is usually provider-side trimming or an implicit reset. The
			// previous growth trend no longer describes the live context.
			c.projectionEWMA = 0
			c.projectionSamples = 1
		} else {
			delta := float64(used - c.projectionLast)
			if c.projectionSamples == 1 {
				c.projectionEWMA = delta
			} else {
				c.projectionEWMA = CompactEWMAAlpha*delta +
					(1-CompactEWMAAlpha)*c.projectionEWMA
			}
			c.projectionSamples++
		}
	}
	c.projectionLast = used
	if c.projectionSamples == 0 {
		c.projectionSamples = 1
	}

	current := float64(used)
	threshold := CompactThreshold * float64(window)
	if c.topicShiftLocked(used, window) {
		return true
	}
	if current >= threshold {
		return true
	}
	if current < CompactProjectionFloor*float64(window) ||
		c.projectionSamples < CompactProjectionMinSamples {
		return false
	}

	projected := current + c.projectionEWMA*CompactProjectionLookahead
	return projected >= threshold
}

// ShouldCompactForConversation is the automatic-turn entry point. Semantic
// compaction only makes sense when the conversation has an older prefix to
// summarize while keeping RecentTurnsToKeep turns; otherwise Compact would
// fail and the same topic-shift signal would fire again on every turn.
func (c *Compactor) ShouldCompactForConversation(used, window int, conv *Conversation) bool {
	if c == nil || conv == nil {
		return false
	}
	// Always sample the context first so an uncompactable early transcript does
	// not erase the predictive history that J6.2 needs once a prefix exists.
	shouldCompact := c.ShouldCompact(used, window)
	if compactionCutoff(conv.Messages(), RecentTurnsToKeep) == 0 {
		return false
	}
	return shouldCompact
}

// resetProjectionLocked clears the EWMA after a successful compaction. The
// caller must hold c.mu; the current context is a new coordinate system and
// must not inherit the pre-compaction growth slope.
func (c *Compactor) resetProjectionLocked() {
	c.projectionWindow = 0
	c.projectionLast = 0
	c.projectionSamples = 0
	c.projectionEWMA = 0
}

// resetEmbeddingHistoryLocked starts a new semantic context. The epoch also
// invalidates a detached provider result that was requested for the previous
// context, so an old turn cannot trigger a false topic shift after compaction.
// The caller must hold c.mu.
func (c *Compactor) resetEmbeddingHistoryLocked() {
	if c.relevanceCancel != nil {
		c.relevanceCancel()
	}
	c.embeddingHistory = nil
	c.embeddingDim = 0
	c.embeddingEpoch++
	c.relevanceReady = false
	c.relevanceKey = 0
	c.relevanceInFlight = false
	c.relevanceInFlightKey = 0
	c.relevanceRun++
	c.relevanceCancel = nil
	c.relevanceEarliest = 0
}

func (c *Compactor) addEmbeddingSnapshotLocked(vector []float32) {
	if len(vector) == 0 {
		return
	}
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return
		}
	}
	if c.embeddingDim != len(vector) {
		c.embeddingHistory = nil
		c.embeddingDim = len(vector)
	}
	copyOfVector := append([]float32(nil), vector...)
	if len(c.embeddingHistory) >= CompactEmbeddingHistoryWindow {
		copy(c.embeddingHistory, c.embeddingHistory[1:])
		c.embeddingHistory = c.embeddingHistory[:CompactEmbeddingHistoryWindow-1]
	}
	c.embeddingHistory = append(c.embeddingHistory, copyOfVector)
}

// topicShiftLocked compares the mean vector of the old half of the semantic
// window with the mean vector of its new half. It deliberately does not call
// the embedding provider: missing vectors are the normal fallback to the
// predictive J6.2 decision, and this method is on the pre-dispatch path.
// The caller must hold c.mu.
func (c *Compactor) topicShiftLocked(used, window int) bool {
	if window <= 0 || float64(used)/float64(window) < CompactProjectionFloor {
		return false
	}
	if len(c.embeddingHistory) < CompactTopicShiftMinSnapshots {
		return false
	}

	half := len(c.embeddingHistory) / 2
	oldMean := meanEmbedding(c.embeddingHistory[:half])
	newMean := meanEmbedding(c.embeddingHistory[half:])
	similarity, ok := cosineSimilarity(oldMean, newMean)
	return ok && similarity < CompactTopicShiftThreshold
}

func meanEmbedding(vectors [][]float32) []float64 {
	if len(vectors) == 0 || len(vectors[0]) == 0 {
		return nil
	}
	mean := make([]float64, len(vectors[0]))
	for _, vector := range vectors {
		if len(vector) != len(mean) {
			return nil
		}
		for i, value := range vector {
			mean[i] += float64(value)
		}
	}
	for i := range mean {
		mean[i] /= float64(len(vectors))
	}
	return mean
}

func cosineSimilarity(a, b []float64) (float64, bool) {
	if len(a) == 0 || len(a) != len(b) {
		return 0, false
	}
	var dot, normA, normB float64
	for i, value := range a {
		dot += value * b[i]
		normA += value * value
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0, false
	}
	return dot / math.Sqrt(normA*normB), true
}

func cosineSimilarity32(a, b []float32) (float64, bool) {
	if len(a) == 0 || len(a) != len(b) {
		return 0, false
	}
	var dot, normA, normB float64
	for i, value := range a {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) ||
			math.IsNaN(float64(b[i])) || math.IsInf(float64(b[i]), 0) {
			return 0, false
		}
		aValue := float64(value)
		bValue := float64(b[i])
		dot += aValue * bValue
		normA += aValue * aValue
		normB += bValue * bValue
	}
	if normA == 0 || normB == 0 {
		return 0, false
	}
	return dot / math.Sqrt(normA*normB), true
}
