package testdata

// clamp is a probe fixture: the mock provider's `diff` scenario edits this
// file, so the inline diff renderer has real code to highlight and tint, and
// the swarm scenario's worker edits it out from under a session that read it.
//
// It is committed in the state BEFORE that edit. Committing the edited version
// makes every scenario's edit fail with "old string not found", and the
// goldens then bake in the failure — which is exactly what happened once.
func clamp(offset, max int) int {
	if offset > max {
		return max
	}
	return offset
}
