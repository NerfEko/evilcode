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

	// ScratchDir is exported to bash children as TMPDIR and
	// EVILCODE_SCRATCH_DIR. Production wiring points it under the data dir;
	// tests and embedders may leave it empty to inherit the environment.
	ScratchDir string

	// lspServer enriches grep hits with document symbols when a language
	// server is configured for the hit's file. It is optional: the declaration
	// scanner is deliberately the fallback for repositories without one.
	lspServer LSPServer

	// exposure is shared with the filesystem tools for one session.
	exposure *Exposure

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
	return &Exec{Root: root, Timeout: DefaultTimeout, cwd: root, Bg: &Background{}, exposure: NewExposure()}
}

// WithLSP lets grep ask the configured language server for enclosing symbols.
// Starting a server remains lazy because a session that never searches should
// not pay the indexing cost.
func (e *Exec) WithLSP(server LSPServer) *Exec {
	e.lspServer = server
	return e
}

// WithExposure shares a session's shown-range ledger with filesystem tools.
func (e *Exec) WithExposure(exposure *Exposure) *Exec {
	e.exposure = exposure
	return e
}

// WithScratchDir keeps large child-created temporary files off the machine's
// RAM-backed /tmp when a session supplies a durable data directory.
func (e *Exec) WithScratchDir(dir string) *Exec {
	e.ScratchDir = dir
	return e
}

func (e *Exec) commandEnv() []string {
	env := os.Environ()
	if e.ScratchDir == "" {
		return env
	}
	if err := os.MkdirAll(e.ScratchDir, 0o700); err != nil {
		return env
	}
	filtered := env[:0]
	for _, entry := range env {
		if strings.HasPrefix(entry, "TMPDIR=") || strings.HasPrefix(entry, "EVILCODE_SCRATCH_DIR=") {
			continue
		}
		filtered = append(filtered, entry)
	}
	return append(filtered, "TMPDIR="+e.ScratchDir, "EVILCODE_SCRATCH_DIR="+e.ScratchDir)
}

// Cwd reports the working directory subsequent commands will run in.
func (e *Exec) Cwd() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cwd
}

// Tools returns the shell and search tools.
func (e *Exec) Tools() Set {
	return Set{e.bashTool(), e.grepTool(), e.bgTool()}
}

type bashArgs struct {
	Cmd        string `json:"cmd"`
	Timeout    int    `json:"timeout,omitempty"`
	Background bool   `json:"background,omitempty"`
	Stdin      string `json:"stdin,omitempty"`
}

func (e *Exec) bashTool() Tool {
	return Tool{
		Name:     "bash",
		Exposure: e.exposure,
		Desc: "Run a shell command in the workspace. The working directory persists " +
			"between calls, so cd carries over. Output is combined stdout and stderr. " +
			"Commands needing input can use stdin; child temporary files use the configured scratch directory.",
		Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "cmd":     {"type": "string",  "description": "Shell command to run"},
    "timeout": {"type": "integer", "description": "Timeout in seconds; defaults to 120"},
    "background": {"type": "boolean",
                   "description": "Return immediately and report when it finishes. Use for long builds, watchers, and servers."},
    "stdin":    {"type": "string", "description": "Optional input written to the command's stdin"}
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
				return e.runBackground(a.Cmd, a.Stdin), nil
			}

			timeout := e.Timeout
			if a.Timeout > 0 {
				timeout = time.Duration(a.Timeout) * time.Second
			}

			// Taken before the timeout starts: a command must not spend its
			// budget waiting for the shell to be free.
			e.run.Lock()
			defer e.run.Unlock()
			workingDir := e.Cwd()

			ctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			// The directory is read once and written back after, so a `cd` in
			// the command carries to the next call. Appending `pwd` on a
			// sentinel line is how the new directory comes back out.
			const marker = "__evilcode_cwd__"
			script := a.Cmd + "\n__evilcode_status=$?\nprintf '\\n" + marker + "%s\\n' \"$PWD\"\nexit $__evilcode_status"

			var buf ringWriter
			cmd := exec.Command("bash", "-c", script)
			cmd.Dir = workingDir
			cmd.Env = e.commandEnv()
			if a.Stdin != "" {
				cmd.Stdin = strings.NewReader(a.Stdin)
			}
			cmd.Stdout = &buf
			cmd.Stderr = &buf
			setProcessGroup(cmd)
			cmd.WaitDelay = 2 * time.Second
			if err := cmd.Start(); err != nil {
				return Result{}, err
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()

			select {
			case runErr := <-done:
				return e.finishForeground(runErr, buf.String(), workingDir, marker)
			case <-ctx.Done():
				if ctx.Err() == context.DeadlineExceeded {
					if e.Bg == nil {
						e.Bg = &Background{}
					}
					task := e.Bg.add(shortCmd(a.Cmd))
					e.Bg.attach(task, &buf, func() { killProcessGroup(cmd) })
					go func() {
						runErr := <-done
						out := Truncate(e.cleanBashOutput(buf.String(), marker))
						e.Bg.finish(task, out, runErr != nil)
					}()
					return Result{
						Output: fmt.Sprintf("command exceeded %s and is still running as background task %d; output is accumulating there — use bg status/output/wait, and do not re-run it", timeout, task.ID),
						Intent: fmt.Sprintf("adopted as background task %d", task.ID),
					}, nil
				}
				killProcessGroup(cmd)
				runErr := <-done
				result, _ := e.finishForeground(runErr, buf.String(), workingDir, marker)
				return result, ctx.Err()
			}
		},
	}
}

func (e *Exec) cleanBashOutput(output, marker string) string {
	if idx := strings.LastIndex(output, marker); idx >= 0 {
		newCwd := strings.TrimSpace(output[idx+len(marker):])
		output = strings.TrimRight(output[:idx], "\n")
		if newCwd != "" {
			e.mu.Lock()
			e.cwd = newCwd
			e.mu.Unlock()
		}
	}
	return output
}

func (e *Exec) finishForeground(runErr error, output, workingDir, marker string) (Result, error) {
	output = Truncate(e.cleanBashOutput(output, marker))
	if runErr != nil {
		return Result{
			Output: output,
			Intent: bashIntent(exitStatus(runErr), output),
			Shown:  RangesFromBash(output, workingDir),
		}, fmt.Errorf("exit status %s", exitStatus(runErr))
	}
	intent := bashIntent("0", output)
	if strings.TrimSpace(output) == "" {
		output = "(no output)"
	}
	return Result{Output: output, Intent: intent, Shown: RangesFromBash(output, workingDir)}, nil
}

// runBackground starts a detached command and returns immediately.
//
// It deliberately does not inherit the caller's context: the point is to
// outlive the tool call. It gets a generous ceiling of its own instead, so a
// runaway watcher still cannot run forever.
func (e *Exec) runBackground(command, stdin string) Result {
	if e.Bg == nil {
		e.Bg = &Background{}
	}
	task := e.Bg.add(shortCmd(command))

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), BackgroundTimeout)
		defer cancel()

		cmd := exec.Command("bash", "-c", command)
		cmd.Dir = e.Cwd()
		cmd.Env = e.commandEnv()
		if stdin != "" {
			cmd.Stdin = strings.NewReader(stdin)
		}
		var buf ringWriter
		cmd.Stdout = &buf
		cmd.Stderr = &buf
		cmd.WaitDelay = 2 * time.Second
		setProcessGroup(cmd)
		if err := cmd.Start(); err != nil {
			e.Bg.finish(task, err.Error(), true)
			return
		}
		e.Bg.attach(task, &buf, func() { killProcessGroup(cmd) })
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			e.Bg.finish(task, Truncate(buf.String()), err != nil)
		case <-ctx.Done():
			killProcessGroup(cmd)
			err := <-done
			e.Bg.finish(task, Truncate(buf.String()), err != nil)
		}
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
	start    int
	size     int
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
	if n == 0 {
		return 0, nil
	}
	if w.buf == nil {
		w.buf = make([]byte, MaxOutputBytes)
	}
	if n >= MaxOutputBytes {
		copy(w.buf, p[n-MaxOutputBytes:])
		w.start = 0
		w.size = MaxOutputBytes
		w.overflow = true
		return n, nil
	}
	if drop := w.size + n - MaxOutputBytes; drop > 0 {
		w.start = (w.start + drop) % MaxOutputBytes
		w.size -= drop
		w.overflow = true
	}
	end := (w.start + w.size) % MaxOutputBytes
	first := min(n, MaxOutputBytes-end)
	copy(w.buf[end:end+first], p[:first])
	copy(w.buf[:n-first], p[first:])
	w.size += n
	return n, nil
}

func (w *ringWriter) snapshotLocked() []byte {
	if w.size == 0 {
		return nil
	}
	out := make([]byte, w.size)
	first := min(w.size, MaxOutputBytes-w.start)
	copy(out, w.buf[w.start:w.start+first])
	copy(out[first:], w.buf[:w.size-first])
	return out
}

func (w *ringWriter) tailLocked(n int) []byte {
	if n > w.size {
		n = w.size
	}
	if n <= 0 {
		return nil
	}
	start := (w.start + w.size - n) % MaxOutputBytes
	out := make([]byte, n)
	first := min(n, MaxOutputBytes-start)
	copy(out, w.buf[start:start+first])
	copy(out[first:], w.buf[:n-first])
	return out
}

// String returns what was kept, marked if anything was dropped. Foreground
// completion uses this full ring so the ordinary Result truncator can retain
// both the head and tail of a command's output.
func (w *ringWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	buf := w.snapshotLocked()
	if !w.overflow {
		return string(buf)
	}
	return "[earlier output dropped: the command produced more than " +
		humanBytes(MaxOutputBytes) + "]\n" + string(buf)
}

// Tail returns a bounded live snapshot. Keeping the live snapshot at the tool
// result limit is important: a fast writer can otherwise make every 200ms
// progress refresh allocate a megabyte even though the model can only receive
// 50 KiB.
func (w *ringWriter) Tail() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.size <= MaxResultBytes && !w.overflow {
		return string(w.snapshotLocked())
	}
	buf := w.tailLocked(MaxResultBytes)
	tailStart := 0
	for tailStart < len(buf) && !utf8Start(buf[tailStart]) {
		tailStart++
	}
	prefix := "[output tail kept: the command produced more than " + humanBytes(MaxResultBytes) + "]\n"
	if w.overflow {
		prefix = "[earlier output dropped: the command produced more than " + humanBytes(MaxOutputBytes) + "]\n"
	}
	return prefix + string(buf[tailStart:])
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

// bashIntent is the bash tool row's intent: the exit status and the captured
// output size. It deliberately is *not* the command — the command is already
// the row's target (toolTarget reads the `cmd` arg). Repeating it as the intent
// made the row print the command twice, because the dedupe guard in applyEvent
// (`!strings.Contains(intent, target)`) cannot hold when a 48-char intent is
// asked to contain a 60-char target. The intent carries information the row
// does not already have instead (§F2.1).
func bashIntent(exit string, out string) string {
	return fmt.Sprintf("exit %s · %s out", exit, humanBytes(int64(len(out))))
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
		Name:     "grep",
		Exposure: e.exposure,
		Desc: "Search file contents with a regular expression, via ripgrep. " +
			"Returns matching lines with their file, line number, and enclosing symbol. " +
			"With a file path and no pattern, returns that file's symbol outline.",
		Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "pattern": {"type": "string",  "description": "Regular expression to search for"},
    "path":    {"type": "string",  "description": "File or directory to search; defaults to the workspace root"},
    "glob":    {"type": "string",  "description": "Restrict to files matching this glob, e.g. '*.go'"},
    "context": {"type": "integer", "description": "Lines of context around each match"},
    "limit":   {"type": "integer", "description": "Maximum matching lines to return"}
  },
  "required": []
}`),
		Run: func(ctx context.Context, raw json.RawMessage) (Result, error) {
			var a grepArgs
			if err := unmarshalArgs(raw, &a); err != nil {
				return Result{}, err
			}
			target := e.Root
			if a.Path != "" {
				if !filepath.IsAbs(a.Path) {
					target = filepath.Join(e.Root, a.Path)
				} else {
					target = a.Path
				}
			}
			if a.Pattern == "" {
				if a.Path == "" {
					return Result{}, fmt.Errorf("pattern is required unless path names a file for outline mode")
				}
				info, err := os.Stat(target)
				if err != nil {
					return Result{}, err
				}
				if info.IsDir() {
					return Result{}, fmt.Errorf("outline mode needs a file path; %s is a directory", a.Path)
				}
				return e.grepOutline(ctx, target, a.Path), nil
			}
			// Never reimplement ripgrep (plan.md §17).
			if _, err := exec.LookPath("rg"); err != nil {
				return Result{}, fmt.Errorf("ripgrep (rg) is not installed; the grep tool needs it")
			}

			limit := a.Limit
			if limit <= 0 {
				limit = 200
			}
			// --null separates the path from the line payload. Without it a
			// directory named `part:123` is indistinguishable from rg's
			// `path:line:text` output.
			args := []string{"--null", "--line-number", "--with-filename", "--color=never", "--max-count=50"}
			if a.Glob != "" {
				args = append(args, "--glob", a.Glob)
			}
			if a.Context > 0 {
				args = append(args, "--context", fmt.Sprint(a.Context))
			}
			args = append(args, "--regexp", a.Pattern)

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
			records := parseRGRecords(out)
			// ripgrep exits 1 for "no matches", which is an answer, not a fault.
			// An explicitly named binary file also exits 1 while emitting a
			// useful `binary file matches` record, so only call it no-match when
			// no record survived parsing.
			var ee *exec.ExitError
			if err != nil && errors.As(err, &ee) && ee.ExitCode() == 1 && len(records) == 0 {
				return Result{Output: "no matches for " + a.Pattern}, nil
			}
			if err != nil && !(errors.As(err, &ee) && ee.ExitCode() == 1 && len(records) > 0) {
				return Result{Output: out}, fmt.Errorf("ripgrep failed: %w", err)
			}

			note := ""
			matchCount := 0
			for _, record := range records {
				if !record.Context {
					matchCount++
				}
			}
			if matchCount > limit {
				note = fmt.Sprintf("\n[%d more matching lines; narrow the pattern]", matchCount-limit)
				kept := make([]grepRecord, 0, len(records))
				pendingContext := make([]grepRecord, 0)
				seen := 0
				group := records[0].Group
				retainedGroup := group
				for _, record := range records {
					if record.Group != group {
						// Flush only the trailing context of the last retained
						// group. Leading context in the next group belongs to a
						// match that may be omitted by the limit.
						if group == retainedGroup {
							kept = append(kept, pendingContext...)
						}
						pendingContext = pendingContext[:0]
						group = record.Group
					}
					if record.Context {
						pendingContext = append(pendingContext, record)
						continue
					}
					if seen >= limit {
						// These lines trail the last retained match and are still
						// part of its requested context. Drop only the omitted
						// match itself and anything after it.
						if group == retainedGroup {
							kept = append(kept, pendingContext...)
						}
						break
					}
					kept = append(kept, pendingContext...)
					pendingContext = pendingContext[:0]
					seen++
					retainedGroup = group
					kept = append(kept, record)
				}
				records = kept
			}
			lines := make([]string, 0, len(records))
			shown := make([]LineRange, 0, len(records))
			symbols := make(map[string][]grepSymbol)
			lspUnavailable := make(map[string]bool)
			maxLines := make(map[string]int)
			for _, record := range records {
				if record.Binary {
					continue
				}
				path := record.Path
				if !filepath.IsAbs(path) {
					path = filepath.Join(e.Root, path)
				}
				if record.Line > maxLines[path] {
					maxLines[path] = record.Line
				}
			}
			for _, record := range records {
				if record.Binary {
					rel := strings.TrimPrefix(record.Path, e.Root+string(filepath.Separator))
					if filepath.IsAbs(rel) {
						rel = filepath.Clean(rel)
					}
					lines = append(lines, fmt.Sprintf("%s: %s", rel, record.Text))
					continue
				}
				// Paths come back absolute; relative reads better and costs fewer
				// tokens. Context records are enriched too, so --context remains
				// useful without reverting to unstructured output.
				rel := strings.TrimPrefix(record.Path, e.Root+string(filepath.Separator))
				if filepath.IsAbs(rel) {
					rel = filepath.Clean(rel)
				}
				path := record.Path
				if !filepath.IsAbs(path) {
					path = filepath.Join(e.Root, path)
				}
				shown = append(shown, LineRange{Path: path, Start: record.Line, End: record.Line})
				if e.exposure != nil && e.exposure.Contains(path, record.Line) {
					rel := strings.TrimPrefix(record.Path, e.Root+string(filepath.Separator))
					if filepath.IsAbs(rel) {
						rel = filepath.Clean(rel)
					}
					lines = append(lines, fmt.Sprintf("%s:%d — shown above", rel, record.Line))
					continue
				}
				if _, ok := symbols[path]; !ok {
					symbols[path] = e.grepSymbols(ctx, path, lspUnavailable, maxLines[path])
				}
				symbol := enclosingGrepSymbol(symbols[path], record.Line)
				label := "top level"
				if symbol != "" {
					label = symbol
				}
				if record.Context {
					label = "context · " + label
				}
				lines = append(lines, fmt.Sprintf("%s:%d: [%s] %s", rel, record.Line, label, record.Text))
			}
			return Result{
				Output: strings.Join(lines, "\n") + note,
				Intent: fmt.Sprintf("%d matches for %s", matchCount, a.Pattern),
				Shown:  shown,
			}, nil
		},
	}
}
