// Handoff — the port of omp's handoff(): generate a full continuation
// document against the live system prompt (so the provider cache stays
// warm), start a fresh session, and carry the document in as the first
// message. Overflow never handoffs (omp rule: the handoff LLM call would hit
// the same overflow).
package compact

import (
	"context"
	"strings"

	"evilcode/internal/provider"
)

// MsgLite is the minimal view GenerateHandoff needs. provider.Message
// satisfies it implicitly.
type MsgLite = provider.Message

// GenerateHandoff runs the handoff document generation: full conversation
// history, live system prompt, handoff-document prompt as the final user
// message. evilcode side-calls carry no tools, so toolChoice:none is
// implicit.
func GenerateHandoff(ctx context.Context, summarize Summarizer, model ModelRef,
	messages []provider.Message, systemPrompt string, customInstructions string,
) (string, error) {
	var b strings.Builder
	// omp keeps the live provider transcript verbatim here, not the tagged
	// summarizer form — the generating model reads it, not describes it.
	// evilcode side-calls take (system, user) text, so the transcript is
	// rendered in the flat form, framed as source material for a document.
	for _, m := range messages {
		switch m.Role {
		case provider.RoleUser:
			if c := strings.TrimSpace(m.Content); c != "" {
				b.WriteString("[User]: " + c + "\n\n")
			}
		case provider.RoleAssistant:
			if c := strings.TrimSpace(m.Content); c != "" {
				b.WriteString("[Assistant]: " + c + "\n\n")
			}
		case provider.RoleTool:
			if c := strings.TrimSpace(m.Content); c != "" {
				b.WriteString("[Tool Result (" + m.ToolName + ")]: " + truncateForSummary(c, TOOL_RESULT_MAX_CHARS) + "\n\n")
			}
		}
	}
	prompt := "<conversation>\n" + b.String() + "\n</conversation>\n\n"
	if customInstructions != "" {
		prompt += "Additional focus: " + customInstructions + "\n\n"
	}
	prompt += HandoffDocumentPrompt
	out, err := summarize(ctx, model, systemPrompt, prompt)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}
