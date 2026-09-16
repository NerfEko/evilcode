package tui

// applyPickerSelection drives the model picker the way a user confirms a
// choice: Enter, and — when the selection opens the reasoning menu (the
// provider advertises effort levels) — a second Enter to accept the
// highlighted level. Tests that assert the direct-apply behavior for
// level-less models keep calling handlePickerKey directly.
func applyPickerSelection(m *Model) {
	_, _ = m.handlePickerKey("enter")
	if m.reasoningPickerOpen {
		_, _ = m.handleReasoningPickerKey("enter")
	}
}
