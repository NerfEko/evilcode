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

// NewMock builds a mock provider. An empty scenario reads ScenarioEnv, falling
// back to "chat".
func NewMock(name, scenario string) *Mock {
	if scenario == "" {
		scenario = os.Getenv(ScenarioEnv)
	}
	if scenario == "" {
		scenario = "chat"
	}
	return &Mock{name: name, scenario: scenario}
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
		turns = mockScenarios[m.scenario]
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

	// An edit that produces a diff for the inline diff renderer (§9.3).
	"diff": {
		{
			{Text: "Fixing the off-by-one."},
			call("call_1", "edit", map[string]any{
				"path": "internal/scroll/scroll.go",
				"old":  "if offset > max {",
				"new":  "if offset >= max {",
			}),
			done(240, 22),
		},
		append(text("That was the clamp bug."), done(380, 6)),
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
