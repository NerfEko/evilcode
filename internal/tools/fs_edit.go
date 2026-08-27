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
	// 0. A trailing newline on `old` that the file lacks is the sole mismatch
	// when the block matches but sits at EOF without a terminating newline.
	// Detect it before the trimmed branch, which would otherwise call it
	// "trimming" and send the model chasing whitespace.
	if line, ok := missingTrailingNewline(content, old); ok {
		return fmt.Sprintf(
			"the file is missing the trailing newline your old string includes "+
				"(the block matches at line %d)", line), true
	}

	// 1. Trimmed match — the model included leading/trailing whitespace the
	// file does not (or vice versa).
	trimmed := strings.TrimSpace(old)
	if trimmed != "" && trimmed != old && strings.Contains(content, trimmed) {
		return fmt.Sprintf("found after trimming leading/trailing whitespace at line %d",
			lineOf(content, trimmed)), true
	}

	// 2. Line-by-line with whitespace normalized — the indentation differs.
	// A trailing newline on `old` would add a synthetic empty final element,
	// inflating the window so the block's last line is compared against the
	// line after it and the match fails; drop it.
	oldLines := strings.Split(old, "\n")
	if strings.HasSuffix(old, "\n") && len(oldLines) > 0 && oldLines[len(oldLines)-1] == "" {
		oldLines = oldLines[:len(oldLines)-1]
	}
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
			// `old` ending in a newline requires one after the block in the
			// file too. If the match is at EOF without a terminating newline,
			// the missing newline — not the indentation — is the real
			// difference, so say so rather than sending the model chasing
			// whitespace.
			if strings.HasSuffix(old, "\n") {
				afterWindow := i + len(oldLines)
				if afterWindow >= len(contentLines) && !strings.HasSuffix(content, "\n") {
					return fmt.Sprintf(
						"found with different indentation around line %d, "+
							"but the file is missing the trailing newline your old string includes",
						i+1), true
				}
			}
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

// missingTrailingNewline reports whether `old` ends in a newline that the file
// lacks: the block old describes (minus its trailing newline) is the last thing
// in content, at a line boundary, with no newline after it. That is the sole
// mismatch — the model's old string is otherwise exact — so the diagnosis names
// the missing newline rather than trimming or indentation.
func missingTrailingNewline(content, old string) (int, bool) {
	if !strings.HasSuffix(old, "\n") {
		return 0, false
	}
	core := old[:len(old)-1]
	if core == "" || !strings.HasSuffix(content, core) {
		return 0, false
	}
	// core must start at a line boundary (or be the whole file), so it is a
	// full trailing block rather than the tail of a longer line.
	if len(content) > len(core) && content[len(content)-len(core)-1] != '\n' {
		return 0, false
	}
	// The match is the suffix, so its line is the count of newlines before
	// that offset — not lineOf, which would report an earlier occurrence.
	idx := len(content) - len(core)
	return strings.Count(content[:idx], "\n") + 1, true
}

// contextAround returns up to `padding` lines either side of the 1-based
// inclusive [startLine, endLine] region in content, numbered the way `read`
// numbers them and truncated the way `read` truncates them. The terminal
// newline is trimmed before splitting, matching `read`, so a newline-terminated
// file does not print a phantom final line. Mirrors jcode's `extract_context`
// (edit.rs:234-254) with padding 3.
func contextAround(content string, startLine, endLine, padding int) string {
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	lo := max(0, startLine-1-padding)
	hi := min(len(lines), endLine+padding)
	var b strings.Builder
	for i := lo; i < hi; i++ {
		line, _ := truncateLine(lines[i])
		fmt.Fprintf(&b, "%d\t%s\n", i+1, line)
	}
	return b.String()
}

// firstChangedLine is the 1-based number of the first line that differs between
// before and after, used to centre context for anchored edits (which have no
// single old/new string to locate). A pure append or truncate returns the first
// line beyond the common prefix.
func firstChangedLine(before, after string) int {
	bl := strings.Split(strings.TrimSuffix(before, "\n"), "\n")
	al := strings.Split(strings.TrimSuffix(after, "\n"), "\n")
	n := min(len(bl), len(al))
	for i := 0; i < n; i++ {
		if bl[i] != al[i] {
			return i + 1
		}
	}
	return n + 1
}
