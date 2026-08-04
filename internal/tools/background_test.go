package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestTimedOutForegroundIsAdopted(t *testing.T) {
	e := NewExec(t.TempDir())
	e.Timeout = 80 * time.Millisecond
	res, err := run(t, e.Tools(), "bash", map[string]any{"cmd": "sleep 0.3; echo finished"})
	if err != nil {
		t.Fatalf("adoption returned an error: %v", err)
	}
	if !strings.Contains(res.Output, "background task 1") || !strings.Contains(res.Output, "do not re-run") {
		t.Fatalf("adoption result = %q", res.Output)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	task, err := e.Bg.Wait(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, failed, output := task.Snapshot(); failed || !strings.Contains(output, "finished") {
		t.Fatalf("adopted task = failed:%v output:%q", failed, output)
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
