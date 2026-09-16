package tools

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

// EC-014: the detached-command timeout is a ceiling, and seconds are validated
// before the seconds-to-Duration multiplication so huge values cannot overflow
// into a nonpositive duration and silently fall back to the default.
func TestBackgroundTimeoutCeiling(t *testing.T) {
	cases := []struct {
		name    string
		timeout any
		wantErr bool
	}{
		{name: "zero means default", timeout: 0, wantErr: false},
		{name: "one second", timeout: 1, wantErr: false},
		{name: "exactly the ceiling", timeout: MaxBackgroundTimeoutSeconds, wantErr: false},
		{name: "one past the ceiling", timeout: MaxBackgroundTimeoutSeconds + 1, wantErr: true},
		{name: "max int cannot overflow", timeout: math.MaxInt, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := NewExec(t.TempDir())
			before := len(e.Bg.Tasks())
			raw, _ := json.Marshal(map[string]any{
				"cmd":        "true",
				"background": true,
				"timeout":    tc.timeout,
			})
			out := e.Tools().RunOne(context.Background(), Call{ID: "c", Name: "bash", Args: raw})
			if tc.wantErr {
				if out.Err == nil {
					t.Fatalf("timeout %v accepted, want rejection at the %d-second ceiling", tc.timeout, MaxBackgroundTimeoutSeconds)
				}
				if got := len(e.Bg.Tasks()); got != before {
					t.Fatalf("rejected timeout registered a task (%d -> %d)", before, got)
				}
				return
			}
			if out.Err != nil {
				t.Fatalf("timeout %v rejected: %v", tc.timeout, out.Err)
			}
			waitForBackground(t, e)
		})
	}
	if MaxBackgroundTimeoutSeconds != int(BackgroundTimeout/time.Second) {
		t.Fatalf("MaxBackgroundTimeoutSeconds = %d, want %d", MaxBackgroundTimeoutSeconds, int(BackgroundTimeout/time.Second))
	}
	if MaxBackgroundTimeoutSeconds != 1800 {
		t.Fatalf("MaxBackgroundTimeoutSeconds = %d, want 1800", MaxBackgroundTimeoutSeconds)
	}
}

// A duration above the ceiling is rejected even when runBackground is reached
// directly, so no caller can smuggle a years-long deadline past the gate.
func TestRunBackgroundTimeoutDurationCeiling(t *testing.T) {
	e := NewExec(t.TempDir())

	if _, err := e.runBackground("true", "", BackgroundTimeout+time.Second); err == nil {
		t.Fatal("duration above BackgroundTimeout accepted, want rejection")
	}
	if n := len(e.Bg.Tasks()); n != 0 {
		t.Fatalf("rejected duration registered %d tasks", n)
	}

	for _, d := range []time.Duration{0, time.Second, BackgroundTimeout} {
		e := NewExec(t.TempDir())
		if _, err := e.runBackground("true", "", d); err != nil {
			t.Fatalf("duration %s rejected: %v", d, err)
		}
		waitForBackground(t, e)
	}
}

// math.MaxInt seconds overflows time.Duration multiplication into a negative
// value; it must be rejected, never normalized to the default ceiling.
func TestBackgroundTimeoutMaxIntIsNotDefaulted(t *testing.T) {
	e := NewExec(t.TempDir())
	raw, _ := json.Marshal(map[string]any{
		"cmd":        "true",
		"background": true,
		"timeout":    math.MaxInt,
	})
	out := e.Tools().RunOne(context.Background(), Call{ID: "c", Name: "bash", Args: raw})
	if out.Err == nil {
		t.Fatal("math.MaxInt timeout accepted, want rejection")
	}
	if msg := strings.ToLower(out.Err.Error()); !strings.Contains(msg, "ceiling") && !strings.Contains(msg, "too large") {
		t.Fatalf("err = %v, want a ceiling/overflow diagnostic", out.Err)
	}
	if got := len(e.Bg.Tasks()); got != 0 {
		t.Fatalf("overflowing timeout registered %d tasks", got)
	}
}

// The same overflow guard applies to foreground commands: a huge timeout must
// fail fast instead of wrapping into a nonsensical deadline.
func TestForegroundTimeoutOverflowRejected(t *testing.T) {
	e := NewExec(t.TempDir())
	res, err := run(t, e.Tools(), "bash", map[string]any{
		"cmd":     "true",
		"timeout": math.MaxInt,
	})
	if err == nil {
		t.Fatalf("foreground math.MaxInt timeout accepted: %+v", res)
	}
}

// The JSON schema advertises the same ceiling the code enforces.
func TestBackgroundTimeoutSchemaExposesCeiling(t *testing.T) {
	e := NewExec(t.TempDir())
	var schema map[string]any
	for _, tool := range e.Tools() {
		if tool.Name != "bash" {
			continue
		}
		if err := json.Unmarshal(tool.Schema, &schema); err != nil {
			t.Fatal(err)
		}
	}
	props, _ := schema["properties"].(map[string]any)
	timeout, _ := props["timeout"].(map[string]any)
	max, _ := timeout["maximum"].(float64)
	if int(max) != MaxBackgroundTimeoutSeconds {
		t.Fatalf("schema timeout.maximum = %v, want %d", timeout["maximum"], MaxBackgroundTimeoutSeconds)
	}
}

// Detached commands must not inherit the turn context: cancelling the caller
// must not kill the task.
func TestBackgroundTimeoutDoesNotInheritCallerContext(t *testing.T) {
	e := NewExec(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	raw, _ := json.Marshal(map[string]any{
		"cmd":        "echo hello",
		"background": true,
		"timeout":    60,
	})
	out := e.Tools().RunOne(ctx, Call{ID: "c", Name: "bash", Args: raw})
	if out.Err != nil {
		t.Fatalf("background start with cancelled caller context failed: %v", out.Err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		done := false
		for _, task := range e.Bg.Tasks() {
			finished, _, output := task.Snapshot()
			if finished {
				done = true
				if !strings.Contains(output, "hello") {
					t.Fatalf("background output = %q, want hello", output)
				}
			}
		}
		if done {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("background task did not finish after its caller context was cancelled")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
