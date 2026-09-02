package tools

import (
	"context"
	"encoding/json"
	"testing"
)

type foregroundSpawnerTestDouble struct {
	foregroundCalled bool
	asyncCalled      bool
}

func (s *foregroundSpawnerTestDouble) Self() string { return "parent" }

func (s *foregroundSpawnerTestDouble) SpawnWorker(string, []string, json.RawMessage) (string, error) {
	s.asyncCalled = true
	return "async", nil
}

func (s *foregroundSpawnerTestDouble) SpawnWorkerForeground(context.Context, string, []string, json.RawMessage) (string, string, error) {
	s.foregroundCalled = true
	return "worker-1", `{"changed":true}`, nil
}

func TestSpawnWorkerUsesForegroundResultWhenRuntimeSupportsIt(t *testing.T) {
	double := &foregroundSpawnerTestDouble{}
	tool, ok := NewSpawn(double).Find("spawn_worker")
	if !ok {
		t.Fatal("spawn_worker tool was not registered")
	}

	result, err := tool.Run(context.Background(), json.RawMessage(`{"task":"make the change"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !double.foregroundCalled || double.asyncCalled {
		t.Fatalf("foreground=%v async=%v", double.foregroundCalled, double.asyncCalled)
	}
	if result.Output != "Worker worker-1 completed:\n{\"changed\":true}" {
		t.Errorf("output = %q", result.Output)
	}
}
