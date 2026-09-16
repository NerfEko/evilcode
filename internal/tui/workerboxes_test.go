package tui

import (
	"strings"
	"testing"

	"evilcode/internal/theme"
)

func TestRenderWorkerBoxesFixedHeight(t *testing.T) {
	r := NewRenderer(theme.Dracula(), 80)
	workers := []SwarmAgent{
		{Name: "grunt-1", Task: "wire auth", Worker: true, Running: true,
			Model: "small@mock", Tokens: 1500,
			Tail: []string{"reading auth.go", "found the race", "patching"}},
		{Name: "grunt-2", Task: "survey", Worker: true},
	}
	lines := r.RenderWorkerBoxes(workers, 80)
	if len(lines) != 2*WorkerBoxRows {
		t.Fatalf("boxes = %d rows, want %d (fixed height each)", len(lines), 2*WorkerBoxRows)
	}
	joined := strings.Join(plainLines(lines), "\n")
	for _, want := range []string{"grunt-1", "wire auth", "working", "found the race", "grunt-2", "click to expand"} {
		if !strings.Contains(joined, want) {
			t.Errorf("boxes = %q, want %q", joined, want)
		}
	}
	if got := r.RenderWorkerBoxes(nil, 80); len(got) != 0 {
		t.Fatalf("no workers rendered %d rows, want none", len(got))
	}
}

func TestWorkerBoxesFollowTheirSpawnRows(t *testing.T) {
	m := newTestModel(t)
	m.blocks = []Block{
		{Kind: BlockUser, Number: 1, Text: "delegate the auth survey"},
		{Kind: BlockTool, ToolName: "spawn_worker", ToolIntent: "grunt-1 · auth survey"},
		{Kind: BlockAssistant, Text: "I will integrate the result."},
	}
	swarm := &SwarmState{}
	swarm.Publish([]SwarmAgent{
		{Name: "grunt-1", Worker: true, Spawner: "dracula", Running: true,
			Tail: []string{"reading auth.go"}},
	})
	m.swarm = swarm

	rows := m.transcriptLines()
	if len(rows.WorkerBoxes) != 1 {
		t.Fatalf("worker boxes = %+v, want one", rows.WorkerBoxes)
	}
	box := rows.WorkerBoxes[0]
	if box.Start <= int(rows.First[1]) {
		t.Fatalf("box starts at %d, want after spawn row at %d", box.Start, rows.First[1])
	}
	if !strings.Contains(plain(rows.Lines[box.Start]), "grunt-1") {
		t.Fatalf("box line = %q, want worker name", plain(rows.Lines[box.Start]))
	}
	if got := m.stackFor(len(rows.Lines)).Heights[SlotSwarm]; got != 0 {
		t.Fatalf("SlotSwarm = %d, want no fixed chrome reservation", got)
	}

	swarm.Publish([]SwarmAgent{
		{Name: "grunt-1", Worker: true, Spawner: "dracula", Running: true,
			Tail: []string{"found the race"}},
	})
	rows = m.transcriptLines()
	if !strings.Contains(plain(rows.Lines[rows.WorkerBoxes[0].Start+1]), "found the race") {
		t.Fatalf("updated box tail missing: %q", plain(rows.Lines[rows.WorkerBoxes[0].Start+1]))
	}

	m.View()
	if len(m.workerBoxHits) != 1 {
		t.Fatalf("visible worker hits = %+v, want one", m.workerBoxHits)
	}
	if !m.expandWorkerAt(m.workerBoxHits[0].Top, 5) || m.expandedWorker != "grunt-1" {
		t.Fatalf("click on transcript-anchored box did not expand: %+v", m.workerBoxHits)
	}
}

func TestWorkerBoxAtMapsClicks(t *testing.T) {
	names := []string{"grunt-1", "grunt-2"}
	if got := workerBoxAt(names, 10, 10, 5, 80); got != "grunt-1" {
		t.Fatalf("top of first box = %q", got)
	}
	if got := workerBoxAt(names, 10, 10+WorkerBoxRows, 5, 80); got != "grunt-2" {
		t.Fatalf("second box = %q", got)
	}
	if got := workerBoxAt(names, 10, 9, 5, 80); got != "" {
		t.Fatalf("above boxes = %q, want none", got)
	}
	if got := workerBoxAt(names, 10, 10+2*WorkerBoxRows, 5, 80); got != "" {
		t.Fatalf("below boxes = %q, want none", got)
	}
	if got := workerBoxAt(names, 10, 12, 90, 80); got != "" {
		t.Fatalf("click on the side panel = %q, want none", got)
	}
	if got := workerBoxAt(nil, -1, 12, 5, 80); got != "" {
		t.Fatalf("no boxes = %q, want none", got)
	}
}

func TestMyWorkersSelectsUnfinishedCrew(t *testing.T) {
	s := &SwarmState{}
	s.Publish([]SwarmAgent{
		{Name: "grunt-1", Worker: true, Spawner: "wisp-13"},
		{Name: "grunt-2", Worker: true, Spawner: "wisp-13", Finished: true},
		{Name: "grunt-3", Worker: true, Spawner: "other"},
		{Name: "ed"},
	})
	got := s.MyWorkers("wisp-13")
	if len(got) != 1 || got[0].Name != "grunt-1" {
		t.Fatalf("my workers = %+v, want only grunt-1", got)
	}
	if got := s.MyWorkers(""); len(got) != 0 {
		t.Fatalf("empty self selected %+v", got)
	}
	var nilState *SwarmState
	if got := nilState.MyWorkers("wisp-13"); len(got) != 0 {
		t.Fatalf("nil state selected %+v", got)
	}
}

func TestExpandWorkerOpensSidePanel(t *testing.T) {
	m := newTestModel(t)
	m.width = 100
	m.workerBoxTop = 20
	m.workerBoxNames = []string{"grunt-1", "grunt-2"}
	if !m.expandWorkerAt(20+WorkerBoxRows, 5) {
		t.Fatal("click on the second box did not expand")
	}
	if m.expandedWorker != "grunt-2" {
		t.Fatalf("expanded = %q", m.expandedWorker)
	}
	if !m.panelOpen {
		t.Fatal("side panel did not open")
	}
	if !strings.Contains(m.panel.Title, "grunt-2") {
		t.Fatalf("panel title = %q", m.panel.Title)
	}
	m.closeSplit()
	if m.expandedWorker != "" || m.panelOpen {
		t.Fatal("closing the split did not close the expansion")
	}
	if m.expandWorkerAt(0, 5) {
		t.Fatal("click above the boxes expanded something")
	}
}
