package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// F1: a timeout is a hard bound, not a handoff. The process group must be
// killed and the caller must get an error — a destructive command must not
// keep running as an adopted background task after the model was told it
// exceeded the timeout.
func TestTimedOutForegroundIsKilled(t *testing.T) {
	e := NewExec(t.TempDir())
	e.Timeout = 80 * time.Millisecond
	marker := filepath.Join(t.TempDir(), "finished")
	res, err := run(t, e.Tools(), "bash", map[string]any{
		"cmd":           "sleep 0.4; echo finished > " + marker,
		"justification": "test: verify the timeout kills the command before it can write its marker",
	})
	if err == nil {
		t.Fatalf("a timed-out command must return an error, got %+v", res)
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("err = %v, want a timeout diagnostic", err)
	}
	if len(e.Bg.Tasks()) != 0 {
		t.Errorf("timed-out command was adopted: %+v", e.Bg.Tasks())
	}
	// Well past when the command would have written its marker.
	time.Sleep(700 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Error("the command kept running after its timeout and wrote to the workspace")
	}
}

func TestBackgroundProgressParsing(t *testing.T) {
	cases := []struct {
		name  string
		out   string
		want  float64
		phase string
	}{
		{name: "explicit", out: `EVILCODE_PROGRESS {"current":3,"total":10,"unit":"tests","phase":"Testing"}`, want: 30, phase: "Testing"},
		{name: "percent", out: "42% complete", want: 42},
		{name: "fraction", out: "3/10 tests", want: 30},
		{name: "of", out: "3 of 10 steps", want: 30},
		{name: "decimal", out: "1.5/3.0 GiB", want: 50},
		{name: "phase", out: "Compiling internal/tools", phase: "Compiling internal/tools"},
		{name: "resolving", out: "Resolving dependencies", phase: "Resolving dependencies"},
		{name: "jcode-marker", out: `JCODE_PROGRESS {"percent":12}`, want: 12},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := parseProgress(tc.out)
			if !p.Known || p.Percent != tc.want || p.Phase != tc.phase {
				t.Fatalf("progress = %+v", p)
			}
		})
	}
}

func TestBackgroundCheckpointProgress(t *testing.T) {
	p := parseProgress(`JCODE_CHECKPOINT {"message":"tests passed"}`)
	if !p.Known || !p.Checkpoint || !p.Indeterminate || p.Message != "tests passed" {
		t.Fatalf("checkpoint progress = %+v", p)
	}
	if p = parseProgress("JCODE_CHECKPOINT tests passed"); !p.Known || p.Message != "tests passed" {
		t.Fatalf("text checkpoint progress = %+v", p)
	}
}

func TestBackgroundFinishedTaskRetainsProgressAfterHidingMarker(t *testing.T) {
	b := &Background{}
	task := b.add("finished")
	b.finish(task, `EVILCODE_PROGRESS {"percent":75,"message":"Testing"}`+"\n", false)
	if progress := task.Progress(); !progress.Known || progress.Percent != 75 {
		t.Fatalf("finished progress = %+v", progress)
	}
	_, _, output := task.Snapshot()
	if strings.Contains(output, "EVILCODE_PROGRESS") {
		t.Fatalf("marker leaked into finished output: %q", output)
	}
}

func TestBackgroundProgressNormalizesAndSurvivesOutputTail(t *testing.T) {
	if got := parseProgress(`EVILCODE_PROGRESS {"percent":140,"message":"done"}`).Percent; got != 100 {
		t.Fatalf("percent was not clamped: %v", got)
	}
	if p, ok := parseFractionProgress("11/10 steps"); ok || p.Known {
		t.Fatalf("invalid counter was accepted: %+v", p)
	}

	e := NewExec(t.TempDir())
	raw, _ := json.Marshal(map[string]any{
		"cmd":        "printf '%s\\n' 'EVILCODE_PROGRESS {\"percent\":25,\"message\":\"Building\"}'; head -c 60000 /dev/zero | tr '\\0' x; sleep 0.5",
		"background": true,
	})
	started := e.Tools().RunOne(context.Background(), Call{Name: "bash", Args: raw})
	if started.Err != nil {
		t.Fatal(started.Err)
	}
	task, ok := e.Bg.Task(1)
	if !ok {
		t.Fatal("background task was not registered")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		task.refreshOutput()
		if progress := task.Progress(); progress.Known {
			if progress.Percent != 25 {
				t.Fatalf("progress = %+v", progress)
			}
			_, _, output := task.Snapshot()
			if strings.Contains(output, "EVILCODE_PROGRESS") {
				t.Fatalf("control marker leaked into live output: %q", output[:min(len(output), 120)])
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("progress marker was lost after it left the live output tail")
}

func TestBashStdinAndScratchEnvironment(t *testing.T) {
	dir := t.TempDir()
	e := NewExec(t.TempDir()).WithScratchDir(dir)
	res, err := run(t, e.Tools(), "bash", map[string]any{
		"cmd":   "read answer; printf 'got:%s' \"$answer\"",
		"stdin": "from-stdin\n",
	})
	if err != nil || strings.TrimSpace(res.Output) != "got:from-stdin" {
		t.Fatalf("stdin result = %q, err=%v", res.Output, err)
	}
	res, err = run(t, e.Tools(), "bash", map[string]any{
		"cmd": "printf '%s|%s' \"$TMPDIR\" \"$EVILCODE_SCRATCH_DIR\"",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := dir + "|" + dir
	if strings.TrimSpace(res.Output) != want {
		t.Fatalf("scratch environment = %q, want %q", res.Output, want)
	}
}

func TestBackgroundCancelBeforeProcessAttach(t *testing.T) {
	b := &Background{}
	task := b.add("pending")
	called := make(chan struct{})
	if err := b.Cancel(task.ID); err != nil {
		t.Fatal(err)
	}
	b.attach(task, &ringWriter{}, func() { close(called) })
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("pending cancellation was not delivered after attach")
	}
	b.finish(task, "", false)
}

func TestBackgroundPrunesFinishedTasksOnDirectAccess(t *testing.T) {
	b := &Background{}
	var first *BackgroundTask
	for i := 0; i < MaxCompletedBackgroundTasks+1; i++ {
		task := b.add("task")
		if i == 0 {
			first = task
		}
		b.finish(task, "done", false)
	}
	if _, ok := b.Task(first.ID); ok {
		t.Fatalf("old finished task %d remained addressable after the retention bound", first.ID)
	}
}

func TestExplicitBackgroundStartRefusesAnOverloadedRegistry(t *testing.T) {
	e := NewExec(t.TempDir())
	for range MaxRunningBackgroundTasks {
		e.Bg.add("already running")
	}
	raw, _ := json.Marshal(map[string]any{"cmd": "true", "background": true})
	outcome := e.Tools().RunOne(context.Background(), Call{Name: "bash", Args: raw})
	if outcome.Err == nil || !strings.Contains(outcome.Result.Output, "limit") {
		t.Fatalf("over-limit background start = %+v; want a clear refusal", outcome)
	}
	if got := len(e.Bg.Tasks()); got != MaxRunningBackgroundTasks {
		t.Fatalf("over-limit start registered %d tasks, want %d", got, MaxRunningBackgroundTasks)
	}
}

func TestBGToolWaitAndTail(t *testing.T) {
	e := NewExec(t.TempDir())
	raw, _ := json.Marshal(map[string]any{"cmd": "printf 'one\\ntwo\\nthree\\n'; sleep 0.5", "background": true})
	started := e.Tools().RunOne(context.Background(), Call{Name: "bash", Args: raw})
	if started.Err != nil {
		t.Fatal(started.Err)
	}
	statusRaw, _ := json.Marshal(map[string]any{"op": "status", "id": 1})
	status := e.Tools().RunOne(context.Background(), Call{Name: "bg", Args: statusRaw})
	if status.Err != nil || !strings.Contains(status.Result.Output, "running") {
		t.Fatalf("status = %+v", status)
	}
	waitRaw, _ := json.Marshal(map[string]any{"op": "wait", "id": 1, "timeout": 2})
	wait := e.Tools().RunOne(context.Background(), Call{Name: "bg", Args: waitRaw})
	if wait.Err != nil || !strings.Contains(wait.Result.Output, "three") {
		t.Fatalf("wait = %+v", wait)
	}
	tailRaw, _ := json.Marshal(map[string]any{"op": "tail", "id": 1, "lines": 1})
	tail := e.Tools().RunOne(context.Background(), Call{Name: "bg", Args: tailRaw})
	if tail.Err != nil || strings.TrimSpace(tail.Result.Output) != "three" {
		t.Fatalf("tail = %+v", tail)
	}
}
