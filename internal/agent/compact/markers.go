// Compaction marker helpers and the tool-boundary guard. These move out of
// the old agent.Compactor: markers stay wire-compatible with the existing
// session format, and safeToolBoundary keeps omp's fail-closed rule for
// manual callers and as a preparation-time assertion.
package compact

import (
	"strings"

	"evilcode/internal/provider"
)

// CompactedPrefix marks the synthetic message a compaction leaves behind
// (evilcode CompactedPrefix — wire format unchanged).
const CompactedPrefix = "[conversation compacted]\n\n"

// CompactedRecentPrefix marks the serialized recent context kept beside a
// summary (wire format unchanged).
const CompactedRecentPrefix = "[conversation recent context]\n\n"

// IsCompactionMarker reports a stored summary message.
func IsCompactionMarker(msg provider.Message) bool {
	return msg.Role == provider.RoleUser && strings.HasPrefix(msg.Content, CompactedPrefix)
}

// IsCompactionRecentMarker reports a stored serialized recent context.
func IsCompactionRecentMarker(msg provider.Message) bool {
	return msg.Role == provider.RoleUser && strings.HasPrefix(msg.Content, CompactedRecentPrefix)
}

// isCompactionMarker — internal alias.
func isCompactionMarker(msg provider.Message) bool { return IsCompactionMarker(msg) }

// isCompactionRecentMarker — internal alias.
func isCompactionRecentMarker(msg provider.Message) bool { return IsCompactionRecentMarker(msg) }

// SafeToolBoundary keeps tool-call/result pairs on one side of a cutoff.
//
// omp guarantees pair integrity by construction: it only cuts at user or
// assistant boundaries, and a cut at an assistant message keeps that
// assistant's calls AND their results together in the tail. evilcode's
// provider transcript is flat, so this check verifies the invariant after
// selection and fails closed (returns 0) rather than sending a request the
// provider would reject: a kept result whose call was summarized, or a kept
// call with no kept result.
//
// This is the same rule the old agent package enforced; it moves here so
// preparation and callers share one implementation.
func SafeToolBoundary(msgs []provider.Message, initial int) int {
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
				return SafeToolBoundary(msgs, call)
			}
			if result < cutoff && call >= cutoff {
				return 0
			}
		}
	}

	// A tool call in the kept suffix must have at least one result in that
	// suffix. Manual compaction must fail closed if invoked mid-tool-call.
	for i := cutoff; i < len(msgs); i++ {
		if msgs[i].Role != provider.RoleAssistant {
			continue
		}
		for _, call := range msgs[i].ToolCalls {
			answered := false
			for _, result := range resultAt[call.ID] {
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

// safeToolBoundary — internal alias.
func safeToolBoundary(msgs []provider.Message, initial int) int {
	return SafeToolBoundary(msgs, initial)
}
