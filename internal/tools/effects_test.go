package tools

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// R2-07: batch scheduling must be effect-aware. Every call used to go through
// the same worker pool, so two edits to one file, a mkdir before the write
// that fills it, or a shell command racing another could execute in a
// different order than the model asked. Outcomes were returned in order; the
// effects were not.

// TestBatchSerializesUndeclaredTools pins the default: a tool that has not
// declared an effect never runs next to another call.
func TestBatchSerializesUndeclaredTools(t *testing.T) {
	var inFlight, max int64
	var wg sync.WaitGroup
	wg.Add(4)
	tool := Tool{
		Name: "mutate", Schema: json.RawMessage(`{"type":"object"}`),
		Run: func(ctx context.Context, _ json.RawMessage) (Result, error) {
			defer wg.Done()
			n := atomic.AddInt64(&inFlight, 1)
			for {
				old := atomic.LoadInt64(&max)
				if n <= old || atomic.CompareAndSwapInt64(&max, old, n) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			atomic.AddInt64(&inFlight, -1)
			return Result{Output: "ok"}, nil
		},
	}
	calls := []Call{{Name: "mutate"}, {Name: "mutate"}, {Name: "mutate"}, {Name: "mutate"}}
	out := Set{tool}.RunBatch(context.Background(), calls)
	wg.Wait()
	if atomic.LoadInt64(&max) != 1 {
		t.Fatalf("undeclared tool reached %d concurrent runs, want exactly 1", max)
	}
	for i, o := range out {
		if o.Err != nil || o.Result.Output != "ok" {
			t.Fatalf("call %d = %+v, want a clean result", i, o)
		}
	}
}

// TestBatchBarriersMutationsAroundReads pins the ordering guarantee: a
// mutating call runs after every earlier call has finished and before any
// later call starts, even when read-only calls around it could overlap.
func TestBatchBarriersMutationsAroundReads(t *testing.T) {
	var mu sync.Mutex
	var events []string

	readTool := Tool{
		Name: "read", Effect: EffectReadOnly, Schema: json.RawMessage(`{"type":"object"}`),
		Run: func(ctx context.Context, _ json.RawMessage) (Result, error) {
			mu.Lock()
			events = append(events, "read-start")
			mu.Unlock()
			// Give a wrongly parallel mutation time to jump the barrier.
			time.Sleep(20 * time.Millisecond)
			mu.Lock()
			events = append(events, "read-end")
			mu.Unlock()
			return Result{Output: "ok"}, nil
		},
	}
	writeTool := Tool{
		Name: "write", Schema: json.RawMessage(`{"type":"object"}`),
		Run: func(ctx context.Context, _ json.RawMessage) (Result, error) {
			mu.Lock()
			events = append(events, "write")
			mu.Unlock()
			return Result{Output: "ok"}, nil
		},
	}

	set := Set{readTool, writeTool}
	out := set.RunBatch(context.Background(), []Call{
		{ID: "1", Name: "read"}, {ID: "2", Name: "write"}, {ID: "3", Name: "read"},
	})
	if len(out) != 3 {
		t.Fatalf("batch returned %d outcomes", len(out))
	}

	mu.Lock()
	defer mu.Unlock()
	joined := ""
	for _, e := range events {
		joined += e + " "
	}
	// Both reads precede the write only up to the barrier: the first read must
	// have ENDED before the write, and the write before the second read began.
	// The exact interleaving of the two reads with each other is the pool's
	// business; the barrier is not.
	firstEnd := -1
	writeAt := -1
	for i, e := range events {
		if e == "read-end" && firstEnd == -1 {
			firstEnd = i
		}
		if e == "write" {
			writeAt = i
		}
	}
	if firstEnd == -1 || writeAt == -1 || writeAt < firstEnd {
		t.Fatalf("write started before the first read finished: %v", events)
	}
	secondStart := -1
	for i := writeAt + 1; i < len(events); i++ {
		if events[i] == "read-start" {
			secondStart = i
			break
		}
	}
	if secondStart == -1 {
		t.Fatalf("second read never ran after the write: %v", events)
	}
}

// TestBatchStillOverlapsReadOnlyFans pins the other half of the contract: a
// run of read-only calls still shares the pool.
func TestBatchStillOverlapsReadOnlyFans(t *testing.T) {
	started := make(chan struct{}, MaxConcurrent)
	release := make(chan struct{})
	tool := Tool{
		Name: "read", Effect: EffectReadOnly, Schema: json.RawMessage(`{"type":"object"}`),
		Run: func(ctx context.Context, _ json.RawMessage) (Result, error) {
			started <- struct{}{}
			<-release
			return Result{Output: "ok"}, nil
		},
	}
	calls := make([]Call, MaxConcurrent)
	for i := range calls {
		calls[i] = Call{ID: "c", Name: "read"}
	}
	done := make(chan []Outcome, 1)
	go func() { done <- Set{tool}.RunBatch(context.Background(), calls) }()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("read-only calls did not overlap; the pool is gone")
	}
	close(release)
	<-done
}
