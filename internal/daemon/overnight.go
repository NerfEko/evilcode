package daemon

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"evilcode/internal/config"
)

const (
	overnightMaxTurns = 40
	overnightBudget   = 400_000
	overnightHours    = 8
	overnightMaxStall = 3
)

const overnightPrompt = `Continue working through the todo list.

Nobody is watching. Do not ask questions — there is no one to answer them. If
you reach something that genuinely needs a decision, mark the item blocked, say
why in one line, and move to the next item.

Work one item at a time and verify each before marking it done. If you cannot
verify it, leave it in progress and say what is missing.`

// overnightState is deliberately runtime state, not TUI state. The daemon is
// the owner of the turn loop, so closing every window cannot strand the next
// prompt or reset its budget.
type overnightState struct {
	mu sync.Mutex

	active   bool
	started  time.Time
	deadline time.Time
	turns    int
	tokens   int
	stalled  int
	lastTodo string
	stopped  string
}

func newOvernightState() *overnightState { return &overnightState{} }

func (o *overnightState) start(now time.Time) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.active {
		return fmt.Errorf("overnight is already active")
	}
	o.active = true
	o.started = now
	o.deadline = now.Add(overnightHours * time.Hour)
	o.turns, o.tokens, o.stalled = 0, 0, 0
	o.lastTodo, o.stopped = "", ""
	return nil
}

func (o *overnightState) stop(reason string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.active {
		return false
	}
	o.active = false
	o.stopped = reason
	return true
}

func (o *overnightState) isActive() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.active
}

func (o *overnightState) addTokens(n int) {
	if n <= 0 {
		return
	}
	o.mu.Lock()
	if o.active {
		o.tokens += n
	}
	o.mu.Unlock()
}

func (o *overnightState) afterTurn(now time.Time, todoState string) (bool, string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.active {
		return false, o.stopped
	}
	o.turns++
	switch {
	case o.turns >= overnightMaxTurns:
		return o.haltLocked(fmt.Sprintf("reached the %d-turn cap", overnightMaxTurns))
	case o.tokens >= overnightBudget:
		return o.haltLocked(fmt.Sprintf("spent the %d-token budget", overnightBudget))
	case now.After(o.deadline):
		return o.haltLocked("ran out of time")
	}
	if todoState == o.lastTodo {
		o.stalled++
		if o.stalled >= overnightMaxStall {
			return o.haltLocked(fmt.Sprintf("the todo list has not moved in %d turns", o.stalled))
		}
	} else {
		o.stalled = 0
		o.lastTodo = todoState
	}
	if strings.TrimSpace(todoState) == "" {
		return o.haltLocked("there is no todo list to work through")
	}
	var done, total int
	if _, err := fmt.Sscanf(todoState, "%d/%d done", &done, &total); err == nil && total > 0 && done >= total {
		return o.haltLocked("the todo list is finished")
	}
	return true, ""
}

func (o *overnightState) haltLocked(reason string) (bool, string) {
	o.active = false
	o.stopped = reason
	return false, reason
}

func (o *overnightState) status() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.active {
		if o.stopped != "" {
			return fmt.Sprintf("⏳ Overnight stopped after %d turns: %s", o.turns, o.stopped)
		}
		return "⏳ Overnight is off · /overnight to start a supervised long run"
	}
	return fmt.Sprintf("⏳ Overnight · turn %d/%d · %d tokens · until %s",
		o.turns, overnightMaxTurns, o.tokens, o.deadline.Format("15:04"))
}

// snapshot returns the durable report inputs without exposing the state's
// mutex to callers that also need to inspect the session. Reports are written
// by the daemon, not by a TUI, so a closed window cannot take the only copy of
// the run summary with it.
func (o *overnightState) snapshot() (active bool, started, deadline time.Time,
	turns, tokens int, stopped, todo string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.active, o.started, o.deadline, o.turns, o.tokens, o.stopped, o.lastTodo
}

// writeOvernightReport persists a small self-contained report for a detached
// run. The interactive TUI still renders its richer report when it owns the
// run, but the server must provide a useful artifact when every client is
// disconnected. The report intentionally contains no tool output or secrets;
// the session log remains the source of truth for that detail.
func (sess *Session) writeOvernightReport() (string, error) {
	if sess.overnight == nil {
		return "", nil
	}
	active, started, deadline, turns, tokens, stopped, lastTodo := sess.overnight.snapshot()
	if started.IsZero() {
		return "", nil
	}
	sess.mu.Lock()
	name, cwd := sess.Name, sess.Cwd
	sess.mu.Unlock()
	if sess.built != nil && sess.built.Todos != nil {
		lastTodo = sess.built.Todos.Summary()
	}
	dir := filepath.Join(config.DataDir(), "overnight-reports")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprintf("%s-%d.html", name, time.Now().UnixNano()))
	state := "stopped"
	if active {
		state = "active"
	}
	if stopped == "" && !active {
		stopped = "not recorded"
	}
	document := fmt.Sprintf(`<!doctype html>
<meta charset="utf-8">
<title>evilcode overnight report</title>
<style>body{font:16px system-ui,sans-serif;max-width:52rem;margin:2rem auto;padding:0 1rem;background:#17151f;color:#eee}dt{color:#aaa;margin-top:1rem}dd{margin:.25rem 0 0;white-space:pre-wrap}code{color:#9fd3ff}</style>
<h1>Overnight run</h1>
<dl>
<dt>Session</dt><dd><code>%s</code></dd>
<dt>Workspace</dt><dd><code>%s</code></dd>
<dt>State</dt><dd>%s</dd>
<dt>Started</dt><dd>%s</dd>
<dt>Deadline</dt><dd>%s</dd>
<dt>Turns</dt><dd>%d / %d</dd>
<dt>Tokens</dt><dd>%d / %d</dd>
<dt>Stop reason</dt><dd>%s</dd>
<dt>Todo state at report time</dt><dd>%s</dd>
</dl>
<p>The complete conversation and tool history remain in the session log.</p>
	`, html.EscapeString(name), html.EscapeString(cwd), state,
		started.Format(time.RFC3339), deadline.Format(time.RFC3339), turns, overnightMaxTurns,
		tokens, overnightBudget, html.EscapeString(stopped), html.EscapeString(lastTodo))
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func isOvernightRequest(id string) bool {
	return strings.HasPrefix(id, "overnight-")
}
