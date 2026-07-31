package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// docPosition is the parameter shape most position-based requests share.
func docPosition(path string, line, char int) map[string]any {
	return map[string]any{
		"textDocument": map[string]any{"uri": URIFromPath(path)},
		// The protocol is zero-based; every human-facing number in evilcode is
		// one-based, so the conversion happens once, here, at the boundary.
		"position": map[string]any{"line": line - 1, "character": char - 1},
	}
}

// Definition resolves where a symbol is defined.
func (c *Client) Definition(ctx context.Context, path string, line, char int) ([]Location, error) {
	if err := c.Open(path); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()

	raw, err := c.call(ctx, "textDocument/definition", docPosition(path, line, char))
	if err != nil {
		return nil, err
	}
	return decodeLocations(raw), nil
}

// References lists every use of a symbol.
func (c *Client) References(ctx context.Context, path string, line, char int) ([]Location, error) {
	if err := c.Open(path); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()

	params := docPosition(path, line, char)
	params["context"] = map[string]any{"includeDeclaration": true}
	raw, err := c.call(ctx, "textDocument/references", params)
	if err != nil {
		return nil, err
	}
	return decodeLocations(raw), nil
}

// decodeLocations copes with the three shapes servers answer with: a single
// location, an array of them, or an array of LocationLinks.
func decodeLocations(raw json.RawMessage) []Location {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var many []Location
	if json.Unmarshal(raw, &many) == nil && len(many) > 0 && many[0].URI != "" {
		return many
	}
	var one Location
	if json.Unmarshal(raw, &one) == nil && one.URI != "" {
		return []Location{one}
	}
	var links []struct {
		TargetURI            string `json:"targetUri"`
		TargetSelectionRange Range  `json:"targetSelectionRange"`
	}
	if json.Unmarshal(raw, &links) == nil {
		out := make([]Location, 0, len(links))
		for _, l := range links {
			out = append(out, Location{URI: l.TargetURI, Range: l.TargetSelectionRange})
		}
		return out
	}
	return nil
}

// Hover returns the server's description of a symbol.
func (c *Client) Hover(ctx context.Context, path string, line, char int) (string, error) {
	if err := c.Open(path); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()

	raw, err := c.call(ctx, "textDocument/hover", docPosition(path, line, char))
	if err != nil {
		return "", err
	}
	var hover struct {
		Contents json.RawMessage `json:"contents"`
	}
	if json.Unmarshal(raw, &hover) != nil {
		return "", nil
	}
	return decodeMarkup(hover.Contents), nil
}

// decodeMarkup flattens the several encodings hover contents come in.
func decodeMarkup(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var markup struct {
		Kind  string `json:"kind"`
		Value string `json:"value"`
	}
	if json.Unmarshal(raw, &markup) == nil && markup.Value != "" {
		return markup.Value
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) == nil {
		var out []string
		for _, p := range parts {
			if v := decodeMarkup(p); v != "" {
				out = append(out, v)
			}
		}
		return strings.Join(out, "\n\n")
	}
	return ""
}

// Symbol is one entry in a file's outline.
type Symbol struct {
	Name string `json:"name"`
	Kind int    `json:"kind"`

	// Range is the whole declaration; SelectionRange is just the name. Servers
	// set one or the other depending on which response shape they use.
	Range          Range  `json:"range"`
	SelectionRange Range  `json:"selectionRange"`
	Detail         string `json:"detail"`

	Children []Symbol `json:"children"`

	// Location is set by the flat (SymbolInformation) encoding.
	Location Location `json:"location"`
}

// Line is the symbol's one-based declaration line, whichever shape it came in.
func (s Symbol) Line() int {
	if s.SelectionRange.Start.Line > 0 || s.Range.End.Line > 0 {
		return s.SelectionRange.Start.Line + 1
	}
	return s.Location.Range.Start.Line + 1
}

// KindName maps the protocol's symbol kinds to something readable.
func (s Symbol) KindName() string {
	names := map[int]string{
		1: "file", 2: "module", 3: "namespace", 4: "package", 5: "class",
		6: "method", 7: "property", 8: "field", 9: "constructor", 10: "enum",
		11: "interface", 12: "function", 13: "variable", 14: "constant",
		15: "string", 16: "number", 17: "boolean", 18: "array", 19: "object",
		20: "key", 21: "null", 22: "enum member", 23: "struct", 24: "event",
		25: "operator", 26: "type parameter",
	}
	if n, ok := names[s.Kind]; ok {
		return n
	}
	return "symbol"
}

// Symbols returns a file's outline.
func (c *Client) Symbols(ctx context.Context, path string) ([]Symbol, error) {
	if err := c.Open(path); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()

	raw, err := c.call(ctx, "textDocument/documentSymbol", map[string]any{
		"textDocument": map[string]any{"uri": URIFromPath(path)},
	})
	if err != nil {
		return nil, err
	}
	var symbols []Symbol
	if json.Unmarshal(raw, &symbols) != nil {
		return nil, nil
	}
	return symbols, nil
}

// RenameResult reports what a rename changed.
type RenameResult struct {
	// Files maps path → number of edits applied.
	Files map[string]int

	// Diffs maps path → unified diff, for §9.3 rendering.
	Before map[string]string
	After  map[string]string
}

// Rename renames a symbol across the workspace, atomically.
//
// Atomic in the sense that matters: every file is read and rewritten in memory
// first (phase one), then staged as synced temp files with each source
// reverified unchanged (phase two), and only then renamed into place (phase
// three) — see commitRename. A rename that touches several files and then
// fails partway leaves either every original untouched (a staging failure) or
// every already-renamed file put back (a commit failure), rather than a
// workspace that does not compile and that nobody can easily undo (plan.md
// §17).
func (c *Client) Rename(ctx context.Context, path string, line, char int, newName string) (*RenameResult, error) {
	if strings.TrimSpace(newName) == "" {
		return nil, fmt.Errorf("rename needs a new name")
	}
	if err := c.Open(path); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()

	params := docPosition(path, line, char)
	params["newName"] = newName
	raw, err := c.call(ctx, "textDocument/rename", params)
	if err != nil {
		return nil, err
	}

	var edit WorkspaceEdit
	if json.Unmarshal(raw, &edit) != nil {
		return nil, fmt.Errorf("the server returned no workspace edit")
	}
	changes := edit.Edits()
	if len(changes) == 0 {
		return nil, fmt.Errorf("nothing to rename at %s:%d:%d", path, line, char)
	}

	// Phase one: compute every new file body. Nothing is written yet.
	res := &RenameResult{
		Files:  map[string]int{},
		Before: map[string]string{},
		After:  map[string]string{},
	}
	for file, edits := range changes {
		// The paths come from the language server, which is a subprocess
		// answering with whatever it likes. A rename that edits files outside
		// the workspace is not a rename, and nothing downstream would notice —
		// the write phase is trusted precisely because phase one succeeded.
		if err := c.insideRoot(file); err != nil {
			return nil, err
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("rename touches %s, which could not be read: %w", file, err)
		}
		updated, err := ApplyEdits(string(data), edits)
		if err != nil {
			return nil, fmt.Errorf("rename could not be applied to %s: %w", file, err)
		}
		res.Before[file] = string(data)
		res.After[file] = updated
		res.Files[file] = len(edits)
	}

	if err := commitRename(res, c.Forget); err != nil {
		return nil, err
	}
	return res, nil
}

// commitRename replaces every source in res.Before with the matching body in
// res.After.
//
// Phase two stages each replacement as a synced temp file beside its
// destination, first re-reading the source to confirm it still matches what
// phase one saw — something else may have written it in between, and this is
// the last chance to notice before overwriting it unseen. A failure anywhere
// in staging leaves every original file untouched, because nothing real has
// been touched yet.
//
// Phase three renames every staged temp into place. Each rename is
// same-directory and effectively instant, which is the smallest failure
// window a multi-file replacement can have — but if one still fails, every
// file already renamed is put back to what phase one read, and the remaining
// staged temps are discarded, rather than leaving the workspace half-renamed.
func commitRename(res *RenameResult, forget func(string)) error {
	staged := make(map[string]string, len(res.After)) // dest path -> temp path
	rollbackStaging := func() {
		for _, tmp := range staged {
			os.Remove(tmp)
		}
	}
	for file, body := range res.After {
		cur, err := os.ReadFile(file)
		if err != nil {
			rollbackStaging()
			return fmt.Errorf("rename touches %s, which could not be re-read before committing: %w", file, err)
		}
		if string(cur) != res.Before[file] {
			rollbackStaging()
			return fmt.Errorf("%s changed on disk since the rename was computed; refusing to overwrite it", file)
		}
		mode := os.FileMode(0o644)
		if info, err := os.Stat(file); err == nil {
			mode = info.Mode().Perm()
		}
		tmp, err := os.CreateTemp(filepath.Dir(file), "."+filepath.Base(file)+".*")
		if err != nil {
			rollbackStaging()
			return fmt.Errorf("rename could not stage %s: %w", file, err)
		}
		name := tmp.Name()
		_, writeErr := tmp.Write([]byte(body))
		if writeErr == nil {
			writeErr = tmp.Sync()
		}
		if closeErr := tmp.Close(); writeErr == nil {
			writeErr = closeErr
		}
		if writeErr == nil {
			writeErr = os.Chmod(name, mode)
		}
		if writeErr != nil {
			os.Remove(name)
			rollbackStaging()
			return fmt.Errorf("rename could not stage %s: %w", file, writeErr)
		}
		staged[file] = name
	}

	committed := make(map[string]bool, len(staged))
	for file, tmp := range staged {
		if err := os.Rename(tmp, file); err != nil {
			for done := range committed {
				os.WriteFile(done, []byte(res.Before[done]), 0o644)
			}
			for other, otherTmp := range staged {
				if !committed[other] && other != file {
					os.Remove(otherTmp)
				}
			}
			return fmt.Errorf("rename failed committing %s: %w", file, err)
		}
		committed[file] = true
		forget(file)
	}
	return nil
}

// utf16ToByte converts a protocol character offset into a byte index into line.
//
// LSP positions are counted in UTF-16 code units unless a server negotiates
// otherwise, and evilcode negotiates nothing — so `é` is one unit and two
// bytes, `🔥` is two units and four. Using the number as a byte index shifts
// every edit on a line with non-ASCII text to its left, and can slice a UTF-8
// sequence in half.
func utf16ToByte(line string, char int) (int, error) {
	if char < 0 {
		return 0, fmt.Errorf("character offset %d is negative", char)
	}
	units := 0
	for i, r := range line {
		if units == char {
			return i, nil
		}
		if r > 0xFFFF {
			units += 2
		} else {
			units++
		}
		if units > char {
			// The offset landed between the halves of a surrogate pair, which
			// names no position in the text.
			return 0, fmt.Errorf("character offset %d falls inside a character", char)
		}
	}
	if units == char {
		return len(line), nil
	}
	return 0, fmt.Errorf("character offset %d is past the end of the line", char)
}

// insideRoot refuses a server-supplied path that leaves the workspace.
//
// Resolved on both sides before comparing, so a workspace reached through a
// symlink does not reject its own files — the same reasoning as the filesystem
// tools' confinement, and the same trap if it is skipped.
func (c *Client) insideRoot(path string) error {
	root := c.Root
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	full := path
	if !filepath.IsAbs(full) {
		full = filepath.Join(root, full)
	}
	if resolved, err := filepath.EvalSymlinks(full); err == nil {
		full = resolved
	}
	rel, err := filepath.Rel(root, filepath.Clean(full))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf(
			"the language server asked to edit %s, which is outside the workspace %s",
			path, c.Root)
	}
	return nil
}

// ApplyEdits applies a file's edits to its text.
//
// Edits are applied last-first so that earlier offsets stay valid: the protocol
// gives every edit in terms of the *original* document, and applying them in
// order would shift every position after the first.
func ApplyEdits(text string, edits []TextEdit) (string, error) {
	lines := strings.Split(text, "\n")

	sorted := append([]TextEdit(nil), edits...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i].Range.Start, sorted[j].Range.Start
		if a.Line != b.Line {
			return a.Line > b.Line
		}
		return a.Character > b.Character
	})

	for _, e := range sorted {
		if e.Range.Start.Line < 0 || e.Range.Start.Line >= len(lines) {
			return "", fmt.Errorf("edit at line %d is outside the file", e.Range.Start.Line+1)
		}
		if e.Range.End.Line >= len(lines) {
			return "", fmt.Errorf("edit ends at line %d, past the file", e.Range.End.Line+1)
		}

		startLine := lines[e.Range.Start.Line]
		endLine := lines[e.Range.End.Line]
		startByte, err := utf16ToByte(startLine, e.Range.Start.Character)
		if err != nil {
			return "", fmt.Errorf("edit at line %d: %w", e.Range.Start.Line+1, err)
		}
		endByte, err := utf16ToByte(endLine, e.Range.End.Character)
		if err != nil {
			return "", fmt.Errorf("edit ending at line %d: %w", e.Range.End.Line+1, err)
		}

		head := startLine[:startByte]
		tail := endLine[endByte:]
		merged := head + e.NewText + tail

		rest := append([]string{merged}, lines[e.Range.End.Line+1:]...)
		lines = append(lines[:e.Range.Start.Line], rest...)
	}
	return strings.Join(lines, "\n"), nil
}
