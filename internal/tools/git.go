package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// Git holds the read-only git helpers. They exist so the model stops burning
// bash calls on `git status` and `git diff`, and so their output arrives in a
// shape the diff renderer already understands (plan.md §17).
type Git struct {
	Root string
}

func NewGit(root string) *Git { return &Git{Root: root} }

// Tools returns the git tool set.
func (g *Git) Tools() Set {
	return Set{g.overviewTool(), g.fileDiffTool(), g.hunkTool()}
}

// run executes a git command in the workspace.
func (g *Git) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = g.Root
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errBuf.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return out.String(), nil
}

func (g *Git) overviewTool() Tool {
	return Tool{
		Name: "git_overview",
		Desc: "Summarize the repository: current branch, staged and unstaged file counts, " +
			"and recent commits. Use this once near the start of a repository change and before " +
			"committing; " +
			"it exposes existing user changes, which must not be discarded. Use this instead " +
			"of shelling out to git status. Do not repeat it unless the working tree changed.",
		Schema: json.RawMessage(`{"type": "object", "properties": {}}`),
		Run: func(ctx context.Context, raw json.RawMessage) (Result, error) {
			branch, err := g.run(ctx, "rev-parse", "--abbrev-ref", "HEAD")
			if err != nil {
				return Result{}, err
			}
			status, err := g.run(ctx, "status", "--porcelain")
			if err != nil {
				return Result{}, err
			}
			log, err := g.run(ctx, "log", "--oneline", "-10")
			if err != nil {
				// A repository with no commits yet is a normal state, not a
				// failure worth aborting the overview for.
				log = ""
			}

			var staged, unstaged, untracked []string
			for _, line := range strings.Split(strings.TrimRight(status, "\n"), "\n") {
				if len(line) < 3 {
					continue
				}
				x, y, path := line[0], line[1], strings.TrimSpace(line[3:])
				switch {
				case x == '?' && y == '?':
					untracked = append(untracked, path)
				default:
					if x != ' ' {
						staged = append(staged, path)
					}
					if y != ' ' {
						unstaged = append(unstaged, path)
					}
				}
			}

			var b strings.Builder
			fmt.Fprintf(&b, "branch: %s\n", strings.TrimSpace(branch))
			fmt.Fprintf(&b, "staged: %d · unstaged: %d · untracked: %d\n",
				len(staged), len(unstaged), len(untracked))
			writeList(&b, "staged", staged)
			writeList(&b, "unstaged", unstaged)
			writeList(&b, "untracked", untracked)
			if strings.TrimSpace(log) != "" {
				b.WriteString("\nrecent commits:\n")
				b.WriteString(log)
			}
			return Result{
				Output: b.String(),
				Intent: fmt.Sprintf("%s · %d changed", strings.TrimSpace(branch),
					len(staged)+len(unstaged)),
			}, nil
		},
	}
}

// writeList prints a capped file list. A thousand untracked files is not
// information, so the tail is summarized rather than dumped.
func writeList(b *strings.Builder, label string, paths []string) {
	if len(paths) == 0 {
		return
	}
	const maxShown = 20
	shown := paths
	if len(shown) > maxShown {
		shown = shown[:maxShown]
	}
	fmt.Fprintf(b, "\n%s:\n", label)
	for _, p := range shown {
		fmt.Fprintf(b, "  %s\n", p)
	}
	if len(paths) > len(shown) {
		fmt.Fprintf(b, "  ... %d more\n", len(paths)-len(shown))
	}
}

type gitFileDiffArgs struct {
	Path   string `json:"path"`
	Staged bool   `json:"staged,omitempty"`
}

func (g *Git) fileDiffTool() Tool {
	return Tool{
		Name: "git_file_diff",
		Desc: "Show the diff for one file. Use this after editing to inspect what changed. " +
			"Set staged to see the index rather than the working tree; this tool is read-only.",
		Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path":   {"type": "string",  "description": "File path, relative to the repository root"},
    "staged": {"type": "boolean", "description": "Diff the index instead of the working tree"}
  },
  "required": ["path"]
}`),
		Run: func(ctx context.Context, raw json.RawMessage) (Result, error) {
			var a gitFileDiffArgs
			if err := unmarshalArgs(raw, &a); err != nil {
				return Result{}, err
			}
			if a.Path == "" {
				return Result{}, fmt.Errorf("path is required")
			}
			args := []string{"diff"}
			if a.Staged {
				args = append(args, "--staged")
			}
			args = append(args, "--", a.Path)

			diff, err := g.run(ctx, args...)
			if err != nil {
				return Result{}, err
			}
			if strings.TrimSpace(diff) == "" {
				return Result{Output: "no changes to " + a.Path}, nil
			}
			stat := countDiff(diff)
			return Result{
				Output:   diff,
				Diff:     diff,
				DiffStat: &stat,
				Intent:   fmt.Sprintf("diff of %s", a.Path),
			}, nil
		},
	}
}

type gitHunkArgs struct {
	Path string `json:"path"`
	N    int    `json:"n"`
}

func (g *Git) hunkTool() Tool {
	return Tool{
		Name: "git_hunk",
		Desc: "Show a single hunk of a file's working-tree diff, numbered from 1. Use this " +
			"to inspect a large diff without loading all of it; it is read-only.",
		Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "path": {"type": "string",  "description": "File path, relative to the repository root"},
    "n":    {"type": "integer", "description": "Which hunk to show, 1-based"}
  },
  "required": ["path", "n"]
}`),
		Run: func(ctx context.Context, raw json.RawMessage) (Result, error) {
			var a gitHunkArgs
			if err := unmarshalArgs(raw, &a); err != nil {
				return Result{}, err
			}
			if a.N < 1 {
				return Result{}, fmt.Errorf("n is 1-based; got %d", a.N)
			}
			diff, err := g.run(ctx, "diff", "--", a.Path)
			if err != nil {
				return Result{}, err
			}

			hunks := splitHunks(diff)
			if len(hunks) == 0 {
				return Result{Output: "no changes to " + a.Path}, nil
			}
			if a.N > len(hunks) {
				return Result{}, fmt.Errorf("%s has %d hunks; %d is out of range",
					a.Path, len(hunks), a.N)
			}
			body := hunks[a.N-1]
			stat := countDiff(body)
			return Result{
				Output:   fmt.Sprintf("hunk %d of %d:\n%s", a.N, len(hunks), body),
				Diff:     body,
				DiffStat: &stat,
				Intent:   fmt.Sprintf("hunk %d/%d of %s", a.N, len(hunks), a.Path),
			}, nil
		},
	}
}

// splitHunks divides a unified diff at its @@ markers.
func splitHunks(diff string) []string {
	var out []string
	var cur []string
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "@@") {
			if len(cur) > 0 {
				out = append(out, strings.Join(cur, "\n"))
			}
			cur = []string{line}
			continue
		}
		if len(cur) > 0 {
			cur = append(cur, line)
		}
	}
	if len(cur) > 0 {
		out = append(out, strings.Join(cur, "\n"))
	}
	return out
}

// countDiff counts changed lines in a unified diff.
func countDiff(diff string) DiffStat {
	var stat DiffStat
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
		case strings.HasPrefix(line, "+"):
			stat.Added++
		case strings.HasPrefix(line, "-"):
			stat.Removed++
		}
	}
	return stat
}
