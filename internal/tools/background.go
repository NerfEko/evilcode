package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Progress is the latest progress marker parsed from a background command.
// Percent is 0..100 when known; Current/Total retain the original counter.
type Progress struct {
	Percent float64
	Current float64
	Total   float64
	Unit    string
	Phase   string
	Message string
	// Indeterminate is set when a marker reports work without a determinate
	// counter or percentage. Checkpoint is retained for callers that want to
	// distinguish a milestone from ordinary progress; bg wait still waits for
	// terminal completion in this Go implementation.
	Indeterminate bool
	Checkpoint    bool
	ETASeconds    int
	Known         bool
}

func (p Progress) String() string {
	if !p.Known {
		return ""
	}
	var parts []string
	if p.Percent != 0 || p.Total > 0 {
		parts = append(parts, fmt.Sprintf("%.0f%%", p.Percent))
	} else if p.Current != 0 {
		parts = append(parts, fmt.Sprintf("%.0f%%", p.Percent))
	}
	if p.Phase != "" {
		parts = append(parts, p.Phase)
	} else if p.Message != "" && len(parts) == 0 {
		parts = append(parts, p.Message)
	}
	if len(parts) == 0 {
		if p.Indeterminate {
			return "working"
		}
		return "progress reported"
	}
	return strings.Join(parts, " · ")
}

// BackgroundTask is a command still running after its tool call returned.
type BackgroundTask struct {
	ID      int
	Label   string
	Started time.Time

	mu              sync.Mutex
	done            bool
	failed          bool
	output          string
	progress        Progress
	writer          *ringWriter
	cancel          func()
	cancelRequested bool
	doneCh          chan struct{}
	doneOnce        sync.Once
}

// Snapshot returns the task's current state and the output captured so far.
func (t *BackgroundTask) Snapshot() (done, failed bool, output string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.done, t.failed, t.currentOutputLocked()
}

// Progress returns the latest parsed progress marker.
func (t *BackgroundTask) Progress() Progress {
	t.mu.Lock()
	writer := t.writer
	output := t.currentOutputLocked()
	progress := t.progress
	t.mu.Unlock()
	if writer != nil {
		if progress := writer.Progress(); progress.Known {
			return progress
		}
	}
	if progress.Known {
		return progress
	}
	return parseProgress(output)
}

func (t *BackgroundTask) currentOutputLocked() string {
	return t.output
}

func (t *BackgroundTask) refreshOutput() {
	t.mu.Lock()
	writer := t.writer
	done := t.done
	t.mu.Unlock()
	if writer == nil || done {
		return
	}
	output := cleanProgressOutput(writer.Tail())
	progress := writer.Progress()
	t.mu.Lock()
	if !t.done {
		t.output = output
		if progress.Known {
			t.progress = progress
		}
	}
	t.mu.Unlock()
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

// MaxCompletedBackgroundTasks bounds finished task metadata and captured
// output retained for the widget. Running tasks are never evicted.
const MaxCompletedBackgroundTasks = 8

func (b *Background) pruneLocked() {
	completed := 0
	kept := make([]*BackgroundTask, 0, len(b.tasks))
	for i := len(b.tasks) - 1; i >= 0; i-- {
		t := b.tasks[i]
		done, _, _ := t.Snapshot()
		if done {
			completed++
			if completed > MaxCompletedBackgroundTasks {
				continue
			}
		}
		kept = append(kept, t)
	}
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}
	b.tasks = kept
}

// Tasks returns the tracked tasks.
func (b *Background) Tasks() []*BackgroundTask {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pruneLocked()
	return append([]*BackgroundTask(nil), b.tasks...)
}

// Task returns a task by id.
func (b *Background) Task(id int) (*BackgroundTask, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pruneLocked()
	for _, task := range b.tasks {
		if task.ID == id {
			return task, true
		}
	}
	return nil, false
}

// Wait blocks until a task finishes or ctx is canceled.
func (b *Background) Wait(ctx context.Context, id int) (*BackgroundTask, error) {
	task, ok := b.Task(id)
	if !ok {
		return nil, fmt.Errorf("background task %d was not found", id)
	}
	select {
	case <-task.doneCh:
		return task, nil
	case <-ctx.Done():
		return task, ctx.Err()
	}
}

// Cancel requests termination of a running task. The process group is killed
// by the command owner; completion still flows through finish and doneCh.
func (b *Background) Cancel(id int) error {
	task, ok := b.Task(id)
	if !ok {
		return fmt.Errorf("background task %d was not found", id)
	}
	task.mu.Lock()
	done, cancel := task.done, task.cancel
	if !done && cancel == nil {
		task.cancelRequested = true
		task.mu.Unlock()
		return nil
	}
	task.mu.Unlock()
	if done {
		return nil
	}
	cancel()
	return nil
}

// add registers a task. Tests and callers that finish their own task keep this
// small helper; command execution attaches a live writer and cancel function.
func (b *Background) add(label string) *BackgroundTask {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pruneLocked()
	b.next++
	t := &BackgroundTask{ID: b.next, Label: label, Started: time.Now(), doneCh: make(chan struct{})}
	b.tasks = append(b.tasks, t)
	return t
}

func (b *Background) attach(t *BackgroundTask, writer *ringWriter, cancel func()) {
	t.mu.Lock()
	t.writer = writer
	t.cancel = cancel
	cancelRequested := t.cancelRequested
	t.mu.Unlock()
	t.refreshOutput()
	if cancelRequested {
		cancel()
	}
	go func() {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				t.refreshOutput()
			case <-t.doneCh:
				return
			}
		}
	}()
}

// finish records a task's result and notifies.
func (b *Background) finish(t *BackgroundTask, output string, failed bool) {
	progress := parseProgress(output)
	t.mu.Lock()
	writer := t.writer
	t.mu.Unlock()
	if writer != nil {
		if observed := writer.Progress(); observed.Known {
			progress = observed
		}
	}
	t.mu.Lock()
	// Keep the registry bounded even for callers that finish a task directly;
	// command execution already truncates, but the registry is the last line of
	// defense against an accidental unbounded completion payload.
	t.output = Truncate(cleanProgressOutput(output))
	if progress.Known {
		t.progress = progress
	}
	t.done, t.failed = true, failed
	t.mu.Unlock()
	t.doneOnce.Do(func() { close(t.doneCh) })
	b.mu.Lock()
	b.pruneLocked()
	b.mu.Unlock()

	if b.OnDone != nil {
		b.OnDone(t)
	}
}

// Progress parsing prefers the explicit marker and then the conventional
// counter/percentage/phase forms used by build tools.
func parseProgress(output string) Progress {
	var latest Progress
	for _, line := range strings.Split(output, "\n") {
		if p, ok := parseProgressLine(line); ok {
			latest = p
		}
	}
	return latest
}

// parseProgressLine is the allocation-free hot path used while a command is
// streaming. Calling parseProgress (which splits a complete output string) for
// every line made a verbose command allocate once per line, defeating the
// bounded ring writer.
func parseProgressLine(line string) (Progress, bool) {
	if p, ok := parseCheckpointMarker(line); ok {
		return p, true
	}
	if p, ok := parseProgressMarker(line); ok {
		return p, true
	}
	if p, ok := parsePercentProgress(line); ok {
		return p, true
	}
	if p, ok := parseFractionProgress(line); ok {
		return p, true
	}
	if p, ok := parseOfProgress(line); ok {
		return p, true
	}
	if phase := strings.TrimSpace(line); isPhaseLine(phase) {
		return Progress{Phase: phase, Message: phase, Known: true}, true
	}
	return Progress{}, false
}

// looksLikeProgressLine avoids invoking the regexp-based parsers for every
// ordinary output line. A command such as `yes` can produce millions of lines;
// allocating a regexp match slice for each one defeats the bounded writer.
func looksLikeProgressLine(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	if strings.ContainsAny(line, "%/") || strings.Contains(line, " of ") {
		return true
	}
	for _, marker := range []string{"EVILCODE_PROGRESS", "JCODE_PROGRESS", "JCODE_CHECKPOINT"} {
		if hasPrefixFold(line, marker) {
			return true
		}
	}
	for _, phase := range []string{"compiling", "building", "running", "testing", "linking", "downloading", "installing", "checking", "fetching", "resolving"} {
		if hasPrefixFold(line, phase) {
			return true
		}
	}
	return false
}

func looksLikeProgressBytes(line []byte) bool {
	start, end := 0, len(line)
	for start < end && (line[start] == ' ' || line[start] == '\t' || line[start] == '\r') {
		start++
	}
	for end > start && (line[end-1] == ' ' || line[end-1] == '\t' || line[end-1] == '\r') {
		end--
	}
	line = line[start:end]
	if len(line) == 0 {
		return false
	}
	for _, c := range line {
		if c == '%' || c == '/' {
			return true
		}
	}
	for i := 0; i+4 <= len(line); i++ {
		if line[i] == ' ' && line[i+1] == 'o' && line[i+2] == 'f' && line[i+3] == ' ' {
			return true
		}
	}
	for _, marker := range []string{"EVILCODE_PROGRESS", "JCODE_PROGRESS", "JCODE_CHECKPOINT", "compiling", "building", "running", "testing", "linking", "downloading", "installing", "checking", "fetching", "resolving"} {
		if hasPrefixFoldBytes(line, marker) {
			return true
		}
	}
	return false
}

func hasPrefixFold(value, prefix string) bool {
	if len(value) < len(prefix) {
		return false
	}
	for i := range prefix {
		v, p := value[i], prefix[i]
		if v >= 'A' && v <= 'Z' {
			v += 'a' - 'A'
		}
		if p >= 'A' && p <= 'Z' {
			p += 'a' - 'A'
		}
		if v != p {
			return false
		}
	}
	return true
}

func hasPrefixFoldBytes(value []byte, prefix string) bool {
	if len(value) < len(prefix) {
		return false
	}
	for i := range prefix {
		v, p := value[i], prefix[i]
		if v >= 'A' && v <= 'Z' {
			v += 'a' - 'A'
		}
		if p >= 'A' && p <= 'Z' {
			p += 'a' - 'A'
		}
		if v != p {
			return false
		}
	}
	return true
}

var (
	progressMarker   = regexp.MustCompile(`(?i)(?:EVILCODE_PROGRESS|JCODE_PROGRESS)\s+(\{.*\})\s*$`)
	checkpointMarker = regexp.MustCompile(`(?i)JCODE_CHECKPOINT(?:\s+(.*))?\s*$`)
	percentPattern   = regexp.MustCompile(`(?i)([0-9]+(?:\.[0-9]+)?)\s*%`)
	fractionPattern  = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)\s*/\s*([0-9]+(?:\.[0-9]+)?)(?:\s+([[:alnum:]_-]+))?`)
	ofPattern        = regexp.MustCompile(`(?i)([0-9]+(?:\.[0-9]+)?)\s+of\s+([0-9]+(?:\.[0-9]+)?)(?:\s+([[:alnum:]_-]+))?`)
	phasePattern     = regexp.MustCompile(`(?i)^(compiling|building|running|testing|linking|downloading|installing|checking|fetching|resolving)\b.*`)
)

func parseProgressMarker(line string) (Progress, bool) {
	match := progressMarker.FindStringSubmatch(line)
	if len(match) != 2 {
		return Progress{}, false
	}
	var raw struct {
		Percent    *float64 `json:"percent"`
		Current    *float64 `json:"current"`
		Total      *float64 `json:"total"`
		Unit       string   `json:"unit"`
		Phase      string   `json:"phase"`
		Message    string   `json:"message"`
		Kind       string   `json:"kind"`
		Checkpoint bool     `json:"checkpoint"`
		ETASeconds int      `json:"eta_seconds"`
	}
	if json.Unmarshal([]byte(match[1]), &raw) != nil {
		return Progress{}, false
	}
	p := Progress{
		Unit: raw.Unit, Phase: raw.Phase, Message: raw.Message,
		Indeterminate: strings.EqualFold(raw.Kind, "indeterminate"),
		Checkpoint:    raw.Checkpoint || strings.EqualFold(raw.Kind, "checkpoint"),
		ETASeconds:    raw.ETASeconds, Known: true,
	}
	if raw.Percent != nil {
		p.Percent = clampPercent(*raw.Percent)
	}
	if raw.Current != nil {
		p.Current = max(0, *raw.Current)
	}
	if raw.Total != nil {
		p.Total = max(0, *raw.Total)
		if raw.Percent == nil && raw.Current != nil && p.Total > 0 {
			if p.Current > p.Total {
				p.Current = p.Total
			}
			p.Percent = clampPercent(p.Current / p.Total * 100)
		}
	}
	if p.Total > 0 && p.Current > p.Total {
		p.Current = p.Total
	}
	return p, true
}

func parseCheckpointMarker(line string) (Progress, bool) {
	match := checkpointMarker.FindStringSubmatch(strings.TrimSpace(line))
	if len(match) == 0 {
		return Progress{}, false
	}
	p := Progress{Checkpoint: true, Indeterminate: true, Known: true}
	payload := ""
	if len(match) > 1 {
		payload = strings.TrimSpace(match[1])
	}
	if strings.HasPrefix(payload, "{") {
		var raw struct {
			Message    string   `json:"message"`
			Percent    *float64 `json:"percent"`
			Current    *float64 `json:"current"`
			Total      *float64 `json:"total"`
			Unit       string   `json:"unit"`
			ETASeconds int      `json:"eta_seconds"`
		}
		if json.Unmarshal([]byte(match[1]), &raw) == nil {
			p.Message, p.Unit, p.ETASeconds = raw.Message, raw.Unit, raw.ETASeconds
			if raw.Percent != nil {
				p.Percent, p.Indeterminate = clampPercent(*raw.Percent), false
			}
			if raw.Current != nil {
				p.Current = max(0, *raw.Current)
			}
			if raw.Total != nil {
				p.Total = max(0, *raw.Total)
			}
			if p.Total > 0 && p.Current <= p.Total {
				p.Percent = clampPercent(p.Current / p.Total * 100)
			}
		}
	} else if payload != "" {
		p.Message = payload
	}
	return p, true
}

func parsePercentProgress(line string) (Progress, bool) {
	match := percentPattern.FindStringSubmatch(line)
	if len(match) != 2 {
		return Progress{}, false
	}
	percent, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		return Progress{}, false
	}
	return Progress{Percent: clampPercent(percent), Message: strings.TrimSpace(line), Known: true}, true
}

func parseFractionProgress(line string) (Progress, bool) {
	match := fractionPattern.FindStringSubmatch(line)
	if len(match) < 3 {
		return Progress{}, false
	}
	current, err1 := strconv.ParseFloat(match[1], 64)
	total, err2 := strconv.ParseFloat(match[2], 64)
	if err1 != nil || err2 != nil || total < 2 || current > total {
		return Progress{}, false
	}
	p := Progress{Current: current, Total: total, Unit: match[3], Message: strings.TrimSpace(line), Known: true}
	p.Percent = clampPercent(current / total * 100)
	return p, true
}

func parseOfProgress(line string) (Progress, bool) {
	match := ofPattern.FindStringSubmatch(line)
	if len(match) < 3 {
		return Progress{}, false
	}
	current, err1 := strconv.ParseFloat(match[1], 64)
	total, err2 := strconv.ParseFloat(match[2], 64)
	if err1 != nil || err2 != nil || total < 2 || current > total {
		return Progress{}, false
	}
	p := Progress{Current: current, Total: total, Unit: match[3], Message: strings.TrimSpace(line), Known: true}
	p.Percent = clampPercent(current / total * 100)
	return p, true
}

func isPhaseLine(line string) bool { return phasePattern.MatchString(line) }

func clampPercent(percent float64) float64 {
	return min(100, max(0, percent))
}

func cleanProgressOutput(output string) string {
	if output == "" {
		return output
	}
	lines := strings.Split(output, "\n")
	kept := lines[:0]
	for _, line := range lines {
		if progressMarker.MatchString(strings.TrimSpace(line)) || checkpointMarker.MatchString(strings.TrimSpace(line)) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}
