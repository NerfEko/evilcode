// Serialization of a conversation for the summarizer — the port of omp's
// compaction/utils.ts serializeConversation.
//
// The transcript is rendered as flat, clearly-tagged text rather than a
// conversation to continue: markers ([User]:, [Think]:, [Assistant]:, [Tool
// Call]:, [Tool Result]:) and the <conversation> wrapper added at prompt
// assembly make it data the summarizer describes, not a chat it continues.
package compact

import (
	"fmt"
	"strings"

	"evilcode/internal/provider"
)

// TOOL_RESULT_MAX_CHARS bounds one tool result inside the serialized
// transcript. A single pasted file cannot crowd out the conversation
// (utils.ts:199). omp truncates from the start and appends a marker.
const TOOL_RESULT_MAX_CHARS = 2000

// truncateForSummary keeps the beginning of text up to maxChars and appends
// an explicit truncation marker (utils.ts:205).
func truncateForSummary(text string, maxChars int) string {
	if maxChars <= 0 {
		return ""
	}
	if len(text) <= maxChars {
		return text
	}
	return text[:maxChars] + "… [truncated]"
}

// serializeConversation renders messages to the flat transcript form the
// summarizer sees. Useless tool results (and their paired calls) are dropped
// when the flag is set — omp drops zero-match grep results for free because
// their source region is discarded after summarization anyway (utils.ts:216).
// evilcode messages do not carry the useless flag yet; the check is present
// and inert until the field exists.
func serializeConversation(messages []provider.Message) string {
	var parts []string
	for _, msg := range messages {
		if msg.Role == provider.RoleSystem || (msg.Hidden && !isCompactionMarker(msg) && !isCompactionRecentMarker(msg)) {
			continue
		}
		switch msg.Role {
		case provider.RoleUser:
			if c := strings.TrimSpace(msg.Content); c != "" {
				parts = append(parts, fmt.Sprintf("[User]: %s", c))
			}
		case provider.RoleAssistant:
			if msg.Reasoning != "" {
				parts = append(parts, fmt.Sprintf("[Think]: %s", strings.TrimSpace(msg.Reasoning)))
			}
			if c := strings.TrimSpace(msg.Content); c != "" {
				parts = append(parts, fmt.Sprintf("[Assistant]: %s", c))
			}
			if len(msg.ToolCalls) > 0 {
				parts = append(parts, fmt.Sprintf("[Tool Call]: %s", renderToolCalls(msg.ToolCalls)))
			}
		case provider.RoleTool:
			c := strings.TrimSpace(msg.Content)
			if c == "" {
				continue
			}
			label := "tool"
			if msg.ToolName != "" {
				label = msg.ToolName
			}
			parts = append(parts, fmt.Sprintf("[Tool Result (%s)]: %s", label, truncateForSummary(c, TOOL_RESULT_MAX_CHARS)))
		}
	}
	return strings.Join(parts, "\n\n")
}

// renderToolCalls renders an assistant turn's tool calls as the compact
// name(key=value, …) list omp uses (utils.ts:309).
func renderToolCalls(calls []provider.ToolCall) string {
	var out []string
	for _, call := range calls {
		args := string(call.Args)
		if args == "null" || args == "" {
			args = ""
		}
		if args == "" {
			out = append(out, fmt.Sprintf("%s()", call.Name))
			continue
		}
		out = append(out, fmt.Sprintf("%s(%s)", call.Name, args))
	}
	return strings.Join(out, "; ")
}
