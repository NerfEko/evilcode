package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// F2: a background command must honor a requested timeout instead of silently
// getting the 30-minute ceiling, and the effective deadline must be visible in
// task status.
func TestBackgroundHonorsRequestedTimeout(t *testing.T) {
	e := NewExec(t.TempDir())
	marker := filepath.Join(t.TempDir(), "done")
	raw, _ := json.Marshal(map[string]any{
		"cmd":           "sleep 3; echo done > " + marker,
		"background":    true,
		"timeout":       1,
		"justification": "test: verify a detached command is bounded by its requested timeout",
	})
	outcome := e.Tools().RunOne(context.Background(), Call{ID: "c", Name: "bash", Args: raw})
	if outcome.Err != nil {
		t.Fatalf("background start failed: %v", outcome.Err)
	}
	task, ok := e.Bg.Task(1)
	if !ok {
		t.Fatal("background task was not registered")
	}
	if deadline := task.Deadline(); deadline.IsZero() || time.Until(deadline) > 2*time.Second {
		t.Fatalf("deadline = %v, want ~1s from start", deadline)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	doneTask, err := e.Bg.Wait(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, failed, _ := doneTask.Snapshot(); !failed {
		t.Error("a task killed by its deadline must finish as failed")
	}
	// Well past when the command would have written its marker.
	time.Sleep(800 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Error("background command outlived its requested timeout and wrote to the workspace")
	}
}

// F3: Background.Close cancels every running task and waits for the process
// groups to die, so session teardown cannot leave detached commands running.
func TestBackgroundCloseKillsRunningTasks(t *testing.T) {
	e := NewExec(t.TempDir())
	marker := filepath.Join(t.TempDir(), "survivor")
	raw, _ := json.Marshal(map[string]any{
		"cmd":           "sleep 30; echo done > " + marker,
		"background":    true,
		"justification": "test: verify Close kills detached commands",
	})
	if out := e.Tools().RunOne(context.Background(), Call{ID: "c", Name: "bash", Args: raw}); out.Err != nil {
		t.Fatalf("background start failed: %v", out.Err)
	}
	e.Close()
	for _, task := range e.Bg.Tasks() {
		if done, _, _ := task.Snapshot(); !done {
			t.Fatalf("task %d still running after Close", task.ID)
		}
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Error("a background command survived Exec.Close and wrote to the workspace")
	}
}
