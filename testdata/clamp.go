package testdata

// clamp is a probe fixture: the mock provider's `diff` scenario edits this
// file, so the inline diff renderer has real code to highlight and tint.
func clamp(offset, max int) int {
	if offset > max {
		return max
	}
	return offset
}
