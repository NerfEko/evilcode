package daemon

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRegistryReportsAConflictToTheReader(t *testing.T) {
	r := NewRegistry()
	r.Read("bat", "auth.go", 12)

	got := r.Write("crypt", "auth.go", 14)
	if len(got) != 1 {
		t.Fatalf("conflicts = %d, want the one reader", len(got))
	}
	if got[0].Session != "bat" || got[0].Other != "crypt" {
		t.Errorf("conflict = %+v", got[0])
	}
	// The turn the reader last saw the file is what makes the notice
	// actionable rather than merely alarming.
	if got[0].ReadTurn != 12 {
		t.Errorf("ReadTurn = %d, want the reader's turn", got[0].ReadTurn)
	}
	if !strings.Contains(got[0].Notice(), "turn 12") {
		t.Errorf("notice = %q", got[0].Notice())
	}
}

func TestRegistryDoesNotReportAWriterToItself(t *testing.T) {
	r := NewRegistry()
	r.Read("bat", "auth.go", 1)
	if got := r.Write("bat", "auth.go", 2); len(got) != 0 {
		t.Errorf("an agent was told about its own write: %+v", got)
	}
}

func TestRegistryMatchesPathsAcrossForms(t *testing.T) {
	// Two agents naming the same file relatively and absolutely must collide,
	// or the registry silently never fires — the failure mode that would make
	// the whole feature look implemented and do nothing.
	r := NewRegistry()
	abs, err := filepath.Abs("auth.go")
	if err != nil {
		t.Fatal(err)
	}
	r.Read("bat", abs, 3)
	if got := r.Write("crypt", "./auth.go", 4); len(got) != 1 {
		t.Errorf("conflicts = %d, want the paths to be recognized as one file", len(got))
	}
}

func TestPendingDeliversOnce(t *testing.T) {
	// A reader that does not re-read must not be told the same thing every
	// turn: a notice repeated forever is a notice that gets ignored.
	r := NewRegistry()
	r.Read("bat", "auth.go", 1)
	conflicts := r.Write("crypt", "auth.go", 2)

	if got := r.Pending("bat", conflicts); len(got) != 1 {
		t.Fatalf("first delivery = %d, want 1", len(got))
	}
	if got := r.Pending("bat", conflicts); len(got) != 0 {
		t.Errorf("the same conflict was delivered twice: %+v", got)
	}
}

func TestRereadingClearsTheConflict(t *testing.T) {
	// Re-reading resolves it, so a later write is worth reporting again.
	r := NewRegistry()
	r.Read("bat", "auth.go", 1)
	r.Pending("bat", r.Write("crypt", "auth.go", 2))

	r.Read("bat", "auth.go", 3)
	again := r.Pending("bat", r.Write("crypt", "auth.go", 4))
	if len(again) != 1 {
		t.Error("a second write after a re-read should be reported")
	}
}

func TestPendingIgnoresOtherSessionsConflicts(t *testing.T) {
	r := NewRegistry()
	r.Read("bat", "auth.go", 1)
	conflicts := r.Write("crypt", "auth.go", 2)
	if got := r.Pending("ghoul", conflicts); len(got) != 0 {
		t.Errorf("a bystander was handed someone else's conflict: %+v", got)
	}
}

func TestCompactNoticeFoldsABurst(t *testing.T) {
	// A worker rewriting twenty files otherwise buries the coordination it is
	// meant to provide under twenty near-identical warnings.
	var conflicts []Conflict
	for _, f := range []string{"a.go", "b.go", "c.go", "d.go", "e.go", "f.go"} {
		conflicts = append(conflicts, Conflict{
			Session: "bat", Other: "crypt", Path: f, ReadTurn: 3,
		})
	}
	got := CompactNotice(conflicts)
	if strings.Count(got, "\n") != 0 {
		t.Errorf("compact notice spans lines:\n%s", got)
	}
	if !strings.Contains(got, "6 files") || !strings.Contains(got, "and 2 more") {
		t.Errorf("notice = %q", got)
	}
	if !strings.Contains(got, "crypt") {
		t.Error("the notice should name who wrote")
	}
}

func TestCompactNoticeOfOneIsJustTheNotice(t *testing.T) {
	c := Conflict{Session: "bat", Other: "crypt", Path: "auth.go", ReadTurn: 2}
	if got := CompactNotice([]Conflict{c}); got != c.Notice() {
		t.Errorf("got %q, want the plain notice", got)
	}
	if CompactNotice(nil) != "" {
		t.Error("no conflicts should produce no notice")
	}
}

func TestToolPathOnlyClaimsWhatItCanAttribute(t *testing.T) {
	// grep and bash touch files too, but not in a way attributable to one
	// path. Claiming otherwise produces notices about files nobody edited.
	cases := []struct {
		tool, args, want string
	}{
		{"read", `{"path":"a.go"}`, "a.go"},
		{"write", `{"path":"b.go"}`, "b.go"},
		{"edit", `{"path":"c.go"}`, "c.go"},
		{"grep", `{"pattern":"x","path":"internal"}`, ""},
		{"bash", `{"cmd":"rm -rf x"}`, ""},
		{"read", `not json`, ""},
	}
	for _, c := range cases {
		if got := ToolPath(c.tool, json.RawMessage(c.args)); got != c.want {
			t.Errorf("ToolPath(%q, %s) = %q, want %q", c.tool, c.args, got, c.want)
		}
	}
}

func TestWritesFiles(t *testing.T) {
	for _, tool := range []string{"write", "edit"} {
		if !WritesFiles(tool) {
			t.Errorf("%s should count as a write", tool)
		}
	}
	for _, tool := range []string{"read", "grep", "glob", "bash"} {
		if WritesFiles(tool) {
			t.Errorf("%s should not count as a write", tool)
		}
	}
}

func TestValidateResultAcceptsFencedJSON(t *testing.T) {
	// Models wrap JSON in fences however firmly the prompt says not to.
	// Rejecting a correct answer over a code fence would make schema
	// validation useless in practice.
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {"files": {"type": "array", "items": {"type": "string"}}},
		"required": ["files"]
	}`)
	output := "Here you go:\n```json\n{\"files\": [\"a.go\", \"b.go\"]}\n```\nHope that helps!"

	got, err := ValidateResult(output, schema)
	if err != nil {
		t.Fatalf("validation failed on fenced JSON: %v", err)
	}
	var parsed struct {
		Files []string `json:"files"`
	}
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("result is not usable JSON: %v", err)
	}
	if len(parsed.Files) != 2 {
		t.Errorf("files = %v", parsed.Files)
	}
}

func TestValidateResultRejectsAMissingField(t *testing.T) {
	// The whole point: a spawner gets a value it can index, or an error saying
	// why it could not — never prose it has to interpret.
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {"answer": {"type": "string"}},
		"required": ["answer"]
	}`)
	if _, err := ValidateResult(`{"other": "thing"}`, schema); err == nil {
		t.Error("a result missing a required field was accepted")
	}
}

func TestValidateResultRejectsProse(t *testing.T) {
	schema := json.RawMessage(`{"type": "object"}`)
	if _, err := ValidateResult("I had a look and it seems fine.", schema); err == nil {
		t.Error("prose was accepted where an object was required")
	}
}

func TestValidateResultWithoutSchemaPassesThrough(t *testing.T) {
	got, err := ValidateResult("  just prose  ", nil)
	if err != nil || got != "just prose" {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestExtractJSONStopsAtTheValue(t *testing.T) {
	// Trailing prose after the JSON is common; taking the last bracket in the
	// string would swallow it.
	got := ExtractJSON(`{"a": {"b": "]"}} and then some commentary [not json]`)
	if got != `{"a": {"b": "]"}}` {
		t.Errorf("got %q", got)
	}
}

func TestValidateResultRejectsAWrongType(t *testing.T) {
	schema := json.RawMessage(`{
      "type": "object",
      "properties": {"count": {"type": "integer"}},
      "required": ["count"]
    }`)
	if _, err := ValidateResult(`{"count":"seven"}`, schema); err == nil {
		t.Error("a string where an integer belongs was accepted")
	}
}

func TestSpawnRefusesAnEmptyTask(t *testing.T) {
	srv, _ := testServer(t)
	if _, err := srv.Spawn("   ", nil, nil); err == nil {
		t.Error("an empty task was accepted")
	}
}

func TestSpawnForRefusesABadSchemaUpFront(t *testing.T) {
	// Discovering the contract is unusable only after the worker finishes means
	// its tokens are already spent.
	srv, _ := testServer(t)
	_, err := srv.SpawnFor("bat", "do a thing", nil, json.RawMessage(`{"type": 42}`))
	if err == nil {
		t.Fatal("an invalid schema was accepted")
	}
	if !strings.Contains(err.Error(), "schema") {
		t.Errorf("err = %v", err)
	}
	if len(srv.Sessions()) != 0 {
		t.Errorf("a worker was started anyway: %v", srv.Sessions())
	}
}

func TestSpawnForBreaksAfterTooManyWorkers(t *testing.T) {
	// Every auto-started loop needs a breaker (plan.md §12.6). A model that
	// decides delegation is going well will spawn workers indefinitely, and each
	// one spends real tokens against the same key.
	srv, _ := testServer(t)
	srv.swarm.mu.Lock()
	srv.swarm.spawnCount["bat"] = MaxWorkersPerSession
	srv.swarm.mu.Unlock()

	if _, err := srv.SpawnFor("bat", "one more", nil, nil); err == nil {
		t.Fatal("the per-session spawn limit did not fire")
	}
}

func TestWorkerTimeoutIsBounded(t *testing.T) {
	// A worker nobody is watching is the one most likely to run forever.
	if WorkerTimeout <= 0 || WorkerTimeout > time.Hour {
		t.Errorf("WorkerTimeout = %v, want a bounded ceiling", WorkerTimeout)
	}
}
