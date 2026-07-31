package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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
// first, and nothing touches disk until all of them have succeeded. A rename
// that writes three files and then fails on the fourth leaves a workspace that
// does not compile and that nobody can easily undo (plan.md §17).
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

	// Phase two: write. Every body is already computed, so the only way to fail
	// here is the filesystem itself.
	for file, body := range res.After {
		info, err := os.Stat(file)
		mode := os.FileMode(0o644)
		if err == nil {
			mode = info.Mode().Perm()
		}
		if err := os.WriteFile(file, []byte(body), mode); err != nil {
			return nil, fmt.Errorf("rename failed writing %s: %w", file, err)
		}
		c.Forget(file)
	}
	return res, nil
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
		if e.Range.Start.Character > len(startLine) || e.Range.End.Character > len(endLine) {
			return "", fmt.Errorf("edit column is past the end of its line")
		}

		head := startLine[:e.Range.Start.Character]
		tail := endLine[e.Range.End.Character:]
		merged := head + e.NewText + tail

		rest := append([]string{merged}, lines[e.Range.End.Line+1:]...)
		lines = append(lines[:e.Range.Start.Line], rest...)
	}
	return strings.Join(lines, "\n"), nil
}
