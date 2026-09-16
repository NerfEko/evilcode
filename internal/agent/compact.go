package agent

// Compactor — the thin adapter that wires the ported engine
// (internal/agent/compact) onto evilcode's Conversation + session storage.
//
// omp's compaction machinery lives in the compact package verbatim; this
// type keeps only what evilcode's call sites need: the summarizer wiring,
// persistence callbacks, the runaway breaker, and the manual entry points.
// The projection EWMA, semantic topic-shift, and relevance cutoff are gone —
// omp compacts when the context is genuinely near full, and every
// speculative trigger multiplied exposure to a weak summarizer (the Toad-22
// failure; docs/plan-compaction-port.md).

import (
	"context"
	"fmt"
	"sync"

	"evilcode/internal/agent/compact"
	"evilcode/internal/provider"
)

// MaxAutoCompactions bounds consecutive automatic compactions without a
// completed model response.
//
// Invariant 6. Without it, a summary that is itself over the threshold
// compacts forever and never sends a request — the model never speaks and
// the loop never ends, which is the worst shape of runaway because it looks
// like hanging. omp replaces this with provider-overflow recovery; until
// that port lands (engine phase 7) the breaker stays.
const MaxAutoCompactions = 3

// CompactionSummaryMaxBytes keeps a successful compaction result from
// becoming a new oversized context window. Enforced by the engine's
// validation gate; re-declared here for callers that pre-check.
const CompactionSummaryMaxBytes = compact.SUMMARIZATION_MAX_BYTES

// Summarizer produces a summary from (system, user) prompts. It is a
// function rather than a router so this package keeps knowing nothing about
// config (invariant 1).
type Summarizer = func(ctx context.Context, system, user string) (string, error)

// EmbeddingProvider stays for the memory feature; compaction no longer
// consumes it.
type EmbeddingProvider = provider.Provider

// Compactor collapses a conversation when it gets too long.
//
// It lives here rather than in the TUI because the TUI is not the only thing
// that runs a long conversation: a daemon session, an overnight run and a
// spawned worker all needed compaction and none of them could reach it.
type Compactor struct {
	// Summarize produces the summary. Nil disables compaction entirely.
	Summarize Summarizer

	// ContextWindow is the active model's context limit, refreshed by
	// automatic callers before each decision.
	ContextWindow int

	// Settings carry the omp compaction knobs. Zero value means defaults;
	// wiring fills them from [compaction] config.
	Settings compact.Settings

	// Candidates is the omp model fallback chain, ordered session model →
	// roles → largest window. Empty means "Summarize alone", which keeps the
	// single-model test path working.
	Candidates []compact.ModelInfoLite

	// SessionModel names the active model for the candidate chain.
	SessionModel string

	// FirstUserMessage is the session's opening user prompt; the validation
	// gate requires the summary to preserve it.
	FirstUserMessage string

	// Persist writes the compacted history to durable storage and returns what
	// a resume would replay.
	Persist func(summary string) ([]provider.Message, error)

	// PersistWithTail is the durable form used by live sessions. It receives
	// the sanitized checkpoint tail (the serialized recent context), not raw
	// provider messages.
	PersistWithTail func(summary string, tail []provider.Message) ([]provider.Message, error)

	// PersistWithInfo is the full omp form: short summary + token accounting
	// ride the compaction meta entry. Nil falls back to PersistWithTail.
	PersistWithInfo func(info compact.CompactInfo, tail []provider.Message) ([]provider.Message, error)

	// OnCompaction resets session-local caches whose contents are no longer in
	// the model context (for example the tool exposure ledger).
	OnCompaction func()

	// PruneRuns the omp per-turn pruning pass: superseded reads blanked in
	// the durable log. Nil disables. Wiring gives it the data dir + session
	// name closure; the Compactor invokes it after a completed turn.
	PruneRuns func() (pruned int, tokensSaved int, err error)

	mu    sync.Mutex
	count int

	// autoCount is the breaker budget MaxAutoCompactions gates. It resets after
	// a real model response; manual /compact calls never consume it.
	autoCount int

	// lastPromptTokens is the newest request's provider-reported prompt size,
	// fed to the engine's floor-and-max token rule by SetPromptTokens.
	lastPromptTokens int

	// lastResult is the newest compaction's full result for display.
	lastResult *compact.Result
}

// SetPromptTokens records the newest request's provider-reported prompt size
// for the compaction decision's max-of-two-arms rule.
func (c *Compactor) SetPromptTokens(n int) {
	c.mu.Lock()
	c.lastPromptTokens = n
	c.mu.Unlock()
}

// nonSystemMessageCount mirrors the old guard: system rows and hidden
// harness prompts are not compactable content.
func nonSystemMessageCount(msgs []provider.Message) int {
	n := 0
	for _, msg := range msgs {
		if msg.Role == provider.RoleSystem || (msg.Hidden && !compact.IsCompactionMarker(msg) && !compact.IsCompactionRecentMarker(msg)) {
			continue
		}
		n++
	}
	return n
}

// noteAutoCompaction records one automatic compaction against the automatic
// budget. Manual compactions do not consume it (R2-14).
func (c *Compactor) noteAutoCompaction() {
	c.mu.Lock()
	c.autoCount++
	c.mu.Unlock()
}

// noteModelOutput resets the automatic breaker after the model has actually
// answered. A lifetime cap would eventually disable protection in a healthy
// long-running session; only repeated compaction without a response is a
// runaway loop.
func (c *Compactor) noteModelOutput() {
	c.mu.Lock()
	c.autoCount = 0
	c.mu.Unlock()
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

// settings resolves the effective settings, defaulting a zero value so
// direct struct literals in tests keep working.
func (c *Compactor) settings() compact.Settings {
	s := c.Settings
	if s.ReserveTokens == 0 && s.KeepRecentTokens == 0 && s.Strategy == "" && s.Enabled == false {
		return compact.DefaultSettings()
	}
	if !s.Enabled {
		// Manual callers must always be able to compact; only the automatic
		// path consults ShouldCompact.
		return s
	}
	return s
}

// ShouldCompact reports whether a turn should compact before dispatching —
// omp's single rule: context tokens above the threshold. There is no
// projection and no topic shift; overflow and incomplete recovery are the
// dedicated emergency paths.
func (c *Compactor) ShouldCompact(used, window int) bool {
	if !c.Enabled() || window <= 0 || used <= 0 {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.autoCount >= MaxAutoCompactions {
		return false
	}
	return compact.ShouldCompact(used, window, c.settings())
}

// ShouldCompactForConversation is the automatic-turn entry point. It adds
// the conversation-aware guard: compaction only makes sense when the
// conversation has an older prefix to summarize while preserving a recent
// token-budget tail; otherwise the same trigger would fire and fail on every
// turn.
func (c *Compactor) ShouldCompactForConversation(used, window int, conv *Conversation) bool {
	if c == nil || conv == nil {
		return false
	}
	if !c.ShouldCompact(used, window) {
		return false
	}
	// Prepare on a throwaway: the real compaction repeats this split under
	// the rewrite lock, so the cheap pre-check cannot drift from it.
	return compact.PrepareCompaction(conv.Messages(), c.settings(), used, used) != nil
}

func Transcript(msgs []provider.Message) string { return compact.Serialize(msgs) }

// Compact summarises a conversation and replaces it with the summary.
//
// The order matters: the summary is written to storage *before* the in-memory
// history is replaced, so a failure to persist leaves the conversation intact
// rather than dropping it on the floor with nothing on disk to recover from.
func (c *Compactor) Compact(ctx context.Context, conv *Conversation) (string, error) {
	window := 0
	if c != nil {
		window = c.ContextWindow
	}
	return c.CompactWithWindow(ctx, conv, window)
}

// CompactWithWindow compacts using window as the active model context limit.
// The explicit argument keeps the automatic preflight and the actual rewrite
// on the same model boundary even when a caller has just switched models.
func (c *Compactor) CompactWithWindow(ctx context.Context, conv *Conversation, window int) (string, error) {
	if !c.Enabled() {
		return "", fmt.Errorf("no summarizer is configured")
	}
	if conv == nil {
		return "", fmt.Errorf("nothing to compact")
	}

	// Hold the conversation rewrite lock from snapshot through persistence.
	// Otherwise an append can land after this snapshot and be erased by
	// resetMessages, or be absent from the stale session-file rewrite while
	// memory is reset.
	conv.compactionMu.Lock()
	defer conv.compactionMu.Unlock()

	msgs := conv.messagesSnapshot()
	if nonSystemMessageCount(msgs) == 0 {
		return "", fmt.Errorf("nothing to compact")
	}
	s := c.settings()
	used := compact.CompactionContextTokens(c.lastPromptTokens, compact.EstimateConversation(msgs))
	prep := compact.PrepareCompaction(msgs, s, used, used)
	if prep == nil {
		return "", fmt.Errorf("not enough history to compact while preserving a usable recent context tail")
	}
	if window > 0 && used > window {
		// The context is already past the window: compaction must not keep a
		// tail at all, or the next request overflows before the summary pays
		// off. omp's overflow path summarizes the whole conversation.
		prep.RecentMessages = nil
		prep.TurnPrefixMessages = nil
		prep.IsSplitTurn = false
	}

	candidates := c.Candidates
	if len(candidates) == 0 {
		candidates = []compact.ModelInfoLite{{Ref: c.SessionModel}}
	}
	res, _, err := compact.Compact(ctx, prep, c.summarizerAdapter(), candidates, "", compact.SummaryOptions{}, c.FirstUserMessage)
	if err != nil {
		return "", err
	}
	// Raw recent messages are not safe checkpoint state. Reasoning traces,
	// provider continuation items, tool envelopes, images, and display-only
	// metadata all belong to the old provider turn. Serialize only the visible
	// facts into one hidden historical message so the next request starts from
	// a clean provider-neutral boundary (unchanged evilcode storage rule).
	var checkpointTail []provider.Message
	if rc, ok := compact.RecentContextMessageHidden(prep.RecentMessages); ok {
		checkpointTail = append(checkpointTail, rc)
	}
	replay := append([]provider.Message{compact.SummaryMessage(res.Summary)}, checkpointTail...)
	var stored []provider.Message
	switch {
	case c.PersistWithInfo != nil:
		stored, err = c.PersistWithInfo(compact.CompactInfo{
			Summary:      res.Summary,
			ShortSummary: res.ShortSummary,
			TokensBefore: res.TokensBefore,
			KeptTokens:   res.KeptTokens,
		}, checkpointTail)
		if err != nil {
			return "", fmt.Errorf("compaction was not saved: %w", err)
		}
	case c.PersistWithTail != nil:
		stored, err = c.PersistWithTail(res.Summary, checkpointTail)
		if err != nil {
			return "", fmt.Errorf("compaction was not saved: %w", err)
		}
	default:
		if c.Persist != nil {
			stored, err = c.Persist(res.Summary)
			if err != nil {
				return "", fmt.Errorf("compaction was not saved: %w", err)
			}
		}
	}
	if len(stored) > 0 {
		// Storage is the source of truth for what a resume will replay. Both
		// persistence callbacks may normalize or repair the message sequence,
		// so the live conversation must follow the canonical value they return.
		replay = stored
	}
	conv.resetMessages(replay)
	if c.OnCompaction != nil {
		c.OnCompaction()
	}

	c.mu.Lock()
	c.count++
	c.lastResult = res
	c.mu.Unlock()
	return res.Summary, nil
}

// LastResult returns the newest compaction's full result (short summary,
// token accounting) for display surfaces.
func (c *Compactor) LastResult() *compact.Result {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastResult
}

// summarizerAdapter feeds one candidate through the configured Summarizer.
// The engine's candidate loop names the model; a single-model Summarizer
// ignores it.
func (c *Compactor) summarizerAdapter() compact.Summarizer {
	return func(ctx context.Context, model compact.ModelRef, system, user string) (string, error) {
		return c.Summarize(ctx, system, user)
	}
}

// CompactSummaryText returns the summary body a caller would store, minus
// the marker prefix. Used by tests and the daemon's compact command.
func CompactSummaryText(res *compact.Result) string {
	if res == nil {
		return ""
	}
	return res.Summary
}
