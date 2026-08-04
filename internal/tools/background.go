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
	Known   bool
}

func (p Progress) String() string {
	if !p.Known {
		return ""
	}
	var parts []string
	if p.Percent > 0 || p.Current > 0 {
		if p.Total > 0 {
			parts = append(parts, fmt.Sprintf("%.0f%%", p.Percent))
		} else {
			parts = append(parts, fmt.Sprintf("%.0f%%", p.Percent))
		}
	}
	if p.Phase != "" {
		parts = append(parts, p.Phase)
	} else if p.Message != "" && len(parts) == 0 {
		parts = append(parts, p.Message)
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
	defer t.mu.Unlock()
	return parseProgress(t.currentOutputLocked())
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
	output := writer.String()
	t.mu.Lock()
	if !t.done {
		t.output = output
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

// Tasks returns the tracked tasks.
func (b *Background) Tasks() []*BackgroundTask {
	b.mu.Lock()
	defer b.mu.Unlock()
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
	return append([]*BackgroundTask(nil), kept...)
}

// Task returns a task by id.
func (b *Background) Task(id int) (*BackgroundTask, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
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
	t.mu.Lock()
	t.output = output
	t.done, t.failed = true, failed
	t.mu.Unlock()
	t.doneOnce.Do(func() { close(t.doneCh) })

	if b.OnDone != nil {
		b.OnDone(t)
	}
}

// Progress parsing prefers the explicit marker and then the conventional
// counter/percentage/phase forms used by build tools.
func parseProgress(output string) Progress {
	var latest Progress
	for _, line := range strings.Split(output, "\n") {
		if p, ok := parseProgressMarker(line); ok {
			latest = p
			continue
		}
		if p, ok := parsePercentProgress(line); ok {
			latest = p
			continue
		}
		if p, ok := parseFractionProgress(line); ok {
			latest = p
			continue
		}
		if p, ok := parseOfProgress(line); ok {
			latest = p
			continue
		}
		if phase := strings.TrimSpace(line); isPhaseLine(phase) {
			latest = Progress{Phase: phase, Message: phase, Known: true}
		}
	}
	return latest
}

var (
	progressMarker  = regexp.MustCompile(`EVILCODE_PROGRESS\s+(\{.*\})\s*$`)
	percentPattern  = regexp.MustCompile(`(?i)([0-9]+(?:\.[0-9]+)?)\s*%`)
	fractionPattern = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)\s*/\s*([0-9]+(?:\.[0-9]+)?)(?:\s+([[:alnum:]_-]+))?`)
	ofPattern       = regexp.MustCompile(`(?i)([0-9]+(?:\.[0-9]+)?)\s+of\s+([0-9]+(?:\.[0-9]+)?)(?:\s+([[:alnum:]_-]+))?`)
	phasePattern    = regexp.MustCompile(`(?i)^(compiling|building|running|testing|linking|downloading|installing|checking|fetching)\b.*`)
)

func parseProgressMarker(line string) (Progress, bool) {
	match := progressMarker.FindStringSubmatch(line)
	if len(match) != 2 {
		return Progress{}, false
	}
	var raw struct {
		Percent *float64 `json:"percent"`
		Current *float64 `json:"current"`
		Total   *float64 `json:"total"`
		Unit    string   `json:"unit"`
		Phase   string   `json:"phase"`
		Message string   `json:"message"`
	}
	if json.Unmarshal([]byte(match[1]), &raw) != nil {
		return Progress{}, false
	}
	p := Progress{Unit: raw.Unit, Phase: raw.Phase, Message: raw.Message, Known: true}
	if raw.Percent != nil {
		p.Percent = *raw.Percent
	}
	if raw.Current != nil {
		p.Current = *raw.Current
	}
	if raw.Total != nil {
		p.Total = *raw.Total
		if raw.Percent == nil && raw.Current != nil && *raw.Total > 0 {
			p.Percent = *raw.Current / *raw.Total * 100
		}
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
	return Progress{Percent: percent, Message: strings.TrimSpace(line), Known: true}, true
}

func parseFractionProgress(line string) (Progress, bool) {
	match := fractionPattern.FindStringSubmatch(line)
	if len(match) < 3 {
		return Progress{}, false
	}
	current, err1 := strconv.ParseFloat(match[1], 64)
	total, err2 := strconv.ParseFloat(match[2], 64)
	if err1 != nil || err2 != nil || total <= 0 {
		return Progress{}, false
	}
	p := Progress{Current: current, Total: total, Unit: match[3], Message: strings.TrimSpace(line), Known: true}
	p.Percent = current / total * 100
	return p, true
}

func parseOfProgress(line string) (Progress, bool) {
	match := ofPattern.FindStringSubmatch(line)
	if len(match) < 3 {
		return Progress{}, false
	}
	current, err1 := strconv.ParseFloat(match[1], 64)
	total, err2 := strconv.ParseFloat(match[2], 64)
	if err1 != nil || err2 != nil || total <= 0 {
		return Progress{}, false
	}
	p := Progress{Current: current, Total: total, Unit: match[3], Message: strings.TrimSpace(line), Known: true}
	p.Percent = current / total * 100
	return p, true
}

func isPhaseLine(line string) bool { return phasePattern.MatchString(line) }
