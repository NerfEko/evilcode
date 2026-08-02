package tools

import (
	"fmt"
	"hash/fnv"
	"os"
	"strings"
	"sync"
	"time"
)

// AnchorLen is how many hex characters a line anchor carries. Four is enough to
// make collisions rare within one file while staying cheap to retype — and the
// model does not retype it anyway, which is the point: it names a line instead
// of reproducing its context (plan.md §17).
const AnchorLen = 4

// LineAnchor is the short content hash shown beside a line by `read`.
func LineAnchor(line string) string {
	h := fnv.New32a()
	// Leading and trailing whitespace is content for an anchor: two lines that
	// differ only in indentation are different lines to an edit.
	h.Write([]byte(line))
	return anchorFromSum(h.Sum32())
}

func anchorFromSum(sum uint32) string {
	return fmt.Sprintf("%0*x", AnchorLen, sum&(1<<(AnchorLen*4)-1))
}

// readState records what a file looked like when it was last read, so an edit
// can tell whether it is acting on what the model actually saw.
type readState struct {
	ModTime time.Time
	Size    int64

	// Anchors maps a line anchor to the 1-based line numbers carrying it.
	// A duplicate anchor is ambiguous and must be refused rather than guessed.
	Anchors map[string][]int
}

// anchorStore tracks read state per file.
type anchorStore struct {
	mu    sync.Mutex
	files map[string]readState
}

func newAnchorStore() *anchorStore {
	return &anchorStore{files: map[string]readState{}}
}

// record captures a file's state at read time.
func (s *anchorStore) record(path string, info os.FileInfo, lines []string) {
	s.recordAt(path, info, lines, 0)
}

// recordAt records a window of lines starting at a zero-based offset.
//
// The offset matters because the model is shown the window numbered from where
// it starts, not from one: recording a paged read as lines 1..N means an anchor
// the model quotes back resolves to a different line entirely.
func (s *anchorStore) recordAt(path string, info os.FileInfo, lines []string, offset int) {
	anchors := make(map[string][]int, len(lines))
	for i, line := range lines {
		a := LineAnchor(line)
		anchors[a] = append(anchors[a], offset+i+1)
	}
	s.recordAtAnchors(path, info, anchors)
}

func (s *anchorStore) recordAtHashes(path string, info os.FileInfo, hashes []string, offset int) {
	anchors := make(map[string][]int, len(hashes))
	for i, hash := range hashes {
		if hash != "" {
			anchors[hash] = append(anchors[hash], offset+i+1)
		}
	}
	s.recordAtAnchors(path, info, anchors)
}

func (s *anchorStore) recordAtAnchors(path string, info os.FileInfo, anchors map[string][]int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.files[path] = readState{ModTime: info.ModTime(), Size: info.Size(), Anchors: anchors}
}

// lookup returns the recorded state for a file.
func (s *anchorStore) lookup(path string) (readState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.files[path]
	return st, ok
}

// forget drops a file's state, which a write does: the model has not seen the
// new contents, so its old anchors are meaningless.
func (s *anchorStore) forget(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.files, path)
}

// AnchorOp is what an anchor patch does at its line.
type AnchorOp string

const (
	OpReplace     AnchorOp = "replace"
	OpInsertAfter AnchorOp = "insert_after"
	OpDelete      AnchorOp = "delete"
)

// AnchorPatch is one anchored change.
type AnchorPatch struct {
	Anchor string   `json:"anchor"`
	Op     AnchorOp `json:"op"`
	Lines  []string `json:"lines,omitempty"`
}

// ErrStaleAnchor is returned when the file changed since it was read.
//
// This must be loud and must refuse, not fuzzily match: silently applying a
// best-effort patch to a file that moved underneath is how anchored editing
// corrupts a file instead of saving a retry (plan.md Part V).
type ErrStaleAnchor struct {
	Path   string
	Reason string
}

func (e *ErrStaleAnchor) Error() string {
	return fmt.Sprintf("%s: %s. Re-read the file to get current anchors before editing it.",
		e.Path, e.Reason)
}

// ApplyAnchors applies anchored patches to a file's lines.
//
// Every patch is resolved against the state captured at read time, and all
// resolution happens before any mutation, so a patch set that is partly invalid
// changes nothing at all.
func ApplyAnchors(path string, lines []string, patches []AnchorPatch, st readState) ([]string, error) {
	if len(patches) == 0 {
		return nil, fmt.Errorf("no patches supplied")
	}

	type resolved struct {
		line  int // 1-based
		patch AnchorPatch
	}
	var plan []resolved
	seen := map[int]bool{}

	for _, p := range patches {
		raw := strings.TrimSpace(p.Anchor)
		anchor := strings.ToLower(raw)

		// A model that passes the line's *text* instead of its anchor code gets
		// a specific error rather than a confusing "not found". Echoing the
		// lowercased form here also made it look as though the tool had mangled
		// the input, which sent one model chasing a case-sensitivity bug that
		// did not exist.
		if !looksLikeAnchor(anchor) {
			return nil, fmt.Errorf(
				"%q is not an anchor. An anchor is the %d-character code read prints "+
					"before each line, as in `a3f2|417| func main() {` — pass %q, not the "+
					"line's text",
				truncateForError(raw), AnchorLen, "a3f2")
		}

		nums, ok := st.Anchors[anchor]
		if !ok {
			return nil, &ErrStaleAnchor{Path: path,
				Reason: fmt.Sprintf("anchor %q is not in the version you read", anchor)}
		}
		if len(nums) > 1 {
			return nil, fmt.Errorf(
				"anchor %q matches %d identical lines (%v) in %s; anchored edits need a unique line, "+
					"so use the exact-string form with surrounding context instead",
				anchor, len(nums), nums, path)
		}
		n := nums[0]
		if n < 1 || n > len(lines) {
			return nil, &ErrStaleAnchor{Path: path,
				Reason: fmt.Sprintf("anchor %q points past the end of the file", anchor)}
		}
		// The recorded anchor must still describe the line actually there.
		if got := LineAnchor(lines[n-1]); got != anchor {
			return nil, &ErrStaleAnchor{Path: path,
				Reason: fmt.Sprintf("line %d no longer matches anchor %q", n, anchor)}
		}
		if seen[n] {
			return nil, fmt.Errorf("two patches target line %d of %s", n, path)
		}
		seen[n] = true

		switch p.Op {
		case OpReplace, OpInsertAfter, OpDelete:
		case "":
			return nil, fmt.Errorf("patch for anchor %q has no op", anchor)
		default:
			return nil, fmt.Errorf("unknown op %q (want replace, insert_after, or delete)", p.Op)
		}
		if p.Op != OpDelete && len(p.Lines) == 0 {
			return nil, fmt.Errorf("op %q for anchor %q needs lines", p.Op, anchor)
		}
		plan = append(plan, resolved{line: n, patch: p})
	}

	// Rebuild in one pass so line numbers stay stable regardless of patch order.
	patchAt := make(map[int]AnchorPatch, len(plan))
	for _, r := range plan {
		patchAt[r.line] = r.patch
	}

	out := make([]string, 0, len(lines)+len(plan))
	for i, line := range lines {
		p, ok := patchAt[i+1]
		if !ok {
			out = append(out, line)
			continue
		}
		switch p.Op {
		case OpReplace:
			out = append(out, p.Lines...)
		case OpInsertAfter:
			out = append(out, line)
			out = append(out, p.Lines...)
		case OpDelete:
			// drop it
		}
	}
	return out, nil
}

// looksLikeAnchor reports whether a string is plausibly an anchor code.
func looksLikeAnchor(s string) bool {
	if len(s) != AnchorLen {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// truncateForError keeps an error message readable when a model passes a whole
// line where an anchor belongs.
func truncateForError(s string) string {
	if len(s) <= 40 {
		return s
	}
	return s[:39] + "…"
}

// AnnotateLines renders `read` output with anchors:
//
//	a3f2|417| func main() {
//
// The anchor comes first so the columns line up regardless of line-number
// width, which matters once a file passes a thousand lines.
// AnnotateLines renders `read` output with anchors. The anchor is hashed from
// the original line (so an edit quoting it validates against the version read),
// while the display text is truncated — a long line keeps its real anchor and a
// cut display, not a hash of the truncated text the edit path would reject.
func AnnotateLines(lines []string, start int) string {
	hashes := make([]string, len(lines))
	for i, line := range lines {
		hashes[i] = LineAnchor(line)
	}
	return annotateLinesWithHashes(lines, hashes, start)
}

// AnnotateLinesWithHashes renders display lines with anchors computed from the
// original content. The paged reader uses this for a line too large to retain in
// memory: its display is bounded, but its streamed hash still validates an
// anchored edit against the real file.
func AnnotateLinesWithHashes(lines, hashes []string, start int) string {
	return annotateLinesWithHashes(lines, hashes, start)
}

func annotateLinesWithHashes(lines, hashes []string, start int) string {
	width := len(fmt.Sprint(start + len(lines)))
	var b strings.Builder
	for i, line := range lines {
		disp, _ := truncateLine(line)
		anchor := LineAnchor(line)
		if i < len(hashes) && hashes[i] != "" {
			anchor = hashes[i]
		}
		fmt.Fprintf(&b, "%s|%*d| %s\n", anchor, width, start+i, disp)
	}
	return b.String()
}
