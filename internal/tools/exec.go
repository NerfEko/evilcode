package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Exec holds the shell and search tools' shared state.
type Exec struct {
	Root string

	// Timeout bounds a single command. A model that runs an interactive
	// program would otherwise hang the turn forever.
	Timeout time.Duration

	// mu guards cwd, which persists across bash calls so `cd` behaves the way
	// a human shell user expects within a turn.
	mu  sync.Mutex
	cwd string
}

// DefaultTimeout is the per-command wall clock budget.
const DefaultTimeout = 2 * time.Minute

func NewExec(root string) *Exec {
	if root == "" {
		root, _ = os.Getwd()
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	return &Exec{Root: root, Timeout: DefaultTimeout, cwd: root}
}

// Cwd reports the working directory subsequent commands will run in.
func (e *Exec) Cwd() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cwd
}

// Tools returns the shell and search tools.
func (e *Exec) Tools() Set {
	return Set{e.bashTool(), e.grepTool()}
}

type bashArgs struct {
	Cmd     string `json:"cmd"`
	Timeout int    `json:"timeout,omitempty"`
}

func (e *Exec) bashTool() Tool {
	return Tool{
		Name: "bash",
		Desc: "Run a shell command in the workspace. The working directory persists " +
			"between calls, so cd carries over. Output is combined stdout and stderr.",
		Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "cmd":     {"type": "string",  "description": "Shell command to run"},
    "timeout": {"type": "integer", "description": "Timeout in seconds; defaults to 120"}
  },
  "required": ["cmd"]
}`),
		Run: func(ctx context.Context, raw json.RawMessage) (Result, error) {
			var a bashArgs
			if err := unmarshalArgs(raw, &a); err != nil {
				return Result{}, err
			}
			if strings.TrimSpace(a.Cmd) == "" {
				return Result{}, fmt.Errorf("cmd is required")
			}

			timeout := e.Timeout
			if a.Timeout > 0 {
				timeout = time.Duration(a.Timeout) * time.Second
			}
			ctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			// The directory is read once and written back after, so a `cd` in
			// the command carries to the next call. Appending `pwd` on a
			// sentinel line is how the new directory comes back out.
			const marker = "__evilcode_cwd__"
			script := a.Cmd + "\n__evilcode_status=$?\nprintf '\\n" + marker + "%s\\n' \"$PWD\"\nexit $__evilcode_status"

			cmd := exec.CommandContext(ctx, "bash", "-c", script)
			cmd.Dir = e.Cwd()
			var buf bytes.Buffer
			cmd.Stdout = &buf
			cmd.Stderr = &buf
			// Killing bash does not close the output pipes if a grandchild
			// still holds them open, so Run would block past the timeout
			// waiting on `sleep 10` rather than returning. WaitDelay forces
			// the pipes shut shortly after cancellation.
			cmd.WaitDelay = 2 * time.Second
			runErr := cmd.Run()

			out := buf.String()
			if idx := strings.LastIndex(out, marker); idx >= 0 {
				newCwd := strings.TrimSpace(out[idx+len(marker):])
				out = strings.TrimRight(out[:idx], "\n")
				if newCwd != "" {
					e.mu.Lock()
					e.cwd = newCwd
					e.mu.Unlock()
				}
			}

			if ctx.Err() == context.DeadlineExceeded {
				return Result{Output: out}, fmt.Errorf(
					"command timed out after %s; if it is meant to run long, raise timeout", timeout)
			}
			if runErr != nil {
				// A non-zero exit is information, not a harness failure: the
				// model needs the output to act on it.
				return Result{
					Output: out,
					Intent: shortCmd(a.Cmd),
				}, fmt.Errorf("exit status %s", exitStatus(runErr))
			}
			if strings.TrimSpace(out) == "" {
				out = "(no output)"
			}
			return Result{Output: out, Intent: shortCmd(a.Cmd)}, nil
		},
	}
}

func exitStatus(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return fmt.Sprint(ee.ExitCode())
	}
	return err.Error()
}

// shortCmd trims a command for the tool row's intent text.
func shortCmd(cmd string) string {
	cmd = strings.TrimSpace(strings.ReplaceAll(cmd, "\n", " "))
	if len(cmd) > 48 {
		return cmd[:47] + "…"
	}
	return cmd
}

type grepArgs struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path,omitempty"`
	Glob    string `json:"glob,omitempty"`
	Context int    `json:"context,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

func (e *Exec) grepTool() Tool {
	return Tool{
		Name: "grep",
		Desc: "Search file contents with a regular expression, via ripgrep. " +
			"Returns matching lines with their file and line number.",
		Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "pattern": {"type": "string",  "description": "Regular expression to search for"},
    "path":    {"type": "string",  "description": "File or directory to search; defaults to the workspace root"},
    "glob":    {"type": "string",  "description": "Restrict to files matching this glob, e.g. '*.go'"},
    "context": {"type": "integer", "description": "Lines of context around each match"},
    "limit":   {"type": "integer", "description": "Maximum matching lines to return"}
  },
  "required": ["pattern"]
}`),
		Run: func(ctx context.Context, raw json.RawMessage) (Result, error) {
			var a grepArgs
			if err := unmarshalArgs(raw, &a); err != nil {
				return Result{}, err
			}
			if a.Pattern == "" {
				return Result{}, fmt.Errorf("pattern is required")
			}
			// Never reimplement ripgrep (plan.md §17).
			if _, err := exec.LookPath("rg"); err != nil {
				return Result{}, fmt.Errorf("ripgrep (rg) is not installed; the grep tool needs it")
			}

			limit := a.Limit
			if limit <= 0 {
				limit = 200
			}
			args := []string{"--line-number", "--with-filename", "--color=never", "--max-count=50"}
			if a.Glob != "" {
				args = append(args, "--glob", a.Glob)
			}
			if a.Context > 0 {
				args = append(args, "--context", fmt.Sprint(a.Context))
			}
			args = append(args, "--regexp", a.Pattern)

			target := e.Root
			if a.Path != "" {
				if !filepath.IsAbs(a.Path) {
					target = filepath.Join(e.Root, a.Path)
				} else {
					target = a.Path
				}
			}
			args = append(args, target)

			ctx, cancel := context.WithTimeout(ctx, e.Timeout)
			defer cancel()

			cmd := exec.CommandContext(ctx, "rg", args...)
			cmd.Dir = e.Root
			var buf bytes.Buffer
			cmd.Stdout = &buf
			cmd.Stderr = &buf
			err := cmd.Run()

			out := buf.String()
			// ripgrep exits 1 for "no matches", which is an answer, not a fault.
			var ee *exec.ExitError
			if err != nil && errors.As(err, &ee) && ee.ExitCode() == 1 {
				return Result{Output: "no matches for " + a.Pattern}, nil
			}
			if err != nil {
				return Result{Output: out}, fmt.Errorf("ripgrep failed: %w", err)
			}

			lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
			note := ""
			if len(lines) > limit {
				note = fmt.Sprintf("\n[%d more matching lines; narrow the pattern]", len(lines)-limit)
				lines = lines[:limit]
			}
			// Paths come back absolute; relative reads better and costs fewer tokens.
			for i, l := range lines {
				lines[i] = strings.TrimPrefix(l, e.Root+string(filepath.Separator))
			}
			return Result{
				Output: strings.Join(lines, "\n") + note,
				Intent: fmt.Sprintf("%d matches for %s", len(lines), a.Pattern),
			}, nil
		},
	}
}
