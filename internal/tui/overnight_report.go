package tui

import (
	"context"
	"fmt"
	htmlpkg "html"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"evilcode/internal/agent"
	"evilcode/internal/todo"
)

// GitSnapshot is the small, stable slice of repository state that makes an
// unattended run auditable. Dirty contains porcelain lines rather than just a
// boolean so the report can say exactly what was already changed at launch.
type GitSnapshot struct {
	Root       string
	Branch     string
	Head       string
	Dirty      []string
	CapturedAt time.Time
	Error      string
}

// OvernightPreflight is the immutable run contract captured before the first
// hidden turn. It records both the worklist and the limits that governed it.
type OvernightPreflight struct {
	Started  time.Time
	MaxTurns int
	Budget   int
	Deadline time.Time
	Git      GitSnapshot
	Todos    []todo.Item
}

// OvernightToolCheck is one tool result that happened during a turn. Output
// is intentionally bounded: the report should remain useful when a command
// prints a large log, and it must not become a second transcript dump.
type OvernightToolCheck struct {
	Name    string
	Command string
	Intent  string
	Output  string
	Error   string
	Success bool
}

// OvernightTimelineEntry records one completed hidden turn.
type OvernightTimelineEntry struct {
	Turn          int
	At            time.Time
	Spent         int
	BeforeSummary string
	AfterSummary  string
	Changes       []string
	Tools         []OvernightToolCheck
}

// OvernightTaskCard is the evidence card for one todo touched by a turn.
// Completed work is only validated when it has both a quality score and a
// successful verification-shaped tool result.
type OvernightTaskCard struct {
	Turn       int
	ID         string
	Content    string
	Change     string
	Before     todo.Item
	After      todo.Item
	Validated  bool
	Validation string
	Tools      []OvernightToolCheck
}

// GitDiffStat describes changes made relative to the preflight HEAD.
type GitDiffStat struct {
	Files   int
	Added   int
	Removed int
	Summary string
	Error   string
}

const overnightReportOutputLimit = 900

func cloneGitSnapshot(in GitSnapshot) GitSnapshot {
	in.Dirty = append([]string(nil), in.Dirty...)
	return in
}

func cloneTodoItems(items []todo.Item) []todo.Item {
	if len(items) == 0 {
		return nil
	}
	out := make([]todo.Item, len(items))
	for i, item := range items {
		out[i] = cloneTodoItem(item)
	}
	return out
}

func cloneTodoItem(in todo.Item) todo.Item {
	out := in
	out.BlockedBy = append([]string(nil), in.BlockedBy...)
	out.ConfidenceHistory = append([]uint8(nil), in.ConfidenceHistory...)
	if in.Group != nil {
		v := *in.Group
		out.Group = &v
	}
	if in.Confidence != nil {
		v := *in.Confidence
		out.Confidence = &v
	}
	if in.CompletionConfidence != nil {
		v := *in.CompletionConfidence
		out.CompletionConfidence = &v
	}
	return out
}

func cloneToolChecks(in []OvernightToolCheck) []OvernightToolCheck {
	return append([]OvernightToolCheck(nil), in...)
}

// RecordTurn folds the completed turn into the report state before the hard
// breaker is evaluated. This ordering means the final turn is never omitted
// just because it also hit the cap.
func (o *Overnight) RecordTurn(at time.Time, spent int, after []todo.Item) {
	if at.IsZero() {
		at = time.Now()
	}
	before := cloneTodoItems(o.BeforeTodos)
	after = cloneTodoItems(after)
	turn := o.Turns + 1
	delta := todo.DiffItems(before, after)
	changesByID := make(map[string]string, len(delta.Changes))
	for _, change := range delta.Changes {
		changesByID[change.Item.ID] = string(change.Change)
	}

	changed := changedTodoIDs(before, after)
	changeLabels := make([]string, 0, len(changed))
	for _, id := range changed {
		label := changesByID[id]
		if label == "" {
			label = string(todo.ChangeEdited)
		}
		changeLabels = append(changeLabels, fmt.Sprintf("%s: %s", id, label))
		old, hadOld := itemByID(before, id)
		current, hasCurrent := itemByID(after, id)
		card := OvernightTaskCard{
			Turn:   turn,
			ID:     id,
			Change: label,
			Before: cloneTodoItem(old),
			After:  cloneTodoItem(current),
			Tools:  cloneToolChecks(o.CurrentTools),
		}
		if hasCurrent {
			card.Content = current.Content
		} else if hadOld {
			card.Content = old.Content
		}
		card.Validated, card.Validation = taskValidation(current, hasCurrent, o.CurrentTools)
		o.Cards = append(o.Cards, card)
	}

	o.Timeline = append(o.Timeline, OvernightTimelineEntry{
		Turn:          turn,
		At:            at,
		Spent:         spent,
		BeforeSummary: summarizeItems(before),
		AfterSummary:  summarizeItems(after),
		Changes:       changeLabels,
		Tools:         cloneToolChecks(o.CurrentTools),
	})
	o.BeforeTodos = after
	o.FinalTodos = cloneTodoItems(after)
	o.CurrentTools = nil
}

func changedTodoIDs(before, after []todo.Item) []string {
	seen := make(map[string]bool, len(before)+len(after))
	var ids []string
	for _, item := range after {
		seen[item.ID] = true
		old, existed := itemByID(before, item.ID)
		if !existed || !reflect.DeepEqual(old, item) {
			ids = append(ids, item.ID)
		}
	}
	for _, item := range before {
		if seen[item.ID] {
			continue
		}
		if _, existed := itemByID(after, item.ID); !existed {
			ids = append(ids, item.ID)
		}
	}
	return ids
}

func itemByID(items []todo.Item, id string) (todo.Item, bool) {
	for _, item := range items {
		if item.ID == id {
			return item, true
		}
	}
	return todo.Item{}, false
}

func taskValidation(item todo.Item, exists bool, checks []OvernightToolCheck) (bool, string) {
	if !exists || item.Status != todo.StatusCompleted {
		return false, "item was touched but is not completed"
	}
	if item.CompletionConfidence == nil {
		return false, "done without a completion-confidence value"
	}
	if int(*item.CompletionConfidence) < todo.QualityGate {
		return false, fmt.Sprintf("completion confidence is %d/100, below %d", *item.CompletionConfidence, todo.QualityGate)
	}
	for _, check := range checks {
		if check.Success && validationTool(check) {
			label := check.Name
			if check.Command != "" {
				label += " " + check.Command
			}
			return true, "verified by " + truncateReport(label, 220)
		}
	}
	return false, "no successful verification command was recorded"
}

func validationTool(check OvernightToolCheck) bool {
	value := strings.ToLower(strings.Join([]string{check.Name, check.Command, check.Intent}, " "))
	for _, word := range []string{"test", "vet", "lint", "check", "verify", "build", "compile", "diff"} {
		if strings.Contains(value, word) {
			return true
		}
	}
	return strings.HasPrefix(value, "git_") && (strings.Contains(value, "status") || strings.Contains(value, "overview") || strings.Contains(value, "hunk"))
}

func overnightToolCheck(e agent.Event) OvernightToolCheck {
	check := OvernightToolCheck{Success: !e.IsError() && !e.Held}
	if e.Call == nil {
		check.Name = "tool"
	} else {
		check.Name = e.Call.Name
		check.Command = truncateReport(toolCommand(e.Call.Args), overnightReportOutputLimit)
		if check.Command == "" {
			check.Command = truncateReport(toolTarget(e.Call.Args), overnightReportOutputLimit)
		}
	}
	check.Intent = truncateReport(e.Intent, 300)
	check.Output = truncateReport(e.Output, overnightReportOutputLimit)
	check.Error = truncateReport(e.ErrText, overnightReportOutputLimit)
	if check.Error == "" && e.Err != nil {
		check.Error = truncateReport(e.Err.Error(), overnightReportOutputLimit)
	}
	return check
}

func truncateReport(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}

func summarizeItems(items []todo.Item) string {
	if len(items) == 0 {
		return "no todos"
	}
	var done, active, blocked int
	for _, item := range items {
		switch {
		case item.Status == todo.StatusCompleted || item.Status == todo.StatusCancelled:
			done++
		case len(item.BlockedBy) > 0:
			blocked++
		case item.Status == todo.StatusInProgress:
			active++
		}
	}
	result := fmt.Sprintf("%d/%d done", done, len(items))
	if active > 0 {
		result += fmt.Sprintf(", %d in progress", active)
	}
	if blocked > 0 {
		result += fmt.Sprintf(", %d blocked", blocked)
	}
	return result
}

func runGit(root string, args ...string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("workspace directory is empty")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, truncateReport(string(out), 300))
	}
	return strings.TrimSpace(string(out)), nil
}

func captureGit(root string) GitSnapshot {
	snapshot := GitSnapshot{Root: root, CapturedAt: time.Now()}
	if strings.TrimSpace(root) == "" {
		snapshot.Error = "workspace directory is empty"
		return snapshot
	}
	var issues []string
	if branch, err := runGit(root, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil {
		snapshot.Branch = branch
	} else if branch, fallbackErr := runGit(root, "rev-parse", "--abbrev-ref", "HEAD"); fallbackErr == nil {
		snapshot.Branch = branch
	} else {
		issues = append(issues, "branch: "+fallbackErr.Error())
	}
	if head, err := runGit(root, "rev-parse", "HEAD"); err == nil {
		snapshot.Head = head
	} else {
		issues = append(issues, "HEAD: "+err.Error())
	}
	if status, err := runGit(root, "status", "--porcelain=v1", "--untracked-files=all"); err == nil {
		if status != "" {
			snapshot.Dirty = strings.Split(status, "\n")
		}
	} else {
		issues = append(issues, "status: "+err.Error())
	}
	snapshot.Error = strings.Join(issues, "\n")
	return snapshot
}

func captureGitDiffStat(root, startHead string) GitDiffStat {
	stat := GitDiffStat{}
	args := []string{"diff", "--numstat"}
	if startHead != "" {
		args = append(args, startHead)
	}
	numstat, err := runGit(root, args...)
	if err != nil {
		stat.Error = err.Error()
		return stat
	}
	for _, line := range strings.Split(numstat, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		stat.Files++
		if n, err := strconv.Atoi(fields[0]); err == nil {
			stat.Added += n
		}
		if n, err := strconv.Atoi(fields[1]); err == nil {
			stat.Removed += n
		}
	}
	args = []string{"diff", "--stat"}
	if startHead != "" {
		args = append(args, startHead)
	}
	if summary, err := runGit(root, args...); err == nil {
		stat.Summary = summary
	} else if stat.Error == "" {
		stat.Error = err.Error()
	}
	if stat.Summary == "" {
		stat.Summary = "no tracked changes"
	}
	return stat
}

// WriteReport writes one complete report under the configured data directory
// and stores the path on the run so the TUI can name it and reopen it later.
func (o *Overnight) WriteReport(dataDir, cwd string, finalTodos []todo.Item) (string, error) {
	if strings.TrimSpace(dataDir) == "" {
		return "", fmt.Errorf("data directory is empty")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return "", err
	}
	if o.StoppedAt.IsZero() {
		o.StoppedAt = time.Now()
	}
	o.EndGit = captureGit(cwd)
	o.FinalTodos = cloneTodoItems(finalTodos)
	stat := captureGitDiffStat(cwd, o.Preflight.Git.Head)
	started := o.Started
	if started.IsZero() {
		started = time.Now()
		o.Started = started
	}
	path := filepath.Join(dataDir, "overnight-"+started.UTC().Format("20060102-150405.000000000")+".html")
	document := o.renderHTML(path, stat)
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		return "", err
	}
	o.ReportPath = path
	return path, nil
}

// LatestOvernightReport finds the last persisted report, allowing `/overnight
// report` to work after a reload as well as immediately after a stop.
func LatestOvernightReport(dataDir string) string {
	if strings.TrimSpace(dataDir) == "" {
		return ""
	}
	paths, err := filepath.Glob(filepath.Join(dataDir, "overnight-*.html"))
	if err != nil || len(paths) == 0 {
		return ""
	}
	sort.Slice(paths, func(i, j int) bool {
		left, leftErr := os.Stat(paths[i])
		right, rightErr := os.Stat(paths[j])
		if leftErr != nil || rightErr != nil {
			return paths[i] > paths[j]
		}
		return left.ModTime().After(right.ModTime())
	})
	return paths[0]
}

func (o *Overnight) renderHTML(path string, stat GitDiffStat) string {
	var b strings.Builder
	escape := func(value string) string { return htmlpkg.EscapeString(value) }
	started := o.Started
	if started.IsZero() {
		started = o.Preflight.Started
	}
	stopped := o.StoppedAt
	if stopped.IsZero() {
		stopped = time.Now()
	}
	duration := stopped.Sub(started)
	if duration < 0 {
		duration = 0
	}
	stopReason := o.Stopped
	if stopReason == "" {
		stopReason = "not recorded"
	}

	fmt.Fprintf(&b, `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>evilcode overnight report</title><style>
:root{color-scheme:dark;--bg:#101217;--panel:#191d25;--line:#303746;--text:#edf1f7;--muted:#9aa6b8;--green:#79d99a;--amber:#f0c36a;--red:#ff8d8d;--blue:#82b7ff}
*{box-sizing:border-box}body{margin:0;padding:2rem;background:var(--bg);color:var(--text);font:15px/1.5 system-ui,-apple-system,Segoe UI,sans-serif}main{max-width:1000px;margin:auto}h1{margin:0 0 .35rem}h2{margin:2rem 0 .65rem;border-bottom:1px solid var(--line);padding-bottom:.35rem}h3{margin:.2rem 0 .5rem}h4{margin:1rem 0 .35rem;color:var(--muted)}p{margin:.35rem 0}.lede,.muted{color:var(--muted)}.reason{color:var(--amber);font-weight:650}.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(190px,1fr));gap:.65rem}.panel,.task{background:var(--panel);border:1px solid var(--line);border-radius:8px;padding:1rem}.panel strong{display:block;color:var(--muted);font-size:.78rem;text-transform:uppercase;letter-spacing:.04em}.panel span{display:block;margin-top:.2rem;font-size:1.05rem}dl{display:grid;grid-template-columns:max-content 1fr;gap:.35rem 1.2rem;margin:0}dt{color:var(--muted)}dd{margin:0;overflow-wrap:anywhere}pre{white-space:pre-wrap;overflow-wrap:anywhere;background:#0b0d11;border:1px solid var(--line);border-radius:6px;padding:.7rem;margin:.45rem 0;color:#dce5f2}code{color:var(--blue)}.task{margin:.7rem 0}.task.validated{border-color:#367b51}.task.unvalidated{border-color:#9a6b2c}.badge{display:inline-block;border-radius:999px;padding:.1rem .5rem;font-size:.78rem;margin-left:.35rem}.badge.validated{background:#214d34;color:var(--green)}.badge.unvalidated{background:#5a3d18;color:var(--amber)}.badge.observed{background:#29354b;color:var(--blue)}ul,ol{padding-left:1.4rem}.timeline li{margin:.7rem 0}.empty{color:var(--muted);font-style:italic}.tool{margin:.35rem 0}.tool-error{color:var(--red)}.small{font-size:.88rem;color:var(--muted)}
</style></head><body><main>
<h1>Overnight report</h1><p class="lede">evilcode unattended work run · <span class="reason">stopped: %s</span></p>
<div class="grid">
<div class="panel"><strong>Started</strong><span>%s</span></div><div class="panel"><strong>Stopped</strong><span>%s</span></div>
<div class="panel"><strong>Duration</strong><span>%s</span></div><div class="panel"><strong>Turns</strong><span>%d / %d</span></div>
<div class="panel"><strong>Tokens</strong><span>%s / %s</span></div><div class="panel"><strong>Deadline</strong><span>%s</span></div>
</div>
<h2>Preflight</h2><dl><dt>Todo list</dt><dd>%s</dd><dt>Budget</dt><dd>%s tokens</dd><dt>Starting branch</dt><dd>%s</dd><dt>Starting HEAD</dt><dd><code>%s</code></dd></dl>
<h3>Starting dirty files</h3>%s
<h2>Git result</h2><dl><dt>Stopping branch</dt><dd>%s</dd><dt>Stopping HEAD</dt><dd><code>%s</code></dd><dt>Diffstat</dt><dd>%d files · +%d additions · -%d deletions</dd></dl>
<pre>%s</pre>%s
<h2>Task cards</h2>%s
<h2>Timeline</h2>%s
<p class="small">Report path: %s</p>
</main></body></html>`,
		escape(stopReason), formatTime(started), formatTime(stopped), duration.Round(time.Second),
		o.Turns, o.MaxTurns, humanTokens(o.Tokens), humanTokens(o.Budget), formatTime(o.Deadline),
		summarizeItems(o.Preflight.Todos), humanTokens(o.Preflight.Budget), escape(o.Preflight.Git.Branch),
		escape(o.Preflight.Git.Head), renderDirty(o.Preflight.Git.Dirty), escape(o.EndGit.Branch),
		escape(o.EndGit.Head), stat.Files, stat.Added, stat.Removed, escape(stat.Summary), renderGitError(stat.Error, escape),
		renderTaskCards(o.Cards, escape), renderTimeline(o.Timeline, escape), escape(path))
	return b.String()
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return "—"
	}
	return value.Local().Format("2006-01-02 15:04:05 MST")
}

func renderDirty(files []string) string {
	if len(files) == 0 {
		return `<p class="empty">clean</p>`
	}
	return "<pre>" + htmlpkg.EscapeString(strings.Join(files, "\n")) + "</pre>"
}

func renderGitError(err string, escape func(string) string) string {
	if err == "" {
		return ""
	}
	return `<p class="tool-error">Git capture note: ` + escape(err) + `</p>`
}

func renderTaskCards(cards []OvernightTaskCard, escape func(string) string) string {
	if len(cards) == 0 {
		return `<p class="empty">No todo item changed during the run.</p>`
	}
	var b strings.Builder
	for _, card := range cards {
		label, class := "observed", ""
		if card.After.Status == todo.StatusCompleted {
			if card.Validated {
				label, class = "validated", "validated"
			} else {
				label, class = "unvalidated", "unvalidated"
			}
		}
		fmt.Fprintf(&b, `<article class="task %s"><h3>%s · %s <span class="badge %s">%s</span></h3><p><strong>Turn %d · %s</strong></p><p>Before: <code>%s</code> · After: <code>%s</code></p><p>Validation: %s</p><h4>What ran</h4>%s</article>`,
			escape(class), escape(card.ID), escape(card.Content), escape(class), escape(label), card.Turn,
			escape(card.Change), escape(itemState(card.Before)), escape(itemState(card.After)), escape(card.Validation),
			renderTools(card.Tools, escape))
	}
	return b.String()
}

func itemState(item todo.Item) string {
	if item.ID == "" && item.Content == "" && item.Status == "" {
		return "not present"
	}
	return string(item.Status)
}

func renderTools(checks []OvernightToolCheck, escape func(string) string) string {
	if len(checks) == 0 {
		return `<p class="empty">No tool result recorded for this todo.</p>`
	}
	var b strings.Builder
	for _, check := range checks {
		state := "success"
		className := "tool"
		if !check.Success {
			state, className = "failed", "tool tool-error"
		}
		command := check.Command
		if command == "" {
			command = check.Name
		}
		fmt.Fprintf(&b, `<div class="%s"><code>%s</code> · %s`, className, escape(command), escape(state))
		if check.Intent != "" {
			fmt.Fprintf(&b, ` · %s`, escape(check.Intent))
		}
		if check.Output != "" {
			fmt.Fprintf(&b, `<pre>%s</pre>`, escape(check.Output))
		}
		if check.Error != "" {
			fmt.Fprintf(&b, `<pre class="tool-error">%s</pre>`, escape(check.Error))
		}
		b.WriteString(`</div>`)
	}
	return b.String()
}

func renderTimeline(entries []OvernightTimelineEntry, escape func(string) string) string {
	if len(entries) == 0 {
		return `<p class="empty">No completed turn was recorded.</p>`
	}
	var b strings.Builder
	b.WriteString(`<ol class="timeline">`)
	for _, entry := range entries {
		changes := "none"
		if len(entry.Changes) > 0 {
			changes = strings.Join(entry.Changes, ", ")
		}
		fmt.Fprintf(&b, `<li><strong>Turn %d</strong> · %s · %s tokens<br>Todo: <code>%s</code> → <code>%s</code><br>Changes: %s`,
			entry.Turn, escape(formatTime(entry.At)), escape(humanTokens(entry.Spent)), escape(entry.BeforeSummary),
			escape(entry.AfterSummary), escape(changes))
		if len(entry.Tools) > 0 {
			fmt.Fprintf(&b, `<h4>Tools run</h4>%s`, renderTools(entry.Tools, escape))
		}
		b.WriteString(`</li>`)
	}
	b.WriteString(`</ol>`)
	return b.String()
}

func (m *Model) finishOvernight(reason string) {
	m.overnight.Stop(reason)
	items := []todo.Item(nil)
	if m.todos != nil {
		items = m.todos.Items()
	}
	path, err := m.overnight.WriteReport(m.dataDir, m.cwd, items)
	message := fmt.Sprintf("⏳ Overnight stopped after %d turns: %s", m.overnight.Turns, reason)
	if err != nil {
		message += "; report unavailable: " + err.Error()
	} else {
		message += "; report: " + path
	}
	m.notice = message
	m.blocks = append(m.blocks, Block{Kind: BlockNotice, Text: message})
	m.scroll.FollowBottom()
}

func (m *Model) showOvernightReport() {
	path := m.overnight.ReportPath
	if path == "" {
		path = LatestOvernightReport(m.dataDir)
		m.overnight.ReportPath = path
	}
	if path == "" {
		m.notice = "⏳ No overnight report has been written yet"
		return
	}
	m.notice = "⏳ Overnight report: " + path
	m.blocks = append(m.blocks, Block{Kind: BlockNotice, Text: m.notice})
	m.scroll.FollowBottom()
}
