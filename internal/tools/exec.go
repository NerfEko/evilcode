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

	// Bg tracks detached commands.
	Bg *Background

	// Timeout bounds a single command. A model that runs an interactive
	// program would otherwise hang the turn forever.
	Timeout time.Duration

	// mu guards cwd, which persists across bash calls so `cd` behaves the way
	// a human shell user expects within a turn.
	mu  sync.Mutex
	cwd string

	// run serializes foreground commands.
	//
	// A stateful shell cannot run in parallel with itself: each call reads the
	// working directory at the start and writes it back at the end, so parallel
	// calls all begin where the previous round left off and the last to finish
	// decides where the next round begins. Every `cd` but one is lost, and a
	// call that expected to be somewhere else does its work in the wrong place
	// while reporting success.
	//
	// ponytail: one lock for the whole shell, not per-directory. A tool that
	// carries a working directory is a single conversation with a single shell,
	// and pretending otherwise is what this is fixing.
	run sync.Mutex
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
	return &Exec{Root: root, Timeout: DefaultTimeout, cwd: root, Bg: &Background{}}
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
	Cmd        string `json:"cmd"`
	Timeout    int    `json:"timeout,omitempty"`
	Background bool   `json:"background,omitempty"`
}

// BackgroundTask is a command still running after its tool call returned.
type BackgroundTask struct {
	ID      int
	Label   string
	Started time.Time

	mu     sync.Mutex
	done   bool
	failed bool
	output string
}

// Snapshot returns the task's current state.
func (t *BackgroundTask) Snapshot() (done, failed bool, output string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.done, t.failed, t.output
}

// Background tracks detached commands.
//
// The registry lives here rather than in the UI because a background task
// outlives the tool call that started it, and the agent loop must be able to
// report completion whether or not anything is watching (plan.md §17).
type Background struct {
	mu    sync.Mutex
	next  int
	tasks []*BackgroundTask

	// OnDone is called when a task finishes, so the UI can raise a notice.
	OnDone func(*BackgroundTask)
}

// Tasks returns the tracked tasks.
func (b *Background) Tasks() []*BackgroundTask {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]*BackgroundTask(nil), b.tasks...)
}

// add registers a task.
func (b *Background) add(label string) *BackgroundTask {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.next++
	t := &BackgroundTask{ID: b.next, Label: label, Started: time.Now()}
	b.tasks = append(b.tasks, t)
	return t
}

// finish records a task's result and notifies.
func (b *Background) finish(t *BackgroundTask, output string, failed bool) {
	t.mu.Lock()
	t.done, t.failed, t.output = true, failed, output
	t.mu.Unlock()

	if b.OnDone != nil {
		b.OnDone(t)
	}
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
    "timeout": {"type": "integer", "description": "Timeout in seconds; defaults to 120"},
    "background": {"type": "boolean",
                   "description": "Return immediately and report when it finishes. Use for long builds, watchers, and servers."}
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

			if a.Background {
				return e.runBackground(a.Cmd), nil
			}

			timeout := e.Timeout
			if a.Timeout > 0 {
				timeout = time.Duration(a.Timeout) * time.Second
			}

			// Taken before the timeout starts: a command must not spend its
			// budget waiting for the shell to be free.
			e.run.Lock()
			defer e.run.Unlock()

			ctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			// The directory is read once and written back after, so a `cd` in
			// the command carries to the next call. Appending `pwd` on a
			// sentinel line is how the new directory comes back out.
			const marker = "__evilcode_cwd__"
			script := a.Cmd + "\n__evilcode_status=$?\nprintf '\\n" + marker + "%s\\n' \"$PWD\"\nexit $__evilcode_status"

			cmd := exec.CommandContext(ctx, "bash", "-c", script)
			cmd.Dir = e.Cwd()
			var buf ringWriter
			cmd.Stdout = &buf
			cmd.Stderr = &buf
			// Its own process group, so cancelling kills the descendants too.
			// Killing the shell alone leaves a grandchild running in the
			// workspace after the tool call has already returned a timeout.
			setProcessGroup(cmd)
			// Killing bash does not close the output pipes if a grandchild
			// still holds them open, so Run would block past the timeout
			// waiting on `sleep 10` rather than returning. WaitDelay forces
			// the pipes shut shortly after cancellation.
			cmd.WaitDelay = 2 * time.Second
			runErr := runGroup(ctx, cmd)

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

// runBackground starts a detached command and returns immediately.
//
// It deliberately does not inherit the caller's context: the point is to
// outlive the tool call. It gets a generous ceiling of its own instead, so a
// runaway watcher still cannot run forever.
func (e *Exec) runBackground(command string) Result {
	task := e.Bg.add(shortCmd(command))

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), BackgroundTimeout)
		defer cancel()

		cmd := exec.CommandContext(ctx, "bash", "-c", command)
		cmd.Dir = e.Cwd()
		var buf ringWriter
		cmd.Stdout = &buf
		cmd.Stderr = &buf
		cmd.WaitDelay = 2 * time.Second
		setProcessGroup(cmd)

		err := runGroup(ctx, cmd)
		e.Bg.finish(task, Truncate(buf.String()), err != nil)
	}()

	return Result{
		Output: fmt.Sprintf("started in the background as task %d; "+
			"its result will be reported when it finishes", task.ID),
		Intent: "bg: " + shortCmd(command),
	}
}

// ringWriter keeps the last MaxOutputBytes of a command's output.
//
// A command's output length is not a fact about the machine — `yes`, a build
// with a verbose flag, a tail on a busy log — and both execution paths were
// accumulating all of it in memory before truncating at the end. The tail is
// what is kept because that is where a failure says why.
type ringWriter struct {
	mu       sync.Mutex
	buf      []byte
	overflow bool
}

// MaxOutputBytes bounds what one command may hold in memory. Well above
// MaxResultBytes, so the truncation the model sees is still the one it was
// always shown; this is the ceiling behind it.
const MaxOutputBytes = 1 << 20

func (w *ringWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	if len(p) >= MaxOutputBytes {
		p = p[len(p)-MaxOutputBytes:]
		w.buf = append(w.buf[:0], p...)
		w.overflow = true
		return n, nil
	}
	if len(w.buf)+len(p) > MaxOutputBytes {
		drop := len(w.buf) + len(p) - MaxOutputBytes
		w.buf = append(w.buf[:0], w.buf[drop:]...)
		w.overflow = true
	}
	w.buf = append(w.buf, p...)
	return n, nil
}

// String returns what was kept, marked if anything was dropped.
func (w *ringWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.overflow {
		return string(w.buf)
	}
	return "[earlier output dropped: the command produced more than " +
		humanBytes(MaxOutputBytes) + "]\n" + string(w.buf)
}

// BackgroundTimeout is the ceiling on a detached command, so a runaway watcher
// cannot outlive the session indefinitely.
const BackgroundTimeout = 30 * time.Minute

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
