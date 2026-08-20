package tui

import (
	"strings"
	"testing"

	"evilcode/internal/todo"
)

func TestPlanFenceRequiresExactOpener(t *testing.T) {
	// The opener must be exactly ```plan with nothing after it, or an ordinary
	// code block mentioning plans would become a card.
	if got := FindPlanSegments("```plan-b\nnot a card\n```"); len(got) != 0 {
		t.Errorf("```plan-b should not open a card: %+v", got)
	}
	if got := FindPlanSegments("```plan\nbody\n```"); len(got) != 1 {
		t.Errorf("segments = %+v, want 1", got)
	}
}

func TestNestedFenceDoesNotTerminateTheCard(t *testing.T) {
	// A bash example inside a plan belongs inside the borders. Terminating on
	// it would cut the card in half and leave the rest as loose prose.
	src := "```plan\n# Title\n\nSteps:\n\n```bash\ngo test ./...\n```\n\nDone.\n```\n"
	segs := FindPlanSegments(src)
	if len(segs) != 1 {
		t.Fatalf("segments = %d, want 1: %+v", len(segs), segs)
	}
	if !strings.Contains(segs[0].Body, "go test ./...") {
		t.Errorf("the nested block was lost:\n%s", segs[0].Body)
	}
	if !strings.Contains(segs[0].Body, "Done.") {
		t.Errorf("content after the nested block was lost:\n%s", segs[0].Body)
	}
	if segs[0].Open {
		t.Error("the card should be closed")
	}
}

func TestUnterminatedPlanFenceRendersAnyway(t *testing.T) {
	// While streaming, the closing fence has not arrived. Holding the card
	// back until it does makes it pop in at the end instead of growing, and
	// the growing is the part that sells it.
	segs := FindPlanSegments("Here is the plan.\n\n```plan\n# Wire the auth flow\n\nGoal: ...")
	if len(segs) != 1 {
		t.Fatalf("segments = %d, want 1", len(segs))
	}
	if !segs[0].Open {
		t.Error("an unterminated fence should be marked open")
	}
	if !strings.Contains(segs[0].Body, "Wire the auth flow") {
		t.Errorf("body = %q", segs[0].Body)
	}
}

func TestPlanFenceInsideAnotherFenceIsIgnored(t *testing.T) {
	src := "```markdown\nYou can write ```plan blocks like this.\n```\n"
	if got := FindPlanSegments(src); len(got) != 0 {
		t.Errorf("a plan fence inside another fence must be ignored: %+v", got)
	}
}

func TestNoPlanFenceIsCheap(t *testing.T) {
	// The fast path matters: this runs on every frame of a streaming message.
	if got := FindPlanSegments("just some prose with no fences at all"); got != nil {
		t.Errorf("got %+v, want nil", got)
	}
}

func TestPlanCardChrome(t *testing.T) {
	r := testRenderer(80)
	rows := plainLines(r.RenderPlanCard(PlanSegment{
		Body: "# Wire the auth flow\n\nGoal: make refresh survive a cold start.\n",
	}))
	if !strings.Contains(rows[0], "⛭") {
		t.Errorf("top border should carry the gear and title: %q", rows[0])
	}
	if !strings.Contains(rows[0], "Wire the auth flow") {
		t.Errorf("title missing from the border: %q", rows[0])
	}
	// The heading became the title, so it must not also appear in the body.
	body := strings.Join(rows[1:], "\n")
	if strings.Contains(body, "# Wire the auth flow") {
		t.Errorf("the title heading should be removed from the body:\n%s", body)
	}

	width := len([]rune(rows[0]))
	for i, row := range rows {
		if got := len([]rune(row)); got != width {
			t.Errorf("row %d is %d cells, want %d: %q", i, got, width, row)
		}
	}
}

func TestPlanCardEmptyBody(t *testing.T) {
	r := testRenderer(80)
	joined := strings.Join(plainLines(r.RenderPlanCard(PlanSegment{Body: "  \n\n"})), "\n")
	if !strings.Contains(joined, "empty plan") {
		t.Errorf("an empty plan should say so:\n%s", joined)
	}
}

func TestPlanCardWrapsBeforeBoxing(t *testing.T) {
	// The box truncates, so unwrapped text would clip at the border instead of
	// flowing onto the next row.
	long := strings.Repeat("word ", 200)
	r := testRenderer(60)
	rows := plainLines(r.RenderPlanCard(PlanSegment{Body: "# T\n\n" + long}))
	if len(rows) < 4 {
		t.Fatalf("expected the body to wrap onto several rows, got %d", len(rows))
	}
	width := len([]rune(rows[0]))
	for i, row := range rows {
		if got := len([]rune(row)); got != width {
			t.Errorf("row %d is %d cells, want %d", i, got, width)
		}
	}
}

func TestPlanPromptSubstitutesTheGoal(t *testing.T) {
	// The prompt is the whole mechanism: /plan is a synthetic user turn, not a
	// mode, so its wording is the feature.
	for _, want := range []string{
		"Do NOT implement anything yet",
		"planning-only mode",
		"avoid an exhaustive repository tour",
		"```plan",
		"stop and wait for the user",
		"todo",
	} {
		if !strings.Contains(PlanPrompt, want) {
			t.Errorf("plan prompt is missing %q", want)
		}
	}
}

func TestTodoCardShowsTheAntiGamingArrow(t *testing.T) {
	// A completed item whose planning and completion confidence differ shows
	// `75→100%`; that arrow is what makes a bulk end-stamp visible.
	plan := uint8(75)
	done := uint8(100)
	r := testRenderer(80)
	joined := strings.Join(plainLines(r.RenderTodoCard(TodoCardState{
		Items: []todo.Item{{
			ID: "a", Content: "Read the handler", Status: todo.StatusCompleted,
			Confidence: &plan, CompletionConfidence: &done,
		}},
	})), "\n")
	if !strings.Contains(joined, "75→100%") {
		t.Errorf("the arrow is missing:\n%s", joined)
	}
}

func TestTodoCardGlyphs(t *testing.T) {
	r := testRenderer(80)
	group := "auth"
	items := []todo.Item{
		{ID: "a", Content: "done", Status: todo.StatusCompleted, Group: &group},
		{ID: "b", Content: "active", Status: todo.StatusInProgress, Group: &group},
		{ID: "c", Content: "blocked", Status: todo.StatusPending, Group: &group, BlockedBy: []string{"b"}},
		{ID: "d", Content: "open", Status: todo.StatusPending, Group: &group},
		{ID: "e", Content: "dropped", Status: todo.StatusCancelled, Group: &group},
	}
	joined := strings.Join(plainLines(r.RenderTodoCard(TodoCardState{Items: items})), "\n")
	for _, want := range []string{"✓", "●", "⊳", "○", "✗", "(blocked)", "auth"} {
		if !strings.Contains(joined, want) {
			t.Errorf("card is missing %q:\n%s", want, joined)
		}
	}
}

func TestTodoCardEmpty(t *testing.T) {
	r := testRenderer(80)
	joined := strings.Join(plainLines(r.RenderTodoCard(TodoCardState{})), "\n")
	if !strings.Contains(joined, "No tasks yet") {
		t.Errorf("got %q", joined)
	}
}

func TestTodoDeltaFormA(t *testing.T) {
	// A single flip reads better as the item itself than as a count.
	prev := []todo.Item{{ID: "a", Content: "Wire the refresh path", Status: todo.StatusPending}}
	next := []todo.Item{{ID: "a", Content: "Wire the refresh path", Status: todo.StatusInProgress}}
	r := testRenderer(80)
	rows := plainLines(r.RenderTodoDelta(todo.DiffItems(prev, next)))
	if len(rows) != 1 {
		t.Fatalf("form A should be one row, got %d: %v", len(rows), rows)
	}
	if !strings.Contains(rows[0], "Wire the refresh path") || !strings.Contains(rows[0], "▶") {
		t.Errorf("row = %q", rows[0])
	}
}

func TestTodoDeltaFormB(t *testing.T) {
	prev := []todo.Item{{ID: "a", Content: "one", Status: todo.StatusPending}}
	next := []todo.Item{
		{ID: "a", Content: "one", Status: todo.StatusCompleted},
		{ID: "b", Content: "two", Status: todo.StatusPending},
	}
	r := testRenderer(80)
	rows := plainLines(r.RenderTodoDelta(todo.DiffItems(prev, next)))
	if len(rows) < 3 {
		t.Fatalf("form B should list items, got %v", rows)
	}
	if !strings.Contains(rows[0], "(1/2)") {
		t.Errorf("summary row = %q, want the counts", rows[0])
	}
}

func TestTodoDeltaCapsRows(t *testing.T) {
	var next []todo.Item
	for i := 0; i < 20; i++ {
		next = append(next, todo.Item{
			ID: string(rune('a' + i)), Content: "task", Status: todo.StatusPending,
		})
	}
	r := testRenderer(80)
	rows := plainLines(r.RenderTodoDelta(todo.DiffItems(nil, next)))
	if len(rows) > 8 {
		t.Errorf("delta rendered %d rows; it should cap and summarize", len(rows))
	}
	if !strings.Contains(strings.Join(rows, "\n"), "more") {
		t.Error("a capped delta should say how many it hid")
	}
}

func TestAssessmentDeltaRendersMovement(t *testing.T) {
	r := testRenderer(80)
	rows := plainLines(r.RenderAssessmentDelta([]todo.AssessmentDelta{
		{Label: "Plan", Metric: "Understands user intent", Old: 72, New: 91},
	}))
	joined := strings.Join(rows, "\n")
	for _, want := range []string{"Plan", "updated", "72%", "→", "91%"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q:\n%s", want, joined)
		}
	}
}
