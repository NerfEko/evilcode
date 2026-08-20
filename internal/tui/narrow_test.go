package tui

import (
	"strings"
	"testing"
	"time"

	"evilcode/internal/memory"
	"evilcode/internal/provider"
	"evilcode/internal/todo"
)

// TestNothingOverflowsANarrowTerminal renders every overlay and card at widths
// down to 40 and fails on any row wider than the terminal.
//
// A row that overflows does not clip — the terminal wraps it, which shifts
// every row below and tears the frame apart. It is invisible at the 140 columns
// the probe runs at, which is exactly why it needs its own test: the model
// picker's key hint was 91 cells wide and broke every terminal under that.
func TestNothingOverflowsANarrowTerminal(t *testing.T) {
	widths := []int{40, 60, 80, 100, 140}

	cases := map[string]func(r *Renderer) []string{
		"picker": func(r *Renderer) []string {
			return r.RenderPicker(PickerState{
				Entries: []ModelEntry{{Name: "a-rather-long-model-name", Provider: "ollama-cloud",
					Detail: "480b", Recommended: true}},
				Height: 6,
			})
		},
		"reasoning-picker": func(r *Renderer) []string {
			return r.RenderReasoningPicker(reasoningPickerState{
				sel:      ModelEntry{Name: "a-rather-long-model-name", Provider: "ollama-cloud"},
				levels:   provider.OllamaReasoningEfforts(),
				selected: 3,
			})
		},
		"help": func(r *Renderer) []string { return r.RenderHelp(0, r.Width, 24) },
		"history": func(r *Renderer) []string {
			var h HistorySearch
			h.Open("", 0)
			h.Query = "auth"
			h.Matches = []string{"fix the auth redirect loop that keeps failing on cold start"}
			return r.RenderHistorySearch(&h)
		},
		"memory-tile": func(r *Renderer) []string {
			return r.RenderMemoryTile([]memory.Hit{{
				Record: memory.Record{Text: "the user prefers tabs over spaces in every language",
					Kind: memory.KindPreference}}})
		},
		"todo-card": func(r *Renderer) []string {
			return r.RenderTodoCard(TodoCardState{Items: []todo.Item{{
				ID: "1", Content: "wire the refresh path through the retry gate",
				Status: todo.StatusInProgress}}})
		},
		"swarm": func(r *Renderer) []string {
			s := &SwarmState{}
			s.Publish([]SwarmAgent{{Name: "bat", Task: "wiring the auth flow",
				Worker: true, Running: true, Since: 42 * time.Second}})
			return r.RenderWidget(r.SwarmStatusWidget(s, 0))
		},
		"productivity": func(r *Renderer) []string {
			return r.RenderProductivity(Stats{
				Sessions: 12, Messages: 340,
				First:   time.Now().Add(-30 * 24 * time.Hour),
				Busiest: DayCount{Day: time.Now(), Prompts: 40},
				ByDay:   []DayCount{{Day: time.Now(), Prompts: 40}},
			}, r.Width)
		},
	}

	for name, render := range cases {
		for _, w := range widths {
			r := testRenderer(w)
			for i, row := range plainLines(render(r)) {
				if got := len([]rune(row)); got > w {
					t.Errorf("%s at width %d: row %d is %d cells and will wrap:\n  %q",
						name, w, i, got, row)
				}
			}
		}
	}
}

func TestContextWidgetReportsWhatIsUsed(t *testing.T) {
	// It used to print the *remaining* fraction beside a bar that fills as
	// context is consumed, so 428 tokens of 200k read as 99% — the opposite of
	// the truth, and in direct contradiction of the composer hint below it.
	r := testRenderer(80)
	rows := plainLines(r.ContextWidget(428, 200_000).Lines)
	joined := strings.Join(rows, "\n")

	if strings.Contains(joined, "99%") {
		t.Errorf("428/200k reported as 99%%:\n%s", joined)
	}
	if !strings.Contains(joined, "0%") {
		t.Errorf("want the used fraction:\n%s", joined)
	}
	if !strings.Contains(joined, "200k") || strings.Contains(joined, "200.0k") {
		t.Errorf("the window should read as a round number:\n%s", joined)
	}

	// And the other end: a nearly-full window must say so.
	full := strings.Join(plainLines(r.ContextWidget(198_000, 200_000).Lines), "\n")
	if !strings.Contains(full, "99%") {
		t.Errorf("198k/200k should read as 99%%:\n%s", full)
	}
}
