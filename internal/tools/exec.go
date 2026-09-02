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

	"evilcode/internal/tools/commandrisk"
)

// Exec holds the shell and search tools' shared state.
type Exec struct {
	Root string

	// ConfigDir and DataDir let the command-risk gate recognize application
	// state even when it lives outside the active workspace.
	ConfigDir string
	DataDir   string

	// ScratchDir is exported to bash children as TMPDIR and
	// EVILCODE_SCRATCH_DIR. Production wiring points it under the data dir;
	// tests and embedders may leave it empty to inherit the environment.
	ScratchDir string

	// EnvPassthrough names beyond the allowlist that model-run commands
	// inherit. Empty by default: the daemon's environment is not the
	// command's business (R2-16).
	EnvPassthrough []string

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

// maxGrepCaptureBytes bounds ripgrep's combined stdout and stderr before the
// structured result is parsed. --max-count is per file, not per invocation,
// so a broad search over a large tree can otherwise grow a bytes.Buffer until
// the process exhausts memory even though the tool will return only a small
// number of matches.
const maxGrepCaptureBytes = 2 << 20

type boundedCapture struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func newBoundedCapture(limit int) *boundedCapture {
	return &boundedCapture{limit: limit}
}

func (b *boundedCapture) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	original := len(p)
	remaining := b.limit - b.buf.Len()
	if remaining < len(p) {
		b.truncated = true
		if remaining < 0 {
			remaining = 0
		}
		p = p[:remaining]
	}
	_, _ = b.buf.Write(p)
	// Report the full input as consumed so ripgrep can finish normally; only
	// the in-memory diagnostic/result capture is intentionally bounded.
	return original, nil
}

func (b *boundedCapture) snapshot() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String(), b.truncated
}

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

// WithEnvPassthrough names environment variables beyond the allowlist that
// model-run shell commands inherit. Everything else the daemon carries —
// provider keys, harness secrets, unrelated build configuration — stays
// behind the boundary (R2-16).
func (e *Exec) WithEnvPassthrough(names []string) *Exec {
	e.EnvPassthrough = append([]string(nil), names...)
	return e
}

// WithRiskPaths supplies the application state roots used by the destructive
// command gate. Keeping these explicit makes tests and embedders deterministic
// without making the classifier read the host filesystem.
func (e *Exec) WithRiskPaths(configDir, dataDir string) *Exec {
	e.ConfigDir = configDir
	e.DataDir = dataDir
	return e
}

// Close cancels every running background task and waits a bounded grace period
// for the process groups to die. Wiring registers it so session teardown cannot
// leave detached commands mutating the workspace (F3).
func (e *Exec) Close() {
	if e.Bg != nil {
		e.Bg.Close()
	}
}

// envAllowlistKeys are the exact names a model-run shell command inherits.
// The daemon's own environment is full of things a shell command has no
// business reading — provider API keys, harness secrets, unrelated build
// configuration — and before this allowlist every one of them was one `env`
// away from the model (R2-16). `[features] env_passthrough` adds names
// explicitly.
var envAllowlistKeys = []string{
	// Process basics: without these almost nothing works.
	"PATH", "HOME", "SHELL", "USER", "LOGNAME", "TERM", "COLORTERM", "TZ", "LANG",
	// TMPDIR is re-exported as the scratch directory when one is configured,
	// and passed through as-is when it does not.
	"TMPDIR",
	// The agent socket lets a command reach the user's own ssh agent, which
	// is what a `git push` from a model-run command needs.
	"SSH_AUTH_SOCK", "SSH_AGENT_PID",
	// git identity set at the process level, so commits a command makes carry
	// the user's identity without reading a config file the model could edit.
	"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL",
}

// envAllowlistPrefixes are inherited by prefix: locale variants (LC_ALL, ...)
// and the XDG user-directory family.
var envAllowlistPrefixes = []string{"LC_", "XDG_"}

func envAllowed(name string) bool {
	for _, k := range envAllowlistKeys {
		if name == k {
			return true
		}
	}
	for _, p := range envAllowlistPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// commandEnv builds the environment a model-run command inherits: the
// allowlist, anything the user passed through by name, and the scratch
// directory override. It deliberately does not start from os.Environ — the
// daemon's environment is not the command's business.
func (e *Exec) commandEnv() []string {
	passthrough := make(map[string]bool, len(e.EnvPassthrough))
	for _, name := range e.EnvPassthrough {
		passthrough[name] = true
	}

	env := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		// TMPDIR is re-exported below when a scratch directory exists; the
		// harness's own EVILCODE_ variables never reach a command.
		if name == "EVILCODE_SCRATCH_DIR" || (name == "TMPDIR" && e.ScratchDir != "") {
			continue
		}
		if passthrough[name] || envAllowed(name) {
			env = append(env, entry)
		}
	}
	if e.ScratchDir != "" {
		if err := os.MkdirAll(e.ScratchDir, 0o700); err == nil {
			env = append(env, "TMPDIR="+e.ScratchDir, "EVILCODE_SCRATCH_DIR="+e.ScratchDir)
		} else {
			// The scratch directory is a convenience; without it the machine's
			// TMPDIR (filtered above) still reaches the command.
			if t, ok := os.LookupEnv("TMPDIR"); ok {
				env = append(env, "TMPDIR="+t)
			}
		}
	}
	return env
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
	Cmd           string `json:"cmd"`
	Timeout       int    `json:"timeout,omitempty"`
	Background    bool   `json:"background,omitempty"`
	Stdin         string `json:"stdin,omitempty"`
	Justification string `json:"justification,omitempty"`
}

func (e *Exec) bashTool() Tool {
	return Tool{
		Name:     "bash",
		Exposure: e.exposure,
		Desc: "Run a shell command in the workspace. Use this for builds, tests, formatters, git, " +
			"package commands, and other terminal work; use the dedicated file tools for " +
			"reading, searching, and editing. The working directory persists between calls, " +
			"so cd carries over. Output combines stdout and stderr. Set background for a " +
			"long-running command, then use bg wait; commands needing input can use stdin. " +
			"Do not use cat, sed, head, tail, grep, rg, or find for file inspection; use " +
			"read, grep, or glob instead. " +
			"After a mutation, use this to run the most relevant check and act on its output; " +
			"do not rerun an unchanged command just to fill a turn. Child temporary files use " +
			"the configured scratch directory. A destructive-command gate may hold a command " +
			"until you supply a justification; if it is refused, do not retry the same form. " +
			"If output is empty or suspiciously narrow, make one or two meaningful fallbacks " +
			"before adapting or reporting the blocker; never spin on the same failed call.",
		Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "cmd":     {"type": "string",  "description": "Shell command to run"},
    "timeout": {"type": "integer", "description": "Timeout in seconds; defaults to 120"},
    "background": {"type": "boolean",
                   "description": "Return immediately and report when it finishes. Use for long builds, watchers, and servers."},
    "stdin":    {"type": "string", "description": "Optional input written to the command's stdin"},
    "justification": {"type": "string", "description": "Substantive reason for a command held by the destructive-command safety gate"}
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

			workingDir := e.Cwd()
			_, verdict := commandrisk.Evaluate(a.Cmd, commandrisk.ContextFromPaths(e.Root, workingDir, e.ConfigDir, e.DataDir), a.Justification)
			switch verdict.Decision {
			case commandrisk.DecisionReflect:
				return Result{Output: verdict.Message, Intent: "held · justification required", Held: true}, fmt.Errorf("command held by destructive-command gate")
			case commandrisk.DecisionRefuse:
				return Result{Output: verdict.Message, Intent: "held · blocked", Held: true}, fmt.Errorf("command refused by destructive-command gate")
			}

			if a.Background {
				timeout := time.Duration(0)
				if a.Timeout > 0 {
					timeout = time.Duration(a.Timeout) * time.Second
				}
				return e.runBackground(a.Cmd, a.Stdin, timeout)
			}

			timeout := e.Timeout
			if a.Timeout > 0 {
				timeout = time.Duration(a.Timeout) * time.Second
			}

			// Taken before the timeout starts: a command must not spend its
			// budget waiting for the shell to be free.
			e.run.Lock()
			defer e.run.Unlock()
			workingDir = e.Cwd()

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
				// A timeout is a hard bound, not a handoff: the process group is
				// killed and the caller gets an error. Adopting the command as a
				// background task let a destructive/build/deploy command keep
				// mutating the machine for up to BackgroundTimeout after the model
				// was told it had exceeded the bound (F1).
				killProcessGroup(cmd)
				runErr := <-done
				result, _ := e.finishForeground(runErr, buf.String(), workingDir, marker)
				if ctx.Err() == context.DeadlineExceeded {
					return result, fmt.Errorf("command exceeded its %s timeout and was killed", timeout)
				}
				return result, ctx.Err()
			}
		},
	}
}

func (e *Exec) cleanBashOutput(output, marker string) string {
	return e.cleanBashOutputIfCurrent(output, marker, "")
}

func (e *Exec) cleanBashOutputIfCurrent(output, marker, expectedCwd string) string {
	if idx := strings.LastIndex(output, marker); idx >= 0 {
		newCwd := strings.TrimSpace(output[idx+len(marker):])
		output = strings.TrimRight(output[:idx], "\n")
		if newCwd != "" {
			e.mu.Lock()
			if expectedCwd == "" || e.cwd == expectedCwd {
				e.cwd = newCwd
			}
			e.mu.Unlock()
		}
	}
	return output
}

func (e *Exec) finishForeground(runErr error, output, workingDir, marker string) (Result, error) {
	output = Truncate(cleanProgressOutput(e.cleanBashOutput(output, marker)))
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
// outlive the tool call. A requested timeout still applies — a caller that
// asks for `timeout: 60` must not get a 30-minute ceiling by detaching — and
// zero means the BackgroundTimeout ceiling.
func (e *Exec) runBackground(command, stdin string, timeout time.Duration) (Result, error) {
	if e.Bg == nil {
		e.Bg = &Background{}
	}
	background := e.Bg
	workingDir := e.Cwd()
	task, ok := background.tryAddExplicit(shortCmd(command))
	if !ok {
		return Result{
			Output: fmt.Sprintf("background task limit reached (%d running); wait for or cancel an existing task before starting another", MaxRunningBackgroundTasks),
			Intent: "background limit reached",
			Held:   true,
		}, fmt.Errorf("background task limit reached")
	}
	if timeout <= 0 {
		timeout = BackgroundTimeout
	}
	background.setDeadline(task, time.Now().Add(timeout))

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		cmd := exec.Command("bash", "-c", command)
		cmd.Dir = workingDir
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
			background.finish(task, err.Error(), true)
			return
		}
		background.attach(task, &buf, func() { killProcessGroup(cmd) })
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			background.finish(task, Truncate(buf.String()), err != nil)
		case <-ctx.Done():
			killProcessGroup(cmd)
			err := <-done
			background.finish(task, Truncate(buf.String()), err != nil)
		}
	}()

	return Result{
		Output: fmt.Sprintf("started in the background as task %d; "+
			"output is retained there — use bg status/output/wait to inspect it when it finishes", task.ID),
		Intent: "bg: " + shortCmd(command),
	}, nil
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
	lineBuf  []byte
	progress Progress
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
		w.observeProgressLocked(p)
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
	w.observeProgressLocked(p)
	return n, nil
}

const maxProgressLineBytes = 64 << 10

func (w *ringWriter) observeProgressLocked(p []byte) {
	for len(p) > 0 {
		idx := bytes.IndexByte(p, '\n')
		if idx < 0 {
			w.appendProgressFragmentLocked(p)
			return
		}
		w.appendProgressFragmentLocked(p[:idx])
		w.recordProgressBytesLocked(w.lineBuf)
		w.lineBuf = w.lineBuf[:0]
		p = p[idx+1:]
	}
}

func (w *ringWriter) appendProgressFragmentLocked(fragment []byte) {
	if len(fragment) >= maxProgressLineBytes {
		w.lineBuf = append(w.lineBuf[:0], fragment[len(fragment)-maxProgressLineBytes:]...)
		return
	}
	if overflow := len(w.lineBuf) + len(fragment) - maxProgressLineBytes; overflow > 0 {
		w.lineBuf = append(w.lineBuf[:0], w.lineBuf[overflow:]...)
	}
	w.lineBuf = append(w.lineBuf, fragment...)
}

func (w *ringWriter) recordProgressLineLocked(line string) {
	if !looksLikeProgressLine(line) {
		return
	}
	if progress, ok := parseProgressLine(line); ok {
		w.progress = progress
	}
}

func (w *ringWriter) recordProgressBytesLocked(line []byte) {
	if !looksLikeProgressBytes(line) {
		return
	}
	if progress, ok := parseProgressLine(string(line)); ok {
		w.progress = progress
	}
}

// Progress returns the latest marker observed, including markers that have
// already scrolled out of the bounded live output tail.
func (w *ringWriter) Progress() Progress {
	w.mu.Lock()
	defer w.mu.Unlock()
	progress := w.progress
	if len(w.lineBuf) > 0 {
		if current := parseProgress(string(w.lineBuf)); current.Known {
			progress = current
		}
	}
	return progress
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
		return cmd[:backToRuneBoundary(cmd, 47)] + "…"
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
		Effect:   EffectReadOnly,
		Exposure: e.exposure,
		Desc: "Search file contents with a regular expression, via ripgrep. Use this for " +
			"content or symbol lookup; use glob for filenames. Narrow with path or glob. " +
			"Returns matching lines with their file, line number, and enclosing symbol. " +
			"With a file path and no pattern, returns that file's symbol outline. Avoid repeating " +
			"the same search after its result already answers the implementation question.",
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
			args := []string{"--null", "--line-number", "--with-filename", "--color=never", "--sort", "path", "--max-count=50"}
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
			capture := newBoundedCapture(maxGrepCaptureBytes)
			cmd.Stdout = capture
			cmd.Stderr = capture
			err := cmd.Run()

			out, captureTruncated := capture.snapshot()
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
			if captureTruncated {
				note = fmt.Sprintf("\n[search output exceeded %s; narrow the path or pattern]", humanBytes(maxGrepCaptureBytes))
			}
			matchCount := 0
			for _, record := range records {
				if !record.Context {
					matchCount++
				}
			}
			if matchCount > limit {
				note += fmt.Sprintf("\n[%d more matching lines; narrow the pattern]", matchCount-limit)
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
			symbols := e.resolveGrepSymbols(ctx, maxLines)
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
