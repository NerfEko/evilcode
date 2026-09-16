// Superseded-read pruning — the port of omp's pruning.ts
// pruneSupersededToolResults: a later read of the same file (selector-stripped)
// blanks the older read's result in place, keeping the live context tight so
// compaction stays a rare event.
package compact

import (
	"encoding/json"
	"strings"

	"evilcode/internal/provider"
)

// SUPERSEDED_NOTICE is the exact placeholder written over a superseded
// read result (pruning.ts:66).
const SUPERSEDED_NOTICE = "[Superseded by a newer read of this file]"

// PruneCandidate locates one superseded result in a message slice.
type PruneCandidate struct {
	Index  int // index into the message slice
	Tokens int
}

// FindSupersededReads returns the message indexes whose read results are
// superseded by a later read of the same path (selector-stripped). The LAST
// read of each path survives; everything older is a candidate. Protected
// tools (results whose call is protected) and error results never prune.
//
// omp mutates its per-entry store in place; evilcode re-encodes the log, so
// candidates carry an index and the replacement text is applied by the
// storage layer.
func FindSupersededReads(msgs []provider.Message, toolCallPath func(callID string) (string, bool)) []PruneCandidate {
	// toolCallsById: call id → (tool name, path) for read-tool calls.
	type callInfo struct {
		name string
		path string
	}
	calls := map[string]callInfo{}
	for _, msg := range msgs {
		if msg.Role != provider.RoleAssistant {
			continue
		}
		for _, call := range msg.ToolCalls {
			if call.ID == "" {
				continue
			}
			info := callInfo{name: call.Name}
			if isReadTool(call.Name) {
				var args struct {
					Path string `json:"path"`
				}
				if err := json.Unmarshal(call.Args, &args); err == nil && args.Path != "" {
					info.path = SplitReadSelector(args.Path)
				}
			}
			calls[call.ID] = info
		}
	}

	// Walk newest→oldest; the first read seen per path wins, everything
	// older with the same path is superseded (pruning.ts:181).
	lastRead := map[string]int{} // path → newest index carrying it
	var out []PruneCandidate
	for i := len(msgs) - 1; i >= 0; i-- {
		msg := msgs[i]
		if msg.Role != provider.RoleTool || msg.ToolCallID == "" || msg.IsError {
			continue
		}
		info, ok := calls[msg.ToolCallID]
		if !ok || info.name != "read" || info.path == "" {
			continue
		}
		if _, seen := lastRead[info.path]; seen {
			tokens := estimateTokens(msg)
			if tokens > 50 { // omp MIN_PRUNE_TOKENS: below this, blanking saves nothing
				out = append(out, PruneCandidate{Index: i, Tokens: tokens})
			}
			continue
		}
		lastRead[info.path] = i
	}
	return out
}

func isReadTool(name string) bool {
	return strings.EqualFold(name, "read")
}

// PruneMessages returns msgs with every superseded candidate's content
// replaced by the notice. Tool-call/result pairing is untouched — only the
// result text changes (pruning.ts:248).
func PruneMessages(msgs []provider.Message, candidates []PruneCandidate) []provider.Message {
	out := make([]provider.Message, len(msgs))
	copy(out, msgs)
	for _, c := range candidates {
		if c.Index < 0 || c.Index >= len(out) {
			continue
		}
		out[c.Index].Content = SUPERSEDED_NOTICE
	}
	return out
}
