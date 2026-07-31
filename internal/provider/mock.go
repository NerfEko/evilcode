package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
)

// ScenarioEnv selects which canned conversation the mock provider replays.
const ScenarioEnv = "EVILCODE_SCENARIO"

// Mock replays deterministic canned streams so TUI and agent tests never need a
// live model (plan.md §14). Each ChatStream call consumes the next turn of the
// selected scenario; running past the end yields a short closing turn rather
// than an error, so a probe that presses Enter once too often does not blow up.
type Mock struct {
	name string

	mu       sync.Mutex
	scenario string
	turn     int

	// Turns overrides the named scenario entirely, for tests that want to
	// script an exact stream.
	Turns [][]Chunk
}

// mockRotation hands out scenarios in order when several are named. The daemon
// builds one provider per session from one config, so a comma-separated
// EVILCODE_SCENARIO is the only way for a probe to give two sessions in the same
// daemon different scripts — which is what a swarm scenario needs to show two
// agents colliding on a file.
var mockRotation struct {
	mu   sync.Mutex
	next int
}

// NewMock builds a mock provider. An empty scenario reads ScenarioEnv, falling
// back to "chat".
//
// A comma-separated scenario is a rotation: successive providers take the next
// entry, and the last one repeats once the list runs out.
func NewMock(name, scenario string) *Mock {
	if scenario == "" {
		scenario = os.Getenv(ScenarioEnv)
	}
	if scenario == "" {
		scenario = "chat"
	}
	if names := strings.Split(scenario, ","); len(names) > 1 {
		mockRotation.mu.Lock()
		i := min(mockRotation.next, len(names)-1)
		mockRotation.next++
		mockRotation.mu.Unlock()
		scenario = strings.TrimSpace(names[i])
	}
	return &Mock{name: name, scenario: scenario}
}

// ResetMockRotation restarts the rotation, so one test's providers do not shift
// the next test's.
func ResetMockRotation() {
	mockRotation.mu.Lock()
	mockRotation.next = 0
	mockRotation.mu.Unlock()
}

func (m *Mock) Name() string { return m.name }

// Scenario reports which script is being replayed.
func (m *Mock) Scenario() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.scenario
}

// Reset rewinds to the first turn.
func (m *Mock) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.turn = 0
}

func (m *Mock) ChatStream(ctx context.Context, req Req) (<-chan Chunk, error) {
	m.mu.Lock()
	turns := m.Turns
	if turns == nil {
		scenario := m.scenario
		// A swarm scenario needs two scripts at once: the agent that reads a
		// file and the worker that edits it out from under it. They are one
		// process with one EVILCODE_SCENARIO, so the worker is recognized by
		// its own brief — which the daemon writes, not the model.
		if worker, ok := mockScenarios[scenario+"-worker"]; ok && isWorkerBrief(req) {
			turns = worker
		} else {
			turns = mockScenarios[scenario]
		}
	}
	if turns == nil {
		m.mu.Unlock()
		return nil, fmt.Errorf("mock: unknown scenario %q", m.scenario)
	}
	idx := m.turn
	m.turn++
	m.mu.Unlock()

	var chunks []Chunk
	if idx < len(turns) {
		chunks = turns[idx]
	} else {
		chunks = closingTurn
	}

	ch := make(chan Chunk)
	go func() {
		defer close(ch)
		for _, c := range chunks {
			select {
			case ch <- c:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

// isWorkerBrief reports whether this conversation is a spawned worker's.
func isWorkerBrief(req Req) bool {
	for _, msg := range req.Messages {
		if msg.Role == RoleUser && strings.HasPrefix(msg.Content, "You are a worker agent.") {
			return true
		}
	}
	return false
}

func (m *Mock) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	// A deterministic, cheap embedding: enough structure that identical text
	// scores identical and different text does not, without pulling in a model.
	out := make([][]float32, len(texts))
	for i, t := range texts {
		vec := make([]float32, 8)
		for j, r := range t {
			vec[j%8] += float32(r%97) / 97
		}
		out[i] = vec
	}
	return out, nil
}

func (m *Mock) Models(ctx context.Context) ([]ModelInfo, error) {
	return []ModelInfo{
		{Name: "mock-small", ContextWindow: 8192, Size: "1b"},
		{Name: "mock-large", ContextWindow: 200000, Size: "480b"},
	}, nil
}

// text builds the chunk sequence for a message streamed word by word, which is
// what makes the mock exercise the same incremental-render path a real model
// does.
func text(s string) []Chunk {
	words := strings.SplitAfter(s, " ")
	out := make([]Chunk, 0, len(words)+1)
	for _, w := range words {
		if w == "" {
			continue
		}
		out = append(out, Chunk{Text: w})
	}
	return out
}

func done(prompt, completion int) Chunk {
	return Chunk{
		Done:  true,
		Usage: &Usage{PromptTokens: prompt, CompletionTokens: completion, ContextMax: 200000},
	}
}

func call(id, name string, args any) Chunk {
	raw, err := json.Marshal(args)
	if err != nil {
		panic("mock: bad canned tool args: " + err.Error())
	}
	return Chunk{ToolCalls: []ToolCall{{ID: id, Name: name, Args: raw}}}
}

var closingTurn = []Chunk{
	{Text: "Done."},
	done(120, 2),
}

// planBody is chunked deliberately across the fence markers: the plan card must
// materialize and grow while streaming rather than popping in at the end
// (plan.md §12.1), and that only gets exercised if a chunk boundary lands
// mid-fence.
var planChunks = []Chunk{
	{Text: "Here is the plan.\n\n``"},
	{Text: "`plan\n# Wire the auth flow\n"},
	{Text: "\n## Goal\nMake the refresh path survive a cold start.\n"},
	{Text: "\n## Approach\n1. Read the callback handler\n2. Add the retry gate\n"},
	{Text: "\n```bash\ngo test ./internal/auth/...\n```\n"},
	{Text: "\n## Validation\nThe test above, plus one manual cold start.\n"},
	{Text: "``"},
	{Text: "`\n"},
	done(340, 96),
}

var mockScenarios = map[string][][]Chunk{
	// Plain prose, no tools: the baseline streaming-markdown path.
	"chat": {
		append(text("Yes — that function parses the config and returns the defaults when the file is absent. "+
			"It never returns a nil map, so callers can index it safely."),
			done(210, 28)),
	},

	// Reasoning traces ahead of the answer (§9.7).
	"thinking": {
		{
			{Reasoning: "The user is asking about "},
			{Reasoning: "the parse path. Let me check the fallback."},
			{Text: "It falls back to the defaults."},
			done(180, 12),
		},
	},

	// One tool call, its result, then prose — the ordinary agentic turn.
	"tools": {
		{
			{Text: "Let me look."},
			call("call_1", "read", map[string]any{"path": "internal/config/config.go"}),
			done(200, 18),
		},
		append(text("The defaults live at the top of that file and are copied per call, "+
			"so mutating the returned map is safe."),
			done(420, 24)),
	},

	// Memory (§19): the first turn stores a preference, the second recalls it.
	// Two turns because the 🧠 tile only appears once the bank has something in
	// it — a probe that boots into an empty bank can only show the empty case.
	"memory": {
		{
			{Text: "Noted."},
			call("call_1", "remember", map[string]any{
				"text": "the user prefers tabs over spaces in Go",
				"kind": "preference",
			}),
			done(180, 20),
		},
		append(text("I'll remember that."), done(220, 8)),
		{
			{Text: "Let me check what I know."},
			call("call_2", "recall", map[string]any{"query": "indentation preference"}),
			done(260, 16),
		},
		// Deliberately long: the memory-activity widget is seven rows with its
		// border, and §8.3 will not dock a box into a region shorter than it.
		// A short reply leaves no room, so the frame would only ever prove the
		// tile and never the widget.
		// Short lines, not long prose: the dock needs free *columns*, and
		// full-width paragraphs leave none. A list is what a real answer of
		// this shape looks like anyway.
		append(text("You prefer tabs, so that is what I used.\n\n"+
			"- reindented the block\n"+
			"- left the rest alone\n"+
			"- no reflow of the file\n"+
			"- gofmt is still clean\n\n"+
			"Say the word to normalize\nthe whole file separately."), done(340, 92)),
	},

	// A tool call the model buffers whole, with no preceding text — the case
	// plan.md Part V warns about.
	"tools-buffered": {
		{
			call("call_1", "grep", map[string]any{"pattern": "func New", "path": "internal"}),
			done(200, 14),
		},
		append(text("Three constructors matched."), done(300, 6)),
	},

	// Several calls in one turn, to exercise batch execution and the batch
	// status block (§9.5).
	"tools-batch": {
		{
			{Text: "Checking a few things."},
			{ToolCalls: []ToolCall{
				{ID: "call_1", Name: "read", Args: json.RawMessage(`{"path":"main.go"}`)},
				{ID: "call_2", Name: "grep", Args: json.RawMessage(`{"pattern":"TODO"}`)},
				{ID: "call_3", Name: "bash", Args: json.RawMessage(`{"cmd":"go vet ./..."}`)},
			}},
			done(260, 30),
		},
		append(text("All three came back clean."), done(600, 8)),
	},

	// A plan fence chunked mid-marker.
	"plan": {planChunks},

	// A todo write, for the inline card, the delta rows, and the arrows that
	// distinguish an evidence-driven rise from a bulk end-stamp (§12.5).
	"todos": {
		{
			{Text: "Tracking the work."},
			call("call_1", "todo", map[string]any{
				"items": []map[string]any{
					{"id": "1", "content": "Read the OAuth callback handler", "status": "completed",
						"group": "auth flow", "confidence": 75, "completion_confidence": 100},
					{"id": "2", "content": "Wire the refresh path", "status": "in_progress",
						"group": "auth flow", "confidence": 82},
					{"id": "3", "content": "Add the retry gate", "status": "pending",
						"group": "auth flow", "confidence": 60, "blocked_by": []string{"2"}},
					{"id": "4", "content": "Write the integration test", "status": "pending",
						"group": "auth flow"},
				},
				"plan": map[string]any{
					"user_intention": "Ship the plan-level intent gate so low-confidence " +
						"plans get re-examined before work starts",
					"understands_user_intent": 87,
				},
				"goals": []map[string]any{{
					"group":                "auth flow",
					"feedback_loop":        "go test ./internal/auth/...",
					"closed_feedback_loop": 92,
					"end_to_end_ownership": 88,
				}},
			}),
			done(300, 40),
		},
		append(text("Tracked. Working through the refresh path next."), done(500, 10)),
	},

	// An edit that produces a diff for the inline diff renderer (§9.3).
	// The reader half of the swarm conflict pair: it reads exactly the file the
	// `diff` scenario edits, so running the two against one daemon produces a
	// real ⚠ notice rather than a simulated one (plan.md §20).
	"conflict-read": {
		{
			{Text: "Let me look at the clamp."},
			call("call_1", "read", map[string]any{"path": "testdata/clamp.go"}),
			done(200, 14),
		},
		append(text("Read it. The bound looks suspicious."), done(320, 10)),
	},

	// The follow-up turn, run after another agent has written the file. It says
	// nothing itself — the point is the conflict notice that arrives ahead of
	// it.
	"conflict-after": {
		append(text("Right — I will re-read it before touching anything."), done(360, 12)),
	},

	// A mermaid fence (§5). With mmdc installed and a kitty-capable terminal the
	// diagram renders inline; without either it falls back to highlighted
	// source under the hint, which is the path most terminals take.
	"mermaid": {
		append(text("Here is the loop:\n\n```mermaid\ngraph TD\n  A[plan.md] --> B[implement]\n"+
			"  B --> C{tests pass?}\n  C -->|yes| D[look at the PNG]\n  C -->|no| B\n"+
			"  D --> E[commit]\n```\n\nThat is the whole discipline."), done(300, 60)),
	},

	"diff": {
		{
			{Text: "Fixing the off-by-one."},
			call("call_1", "edit", map[string]any{
				"path": "testdata/clamp.go",
				"old":  "if offset > max {",
				"new":  "if offset >= max {",
			}),
			done(240, 22),
		},
		append(text("That was the clamp bug."), done(380, 6)),
	},

	// The ask tool's inline option picker (§17, §5.3 chrome).
	"ask": {
		{
			{Text: "One decision first."},
			call("call_1", "ask", map[string]any{
				"question": "Should the retry gate back off exponentially or on a fixed interval?",
				"options": []map[string]any{
					{"label": "Exponential backoff", "description": "Recommended; matches the rest of the client"},
					{"label": "Fixed interval", "description": "Simpler, but retries a dead endpoint hard"},
				},
			}),
			done(220, 30),
		},
		append(text("Exponential it is."), done(400, 6)),
	},

	// A detached command, for the background-task widget and its completion
	// notice (§17, §8.3).
	"background": {
		{
			{Text: "Starting the build."},
			call("call_1", "bash", map[string]any{
				"cmd": "sleep 2 && echo built", "background": true,
			}),
			done(200, 20),
		},
		append(text("Kicked off; I will report when it lands."), done(300, 8)),
	},

	// A failure mid-stream, for the error row and retry paths (§9.8, §9.9).
	"error": {
		{
			{Text: "Starting..."},
			{Err: fmt.Errorf("mock: canned stream failure")},
		},
	},

	// A long answer, for scrolling, packed-vs-scrolling, and the tail-follow
	// catch-up animation (§3.2, §4.2).
	"long": {
		func() []Chunk {
			var out []Chunk
			for i := 1; i <= 40; i++ {
				out = append(out, Chunk{Text: fmt.Sprintf("Line %d of a deliberately long answer.\n", i)})
			}
			return append(out, done(300, 400))
		}(),
	},
}

// MockScenarios lists the built-in scenario names, for `--help` output and
// tests that want to sweep all of them.
func MockScenarios() []string {
	out := make([]string, 0, len(mockScenarios))
	for name := range mockScenarios {
		out = append(out, name)
	}
	return out
}

// Swarm conflict (§20): one agent reads a file, a worker edits it, and the
// reader is told at its next turn end. Two scripts because the daemon runs both
// in one process — the worker is picked out by its brief.
func init() {
	mockScenarios["conflict"] = [][]Chunk{
		{
			{Text: "Let me look at the clamp."},
			call("call_1", "read", map[string]any{"path": "testdata/clamp.go"}),
			done(200, 14),
		},
		append(text("It clamps with `>` where it wants `>=`. Summoning help."), done(320, 10)),
		append(text("Understood — I will re-read it before editing."), done(420, 8)),
	}
	mockScenarios["conflict-worker"] = [][]Chunk{
		{
			{Text: "Fixing it."},
			call("call_1", "edit", map[string]any{
				"path": "testdata/clamp.go",
				"old":  "if offset > max {",
				"new":  "if offset >= max {",
			}),
			done(240, 18),
		},
		append(text(`{"file":"testdata/clamp.go","changed":true}`), done(300, 12)),
	}
}
