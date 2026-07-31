package tools

import (
	"context"
	"encoding/json"
	"runtime"
	"sync"
	"testing"
	"time"
)

// H2.13: a goroutine was created for every call the model asked for, before the
// semaphore had any say. The concurrency cap bounded how many tools *ran*, not
// how many goroutines existed, so a pathological call list costs stacks and
// scheduler time regardless of it.
func TestBatchDoesNotSpawnAGoroutinePerCall(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	running := 0
	tool := Tool{
		Name: "hold", Desc: "holds until released",
		Schema: json.RawMessage(`{"type":"object"}`),
		Run: func(ctx context.Context, args json.RawMessage) (Result, error) {
			mu.Lock()
			running++
			mu.Unlock()
			<-release
			return Result{Output: "ok"}, nil
		},
	}

	const asked = 5000
	calls := make([]Call, asked)
	for i := range calls {
		calls[i] = Call{ID: "c", Name: "hold", Args: json.RawMessage(`{}`)}
	}

	before := runtime.NumGoroutine()
	done := make(chan []Outcome, 1)
	go func() { done <- Set{tool}.RunBatch(context.Background(), calls) }()

	// Wait until the batch is fully dispatched: every tool that is going to run
	// concurrently is now blocked in Run.
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := running
		mu.Unlock()
		if n >= MaxConcurrent || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	inFlight := runtime.NumGoroutine() - before
	mu.Lock()
	concurrent := running
	mu.Unlock()

	close(release)
	out := <-done

	if len(out) != asked {
		t.Fatalf("batch returned %d outcomes, want %d", len(out), asked)
	}
	if inFlight > MaxConcurrent*4 {
		t.Errorf("a %d-call batch put %d goroutines in flight; the cap is %d",
			asked, inFlight, MaxConcurrent)
	}
	if concurrent > MaxConcurrent {
		t.Errorf("%d tools were running at once, past the %d cap", concurrent, MaxConcurrent)
	}
}

// Every call gets an outcome, including the ones past the cap: an unanswered
// tool_use is a transcript the provider rejects (H1.2).
func TestBatchPastTheCapIsRefusedNotDropped(t *testing.T) {
	tool := Tool{
		Name: "noop", Desc: "does nothing",
		Schema: json.RawMessage(`{"type":"object"}`),
		Run: func(ctx context.Context, args json.RawMessage) (Result, error) {
			return Result{Output: "ok"}, nil
		},
	}
	calls := make([]Call, MaxBatch+25)
	for i := range calls {
		calls[i] = Call{ID: "c", Name: "noop", Args: json.RawMessage(`{}`)}
	}

	out := Set{tool}.RunBatch(context.Background(), calls)
	if len(out) != len(calls) {
		t.Fatalf("batch returned %d outcomes for %d calls", len(out), len(calls))
	}
	for i, o := range out {
		switch {
		case i < MaxBatch && o.Err != nil:
			t.Fatalf("call %d was within the cap but failed: %v", i, o.Err)
		case i >= MaxBatch && o.Err == nil:
			t.Fatalf("call %d is past the cap but reported success", i)
		}
	}
}

// A cancelled batch still answers every call it was given.
func TestCancelledBatchAnswersEveryCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tool := Tool{
		Name: "noop", Desc: "does nothing",
		Schema: json.RawMessage(`{"type":"object"}`),
		Run: func(ctx context.Context, args json.RawMessage) (Result, error) {
			return Result{Output: "ok"}, nil
		},
	}
	calls := make([]Call, 32)
	for i := range calls {
		calls[i] = Call{ID: "c", Name: "noop", Args: json.RawMessage(`{}`)}
	}

	out := Set{tool}.RunBatch(ctx, calls)
	for i, o := range out {
		if o.Err == nil && o.Result.Output == "" {
			t.Fatalf("call %d came back with neither a result nor an error", i)
		}
	}
}
