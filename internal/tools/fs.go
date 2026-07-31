package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aymanbagabas/go-udiff"
)

// FS holds the filesystem tools' shared settings.
type FS struct {
	// Root confines every path. A tool must not be able to read /etc/shadow
	// because a model asked nicely.
	Root string

	// MaxReadBytes caps a single file read before truncation.
	MaxReadBytes int
}

// NewFS builds the filesystem tool group rooted at dir.
func NewFS(root string) *FS {
	if root == "" {
		root, _ = os.Getwd()
	}
	abs, err := filepath.Abs(root)
	if err == nil {
		root = abs
	}
	return &FS{Root: root, MaxReadBytes: MaxResultBytes}
}

// resolve turns a tool-supplied path into an absolute path inside Root, or
// refuses. Symlinks are resolved before the check so a link cannot be used to
// step outside the workspace.
func (f *FS) resolve(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	full := path
	if !filepath.IsAbs(full) {
		full = filepath.Join(f.Root, full)
	}
	full = filepath.Clean(full)

	// EvalSymlinks fails for a file that does not exist yet (write creates
	// them), so fall back to checking the nearest existing ancestor.
	checked := full
	if resolved, err := filepath.EvalSymlinks(full); err == nil {
		checked = resolved
	} else {
		dir := filepath.Dir(full)
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			checked = filepath.Join(resolved, filepath.Base(full))
		}
	}

	root := f.Root
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	rel, err := filepath.Rel(root, checked)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside the workspace", path)
	}
	return full, nil
}

// rel renders a path relative to Root for display, so tool rows read
// `internal/tui/app.go` rather than an absolute path.
func (f *FS) rel(full string) string {
	if r, err := filepath.Rel(f.Root, full); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return full
}

// Tools returns the filesystem tool set.
func (f *FS) Tools() Set {
	return Set{f.readTool(), f.writeTool(), f.editTool(), f.globTool()}
}

type readArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

func (f *FS) readTool() Tool {
	return Tool{
		Name: "read",
		Desc: "Read a file from the workspace. Returns the contents with line numbers. " +
			"Use offset and limit to page through a large file.",
		Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path":   {"type": "string", "description": "File path, relative to the workspace root"},
    "offset": {"type": "integer", "description": "First line to return, 1-based"},
    "limit":  {"type": "integer", "description": "Maximum number of lines to return"}
  },
  "required": ["path"]
}`),
		Run: func(ctx context.Context, raw json.RawMessage) (Result, error) {
			var a readArgs
			if err := unmarshalArgs(raw, &a); err != nil {
				return Result{}, err
			}
			full, err := f.resolve(a.Path)
			if err != nil {
				return Result{}, err
			}
			info, err := os.Stat(full)
			if err != nil {
				return Result{}, err
			}
			if info.IsDir() {
				return Result{}, fmt.Errorf("%s is a directory; use glob to list it", a.Path)
			}
			data, err := os.ReadFile(full)
			if err != nil {
				return Result{}, err
			}
			if isBinary(data) {
				return Result{}, fmt.Errorf("%s looks like a binary file (%d bytes)", a.Path, len(data))
			}

			lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
			start := 0
			if a.Offset > 0 {
				start = a.Offset - 1
			}
			if start > len(lines) {
				start = len(lines)
			}
			end := len(lines)
			if a.Limit > 0 && start+a.Limit < end {
				end = start + a.Limit
			}

			var b strings.Builder
			for i := start; i < end; i++ {
				fmt.Fprintf(&b, "%d\t%s\n", i+1, lines[i])
			}
			if end < len(lines) {
				fmt.Fprintf(&b, "\n[%d more lines; re-read with offset=%d]\n", len(lines)-end, end+1)
			}
			return Result{
				Output: b.String(),
				Intent: fmt.Sprintf("reading %s", f.rel(full)),
			}, nil
		},
	}
}

type writeArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (f *FS) writeTool() Tool {
	return Tool{
		Name: "write",
		Desc: "Write a file, creating it or replacing its entire contents. " +
			"To change part of an existing file, prefer edit.",
		Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path":    {"type": "string", "description": "File path, relative to the workspace root"},
    "content": {"type": "string", "description": "Full file contents"}
  },
  "required": ["path", "content"]
}`),
		Run: func(ctx context.Context, raw json.RawMessage) (Result, error) {
			var a writeArgs
			if err := unmarshalArgs(raw, &a); err != nil {
				return Result{}, err
			}
			full, err := f.resolve(a.Path)
			if err != nil {
				return Result{}, err
			}

			before := ""
			if old, err := os.ReadFile(full); err == nil {
				before = string(old)
			}
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return Result{}, err
			}
			if err := os.WriteFile(full, []byte(a.Content), 0o644); err != nil {
				return Result{}, err
			}

			name := f.rel(full)
			diff, stat := makeDiff(name, before, a.Content)
			verb := "wrote"
			if before == "" {
				verb = "created"
			}
			return Result{
				Output:   fmt.Sprintf("%s %s (+%d -%d)", verb, name, stat.Added, stat.Removed),
				Diff:     diff,
				DiffStat: &stat,
				Intent:   fmt.Sprintf("%s %s", verb, name),
			}, nil
		},
	}
}

type editArgs struct {
	Path string `json:"path"`
	Old  string `json:"old"`
	New  string `json:"new"`
	All  bool   `json:"all,omitempty"`
}

func (f *FS) editTool() Tool {
	return Tool{
		Name: "edit",
		Desc: "Replace an exact string in a file. The old string must appear exactly once " +
			"unless all is true. Include enough surrounding context to make it unique.",
		Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "File path, relative to the workspace root"},
    "old":  {"type": "string", "description": "Exact text to replace, including indentation"},
    "new":  {"type": "string", "description": "Replacement text"},
    "all":  {"type": "boolean", "description": "Replace every occurrence instead of requiring exactly one"}
  },
  "required": ["path", "old", "new"]
}`),
		Run: func(ctx context.Context, raw json.RawMessage) (Result, error) {
			var a editArgs
			if err := unmarshalArgs(raw, &a); err != nil {
				return Result{}, err
			}
			if a.Old == a.New {
				return Result{}, fmt.Errorf("old and new are identical; nothing to do")
			}
			full, err := f.resolve(a.Path)
			if err != nil {
				return Result{}, err
			}
			data, err := os.ReadFile(full)
			if err != nil {
				return Result{}, err
			}
			before := string(data)

			count := strings.Count(before, a.Old)
			switch {
			case count == 0:
				return Result{}, fmt.Errorf(
					"old string not found in %s. Re-read the file — it may have changed, "+
						"or the indentation may differ from what you expected", a.Path)
			case count > 1 && !a.All:
				return Result{}, fmt.Errorf(
					"old string appears %d times in %s; include more surrounding context "+
						"to make it unique, or set all=true to replace every occurrence", count, a.Path)
			}

			after := before
			if a.All {
				after = strings.ReplaceAll(before, a.Old, a.New)
			} else {
				after = strings.Replace(before, a.Old, a.New, 1)
			}
			if err := os.WriteFile(full, []byte(after), 0o644); err != nil {
				return Result{}, err
			}

			name := f.rel(full)
			diff, stat := makeDiff(name, before, after)
			return Result{
				Output:   fmt.Sprintf("edited %s (+%d -%d)", name, stat.Added, stat.Removed),
				Diff:     diff,
				DiffStat: &stat,
				Intent:   fmt.Sprintf("editing %s", name),
			}, nil
		},
	}
}

type globArgs struct {
	Pattern string `json:"pattern"`
	Limit   int    `json:"limit,omitempty"`
}

// globIgnored are directories never worth walking. Skipping them at the
// directory level (rather than filtering results) is what keeps glob fast in a
// repo with a populated node_modules.
var globIgnored = map[string]bool{
	".git": true, "node_modules": true, "vendor": true,
	".venv": true, "__pycache__": true, "target": true, "dist": true,
}

func (f *FS) globTool() Tool {
	return Tool{
		Name: "glob",
		Desc: "Find files by glob pattern, e.g. '**/*.go' or 'internal/**/*_test.go'. " +
			"Returns paths relative to the workspace root.",
		Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "pattern": {"type": "string", "description": "Glob pattern; ** matches across directories"},
    "limit":   {"type": "integer", "description": "Maximum paths to return"}
  },
  "required": ["pattern"]
}`),
		Run: func(ctx context.Context, raw json.RawMessage) (Result, error) {
			var a globArgs
			if err := unmarshalArgs(raw, &a); err != nil {
				return Result{}, err
			}
			if a.Pattern == "" {
				return Result{}, fmt.Errorf("pattern is required")
			}
			limit := a.Limit
			if limit <= 0 {
				limit = 500
			}

			var matches []string
			err := filepath.WalkDir(f.Root, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return nil // an unreadable directory is not fatal to the search
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if d.IsDir() {
					if globIgnored[d.Name()] {
						return filepath.SkipDir
					}
					return nil
				}
				rel, err := filepath.Rel(f.Root, path)
				if err != nil {
					return nil
				}
				if matchGlob(a.Pattern, rel) {
					matches = append(matches, rel)
				}
				return nil
			})
			if err != nil {
				return Result{}, err
			}

			sort.Strings(matches)
			note := ""
			if len(matches) > limit {
				note = fmt.Sprintf("\n[%d more matches; narrow the pattern]", len(matches)-limit)
				matches = matches[:limit]
			}
			if len(matches) == 0 {
				return Result{Output: "no files matched " + a.Pattern}, nil
			}
			return Result{
				Output: strings.Join(matches, "\n") + note,
				Intent: fmt.Sprintf("%d files matching %s", len(matches), a.Pattern),
			}, nil
		},
	}
}

// matchGlob matches a pattern against a slash-separated path, supporting `**`
// as "any number of path segments". path/filepath.Match alone cannot cross
// separators, which is the one thing every caller expects `**` to do.
func matchGlob(pattern, path string) bool {
	pattern = filepath.ToSlash(pattern)
	path = filepath.ToSlash(path)

	// A pattern with no separator matches on the basename, which is what
	// people mean by `*.go`.
	if !strings.Contains(pattern, "/") {
		ok, _ := filepath.Match(pattern, filepath.Base(path))
		return ok
	}
	return matchSegments(strings.Split(pattern, "/"), strings.Split(path, "/"))
}

func matchSegments(pat, seg []string) bool {
	switch {
	case len(pat) == 0:
		return len(seg) == 0
	case pat[0] == "**":
		// `**` may consume zero or more segments; try each split.
		for i := 0; i <= len(seg); i++ {
			if matchSegments(pat[1:], seg[i:]) {
				return true
			}
		}
		return false
	case len(seg) == 0:
		return false
	default:
		if ok, _ := filepath.Match(pat[0], seg[0]); !ok {
			return false
		}
		return matchSegments(pat[1:], seg[1:])
	}
}

// makeDiff renders a unified diff and counts the changed lines. go-udiff is
// already in the module graph (via the Charm stack), so this costs no new
// dependency — plan.md §1.5's rule is against adding one.
func makeDiff(name, before, after string) (string, DiffStat) {
	diff := udiff.Unified("a/"+name, "b/"+name, before, after)
	var stat DiffStat
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
			// File headers are not content changes.
		case strings.HasPrefix(line, "+"):
			stat.Added++
		case strings.HasPrefix(line, "-"):
			stat.Removed++
		}
	}
	return diff, stat
}

// isBinary reports whether data looks like something a model should not be
// shown. A NUL byte in the first chunk is the classic, cheap signal.
func isBinary(data []byte) bool {
	n := min(len(data), 8000)
	for i := 0; i < n; i++ {
		if data[i] == 0 {
			return true
		}
	}
	return false
}
