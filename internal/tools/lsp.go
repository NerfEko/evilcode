package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aymanbagabas/go-udiff"

	"evilcode/internal/lsp"
)

// lspDesc leads with when *not* to reach for it. A model handed a language
// server will use `references` where a grep would do and pay a second of
// indexing for it — and will use `hover` where reading the function is both
// faster and more informative.
const lspDesc = `Ask a language server about code: what a symbol means, where it is defined,
everywhere it is used, and how to rename it safely.

Use it when the answer needs to be exact rather than textual:
  references — before changing a signature, to see every caller
  rename     — to rename a symbol across the project without missing a use
  diagnostics— to see what the compiler thinks, without running a build

Prefer grep for finding text and read for understanding code; they are faster
and they do not need an indexed project.

ops:
  diagnostics {path}                     problems in a file
  definition  {path, line, column}       where the symbol is defined
  references  {path, line, column}       every use of it
  hover       {path, line, column}       its type and documentation
  symbols     {path}                     the file's outline
  rename      {path, line, column, new_name}  rename it everywhere

Line and column are 1-based, as read prints them. rename writes to disk: every
file is computed first and only then written, so it cannot half-apply.`

// LSPServer is what the tool needs. An interface so the tool can be tested
// without a language server installed, which matters because CI has none.
type LSPServer interface {
	For(ctx context.Context, path string) (*lsp.Client, error)
}

// NewLSP builds the `lsp` tool.
func NewLSP(m LSPServer) Tool {
	return Tool{
		Name: "lsp",
		Desc: lspDesc,
		Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "op":       {"type": "string",
                 "enum": ["diagnostics", "definition", "references", "hover", "symbols", "rename"]},
    "path":     {"type": "string", "description": "File to act on"},
    "line":     {"type": "integer", "description": "1-based line, as read prints it"},
    "column":   {"type": "integer", "description": "1-based column"},
    "new_name": {"type": "string", "description": "Replacement identifier, for rename"}
  },
  "required": ["op", "path"]
}`),
		Run: func(ctx context.Context, raw json.RawMessage) (Result, error) {
			var args struct {
				Op      string `json:"op"`
				Path    string `json:"path"`
				Line    int    `json:"line"`
				Column  int    `json:"column"`
				NewName string `json:"new_name"`
			}
			if err := unmarshalArgs(raw, &args); err != nil {
				return Result{}, err
			}
			if strings.TrimSpace(args.Path) == "" {
				return Result{}, fmt.Errorf("lsp needs a path")
			}

			client, err := m.For(ctx, args.Path)
			if err != nil {
				return Result{}, err
			}

			needsPosition := func() error {
				if args.Line <= 0 || args.Column <= 0 {
					return fmt.Errorf("op %q needs a 1-based line and column; "+
						"read prints both beside each line", args.Op)
				}
				return nil
			}

			switch args.Op {
			case "diagnostics":
				return lspDiagnostics(ctx, client, args.Path)

			case "definition":
				if err := needsPosition(); err != nil {
					return Result{}, err
				}
				locs, err := client.Definition(ctx, args.Path, args.Line, args.Column)
				if err != nil {
					return Result{}, err
				}
				return lspLocations("definition", locs), nil

			case "references":
				if err := needsPosition(); err != nil {
					return Result{}, err
				}
				locs, err := client.References(ctx, args.Path, args.Line, args.Column)
				if err != nil {
					return Result{}, err
				}
				return lspLocations("reference", locs), nil

			case "hover":
				if err := needsPosition(); err != nil {
					return Result{}, err
				}
				text, err := client.Hover(ctx, args.Path, args.Line, args.Column)
				if err != nil {
					return Result{}, err
				}
				if strings.TrimSpace(text) == "" {
					return Result{Output: "The server knows nothing about that position."}, nil
				}
				return Result{Output: text, Intent: "hover"}, nil

			case "symbols":
				syms, err := client.Symbols(ctx, args.Path)
				if err != nil {
					return Result{}, err
				}
				return lspSymbols(args.Path, syms), nil

			case "rename":
				if err := needsPosition(); err != nil {
					return Result{}, err
				}
				res, err := client.Rename(ctx, args.Path, args.Line, args.Column, args.NewName)
				if err != nil {
					return Result{}, err
				}
				return lspRename(res, args.NewName), nil

			default:
				return Result{}, fmt.Errorf(
					"unknown op %q (want diagnostics, definition, references, hover, symbols, or rename)",
					args.Op)
			}
		},
	}
}

func lspDiagnostics(ctx context.Context, c *lsp.Client, path string) (Result, error) {
	if err := c.Open(path); err != nil {
		return Result{}, err
	}
	diags := c.Diagnostics(ctx, path)
	if len(diags) == 0 {
		return Result{
			Output: "No problems reported in " + path + ".",
			Intent: "clean",
		}, nil
	}

	var b strings.Builder
	for _, d := range diags {
		fmt.Fprintf(&b, "%s:%d:%d: %s: %s",
			path, d.Range.Start.Line+1, d.Range.Start.Character+1,
			d.SeverityName(), d.Message)
		if d.Source != "" {
			fmt.Fprintf(&b, " (%s)", d.Source)
		}
		b.WriteString("\n")
	}
	return Result{
		Output: strings.TrimRight(b.String(), "\n"),
		Intent: fmt.Sprintf("%d %s", len(diags), problemNoun(len(diags))),
	}, nil
}

func problemNoun(n int) string {
	if n == 1 {
		return "problem"
	}
	return "problems"
}

func lspLocations(noun string, locs []lsp.Location) Result {
	if len(locs) == 0 {
		return Result{Output: "No " + noun + "s found."}
	}
	// Sorted so two runs of the same query read the same, which matters when
	// the model is comparing before and after a change.
	sort.SliceStable(locs, func(i, j int) bool {
		if locs[i].Path() != locs[j].Path() {
			return locs[i].Path() < locs[j].Path()
		}
		return locs[i].Range.Start.Line < locs[j].Range.Start.Line
	})

	var b strings.Builder
	for _, l := range locs {
		fmt.Fprintf(&b, "%s:%d:%d\n",
			relativePath(l.Path()), l.Range.Start.Line+1, l.Range.Start.Character+1)
	}
	plural := noun + "s"
	if len(locs) == 1 {
		plural = noun
	}
	return Result{
		Output: strings.TrimRight(b.String(), "\n"),
		Intent: fmt.Sprintf("%d %s", len(locs), plural),
	}
}

func lspSymbols(path string, syms []lsp.Symbol) Result {
	if len(syms) == 0 {
		return Result{Output: "No symbols in " + path + "."}
	}
	var b strings.Builder
	var walk func(list []lsp.Symbol, depth int)
	walk = func(list []lsp.Symbol, depth int) {
		for _, s := range list {
			fmt.Fprintf(&b, "%s%d: %s %s", strings.Repeat("  ", depth), s.Line(), s.KindName(), s.Name)
			if s.Detail != "" {
				fmt.Fprintf(&b, " %s", s.Detail)
			}
			b.WriteString("\n")
			walk(s.Children, depth+1)
		}
	}
	walk(syms, 0)
	return Result{
		Output: strings.TrimRight(b.String(), "\n"),
		Intent: fmt.Sprintf("%d top-level", len(syms)),
	}
}

func lspRename(res *lsp.RenameResult, newName string) Result {
	files := make([]string, 0, len(res.Files))
	total := 0
	for path, n := range res.Files {
		files = append(files, path)
		total += n
	}
	sort.Strings(files)

	var out strings.Builder
	var diff strings.Builder
	stat := DiffStat{}
	for _, path := range files {
		fmt.Fprintf(&out, "%s: %d %s\n", relativePath(path), res.Files[path],
			editNoun(res.Files[path]))
		// One combined diff so the §9.3 renderer shows the whole rename as a
		// single change, which is what it was.
		unified := udiff.Unified(relativePath(path), relativePath(path),
			res.Before[path], res.After[path])
		diff.WriteString(unified)
		for _, line := range strings.Split(unified, "\n") {
			switch {
			case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
			case strings.HasPrefix(line, "+"):
				stat.Added++
			case strings.HasPrefix(line, "-"):
				stat.Removed++
			}
		}
	}

	return Result{
		Output: fmt.Sprintf("Renamed to %s across %d %s (%d %s):\n%s",
			newName, len(files), fileNoun(len(files)), total, editNoun(total),
			strings.TrimRight(out.String(), "\n")),
		Diff:     diff.String(),
		DiffStat: &stat,
		Intent:   fmt.Sprintf("rename → %s · %d %s", newName, len(files), fileNoun(len(files))),
	}
}

func editNoun(n int) string {
	if n == 1 {
		return "edit"
	}
	return "edits"
}

func fileNoun(n int) string {
	if n == 1 {
		return "file"
	}
	return "files"
}

// relativePath shortens a path against the working directory, so tool output
// reads like the paths the user types rather than like absolute noise.
func relativePath(path string) string {
	cwd, err := filepath.Abs(".")
	if err != nil {
		return path
	}
	if rel, err := filepath.Rel(cwd, path); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return path
}
