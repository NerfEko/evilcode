package tools

import (
	"context"
	"encoding/json"
	"testing"
)

type modelCaptureDouble struct {
	gotModel string
}

func (s *modelCaptureDouble) Self() string { return "parent" }

func (s *modelCaptureDouble) SpawnWorker(_ string, _ []string, _ json.RawMessage, model string) (string, error) {
	s.gotModel = model
	return "w1", nil
}

func (s *modelCaptureDouble) SpawnWorkerForeground(_ context.Context, _ string, _ []string, _ json.RawMessage, model string) (string, string, error) {
	s.gotModel = model
	return "w1", "done", nil
}

func TestSpawnWorkerPassesModelThrough(t *testing.T) {
	double := &modelCaptureDouble{}
	tool, ok := NewSpawn(double).Find("spawn_worker")
	if !ok {
		t.Fatal("spawn_worker tool was not registered")
	}
	if _, err := tool.Run(context.Background(), json.RawMessage(`{"task":"brief","model":"small@mock"}`)); err != nil {
		t.Fatal(err)
	}
	if double.gotModel != "small@mock" {
		t.Fatalf("model = %q, want %q", double.gotModel, "small@mock")
	}
}

func TestSpawnWorkerModelDefaultsEmpty(t *testing.T) {
	double := &modelCaptureDouble{}
	tool, ok := NewSpawn(double).Find("spawn_worker")
	if !ok {
		t.Fatal("spawn_worker tool was not registered")
	}
	if _, err := tool.Run(context.Background(), json.RawMessage(`{"task":"brief","wait":false}`)); err != nil {
		t.Fatal(err)
	}
	if double.gotModel != "" {
		t.Fatalf("model = %q, want empty default", double.gotModel)
	}
}
