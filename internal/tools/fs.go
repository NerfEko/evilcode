package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/aymanbagabas/go-udiff"
)

// FS holds the filesystem tools' shared settings.
type FS struct {
	// Root is the workspace: where relative paths resolve and what tool rows
	// display paths relative to.
	Root string

	// Confine restricts every path to Root. It is OFF by default — this is a
	// single-user tool on the user's own machine, and refusing to read a file
	// next door is friction rather than protection there. Turn it on for a
	// session you want kept inside one tree (`[features] confine_to_workspace`).
	Confine bool

	// MaxReadBytes caps a single file read before truncation.
	MaxReadBytes int

	// Anchors enables hash-anchored reads and edits (plan.md §17). Some models
	// handle anchors well and some do not, so it is per-model configuration
	// rather than a global switch.
	Anchors bool

	// Vision reports whether the active model accepts image attachments. `read`
	// on an image attaches bytes for the vision path only when this is true, so
	// a text-only backend is told the picture's dimensions and that it cannot
	// see it, rather than being handed bytes its provider will reject. Mirrors
	// the user-attachment guard in the TUI.
	Vision bool

	// VisionFn overrides Vision when set, so a session that switches models
	// mid-run (the TUI's /model picker) re-evaluates the capability against the
	// new model rather than the one it started with. Headless paths leave it
	// nil and use the static Vision set at construction.
	VisionFn func() bool

	anchors *anchorStore

	// paths serializes read-modify-write on one file. A batch runs eight-way
	// concurrent, and two edits to the same file would otherwise both compute
	// their replacement against the same original and the second would erase
	// the first.
	pathMu sync.Mutex
	paths  map[string]*sync.Mutex
}

// lockPath serializes changes to one file, returning the unlock.
//
// The key is the symlink-resolved path, because the lock is on the *file*: two
// calls spelling one file two ways — through a link, or as a link and its
// target — would otherwise take two different locks and race each other.
//
// ponytail: the map is never pruned. It holds one mutex per file edited in a
// session, which is bounded by how much work one session does; a sweep is worth
// adding only if that stops being true.
func (f *FS) lockPath(full string) func() {
	full = resolveExisting(full)
	f.pathMu.Lock()
	if f.paths == nil {
		f.paths = map[string]*sync.Mutex{}
	}
	mu, ok := f.paths[full]
	if !ok {
		mu = &sync.Mutex{}
		f.paths[full] = mu
	}
	f.pathMu.Unlock()

	mu.Lock()
	return mu.Unlock
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
	return &FS{Root: root, MaxReadBytes: MaxResultBytes, anchors: newAnchorStore()}
}

// WithAnchors turns hash-anchored editing on.
func (f *FS) WithAnchors(on bool) *FS {
	f.Anchors = on
	return f
}

// WithConfine restricts paths to the workspace root.
func (f *FS) WithConfine(on bool) *FS {
	f.Confine = on
	return f
}

// WithVision declares whether the active model accepts image attachments, so
// `read` on an image gates the vision path on the same flag the user-attachment
// path uses.
func (f *FS) WithVision(on bool) *FS {
	f.Vision = on
	return f
}

// WithVisionFn installs a dynamic vision-capability lookup, used by the TUI so a
// mid-session model switch re-evaluates the gate. nil clears it (headless paths
// use the static WithVision instead).
func (f *FS) WithVisionFn(fn func() bool) *FS {
	f.VisionFn = fn
	return f
}

// visionOK reports whether the active model accepts images, preferring the
// dynamic lookup when one is installed.
func (f *FS) visionOK() bool {
	if f.VisionFn != nil {
		return f.VisionFn()
	}
	return f.Vision
}

// resolve turns a tool-supplied path into an absolute path.
//
// With Confine on it must land inside Root, and symlinks are resolved before
// the check so a link cannot be used to step outside. With Confine off — the
// default — any readable path is allowed, and this only normalizes.
func (f *FS) resolve(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	full := path
	if !filepath.IsAbs(full) {
		full = filepath.Join(f.Root, full)
	}
	full = filepath.Clean(full)

	if !f.Confine {
		return full, nil
	}

	// Both sides of the comparison must be resolved the same way, or a
	// workspace reachable through a symlink (a home directory bind-mounted at
	// two paths, say) rejects its own files.
	root := f.Root
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	checked := resolveExisting(full)

	rel, err := filepath.Rel(root, checked)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf(
			"path %q is outside the workspace %s; this session is confined to it "+
				"(unset features.confine_to_workspace to allow anything)", path, f.Root)
	}
	return full, nil
}

// resolveExisting resolves symlinks as far up the path as actually exists, then
// re-appends the rest. A file being created does not exist yet, and neither may
// its parent directories, so EvalSymlinks alone cannot answer where a path
// really points — but the deepest existing ancestor can, and that is the part
// an attacker could have pointed elsewhere.
func resolveExisting(full string) string {
	var missing []string
	cur := full
	for {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			parts := append([]string{resolved}, missing...)
			return filepath.Join(parts...)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			// Reached the root without finding anything that exists.
			return full
		}
		missing = append([]string{filepath.Base(cur)}, missing...)
		cur = parent
	}
}

// rel renders a path relative to Root for display, so tool rows read
// `internal/tui/app.go` rather than an absolute path.
func (f *FS) rel(full string) string {
	if r, err := filepath.Rel(f.Root, full); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return full
}

// suggestNear scans the parent directory of a missing path for names that
// contain, or are contained by, the requested one, and names up to three. It is
// one ReadDir on a path that was already an error, so a miss costs almost
// nothing and a typo gets the model pointed at its neighbour instead of a bare
// "no such file". Mirrors jcode's `find_similar_files` (read.rs:307-330).
//
// The scan goes through the confined open when Confine is on, so a symlink
// swapped into the parent after resolve cannot list names outside the workspace.
// A case-only typo (FS.GO when fs.go exists) is still suggested: the skip is on
// the exact original name, not the case-folded one.
func (f *FS) suggestNear(full string) []string {
	parent := filepath.Dir(full)
	base := filepath.Base(full)
	if base == "" {
		return nil
	}
	entries, err := f.readDirConfined(parent)
	if err != nil {
		return nil
	}
	ltarget := strings.ToLower(base)
	var out []string
	for _, e := range entries {
		if e.Name() == base {
			continue
		}
		name := strings.ToLower(e.Name())
		if strings.Contains(name, ltarget) || strings.Contains(ltarget, name) {
			out = append(out, f.rel(filepath.Join(parent, e.Name())))
			if len(out) >= 3 {
				break
			}
		}
	}
	return out
}

// readDirConfined lists a directory through the confined open when Confine is
// on, so a parent swapped for an external symlink after resolve cannot expose
// names outside the workspace. Mirrors openConfined's check shape.
func (f *FS) readDirConfined(parent string) ([]os.DirEntry, error) {
	if !f.Confine {
		return os.ReadDir(parent)
	}
	root := f.Root
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	dir, err := openBeneath(root, resolveExisting(parent), os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	return dir.ReadDir(-1)
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
				if errors.Is(err, fs.ErrNotExist) {
					if s := f.suggestNear(full); len(s) > 0 {
						return Result{}, fmt.Errorf("%s: %w\nDid you mean: %s",
							a.Path, err, strings.Join(s, ", "))
					}
				}
				return Result{}, err
			}
			if info.IsDir() {
				return Result{}, fmt.Errorf("%s is a directory; use glob to list it", a.Path)
			}

			// Images are extension-keyed and attach to the vision path rather
			// than being refused as binary. They have their own size ceiling, so
			// they bypass MaxReadBytes.
			if isImageExt(a.Path) {
				return f.readImage(full, f.rel(full))
			}

			// MaxReadBytes was declared, documented as capping a single read,
			// initialized — and never referenced. A file larger than the cap
			// was loaded whole and split into lines before any truncation
			// applied, so the peak cost of reading a multi-gigabyte file was
			// the file itself, twice.
			cap := f.MaxReadBytes
			if cap <= 0 {
				cap = MaxResultBytes
			}
			if info.Size() > int64(cap) {
				if a.Offset <= 0 && a.Limit <= 0 {
					return Result{}, fmt.Errorf(
						"%s is %s, past the %s single-read limit; read it in pieces "+
							"with offset and limit, or grep it for what you need",
						a.Path, humanBytes(info.Size()), humanBytes(int64(cap)))
				}
				return f.readWindow(full, info, a, cap)
			}

			data, err := f.readConfined(full)
			if err != nil {
				return Result{}, err
			}
			if isBinary(data) {
				return Result{}, fmt.Errorf(
					"%s looks like a binary file (%d bytes); if it is an image, "+
						"give it the right extension (.png, .jpg, .gif, .webp, .bmp) "+
						"and read will attach it to the vision path", a.Path, len(data))
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

			// Record what the model is about to see, so a later anchored edit
			// can tell whether it is acting on this version.
			f.anchors.record(full, info, lines)

			var b strings.Builder
			truncated := 0
			if f.Anchors {
				for i := start; i < end; i++ {
					if len(lines[i]) > MaxLineLen {
						truncated++
					}
				}
				b.WriteString(AnnotateLines(lines[start:end], start+1))
			} else {
				for i := start; i < end; i++ {
					line := lines[i]
					if t, ok := truncateLine(line); ok {
						line = t
						truncated++
					}
					fmt.Fprintf(&b, "%d\t%s\n", i+1, line)
				}
			}
			if truncated > 0 {
				b.WriteString(truncNotice(truncated))
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

// MaxLineLen caps a single output line. A minified bundle line of tens of
// thousands of characters would otherwise consume the whole read budget and
// drown the rest of the file; jcode truncates at 2000 (read.rs:13), and so does
// this. Lines past the cap are cut with a marker, and the count is said once at
// the end rather than per line.
const MaxLineLen = 2000

// truncateLine caps a single line at MaxLineLen bytes, backing up to a UTF-8
// rune boundary so the cut does not split a multibyte character (which would
// leave an invalid string the provider serializes as U+FFFD), and appends a
// marker so the model can see the line was cut. Returns the (possibly
// truncated) line and whether it was capped.
func truncateLine(s string) (string, bool) {
	if len(s) <= MaxLineLen {
		return s, false
	}
	cut := backToRuneBoundary(s, MaxLineLen)
	return s[:cut] + "...", true
}

// truncNotice is the one-line summary appended when any lines were truncated.
func truncNotice(n int) string {
	return fmt.Sprintf("\n[%d line(s) truncated at %d characters]\n", n, MaxLineLen)
}

// readWindow reads one offset/limit window without loading the whole file.
//
// The window is what the model asked for, so a large file stays readable in
// pieces rather than becoming unreadable — refusing outright would make `read`
// useless on exactly the files where paging matters.
func (f *FS) readWindow(full string, info os.FileInfo, a readArgs, cap int) (Result, error) {
	file, err := f.openConfined(full)
	if err != nil {
		return Result{}, err
	}
	defer file.Close()

	start := 0
	if a.Offset > 0 {
		start = a.Offset - 1
	}
	limit := a.Limit
	if limit <= 0 {
		limit = -1
	}

	// The scanner buffer must hold one whole line, even a minified bundle line
	// far larger than the output cap, or the scanner errors "token too long"
	// before truncation can run. 1 MiB covers realistic single-line bundles; a
	// line past that surfaces a scan error rather than hanging.
	sc := bufio.NewScanner(file)
	sc.Buffer(make([]byte, 0, 64*1024), max(cap, 1<<20))

	var lines []string
	size := 0
	n := 0
	truncated := false
	for sc.Scan() {
		if n < start {
			n++
			continue
		}
		if limit >= 0 && len(lines) >= limit {
			truncated = true
			break
		}
		line := sc.Text()
		// Always emit at least the first line of the window, even a single
		// line larger than the cap: truncating it for display keeps the output
		// bounded, and paging advances past it instead of returning
		// "re-read with offset=1" forever.
		if len(lines) > 0 && size+len(line)+1 > cap {
			truncated = true
			break
		}
		lines = append(lines, line)
		size += len(line) + 1
		n++
	}
	if err := sc.Err(); err != nil {
		return Result{}, fmt.Errorf("reading %s: %w", f.rel(full), err)
	}
	// Binary detection looks at the whole window rather than its first line:
	// checking one line means a file whose NULs start further in reads as text.
	if isBinary([]byte(strings.Join(lines, "\n"))) {
		return Result{}, fmt.Errorf("%s looks like a binary file (%s)",
			f.rel(full), humanBytes(info.Size()))
	}

	// Anchors describe what the model saw, and the model sees these lines
	// numbered from the offset — recording them from 1 would make an anchor
	// the model quotes back point at a different line.
	f.anchors.recordAt(full, info, lines, start)

	var b strings.Builder
	truncatedLines := 0
	if f.Anchors {
		for _, line := range lines {
			if len(line) > MaxLineLen {
				truncatedLines++
			}
		}
		b.WriteString(AnnotateLines(lines, start+1))
	} else {
		for i, line := range lines {
			if t, ok := truncateLine(line); ok {
				line = t
				truncatedLines++
			}
			fmt.Fprintf(&b, "%d\t%s\n", start+1+i, line)
		}
	}
	if truncatedLines > 0 {
		b.WriteString(truncNotice(truncatedLines))
	}
	if truncated {
		fmt.Fprintf(&b, "\n[more lines; re-read with offset=%d]\n", start+len(lines)+1)
	}
	return Result{
		Output: b.String(),
		Intent: fmt.Sprintf("reading %s", f.rel(full)),
	}, nil
}

// humanBytes renders a size the way an error message wants to say it.
func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d bytes", n)
	}
}

// mkdirAllConfined creates a file's parent directories, refusing to create
// anything outside the workspace.
//
// MkdirAll follows symlinks, so on a confined session it could otherwise build
// a path outside the root before the bounded write ever ran.
func (f *FS) mkdirAllConfined(full string) error {
	dir := filepath.Dir(full)
	if !f.Confine {
		return os.MkdirAll(dir, 0o755)
	}
	// resolve() is the check the rest of the tools already agree on, including
	// its handling of a workspace reached through a symlink.
	if _, err := f.resolve(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// And verify what was actually created is where it was meant to be:
	// MkdirAll follows symlinks, so the check above describes intent and this
	// describes the result.
	root := f.Root
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	probe, err := openBeneath(root, resolveExisting(dir), os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	return probe.Close()
}

// openConfined opens a file for reading, atomically bounded to the workspace
// when confinement is on.
//
// resolve() has already validated the path, but validating and opening are two
// operations: a directory component swapped for a symlink in between escapes.
// With confinement off there is nothing to bound, and the plain open is the
// whole story.
func (f *FS) openConfined(full string) (*os.File, error) {
	if !f.Confine {
		return os.Open(full)
	}
	root := f.Root
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	return openBeneath(root, resolveExisting(full), os.O_RDONLY, 0)
}

// readConfined reads a whole file through the confined open.
func (f *FS) readConfined(full string) ([]byte, error) {
	file, err := f.openConfined(full)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(file)
}

// writeAtomic replaces a file's contents in one visible step.
//
// `os.WriteFile` truncates the destination and then writes into it, so for the
// length of the write the file on disk is neither the old version nor the new
// one — and a crash, a short write or a full disk in that window leaves the
// truncated remains and nothing else. Writing a same-directory temp file,
// syncing it and renaming means a reader sees one version or the other, and a
// failure leaves the original untouched.
//
// The temp file is in the destination's directory because rename is only
// atomic within a filesystem, and the mode is copied from the destination
// because the replacement is the same file as far as anyone using it is
// concerned.
func (f *FS) writeConfined(full string, data []byte) error {
	if !f.Confine {
		return writeAtomic(full, data)
	}
	// Through a descriptor on the parent directory, so the temp file, the write
	// and the rename all name the directory that was verified rather than
	// re-resolving a pathname that something may have changed underneath.
	root := f.Root
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	return writeAtomicBeneath(root, resolveExisting(full), data)
}

func writeAtomic(path string, data []byte) error {
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	// Sync before the rename: without it the rename can be durable while the
	// contents are not, which is the crash that leaves an empty file behind.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, mode); err != nil {
		return err
	}
	return os.Rename(name, path)
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

			defer f.lockPath(full)()

			before := ""
			if old, err := f.readConfined(full); err == nil {
				before = string(old)
			}
			if err := f.mkdirAllConfined(full); err != nil {
				return Result{}, err
			}
			if err := f.writeConfined(full, []byte(a.Content)); err != nil {
				return Result{}, err
			}
			// The model has not seen the new contents, so its anchors for this
			// file are meaningless now.
			f.anchors.forget(full)

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

	// Patches is the hash-anchored form. It points at lines by the anchors
	// `read` printed, so the model names a line instead of retyping its
	// surrounding context (plan.md §17).
	Patches []AnchorPatch `json:"patches,omitempty"`
}

func (f *FS) editTool() Tool {
	return Tool{
		Name: "edit",
		Desc: "Change a file. Two forms:\n" +
			"  anchored — patches: [{anchor, op: replace|insert_after|delete, lines}].\n" +
			"    The anchor is the short code read prints BEFORE each line, not the line\n" +
			"    itself: in `a3f2|417| func main() {` the anchor is a3f2. This is the\n" +
			"    cheapest form — you name a line instead of retyping its context.\n" +
			"  exact — old/new strings. The old string must appear exactly once unless all\n" +
			"    is true, and must match the file byte for byte including indentation.\n" +
			"Anchors are only valid for the version you read; if the file changed, re-read it.",
		Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {"type": "string", "description": "File path, relative to the workspace root"},
    "old":  {"type": "string", "description": "Exact text to replace, including indentation"},
    "new":  {"type": "string", "description": "Replacement text"},
    "all":  {"type": "boolean", "description": "Replace every occurrence instead of requiring exactly one"},
    "patches": {
      "type": "array",
      "description": "Anchored edits, using the anchors read printed beside each line",
      "items": {
        "type": "object",
        "properties": {
          "anchor": {"type": "string", "description": "The line anchor from read output"},
          "op":     {"type": "string", "enum": ["replace", "insert_after", "delete"]},
          "lines":  {"type": "array", "items": {"type": "string"},
                     "description": "Replacement or inserted lines; omit for delete"}
        },
        "required": ["anchor", "op"]
      }
    }
  },
  "required": ["path"]
}`),
		Run: func(ctx context.Context, raw json.RawMessage) (Result, error) {
			var a editArgs
			if err := unmarshalArgs(raw, &a); err != nil {
				return Result{}, err
			}
			full, err := f.resolve(a.Path)
			if err != nil {
				return Result{}, err
			}
			defer f.lockPath(full)()

			data, err := f.readConfined(full)
			if err != nil {
				return Result{}, err
			}
			before := string(data)

			if len(a.Patches) > 0 {
				return f.applyAnchoredEdit(full, before, a.Patches)
			}
			if a.Old == "" && a.New == "" {
				return Result{}, fmt.Errorf(
					"edit needs either patches (anchored) or old/new (exact strings)")
			}
			if a.Old == a.New {
				return Result{}, fmt.Errorf("old and new are identical; nothing to do")
			}

			count := strings.Count(before, a.Old)
			switch {
			case count == 0:
				if msg, ok := flexibleMatch(before, a.Old); ok {
					return Result{}, fmt.Errorf(
						"old string not found exactly in %s, but %s. "+
							"Re-read the file and use the exact text including indentation",
						a.Path, msg)
				}
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
			if err := f.writeConfined(full, []byte(after)); err != nil {
				return Result{}, err
			}
			f.anchors.forget(full)

			name := f.rel(full)
			diff, stat := makeDiff(name, before, after)
			// Three lines of context either side of the change, so a consecutive
			// edit to the same region needs no re-read (§1.2).
			firstIdx := strings.Index(before, a.Old)
			around := editContext(after, a.New, firstIdx, 3)
			return Result{
				Output:   fmt.Sprintf("edited %s (+%d -%d)\n\n%s", name, stat.Added, stat.Removed, around),
				Diff:     diff,
				DiffStat: &stat,
				Intent:   fmt.Sprintf("editing %s", name),
			}, nil
		},
	}
}

// applyAnchoredEdit resolves anchor patches against the version the model read,
// refusing loudly rather than fuzzily matching. Silently best-effort applying a
// patch to a file that moved underneath corrupts it, which is strictly worse
// than the retry the anchors were meant to save (plan.md Part V).
func (f *FS) applyAnchoredEdit(full, before string, patches []AnchorPatch) (Result, error) {
	name := f.rel(full)

	st, ok := f.anchors.lookup(full)
	if !ok {
		return Result{}, &ErrStaleAnchor{Path: name,
			Reason: "you have not read this file in this session, so its anchors are unknown"}
	}
	info, err := os.Stat(full)
	if err != nil {
		return Result{}, err
	}
	if !info.ModTime().Equal(st.ModTime) || info.Size() != st.Size {
		f.anchors.forget(full)
		return Result{}, &ErrStaleAnchor{Path: name,
			Reason: "the file changed since you read it"}
	}

	lines := strings.Split(strings.TrimSuffix(before, "\n"), "\n")
	patched, err := ApplyAnchors(name, lines, patches, st)
	if err != nil {
		return Result{}, err
	}

	after := strings.Join(patched, "\n")
	if strings.HasSuffix(before, "\n") {
		after += "\n"
	}
	if after == before {
		return Result{}, fmt.Errorf("the patches produced no change to %s", name)
	}
	if err := f.writeConfined(full, []byte(after)); err != nil {
		return Result{}, err
	}
	f.anchors.forget(full)

	diff, stat := makeDiff(name, before, after)
	return Result{
		Output:   fmt.Sprintf("edited %s (+%d -%d)", name, stat.Added, stat.Removed),
		Diff:     diff,
		DiffStat: &stat,
		Intent:   fmt.Sprintf("editing %s", name),
	}, nil
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
