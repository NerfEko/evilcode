package agent

import (
	"context"
	"strings"
	"sync"

	"evilcode/internal/provider"
	"evilcode/internal/todo"
)

// PokeHook is the post-turn hook that runs the §12.4 decision tree. It is the
// harness arguing with the agent about whether "done" is believable.
type PokeHook struct {
	Store *todo.Store

	mu      sync.Mutex
	state   todo.State
	enabled bool
}

// NewPokeHook builds the hook. Auto-poke defaults on (config `features.auto_poke`).
func NewPokeHook(store *todo.Store, enabled bool) *PokeHook {
	return &PokeHook{Store: store, enabled: enabled}
}

// Enabled reports whether poking is armed.
func (h *PokeHook) Enabled() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.enabled
}

// SetEnabled toggles auto-poke (Ctrl+P, `/poke on|off`).
func (h *PokeHook) SetEnabled(on bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.enabled = on
	if on {
		h.state.Rearm()
	}
}

// Disarm stops the cycle without disabling the feature. Esc-interrupt does
// this: "stop" means stop, and the harness must not immediately re-poke
// (plan.md §6.7).
func (h *PokeHook) Disarm() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.state.Disarmed = true
}

// Rearm clears the cycle flags, which a fresh todo write does.
func (h *PokeHook) Rearm() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.state.Rearm()
}

// RecordRefusal counts a provider refusal toward the breaker. A refusal is
// deterministic for the same request, so re-poking it loops forever.
func (h *PokeHook) RecordRefusal() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.state.RecordRefusal()
}

// RecordSuccess resets the refusal counter.
func (h *PokeHook) RecordSuccess() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.state.RecordSuccess()
}

// State returns a copy of the breaker state, for tests and `/poke status`.
func (h *PokeHook) State() todo.State {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.state
}

// refusalMarkers are the openings a refusal usually takes. Detection is
// deliberately conservative: a false positive disarms a working session, which
// is worse than missing one refusal.
var refusalMarkers = []string{
	"i can't help with that",
	"i cannot help with that",
	"i'm not able to help with that",
	"i won't be able to help",
	"i can't assist with that",
	"i cannot assist with that",
}

// looksLikeRefusal reports whether an assistant message reads as a refusal.
func looksLikeRefusal(content string) bool {
	head := strings.ToLower(strings.TrimSpace(content))
	if len(head) > 200 {
		head = head[:200]
	}
	for _, m := range refusalMarkers {
		if strings.Contains(head, m) {
			return true
		}
	}
	return false
}

// PostTurn implements Hooks.
func (h *PokeHook) PostTurn(ctx context.Context, a *Agent) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if !h.enabled || h.Store == nil {
		return false, nil
	}

	// A refusal must be counted before deciding, or the breaker never trips.
	if last, ok := a.Conv.Last(); ok && last.Role == provider.RoleAssistant {
		if looksLikeRefusal(last.Content) {
			h.state.RecordRefusal()
		} else {
			h.state.RecordSuccess()
		}
	}

	in := todo.Inputs{
		Items:        h.Store.Items(),
		Plan:         h.Store.Plan(),
		Goals:        h.Store.Goals(),
		Observations: h.Store.Observations(),
	}
	poke := todo.Decide(in, &h.state)

	if poke.SystemLine != "" {
		level := LevelInfo
		if poke.Decision == todo.Disarm && poke.Queued == "" && strings.HasPrefix(poke.SystemLine, "⚠") {
			level = LevelWarning
		}
		a.Notice(level, "%s", poke.SystemLine)
	}

	if poke.Decision != todo.Continue || poke.Queued == "" {
		return false, nil
	}

	// The continuation persists as user-role so the model reads a normal
	// message, and is re-rendered as a system line on replay by its
	// `[automated ...]` prefix (plan.md §12.4).
	a.Conv.Append(provider.Message{Role: provider.RoleUser, Content: poke.Queued})

	// The digest has been delivered, so the observations that produced it are
	// spent.
	if strings.HasPrefix(poke.Queued, todo.DigestPrefix) {
		_ = h.Store.ClearObservations()
	}
	return true, nil
}
