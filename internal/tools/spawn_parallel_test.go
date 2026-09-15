package tools

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Phase 1: a maximal run of consecutive spawns shares the bounded pool.
func TestBatchOverlapsSpawnCalls(t *testing.T) {
	var inFlight, max int64
	var entered sync.WaitGroup
	release := make(chan struct{})
	tool := Tool{
		Name: "spawn_worker", Effect: EffectSpawn, Schema: json.RawMessage(`{"type":"object"}`),
		Run: func(ctx context.Context, _ json.RawMessage) (Result, error) {
			n := atomic.AddInt64(&inFlight, 1)
			for {
				old := atomic.LoadInt64(&max)
				if n <= old || atomic.CompareAndSwapInt64(&max, old, n) {
					break
				}
			}
			entered.Done()
			<-release
			atomic.AddInt64(&inFlight, -1)
			return Result{Output: "ok"}, nil
		},
	}
	calls := []Call{{Name: "spawn_worker"}, {Name: "spawn_worker"}, {Name: "spawn_worker"}, {Name: "spawn_worker"}}
	entered.Add(len(calls))
	done := make(chan []Outcome, 1)
	go func() { done <- Set{tool}.RunBatch(context.Background(), calls) }()
	entered.Wait()
	close(release)
	<-done
	if got := atomic.LoadInt64(&max); got < 2 {
		t.Fatalf("spawn batch never overlapped (max in flight %d), want concurrent execution", got)
	}
}

// Phase 1: spawns still barrier against mutations in the same round.
func TestBatchBarriersSpawnsAroundMutations(t *testing.T) {
	var mu sync.Mutex
	var events []string
	spawn := Tool{
		Name: "spawn_worker", Effect: EffectSpawn, Schema: json.RawMessage(`{"type":"object"}`),
		Run: func(ctx context.Context, _ json.RawMessage) (Result, error) {
			mu.Lock()
			events = append(events, "spawn-start")
			mu.Unlock()
			time.Sleep(20 * time.Millisecond)
			mu.Lock()
			events = append(events, "spawn-end")
			mu.Unlock()
			return Result{Output: "ok"}, nil
		},
	}
	write := Tool{
		Name: "write", Schema: json.RawMessage(`{"type":"object"}`),
		Run: func(ctx context.Context, _ json.RawMessage) (Result, error) {
			mu.Lock()
			events = append(events, "write")
			mu.Unlock()
			return Result{Output: "ok"}, nil
		},
	}
	set := Set{spawn, write}
	out := set.RunBatch(context.Background(), []Call{
		{ID: "1", Name: "spawn_worker"}, {ID: "2", Name: "write"}, {ID: "3", Name: "spawn_worker"},
	})
	if len(out) != 3 {
		t.Fatalf("batch returned %d outcomes", len(out))
	}
	mu.Lock()
	defer mu.Unlock()
	end, writeAt := -1, -1
	for i, e := range events {
		if e == "spawn-end" && end == -1 {
			end = i
		}
		if e == "write" {
			writeAt = i
		}
	}
	if end == -1 || writeAt == -1 || writeAt < end {
		t.Fatalf("write ran before the spawn finished: %v", events)
	}
}

// D9: disjoint hints stay parallel; overlapping hints serialize.
func TestSpawnFilesHintOverlapDowngradesToSerial(t *testing.T) {
	overlap := spawnFilesOverlap(
		[]Call{
			{Args: json.RawMessage(`{"files_hint":["a.go"]}`)},
			{Args: json.RawMessage(`{"files_hint":["a.go"]}`)},
		},
		[]int{0, 1},
	)
	if !overlap {
		t.Fatal("overlapping files_hint did not downgrade to serial")
	}
	disjoint := spawnFilesOverlap(
		[]Call{
			{Args: json.RawMessage(`{"files_hint":["a.go"]}`)},
			{Args: json.RawMessage(`{"files_hint":["b.go"]}`)},
		},
		[]int{0, 1},
	)
	if disjoint {
		t.Fatal("disjoint files_hint blocked parallelism")
	}
	undeclared := spawnFilesOverlap(
		[]Call{{}, {}},
		[]int{0, 1},
	)
	if undeclared {
		t.Fatal("undeclared hints blocked parallelism")
	}
}

func TestSpawnOverlappingHintsRunSerially(t *testing.T) {
	var inFlight, max int64
	tool := Tool{
		Name: "spawn_worker", Effect: EffectSpawn, Schema: json.RawMessage(`{"type":"object"}`),
		Run: func(ctx context.Context, _ json.RawMessage) (Result, error) {
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
	calls := []Call{
		{Name: "spawn_worker", Args: json.RawMessage(`{"files_hint":["a.go"]}`)},
		{Name: "spawn_worker", Args: json.RawMessage(`{"files_hint":["a.go"]}`)},
	}
	Set{tool}.RunBatch(context.Background(), calls)
	if got := atomic.LoadInt64(&max); got != 1 {
		t.Fatalf("overlapping spawn hints reached %d concurrent runs, want exactly 1", got)
	}
}

type waitTestDouble struct {
	foregroundCalled bool
	asyncCalled      bool
}

func (s *waitTestDouble) Self() string { return "parent" }

func (s *waitTestDouble) SpawnWorker(string, []string, json.RawMessage, string) (string, error) {
	s.asyncCalled = true
	return "async-1", nil
}

func (s *waitTestDouble) SpawnWorkerForeground(context.Context, string, []string, json.RawMessage, string) (SpawnResult, error) {
	s.foregroundCalled = true
	return SpawnResult{Name: "worker-1", Output: `{"ok":true}`, Status: StatusComplete}, nil
}

func TestSpawnWaitFalseUsesAsyncPath(t *testing.T) {
	double := &waitTestDouble{}
	tool, ok := NewSpawn(double).Find("spawn_worker")
	if !ok {
		t.Fatal("spawn_worker tool was not registered")
	}
	result, err := tool.Run(context.Background(), json.RawMessage(`{"task":"brief","wait":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if !double.asyncCalled || double.foregroundCalled {
		t.Fatalf("wait:false used foreground=%v async=%v, want async only", double.foregroundCalled, double.asyncCalled)
	}
	if result.Output == "" || len(result.Output) >= len("Worker async-1 completed:") && result.Output[:6] == "Worker" && contains(result.Output, "completed") {
		t.Fatalf("wait:false output = %q, want the started message", result.Output)
	}
}

func TestSpawnWaitDefaultsToForeground(t *testing.T) {
	double := &waitTestDouble{}
	tool, ok := NewSpawn(double).Find("spawn_worker")
	if !ok {
		t.Fatal("spawn_worker tool was not registered")
	}
	if _, err := tool.Run(context.Background(), json.RawMessage(`{"task":"brief"}`)); err != nil {
		t.Fatal(err)
	}
	if !double.foregroundCalled || double.asyncCalled {
		t.Fatalf("default wait used foreground=%v async=%v, want foreground", double.foregroundCalled, double.asyncCalled)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
