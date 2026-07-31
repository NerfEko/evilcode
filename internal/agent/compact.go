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

// CompactThreshold is the fraction of the context window at which a turn
// compacts before dispatching (plan.md §9.9).
//
// A constant rather than a config knob: it is the kind of setting nobody tunes
// and everybody would have to understand to tune correctly.
const CompactThreshold = 0.85

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

	mu    sync.Mutex
	count int
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

	summary, err := c.Summarize(ctx, CompactPrompt, Transcript(conv.Messages()))
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(summary) == "" {
		return "", fmt.Errorf("the summarizer returned nothing")
	}

	replay := []provider.Message{CompactMessage(summary)}
	if c.Persist != nil {
		stored, err := c.Persist(summary)
		if err != nil {
			return "", fmt.Errorf("compaction was not saved: %w", err)
		}
		if len(stored) > 0 {
			replay = stored
		}
	}
	conv.Reset(replay)

	c.mu.Lock()
	c.count++
	c.mu.Unlock()
	return summary, nil
}

// ShouldCompact reports whether a turn should compact before dispatching.
func (c *Compactor) ShouldCompact(used, window int) bool {
	if !c.Enabled() || window <= 0 || used <= 0 {
		return false
	}
	if c.Count() >= MaxAutoCompactions {
		return false
	}
	return float64(used)/float64(window) >= CompactThreshold
}
