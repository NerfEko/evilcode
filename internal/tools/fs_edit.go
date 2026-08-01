package tools

import (
	"fmt"
	"strings"
)

// flexibleMatch explains a failed exact match by trying looser forms, so the
// model gets an actionable error ("found at line N with different indentation")
// rather than a bare "not found" it can only answer by re-reading the whole
// file. Mirrors jcode's `try_flexible_match` (edit.rs:256-290): try the trimmed
// string, then a line-by-line comparison with whitespace normalized. Unlike
// jcode it also reports the line for the trimmed case.
//
// Returns a human-readable clause ("found … at line N") and ok=true when a
// looser form matched; the caller folds it into the not-found error.
func flexibleMatch(content, old string) (msg string, ok bool) {
	// 1. Trimmed match — the model included leading/trailing whitespace the
	// file does not (or vice versa).
	trimmed := strings.TrimSpace(old)
	if trimmed != "" && trimmed != old && strings.Contains(content, trimmed) {
		return fmt.Sprintf("found after trimming leading/trailing whitespace at line %d",
			lineOf(content, trimmed)), true
	}

	// 2. Line-by-line with whitespace normalized — the indentation differs.
	oldLines := strings.Split(old, "\n")
	contentLines := strings.Split(content, "\n")
	if len(oldLines) == 0 || len(oldLines) > len(contentLines) {
		return "", false
	}
	for i := 0; i+len(oldLines) <= len(contentLines); i++ {
		match := true
		for j := range oldLines {
			if strings.TrimSpace(contentLines[i+j]) != strings.TrimSpace(oldLines[j]) {
				match = false
				break
			}
		}
		if match {
			return fmt.Sprintf("found with different indentation around line %d", i+1), true
		}
	}
	return "", false
}

// lineOf is the 1-based line number of the first occurrence of sub in content.
func lineOf(content, sub string) int {
	idx := strings.Index(content, sub)
	if idx < 0 {
		return 1
	}
	return strings.Count(content[:idx], "\n") + 1
}

// editContext returns the lines padding either side of a replacement in `after`,
// numbered the way `read` numbers them, so a consecutive edit to the same region
// needs no re-read. firstIdx is the byte offset of the replacement in `after`
// (the prefix before a Replace is identical to the original, so the offset the
// match was found at in `before` is valid here). Mirrors jcode's
// `extract_context` (edit.rs:234-254) with padding 3.
func editContext(after, replaced string, firstIdx, padding int) string {
	if firstIdx < 0 || firstIdx > len(after) {
		firstIdx = 0
	}
	end := firstIdx + len(replaced)
	if end > len(after) {
		end = len(after)
	}
	lines := strings.Split(after, "\n")
	startLine := strings.Count(after[:firstIdx], "\n") // 0-based
	endLine := strings.Count(after[:end], "\n")
	lo := max(0, startLine-padding)
	hi := min(len(lines), endLine+1+padding)
	var b strings.Builder
	for i := lo; i < hi; i++ {
		fmt.Fprintf(&b, "%d\t%s\n", i+1, lines[i])
	}
	return b.String()
}