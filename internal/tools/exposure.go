package tools

import (
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// LineRange identifies source lines that have already been put in the model's
// context. End is inclusive, matching the one-based line numbers tools print.
type LineRange struct {
	Path  string
	Start int
	End   int
}

// Exposure is the per-session ledger of source ranges shown to the model. It is
// shared by read and grep (and by bash's parsed diagnostics) so a later search
// can replace repeated text with a cheap reference.
type Exposure struct {
	mu     sync.RWMutex
	ranges map[string][]LineRange
}

// NewExposure creates an empty session ledger.
func NewExposure() *Exposure { return &Exposure{ranges: make(map[string][]LineRange)} }

// Record adds ranges to the ledger, merging overlaps and adjacent lines.
func (e *Exposure) Record(ranges []LineRange) {
	if e == nil || len(ranges) == 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ranges == nil {
		e.ranges = make(map[string][]LineRange)
	}
	for _, span := range ranges {
		path := exposurePath(span.Path)
		if path == "" || span.Start < 1 || span.End < span.Start {
			continue
		}
		span.Path = path
		e.ranges[path] = append(e.ranges[path], span)
		e.ranges[path] = mergeExposureRanges(e.ranges[path])
	}
}

// Contains reports whether a line has already been shown.
func (e *Exposure) Contains(path string, line int) bool {
	if e == nil || line < 1 {
		return false
	}
	path = exposurePath(path)
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, span := range e.ranges[path] {
		if line < span.Start {
			return false
		}
		if line <= span.End {
			return true
		}
	}
	return false
}

// Reset starts a fresh exposure epoch. Compaction calls this because the old
// transcript is no longer in the model's context.
func (e *Exposure) Reset() {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.ranges = make(map[string][]LineRange)
	e.mu.Unlock()
}

// Snapshot returns a copy for diagnostics and tests.
func (e *Exposure) Snapshot() []LineRange {
	if e == nil {
		return nil
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	var out []LineRange
	for _, spans := range e.ranges {
		out = append(out, spans...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Start < out[j].Start
	})
	return out
}

func exposurePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return filepath.Clean(path)
}

func mergeExposureRanges(spans []LineRange) []LineRange {
	sort.Slice(spans, func(i, j int) bool { return spans[i].Start < spans[j].Start })
	out := spans[:0]
	for _, span := range spans {
		if len(out) == 0 || span.Start > out[len(out)-1].End+1 {
			out = append(out, span)
			continue
		}
		if span.End > out[len(out)-1].End {
			out[len(out)-1].End = span.End
		}
	}
	return out
}

// fileLinePattern recognizes the path:line (or path:start-end) forms emitted
// by compilers, test runners, and common command-line tools. It intentionally
// requires a path separator or an extension so timestamps and ordinary numbers
// in bash output do not become phantom source exposure.
var fileLinePattern = regexp.MustCompile(`(?:^|[\t \"'\(\[])((?:\./|\.\./|/)?[^\s:()\[\]]+):([1-9][0-9]*)(?:-([1-9][0-9]*))?(?::[0-9]+)?(?:$|[^0-9])`)

// RangesFromBash extracts source ranges from command output. Relative paths are
// resolved against the shell directory that produced the output.
func RangesFromBash(output, root string) []LineRange {
	if strings.TrimSpace(output) == "" {
		return nil
	}
	var out []LineRange
	for _, line := range strings.Split(output, "\n") {
		for _, match := range fileLinePattern.FindAllStringSubmatch(line, -1) {
			path := match[1]
			if !strings.ContainsAny(path, `/\\.`) {
				continue
			}
			if !filepath.IsAbs(path) {
				path = filepath.Join(root, path)
			}
			start, err := strconv.Atoi(match[2])
			if err != nil || start < 1 {
				continue
			}
			end := start
			if match[3] != "" {
				if end, err = strconv.Atoi(match[3]); err != nil || end < start {
					continue
				}
			}
			out = append(out, LineRange{Path: path, Start: start, End: end})
		}
	}
	return out
}
