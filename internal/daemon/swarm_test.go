package daemon

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"evilcode/internal/agent"
	"evilcode/internal/provider"
)

// toolResult builds the event a finished tool call produces, which is what the
// session's observer reads. Driving the observer with real events rather than
// calling the registry directly is the point: it is the wiring between the
// event stream and the registry that this exercises.
func toolResult(name, path string) agent.Event {
	args, _ := json.Marshal(map[string]string{"path": path})
	return agent.Event{
		Kind: agent.EventToolResult,
		Call: &provider.ToolCall{ID: "c1", Name: name, Args: args},
	}
}

func TestConflictNoticeReachesTheReadersConversation(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()

	reader, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	writer, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}

	// The reader reads a file and finishes its turn.
	reader.observe(toolResult("read", "auth.go"))
	reader.observe(agent.Event{Kind: agent.EventTurnEnd})

	// The writer edits it and finishes its turn, which is when the conflict is
	// raised — and then the reader's own turn end is what delivers it.
	writer.observe(toolResult("edit", "auth.go"))
	writer.observe(agent.Event{Kind: agent.EventTurnEnd})
	reader.observe(toolResult("read", "unrelated.go"))
	reader.observe(agent.Event{Kind: agent.EventTurnEnd})

	msgs := reader.built.Agent.DrainInterrupts(false)
	if len(msgs) == 0 {
		t.Fatal("the reader was never told its file changed")
	}
	joined := ""
	for _, m := range msgs {
		joined += m.Content
	}
	if !strings.Contains(joined, "auth.go") || !strings.Contains(joined, writer.Name) {
		t.Errorf("notice = %q, want it to name the file and the writer", joined)
	}
}

func TestAWriterIsNotToldAboutItsOwnEdit(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()

	sess, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	sess.observe(toolResult("read", "auth.go"))
	sess.observe(toolResult("edit", "auth.go"))
	sess.observe(agent.Event{Kind: agent.EventTurnEnd})

	if msgs := sess.built.Agent.DrainInterrupts(false); len(msgs) != 0 {
		t.Errorf("an agent was warned about its own edit: %v", msgs)
	}
}

func TestFailedToolCallsDoNotRegister(t *testing.T) {
	// A write that errored changed nothing, and warning a reader about it
	// would send them to re-read a file that never moved.
	srv, _ := testServer(t)
	defer srv.Close()

	reader, _ := srv.Open("")
	writer, _ := srv.Open("")
	reader.observe(toolResult("read", "auth.go"))
	reader.observe(agent.Event{Kind: agent.EventTurnEnd})

	failed := toolResult("edit", "auth.go")
	failed.ErrText = "no such file"
	writer.observe(failed)
	writer.observe(agent.Event{Kind: agent.EventTurnEnd})
	reader.observe(agent.Event{Kind: agent.EventTurnEnd})

	if msgs := reader.built.Agent.DrainInterrupts(false); len(msgs) != 0 {
		t.Errorf("a failed write produced a conflict notice: %v", msgs)
	}
}

func TestCompactNoticesFoldTheBurst(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()
	srv.CompactNotices = true

	reader, _ := srv.Open("")
	writer, _ := srv.Open("")
	files := []string{"a.go", "b.go", "c.go"}
	for _, f := range files {
		reader.observe(toolResult("read", f))
	}
	reader.observe(agent.Event{Kind: agent.EventTurnEnd})

	for _, f := range files {
		writer.observe(toolResult("edit", f))
	}
	writer.observe(agent.Event{Kind: agent.EventTurnEnd})
	reader.observe(agent.Event{Kind: agent.EventTurnEnd})

	msgs := reader.built.Agent.DrainInterrupts(false)
	if len(msgs) != 1 {
		t.Fatalf("interrupts = %d, want one folded notice", len(msgs))
	}
	if strings.Count(msgs[0].Content, "⚠") != 1 {
		t.Errorf("notice was not folded:\n%s", msgs[0].Content)
	}
	if !strings.Contains(msgs[0].Content, "3 files") {
		t.Errorf("notice = %q", msgs[0].Content)
	}
}

func TestSpawnedWorkerIsAPeerWithSwarmTools(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()

	spawner, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	name, err := srv.SpawnFor(spawner.Name, "count the TODOs", nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	// A worker is an ordinary session with no client, which is what makes
	// attaching to one later work for free (plan.md §20).
	peers := srv.Peers(spawner.Name)
	found := false
	for _, p := range peers {
		if p.Name == name {
			found = true
			if !p.Worker {
				t.Error("the spawned session is not marked as a worker")
			}
			if p.Task != "count the TODOs" {
				t.Errorf("task = %q", p.Task)
			}
		}
	}
	if !found {
		t.Fatalf("the worker is not among %d peers", len(peers))
	}

	// It can coordinate like anyone else.
	srv.mu.Lock()
	worker := srv.sessions[name]
	srv.mu.Unlock()
	if _, ok := worker.built.Agent.Tools.Find("send_message"); !ok {
		t.Error("a worker cannot message its peers")
	}
	if _, ok := worker.built.Agent.Tools.Find("spawn_worker"); !ok {
		t.Error("a worker cannot spawn")
	}
}

func TestSpawnBreakerBoundsOneSessionsWorkers(t *testing.T) {
	// Every auto-started loop needs a breaker (§12.6). This is the one that
	// matters most: a model that decides delegation is going well can spawn
	// workers that spawn workers, all against the same key.
	//
	// The per-session cap rather than the concurrency cap, because the mock
	// finishes instantly — which is itself correct, a session that runs workers
	// one after another is not doing anything wrong until it does it too often.
	srv, _ := testServer(t)
	defer srv.Close()

	spawner, _ := srv.Open("")
	var lastErr error
	for i := 0; i < MaxWorkersPerSession+2; i++ {
		if _, err := srv.SpawnFor(spawner.Name, "busywork", nil, nil); err != nil {
			lastErr = err
			break
		}
	}
	if lastErr == nil {
		t.Fatalf("spawned past MaxWorkersPerSession (%d) without a breaker",
			MaxWorkersPerSession)
	}
	if !strings.Contains(lastErr.Error(), "limit") {
		t.Errorf("breaker error = %q, want it to explain what stopped", lastErr)
	}
}

func TestLiveWorkerBreakerCountsUnfinishedWorkers(t *testing.T) {
	// Counting Running() would let a model spawn straight past the limit in one
	// turn: a worker's turn starts on a goroutine, so for the first instants
	// after Spawn it is neither running nor done.
	srv, _ := testServer(t)
	defer srv.Close()

	sess, err := srv.Spawn("hold still", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	sess.mu.Lock()
	sess.closedDone, sess.done = false, make(chan struct{})
	sess.mu.Unlock()

	if got := srv.liveWorkers(); got == 0 {
		t.Error("an unfinished worker was not counted")
	}
	sess.markFinished()
	if got := srv.liveWorkers(); got != 0 {
		t.Errorf("live workers = %d after finishing", got)
	}
	// Closing twice must not panic; both the turn-end path and the Run-returned
	// path call it.
	sess.markFinished()
}

func TestSpawnRejectsABadSchemaUpFront(t *testing.T) {
	// Compiled at spawn time rather than at completion: a malformed schema is
	// the spawner's mistake and should be reported at the call that made it,
	// not half an hour later as a worker whose good answer cannot be checked.
	srv, _ := testServer(t)
	defer srv.Close()

	spawner, _ := srv.Open("")
	_, err := srv.SpawnFor(spawner.Name, "task", nil, json.RawMessage(`{"type": 12345}`))
	if err == nil {
		t.Error("a malformed schema was accepted at spawn time")
	}
}

func TestMessageReachesAPeerAtItsSafePoint(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()

	a, _ := srv.Open("")
	b, _ := srv.Open("")

	if err := srv.SendMessage(a.Name, b.Name, "leave auth.go alone"); err != nil {
		t.Fatal(err)
	}
	msgs := b.built.Agent.DrainInterrupts(false)
	if len(msgs) == 0 {
		t.Fatal("the message never arrived")
	}
	if !strings.Contains(msgs[0].Content, "leave auth.go alone") {
		t.Errorf("message = %q", msgs[0].Content)
	}
	if !strings.Contains(msgs[0].Content, a.Name) {
		t.Errorf("message = %q, want it to name the sender", msgs[0].Content)
	}
}

func TestSendMessageToNobodyIsAnError(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()
	a, _ := srv.Open("")
	if err := srv.SendMessage(a.Name, "nosuchagent", "hello"); err == nil {
		t.Error("a message to a session that does not exist was accepted")
	}
}

func TestBroadcastSkipsTheSender(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()

	a, _ := srv.Open("")
	b, _ := srv.Open("")
	c, _ := srv.Open("")

	if n := srv.Broadcast(a.Name, "starting the migration"); n != 2 {
		t.Errorf("broadcast reached %d agents, want 2", n)
	}
	if msgs := a.built.Agent.DrainInterrupts(false); len(msgs) != 0 {
		t.Error("the sender received its own broadcast")
	}
	for _, sess := range []*Session{b, c} {
		if msgs := sess.built.Agent.DrainInterrupts(false); len(msgs) == 0 {
			t.Errorf("%s never got the broadcast", sess.Name)
		}
	}
}

func TestPeersReportsFilesTouched(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()

	a, _ := srv.Open("")
	b, _ := srv.Open("")
	b.observe(toolResult("read", "auth.go"))

	for _, p := range srv.Peers(a.Name) {
		if p.Name != b.Name {
			continue
		}
		if len(p.Files) == 0 {
			t.Error("peers reported no files for an agent that read one")
		}
		return
	}
	t.Fatal("the peer was not listed")
}

func TestRingReplaysAfterAReconnect(t *testing.T) {
	// The reason `attach` survives a killed terminal: the ring holds what the
	// client missed, and the JSONL on disk is still the source of truth.
	r := NewRing()
	for i := 0; i < 5; i++ {
		r.Add(agent.Event{Kind: agent.EventTextDelta, Text: "chunk", Seq: i})
	}
	got, seq := r.Since(2)
	if len(got) != 3 {
		t.Errorf("replayed %d events from seq 2, want 3", len(got))
	}
	if seq != r.Seq() {
		t.Errorf("seq = %d, want %d", seq, r.Seq())
	}
	if all, _ := r.Since(0); len(all) != 5 {
		t.Errorf("a fresh client got %d events, want all 5", len(all))
	}
}

func TestObserveIsInertWithoutAServer(t *testing.T) {
	// A session built outside a daemon has no registry, and every path has to
	// survive that rather than panicking on a nil.
	sess := &Session{}
	sess.observe(toolResult("edit", "auth.go"))
	sess.observe(agent.Event{Kind: agent.EventTurnEnd})
}

// waitFor polls until cond holds, which is how a test waits on a worker: a
// worker runs on its own goroutine by design, so there is nothing to join.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestWorkerRunsAndReportsAValidatedResult(t *testing.T) {
	// The whole §20 chain in one test: a worker is spawned, does real tool
	// work, and its answer comes back schema-validated rather than as prose the
	// spawner has to interpret.
	srv, _ := testServer(t)
	defer srv.Close()

	// After testServer, which pins its own scenario. The rotation gives the
	// session the first script and the worker it spawns the second.
	t.Setenv("EVILCODE_SCENARIO", "conflict,conflict-worker")
	provider.ResetMockRotation()

	spawner, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}

	schema := json.RawMessage(`{
		"type": "object",
		"properties": {"file": {"type": "string"}, "changed": {"type": "boolean"}},
		"required": ["file", "changed"]
	}`)
	name, err := srv.SpawnFor(spawner.Name, "fix the clamp", []string{"testdata/clamp.go"}, schema)
	if err != nil {
		t.Fatal(err)
	}

	srv.mu.Lock()
	worker := srv.sessions[name]
	srv.mu.Unlock()
	waitFor(t, "the worker to finish", func() bool { return !worker.built.Agent.Running() })

	// The result reaches the spawner as a message, not a return value: the
	// spawner was free to keep working while the worker ran.
	var report string
	waitFor(t, "the worker's result", func() bool {
		for _, m := range spawner.built.Agent.DrainInterrupts(false) {
			report += m.Content
		}
		return strings.Contains(report, name)
	})

	if strings.Contains(report, "did not match") {
		t.Errorf("the worker's output failed validation:\n%s", report)
	}
	if !strings.Contains(report, "clamp.go") {
		t.Errorf("report = %q, want the worker's validated answer", report)
	}
}

func TestWorkerResultFailingItsSchemaIsReportedAsSuch(t *testing.T) {
	// A worker whose answer does not fit gets one retry, then the spawner is
	// told plainly rather than handed prose to guess at.
	srv, _ := testServer(t)
	defer srv.Close()
	provider.ResetMockRotation()

	spawner, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {"answer": {"type": "string"}},
		"required": ["answer"]
	}`)
	name, err := srv.SpawnFor(spawner.Name, "answer something", nil, schema)
	if err != nil {
		t.Fatal(err)
	}

	srv.mu.Lock()
	worker := srv.sessions[name]
	srv.mu.Unlock()

	var report string
	waitFor(t, "the failure report", func() bool {
		if worker.built.Agent.Running() {
			return false
		}
		for _, m := range spawner.built.Agent.DrainInterrupts(false) {
			report += m.Content
		}
		return strings.Contains(report, name)
	})
	if !strings.Contains(report, "schema") {
		t.Errorf("report = %q, want it to say the schema was not met", report)
	}
}

// A successful multiedit registers as a write: a reader of the file is told it
// changed, just as for edit.
func TestMultiEditRegistersAsAWrite(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()
	reader, _ := srv.Open("")
	writer, _ := srv.Open("")

	reader.observe(toolResult("read", "auth.go"))
	reader.observe(agent.Event{Kind: agent.EventTurnEnd})
	writer.observe(toolResult("multiedit", "auth.go"))
	writer.observe(agent.Event{Kind: agent.EventTurnEnd})
	reader.observe(toolResult("read", "other.go"))
	reader.observe(agent.Event{Kind: agent.EventTurnEnd})

	msgs := reader.built.Agent.DrainInterrupts(false)
	if len(msgs) == 0 {
		t.Fatal("the reader was never told its file changed after a multiedit")
	}
}

// A fully-failed multiedit (NoWrite) does not register as a write, so a reader
// is not sent a stale-file notice for a file that never changed.
func TestMultiEditNoWriteDoesNotRegister(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()
	reader, _ := srv.Open("")
	writer, _ := srv.Open("")

	reader.observe(toolResult("read", "auth.go"))
	reader.observe(agent.Event{Kind: agent.EventTurnEnd})
	args, _ := json.Marshal(map[string]string{"path": "auth.go"})
	writer.observe(agent.Event{
		Kind: agent.EventToolResult, NoWrite: true,
		Call: &provider.ToolCall{ID: "c1", Name: "multiedit", Args: args},
	})
	writer.observe(agent.Event{Kind: agent.EventTurnEnd})
	reader.observe(toolResult("read", "other.go"))
	reader.observe(agent.Event{Kind: agent.EventTurnEnd})

	if msgs := reader.built.Agent.DrainInterrupts(false); len(msgs) != 0 {
		t.Errorf("a no-write multiedit queued a stale-file notice: %v", msgs)
	}
}
