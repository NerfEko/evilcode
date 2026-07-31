package agent

import (
	"context"
	"strings"
	"sync"

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

	mu     sync.Mutex
	active map[int]context.CancelFunc
	next   int
	closed bool
	wg     sync.WaitGroup
}

// NewMemoryHook builds the hook.
func NewMemoryHook(m *memory.Manager) *MemoryHook {
	h := &MemoryHook{Manager: m, active: map[int]context.CancelFunc{}}
	h.extract = h.startExtraction
	return h
}

// runExtraction is the default sink: a detached side-call, because extraction
// takes a model round-trip and the turn has already ended. Its own context
// bounds it so a hung `smol` provider cannot leak a goroutine for the session's
// lifetime.
func (h *MemoryHook) startExtraction(_ context.Context, m *memory.Manager) {
	ctx, cancel := context.WithTimeout(context.Background(), memory.ExtractTimeout)
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		cancel()
		return
	}
	h.next++
	id := h.next
	h.active[id] = cancel
	h.wg.Add(1)
	h.mu.Unlock()
	go func() {
		defer cancel()
		defer h.wg.Done()
		defer func() {
			h.mu.Lock()
			delete(h.active, id)
			h.mu.Unlock()
		}()
		_, _ = m.Extract(ctx)
	}()
}

// Close cancels and joins detached extraction calls before their store can be
// closed. Without this, a TUI session switch left a side-call goroutine holding
// a memory store reference into the next session's lifetime.
func (h *MemoryHook) Close() {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.closed = true
	for _, cancel := range h.active {
		cancel()
	}
	h.mu.Unlock()
	h.wg.Wait()
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
