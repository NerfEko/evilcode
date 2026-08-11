package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// waitForBackground blocks until every background task has finished.
func waitForBackground(t *testing.T, e *Exec) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		pending := false
		for _, task := range e.Bg.Tasks() {
			if done, _, _ := task.Snapshot(); !done {
				pending = true
			}
		}
		if !pending {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("a background command never finished")
}

// H3.3: foreground and background execution both accumulate stdout and stderr
// in an unbounded buffer. A command that prints without stopping is bounded
// only by memory — and a background one holds whatever it produced for up to
// thirty minutes before Truncate runs.
func TestForegroundOutputIsBounded(t *testing.T) {
	e := NewExec(t.TempDir())
	e.Timeout = 30 * time.Second
	raw, _ := json.Marshal(map[string]any{
		// ~64 MB if nothing bounds it.
		"cmd": "yes 0123456789012345678901234567890123456789 | head -c 64000000",
	})

	// The result was already truncated before this fix; what was not bounded
	// is what the buffer holds on the way there. Allocation is how that shows
	// from outside: an unbounded buffer has to allocate the whole 64 MB.
	grew := allocatedDuring(t, func() {
		out := e.Tools().RunOne(context.Background(), Call{ID: "c", Name: "bash", Args: raw})
		if got := len(out.Result.Output); got > 4*MaxResultBytes {
			t.Errorf("a 64 MB command produced %d bytes of result", got)
		}
	})
	if grew > 32<<20 {
		t.Errorf("a 64 MB command allocated %s; the output buffer is unbounded",
			humanBytes(int64(grew)))
	}
}

// allocatedDuring reports how many bytes were allocated while fn ran.
func allocatedDuring(t *testing.T, fn func()) uint64 {
	t.Helper()
	if raceDetectorEnabled {
		// The race runtime instruments every copy from the subprocess pipe and
		// charges several bytes per byte transferred to TotalAlloc. Run the
		// behavioral assertions, but leave heap-budget measurement to the
		// ordinary binary where TotalAlloc reflects application allocations.
		fn()
		t.Log("allocation threshold skipped under race instrumentation")
		return 0
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func TestBackgroundOutputIsBounded(t *testing.T) {
	e := NewExec(t.TempDir())
	e.Bg = &Background{}
	raw, _ := json.Marshal(map[string]any{
		"cmd":        "yes 0123456789012345678901234567890123456789 | head -c 64000000",
		"background": true,
	})

	var out Outcome
	grew := allocatedDuring(t, func() {
		out = e.Tools().RunOne(context.Background(), Call{ID: "c", Name: "bash", Args: raw})
		waitForBackground(t, e)
	})
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	if grew > 32<<20 {
		t.Errorf("a 64 MB background command allocated %s; its output is unbounded",
			humanBytes(int64(grew)))
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		for _, task := range e.Bg.Tasks() {
			done, _, output := task.Snapshot()
			if !done {
				continue
			}
			if got := len(output); got > 4*MaxResultBytes {
				t.Errorf("a background command held %d bytes; the output is not bounded", got)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the background command never finished")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestBackgroundDropsOldFinishedTasks(t *testing.T) {
	b := &Background{}
	for i := 0; i < MaxCompletedBackgroundTasks+3; i++ {
		task := b.add("task")
		b.finish(task, "done", false)
	}
	if got := len(b.Tasks()); got != MaxCompletedBackgroundTasks {
		t.Fatalf("retained %d completed tasks, want %d", got, MaxCompletedBackgroundTasks)
	}
}

// H3.4: cancelling kills the shell but not its descendants, so a grandchild
// outlives the timeout and keeps working in the workspace after the tool call
// that started it has returned.
func TestATimeoutKillsTheWholeProcessGroup(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "still-alive")

	e := NewExec(dir)
	e.Timeout = 700 * time.Millisecond
	raw, _ := json.Marshal(map[string]any{
		// The grandchild outlives its parent shell and writes after the
		// timeout has already returned an error to the caller.
		"cmd": "( sleep 2; echo yes > " + marker + " ) & wait",
	})

	out := e.Tools().RunOne(context.Background(), Call{ID: "c", Name: "bash", Args: raw})
	if out.Err != nil || !strings.Contains(out.Result.Output, "background task 1") {
		t.Fatalf("want an adopted task, got %+v", out)
	}
	if err := e.Bg.Cancel(1); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := e.Bg.Wait(ctx, 1); err != nil {
		t.Fatal(err)
	}

	// Well past when the grandchild would have written.
	time.Sleep(3 * time.Second)
	if _, err := os.Stat(marker); err == nil {
		t.Error("a grandchild survived the timeout and wrote to the workspace " +
			"after the tool call had returned")
	}
}
