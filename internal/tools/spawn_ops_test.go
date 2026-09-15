package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type cancelStub struct {
	gotWorker string
	summary   string
	err       error
}

func (s *cancelStub) Self() string { return "parent" }

func (s *cancelStub) CancelWorker(worker string) (string, error) {
	s.gotWorker = worker
	return s.summary, s.err
}

func TestCancelWorkerValidatesAndReports(t *testing.T) {
	stub := &cancelStub{summary: "Worker w1 cancellation requested."}
	tool, ok := NewCancel(stub).Find("cancel_worker")
	if !ok {
		t.Fatal("cancel_worker tool was not registered")
	}
	if tool.Effect != EffectUnknown {
		t.Fatalf("cancel_worker effect = %v, want the serialized default", tool.Effect)
	}
	result, err := tool.Run(context.Background(), json.RawMessage(`{"worker":"w1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if stub.gotWorker != "w1" {
		t.Fatalf("cancelled %q, want w1", stub.gotWorker)
	}
	if !strings.Contains(result.Output, "w1") {
		t.Fatalf("output = %q, want the summary", result.Output)
	}
	if _, err := tool.Run(context.Background(), json.RawMessage(`{"worker":"  "}`)); err == nil {
		t.Fatal("empty worker name did not fail")
	}
}

type statusStub struct {
	res SpawnResult
	err error
}

func (s *statusStub) Self() string { return "parent" }

func (s *statusStub) SpawnWorker(string, []string, json.RawMessage, string) (string, error) {
	return "async", nil
}

func (s *statusStub) SpawnWorkerForeground(context.Context, string, []string, json.RawMessage, string) (SpawnResult, error) {
	return s.res, s.err
}

func TestSpawnResultStatusTravelsWithOutput(t *testing.T) {
	stub := &statusStub{res: SpawnResult{Name: "w1", Output: "half done", Status: StatusPartial}}
	tool, ok := NewSpawn(stub).Find("spawn_worker")
	if !ok {
		t.Fatal("spawn_worker tool was not registered")
	}
	result, err := tool.Run(context.Background(), json.RawMessage(`{"task":"brief"}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusPartial {
		t.Fatalf("status = %q, want partial", result.Status)
	}
	if !strings.Contains(result.Output, "[status: partial]") || !strings.Contains(result.Output, "half done") {
		t.Fatalf("output = %q, want the marker and the content", result.Output)
	}

	stub.res = SpawnResult{Name: "w1", Output: "done", Status: StatusComplete}
	result, err = tool.Run(context.Background(), json.RawMessage(`{"task":"brief"}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusComplete || strings.Contains(result.Output, "[status:") {
		t.Fatalf("complete result = %+v, want no marker", result)
	}
}
