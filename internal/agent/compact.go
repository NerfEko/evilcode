package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"

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

// Compactor collapses a conversation when it gets too long.
//
// It lives here rather than in the TUI because the TUI is not the only thing
// that runs a long conversation: a daemon session, an overnight run and a
// spawned worker all needed compaction and none of them could reach it.
type Compactor struct {
	// Summarize produces the summary. Nil disables compaction entirely.
	Summarize Summarizer

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
		text := strings.TrimSpace(msg.Content)
		if text == "" {
			continue
		}
		if len(text) > CompactMessageCap {
			text = truncateAtRune(text, CompactMessageCap) + "…"
		}
		fmt.Fprintf(&b, "%s: %s\n\n", msg.Role, text)
	}
	return b.String()
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
	c.mu.Unlock()
	return summary, nil
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
		c.resetProjectionLocked()
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

// resetProjectionLocked clears the EWMA after a successful compaction. The
// caller must hold c.mu; the current context is a new coordinate system and
// must not inherit the pre-compaction growth slope.
func (c *Compactor) resetProjectionLocked() {
	c.projectionWindow = 0
	c.projectionLast = 0
	c.projectionSamples = 0
	c.projectionEWMA = 0
}
