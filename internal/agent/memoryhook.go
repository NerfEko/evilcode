package agent

import (
	"context"
	"strings"

	"evilcode/internal/memory"
	"evilcode/internal/provider"
)

// MemoryHook feeds finished turns to ambient extraction (plan.md §19).
//
// It never appends to the conversation, so it belongs first in the Chain: a
// hook that always returns false cannot starve the ones after it, and putting
// it last would mean an auto-poked turn is never observed.
type MemoryHook struct {
	Manager *memory.Manager

	// extract is swappable so a test can observe the call without a model.
	extract func(context.Context, *memory.Manager)
}

// NewMemoryHook builds the hook.
func NewMemoryHook(m *memory.Manager) *MemoryHook {
	return &MemoryHook{Manager: m, extract: runExtraction}
}

// runExtraction is the default sink: a detached side-call, because extraction
// takes a model round-trip and the turn has already ended. Its own context
// bounds it so a hung `smol` provider cannot leak a goroutine for the session's
// lifetime.
func runExtraction(_ context.Context, m *memory.Manager) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), memory.ExtractTimeout)
		defer cancel()
		_, _ = m.Extract(ctx)
	}()
}

// PostTurn implements Hooks.
func (h *MemoryHook) PostTurn(ctx context.Context, a *Agent) (bool, error) {
	if h.Manager == nil || !h.Manager.Enabled() {
		return false, nil
	}
	if h.Manager.ObserveTurn(turnText(a)) {
		h.extract(ctx, h.Manager)
	}
	return false, nil
}

// turnText is what extraction reads: the last user message and the reply to it.
// Tool traffic is deliberately excluded — file contents and command output are
// exactly the transient noise §19 says not to remember.
func turnText(a *Agent) string {
	msgs := a.Conv.Messages()
	var user, assistant string
	for i := len(msgs) - 1; i >= 0; i-- {
		switch msgs[i].Role {
		case provider.RoleAssistant:
			if assistant == "" && msgs[i].Content != "" {
				assistant = msgs[i].Content
			}
		case provider.RoleUser:
			// The `<memories>` tail is something memory itself wrote; feeding it
			// back in is how a bank starts remembering its own output.
			if user == "" && !strings.HasPrefix(msgs[i].Content, "<memories>") {
				user = msgs[i].Content
			}
		}
		if user != "" && assistant != "" {
			break
		}
	}
	if user == "" && assistant == "" {
		return ""
	}
	return strings.TrimSpace("user: " + user + "\nassistant: " + assistant)
}
