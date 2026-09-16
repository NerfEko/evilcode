package agent

import (
	"context"
	"strings"
	"sync"

	"evilcode/internal/provider"
)

// OrchestrateContract is the fan-out contract injected as a hidden
// system-authored notice when orchestrator mode arms (orchestrator fan-out
// D5). It states the same rules as the `orchestrate` skill in compressed
// form, so the two cannot drift: edit both together.
//
// One Go const (the OvernightPrompt pattern) rather than config text, because
// the detector and the notice must agree on what "orchestrate" means.
const OrchestrateContract = `Orchestrator mode is on: fan out with spawn_worker.

- Batch independent spawns in one round and wait only for the batch. The
  scheduler runs consecutive spawns concurrently; sequential rounds are for
  dependent work.
- Brief each worker self-containedly: objective, files to start from,
  done-condition, result schema when the shape matters, and a stopping
  condition. A worker sees only its brief.
- A per-call model is optional; default is the session model. Never spawn for
  what one read answers.
- Todo tracking and auto-poke are disabled while this mode is active; use the
  worker results and the delegation contract instead.
- You validate and integrate results; workers do not coordinate each other.
  A failed worker is data — re-spawn the gap, do not absorb it.
- Prefer messaging an idle worker with its context over spawning fresh for a
  follow-up. Load the orchestrate skill for the full playbook.`

// HasOrchestrateKeyword reports whether text contains the standalone
// lowercase word "orchestrate" in prose. Trigger semantics:
//
//   - standalone lowercase only: "Orchestrate", "orchestrated" and
//     "reorchestrate" never fire;
//   - prose only: occurrences inside single quotes, double quotes, or
//     backticks never fire;
//   - never a path: occurrences adjacent to '/' or forming "orchestrate.<ext>"
//     never fire.
//
// A trailing period or comma still fires: "run orchestrate." is prose.
func HasOrchestrateKeyword(text string) bool {
	const word = "orchestrate"
	for i := 0; i+len(word) <= len(text); i++ {
		if !strings.HasPrefix(text[i:], word) {
			continue
		}
		if i > 0 && isWordChar(text[i-1]) {
			continue
		}
		if end := i + len(word); end < len(text) && isWordChar(text[end]) {
			continue
		}
		if inQuotes(text, i) || isPathUse(text, i, len(word)) {
			continue
		}
		return true
	}
	return false
}

func isWordChar(b byte) bool {
	return b == '_' || ('0' <= b && b <= '9') ||
		('a' <= b && b <= 'z') || ('A' <= b && b <= 'Z') ||
		b >= 0x80
}

// inQuotes reports whether position pos sits inside single quotes, double
// quotes, or backticks. The scan is deliberately naive — an apostrophe in
// "don't" toggles single-quote state — because the failure mode is a missed
// trigger, never a wrong action: the hook only injects guidance.
func inQuotes(text string, pos int) bool {
	var single, double, back bool
	for i := 0; i < pos && i < len(text); i++ {
		if text[i] == '\\' && i+1 < len(text) {
			i++
			continue
		}
		switch text[i] {
		case '\'':
			if !double && !back {
				single = !single
			}
		case '"':
			if !single && !back {
				double = !double
			}
		case '`':
			if !single && !double {
				back = !back
			}
		}
	}
	return single || double || back
}

// isPathUse reports path-component uses: adjacent slashes, or a dotted
// extension ("orchestrate.md"). A trailing sentence period ("run
// orchestrate.") still fires: only a period followed by a word character
// reads as an extension.
func isPathUse(text string, pos, wordLen int) bool {
	if pos > 0 && text[pos-1] == '/' {
		return true
	}
	end := pos + wordLen
	if end < len(text) {
		if text[end] == '/' {
			return true
		}
		if text[end] == '.' && end+1 < len(text) && isWordChar(text[end+1]) {
			return true
		}
	}
	return false
}

// OrchestrateHook arms orchestrator mode from the `orchestrate` keyword and
// injects the contract (fan-out D5). It is a Chain hook like auto-poke and
// the advisor, so the loop stays readable and it can be tested alone.
type OrchestrateHook struct {
	mu       sync.Mutex
	changeMu sync.Mutex

	// enabled is the [features] orchestrate_keyword gate. Off means the
	// detector never fires; explicit /orchestrate on still arms.
	enabled bool

	// armed means the contract is in context (or queued for the next turn).
	armed bool

	// pending injects the contract at the next PostTurn. Arming outside a
	// turn must not append to the conversation from another goroutine, so
	// the in-turn hook does the append and continues the loop.
	pending bool

	// scanned is how many conversation messages have been checked for the
	// keyword, so each message is considered once.
	scanned int

	// onChange runs after armed changes, outside the hook lock. Runtimes use it
	// to gate mode-specific capabilities without polling between turns.
	onChange func(bool)
}

// NewOrchestrateHook builds the hook. Enabled defaults from config
// `features.orchestrate_keyword`.
func NewOrchestrateHook(enabled bool) *OrchestrateHook {
	return &OrchestrateHook{enabled: enabled}
}

// notifyStateChange serializes callbacks and reads the current state at
// delivery time, so a concurrent disarm cannot be followed by a stale "on".
func (h *OrchestrateHook) notifyStateChange() {
	h.changeMu.Lock()
	defer h.changeMu.Unlock()
	h.mu.Lock()
	armed, fn := h.armed, h.onChange
	h.mu.Unlock()
	if fn != nil {
		fn(armed)
	}
}

// SetOnChange registers a runtime callback and immediately synchronizes it with
// the current armed state. The callback always runs outside the hook lock.
func (h *OrchestrateHook) SetOnChange(fn func(bool)) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.onChange = fn
	h.mu.Unlock()
	h.notifyStateChange()
}

// Active reports whether orchestrator mode is armed.
func (h *OrchestrateHook) Active() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.armed
}

// SetEnabled toggles the keyword detector (`/orchestrate` never touches
// this; the gate is config-only).
func (h *OrchestrateHook) SetEnabled(on bool) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.enabled = on
}

// Arm turns orchestrator mode on explicitly (`/orchestrate on`). The
// contract lands at the next turn boundary, like a keyword trigger.
func (h *OrchestrateHook) Arm() {
	if h == nil {
		return
	}
	h.mu.Lock()
	changed := !h.armed
	if changed {
		h.armed = true
		h.pending = true
	}
	h.mu.Unlock()
	if changed {
		h.notifyStateChange()
	}
}

// Disarm turns orchestrator mode off (`/orchestrate off`). Already-scanned
// messages are not reconsidered, so only a new keyword message re-arms.
func (h *OrchestrateHook) Disarm() {
	if h == nil {
		return
	}
	h.mu.Lock()
	changed := h.armed
	h.armed = false
	h.pending = false
	h.mu.Unlock()
	if changed {
		h.notifyStateChange()
	}
}

// PostTurn implements Hooks.
func (h *OrchestrateHook) PostTurn(_ context.Context, a *Agent) (bool, error) {
	if h == nil {
		return false, nil
	}
	h.mu.Lock()
	msgs := a.Conv.Messages()
	if h.scanned > len(msgs) {
		// The conversation shrank under the hook (compact, rewind): rescan
		// from the start rather than slicing past the end.
		h.scanned = 0
	}
	changed := false
	if h.enabled && !h.armed {
		for _, m := range msgs[h.scanned:] {
			if m.Role == provider.RoleUser && HasOrchestrateKeyword(m.Content) {
				h.armed = true
				h.pending = true
				changed = true
				break
			}
		}
	}
	h.scanned = len(msgs)

	appendContract := h.armed && h.pending
	if appendContract {
		h.pending = false
	}
	if appendContract {
		a.Conv.Append(provider.Message{Role: provider.RoleSystem, Content: OrchestrateContract})
	}
	h.mu.Unlock()

	if changed {
		h.notifyStateChange()
	}
	if appendContract {
		return true, nil
	}
	return false, nil
}
