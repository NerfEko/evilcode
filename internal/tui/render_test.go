package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"evilcode/internal/theme"
)

func testRenderer(width int) *Renderer {
	return NewRenderer(theme.Dracula(), width)
}

// plain strips ANSI so tests assert on content, not escape sequences.
func plain(s string) string {
	var b strings.Builder
	inEscape := false
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == 0x1b:
			inEscape = true
		case inEscape && (s[i] == 'm' || s[i] == 'K'):
			inEscape = false
		case !inEscape:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

func plainLines(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = plain(l)
	}
	return out
}

func TestSendActionMatrix(t *testing.T) {
	// The send model from plan.md §6.3: idle submits, processing queues, a
	// slash command runs now regardless.
	tests := []struct {
		name       string
		processing bool
		input      string
		want       SendAction
	}{
		{"idle, enter", false, "hi", Submit},

		{"processing, enter", true, "hi", Queue},

		// A harness command is not for the model, so it runs now regardless.
		{"slash command while processing", true, "/model", Submit},
		{"slash command with leading space", true, "  /help", Submit},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SendActionFor(tt.processing, tt.input)
			if got != tt.want {
				t.Errorf("SendActionFor(%v, %q) = %v, want %v",
					tt.processing, tt.input, got, tt.want)
			}
		})
	}
}

func TestProcessingAlwaysQueues(t *testing.T) {
	// There is no immediate-send path anymore: every message typed while a
	// turn runs waits for it to end, so nothing can be delivered twice.
	if got := SendActionFor(true, "hi"); got != Queue {
		t.Errorf("SendActionFor(true, \"hi\") = %v, want Queue", got)
	}
}

func TestSpinnerIsACircularSpin(t *testing.T) {
	// The exact sequence matters: this is a circular spin, and reordering it
	// into a grow-and-recede reads completely differently (plan.md §8.1).
	want := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	if len(SpinnerFrames) != len(want) {
		t.Fatalf("frames = %d, want %d", len(SpinnerFrames), len(want))
	}
	for i := range want {
		if SpinnerFrames[i] != want[i] {
			t.Errorf("frame %d = %q, want %q", i, SpinnerFrames[i], want[i])
		}
	}
}

func TestSpinnerRunsAt12Point5FPS(t *testing.T) {
	if SpinnerInterval != 80*time.Millisecond {
		t.Errorf("interval = %v, want 80ms (12.5 fps)", SpinnerInterval)
	}
	// Each tick must advance exactly one frame.
	for i := 0; i < len(SpinnerFrames)*2; i++ {
		got := SpinnerFrame(time.Duration(i) * SpinnerInterval)
		want := SpinnerFrames[i%len(SpinnerFrames)]
		if got != want {
			t.Fatalf("at tick %d got %q, want %q", i, got, want)
		}
	}
}

func TestSpinnerFramesAreSingleCell(t *testing.T) {
	for _, f := range SpinnerFrames {
		if w := lipgloss.Width(f); w != 1 {
			t.Errorf("frame %q is %d cells wide, want 1", f, w)
		}
	}
}

func TestUserBandFillsTheWidth(t *testing.T) {
	// The band is the design's only "bubble"; a ragged right edge reads as a
	// highlight artifact instead.
	r := testRenderer(40)
	lines := r.Lines(&Block{Kind: BlockUser, Text: "hello", Number: 0})
	if len(lines) != 1 {
		t.Fatalf("lines = %d", len(lines))
	}
	if w := lipgloss.Width(lines[0]); w != 40 {
		t.Errorf("band width = %d, want the full 40", w)
	}
	if !strings.Contains(plain(lines[0]), "1› hello") {
		t.Errorf("band = %q", plain(lines[0]))
	}
}

func TestUserBandWrapsAndKeepsTheBand(t *testing.T) {
	r := testRenderer(20)
	lines := r.Lines(&Block{Kind: BlockUser, Text: strings.Repeat("word ", 12), Number: 0})
	if len(lines) < 2 {
		t.Fatalf("expected wrapping, got %d line(s)", len(lines))
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w != 20 {
			t.Errorf("line %d width = %d, want 20 — continuations must keep the band", i, w)
		}
	}
	// Continuations indent to the prefix width rather than restarting at zero.
	if !strings.HasPrefix(plain(lines[1]), "   ") {
		t.Errorf("continuation = %q, want it indented under the prefix", plain(lines[1]))
	}
}

func TestPromptNumbersAreRainbowDecayed(t *testing.T) {
	// The newest prompt is full red and older ones fade; this is the strongest
	// identity cue in the transcript (plan.md §7.7).
	r := testRenderer(40)
	newest := r.Lines(&Block{Kind: BlockUser, Text: "x", Number: 0})[0]
	older := r.Lines(&Block{Kind: BlockUser, Text: "x", Number: 4})[0]
	if newest == older {
		t.Error("prompt numbers at different distances rendered identically")
	}
	// lipgloss emits truecolor SGR, not hex, so assert on the RGB triple.
	c := theme.Rainbow(0)
	want := fmt.Sprintf("38;2;%d;%d;%d", c.R, c.G, c.B)
	if !strings.Contains(newest, want) {
		t.Errorf("newest prompt does not use the distance-0 color %s:\n%q", want, newest)
	}
}

func TestToolRowFormat(t *testing.T) {
	r := testRenderer(80)
	lines := r.Lines(&Block{
		Kind:       BlockTool,
		ToolName:   "read",
		ToolTarget: "src/main.go",
		ToolIntent: "load entry point",
		ToolTokens: 1200,
		HasDiff:    true,
		Added:      8,
		Removed:    5,
	})
	got := plain(lines[0])
	for _, want := range []string{"✓", "read", "src/main.go", "load entry point", "1.2k tok", "+8", "-5"} {
		if !strings.Contains(got, want) {
			t.Errorf("tool row %q is missing %q", got, want)
		}
	}
}

func TestFailedToolRowUsesTheCross(t *testing.T) {
	r := testRenderer(80)
	got := plain(r.Lines(&Block{Kind: BlockTool, ToolName: "bash", Failed: true})[0])
	if !strings.Contains(got, "✗") {
		t.Errorf("failed tool row = %q, want a cross", got)
	}
}

func TestHumanTokens(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{0, "0"}, {999, "999"}, {1000, "1.0k"}, {1200, "1.2k"},
		{1_500_000, "1.5M"},
	}
	for _, tt := range tests {
		if got := humanTokens(tt.n); got != tt.want {
			t.Errorf("humanTokens(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

func TestSplitSegments(t *testing.T) {
	segs := SplitSegments("before\n\n```go\nfunc main() {}\n```\n\nafter")
	if len(segs) != 3 {
		t.Fatalf("segments = %d, want 3: %+v", len(segs), segs)
	}
	if segs[0].Code || !strings.Contains(segs[0].Text, "before") {
		t.Errorf("segment 0 = %+v", segs[0])
	}
	if !segs[1].Code || segs[1].Lang != "go" || segs[1].Open {
		t.Errorf("segment 1 = %+v", segs[1])
	}
	if segs[2].Code || !strings.Contains(segs[2].Text, "after") {
		t.Errorf("segment 2 = %+v", segs[2])
	}
}

func TestUnterminatedFenceRendersAnyway(t *testing.T) {
	// While streaming, the closing fence has not arrived yet. Holding the block
	// back until it does makes code pop in at the end instead of growing.
	segs := SplitSegments("here:\n\n```go\nfunc main() {")
	if len(segs) != 2 {
		t.Fatalf("segments = %d, want 2", len(segs))
	}
	if !segs[1].Code || !segs[1].Open {
		t.Errorf("the trailing fence should be an open code segment: %+v", segs[1])
	}
	if !strings.Contains(segs[1].Text, "func main() {") {
		t.Errorf("open segment lost its body: %+v", segs[1])
	}
}

func TestStreamingCodeBlockShowsItsState(t *testing.T) {
	r := testRenderer(60)
	lines := plainLines(r.renderCodeBlock(Segment{Code: true, Lang: "go", Text: "x := 1", Open: true}))
	if !strings.Contains(lines[0], "streaming") {
		t.Errorf("open block header = %q, want a streaming marker", lines[0])
	}
	last := lines[len(lines)-1]
	if !strings.Contains(last, "▌") {
		t.Errorf("open block should end with a live cursor row, got %q", last)
	}
	// A closed block gets the closing chrome instead.
	closed := plainLines(r.renderCodeBlock(Segment{Code: true, Lang: "go", Text: "x := 1"}))
	if !strings.HasPrefix(closed[len(closed)-1], "└─") {
		t.Errorf("closed block should end with └─, got %q", closed[len(closed)-1])
	}
}

func TestCodeBlockChrome(t *testing.T) {
	r := testRenderer(60)
	lines := plainLines(r.renderCodeBlock(Segment{Code: true, Lang: "go", Text: "a\nb"}))
	if lines[0] != "┌─ go" {
		t.Errorf("header = %q", lines[0])
	}
	for _, l := range lines[1 : len(lines)-1] {
		if !strings.HasPrefix(l, "│ ") {
			t.Errorf("body line %q is missing its gutter", l)
		}
	}
}

func TestDiffRendering(t *testing.T) {
	diff := "--- a/x.go\n+++ b/x.go\n@@ -1,3 +1,3 @@\n ctx\n-old line\n+new line\n more\n"
	r := testRenderer(60)
	lines := plainLines(r.renderDiffLang(diff, "go"))

	if lines[0] != "┌─ diff" {
		t.Errorf("header = %q", lines[0])
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "old line") || !strings.Contains(joined, "new line") {
		t.Errorf("diff body missing:\n%s", joined)
	}
	// File headers and hunk markers are chrome, not content.
	if strings.Contains(joined, "+++") || strings.Contains(joined, "@@") {
		t.Errorf("diff should hide file and hunk headers:\n%s", joined)
	}
	if !strings.Contains(lines[len(lines)-1], "(+1 -1 total)") {
		t.Errorf("footer = %q", lines[len(lines)-1])
	}
}

func TestLongDiffElidesTheMiddle(t *testing.T) {
	var b strings.Builder
	b.WriteString("--- a/x\n+++ b/x\n")
	for i := 0; i < 60; i++ {
		b.WriteString("+added line\n")
	}
	r := testRenderer(60)
	lines := plainLines(r.renderDiffLang(b.String(), ""))
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "more changes") {
		t.Errorf("a long diff must elide its middle:\n%s", joined)
	}
	if len(lines) > MaxInlineDiffLines+4 {
		t.Errorf("rendered %d lines for a long diff", len(lines))
	}
}

func TestDiffBodyKeepsSyntaxHighlighting(t *testing.T) {
	// The whole point of the tint formula: code keeps its highlighting rather
	// than becoming a flat block of red or green (plan.md §9.3).
	diff := "--- a/x.go\n+++ b/x.go\n+func main() { return }\n"
	r := testRenderer(60)
	out := strings.Join(r.renderDiffLang(diff, "go"), "\n")
	// More than one foreground color inside the added line means the syntax
	// survived the tint.
	colors := strings.Count(out, "\x1b[38;2;")
	if colors < 2 {
		t.Errorf("added line has %d colors; syntax highlighting was flattened", colors)
	}
}

func TestStatusLinePhases(t *testing.T) {
	r := testRenderer(120)
	tests := []struct {
		name  string
		state StatusState
		want  []string
	}{
		{"sending", StatusState{Phase: PhaseSending, Elapsed: 3 * time.Second}, []string{"sending", "3s"}},
		{"thinking", StatusState{Phase: PhaseThinking, Elapsed: 3 * time.Second}, []string{"thinking", "3s"}},
		{
			"streaming",
			StatusState{Phase: PhaseStreaming, Elapsed: 12 * time.Second,
				TokensPerSecond: 47.3, TokensIn: 12000, TokensOut: 840},
			[]string{"streaming", "12s", "47.3 tps", "↑12.0k", "↓840"},
		},
		{
			"rate limited",
			StatusState{Phase: PhaseRateLimited, RetryIn: 260 * time.Second},
			[]string{"Rate limited", "4m 20s"},
		},
		{
			"waiting for network",
			StatusState{Phase: PhaseWaitingNetwork, Elapsed: 8 * time.Second},
			[]string{"network disconnected", "8s"},
		},
		{
			"running tool",
			StatusState{Phase: PhaseRunningTool, ToolName: "bash",
				ToolIntent: "reading foo.go", Elapsed: 4 * time.Second},
			[]string{"bash", "reading foo.go", "4s", "Alt+B bg"},
		},
		{
			"batch",
			StatusState{Phase: PhaseRunningTool, BatchTotal: 7, BatchDone: 3,
				BatchRunning: []string{"read", "grep"}, BatchLastDone: "bash"},
			[]string{"batch", "3/7 done", "running: read, grep", "last done: bash"},
		},
		{"idle tip", StatusState{Phase: PhaseIdle, Tip: "press things"}, []string{"💡", "press things"}},
		{"idle warning", StatusState{Phase: PhaseIdle, Warning: "context is large"}, []string{"⚠", "context is large"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := plain(r.RenderStatus(tt.state))
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("status %q is missing %q", got, want)
				}
			}
		})
	}
}

func TestStatusLineAppendsQueuedCount(t *testing.T) {
	r := testRenderer(120)
	got := plain(r.RenderStatus(StatusState{Phase: PhaseThinking, Queued: 3}))
	if !strings.Contains(got, "+3 queued") {
		t.Errorf("status = %q, want the queued suffix", got)
	}
}

func TestCacheMissWarning(t *testing.T) {
	r := testRenderer(120)
	got := plain(r.RenderStatus(StatusState{
		Phase: PhaseStreaming, CacheMiss: true, CacheMissSize: 8000,
	}))
	if !strings.Contains(got, "cache miss") || !strings.Contains(got, "⚠") {
		t.Errorf("status = %q, want a cache-miss warning", got)
	}
}

func TestKnightRiderMirrors(t *testing.T) {
	// The two bars mirror, which is what makes it read as one sweeping object.
	r := testRenderer(120)
	seen := map[string]bool{}
	for i := 0; i < KnightRiderCells; i++ {
		got := plain(r.knightRider(StatusState{
			Phase: PhaseRunningTool, ToolName: "bash",
			Elapsed: time.Duration(i) * SpinnerInterval,
		}))
		bars := strings.SplitN(got, " bash ", 2)
		if len(bars) != 2 {
			t.Fatalf("cannot split %q", got)
		}
		left := bars[0]
		right := strings.SplitN(bars[1], " ", 2)[0]
		// Count runes: these glyphs are multibyte.
		if len([]rune(left)) != KnightRiderCells || len([]rune(right)) != KnightRiderCells {
			t.Fatalf("bars are %q and %q, want %d cells each", left, right, KnightRiderCells)
		}
		if left != reverse(right) {
			t.Errorf("bars do not mirror: %q vs %q", left, right)
		}
		seen[left] = true
	}
	if len(seen) != KnightRiderCells {
		t.Errorf("the sweep visited %d of %d positions", len(seen), KnightRiderCells)
	}
}

func reverse(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}

func TestComposerPromptGlyphs(t *testing.T) {
	r := testRenderer(60)
	tests := []struct {
		name  string
		state ComposerState
		want  string
	}{
		{"default", ComposerState{}, "> "},
		{"processing", ComposerState{Processing: true}, "… "},
		{"skill", ComposerState{SkillMode: true}, "» "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			glyph, _ := r.promptGlyph(tt.state)
			if glyph != tt.want {
				t.Errorf("glyph = %q, want %q", glyph, tt.want)
			}
		})
	}
}

func TestComposerHintHiddenWhilePaletteOpen(t *testing.T) {
	// The palette floats over exactly the hint's row.
	r := testRenderer(60)
	if got := r.hintLine(ComposerState{PaletteOpen: true}); got != "" {
		t.Errorf("hint = %q, want none while the palette is open", got)
	}
	if got := r.hintLine(ComposerState{}); got == "" {
		t.Error("hint should show when the palette is closed")
	}
}

func TestPendingRows(t *testing.T) {
	r := testRenderer(60)
	lines := plainLines(r.RenderPending([]PendingMessage{
		{Kind: PendingQueued, Text: "first"},
		{Kind: PendingQueued, Text: "second"},
	}))
	if len(lines) != 2 {
		t.Fatalf("rows = %d", len(lines))
	}
	for i, want := range []string{"first", "second"} {
		if !strings.Contains(lines[i], want) {
			t.Errorf("row %d = %q, want text %q", i, lines[i], want)
		}
		if !strings.Contains(lines[i], "⏳") {
			t.Errorf("row %d = %q, want the queued glyph", i, lines[i])
		}
	}
}

func TestPendingRowsAreCapped(t *testing.T) {
	r := testRenderer(60)
	var msgs []PendingMessage
	for i := 0; i < 10; i++ {
		msgs = append(msgs, PendingMessage{Text: "x"})
	}
	if got := len(r.RenderPending(msgs)); got != MaxPendingRows {
		t.Errorf("rows = %d, want the cap of %d", got, MaxPendingRows)
	}
}

func TestHeaderContent(t *testing.T) {
	r := testRenderer(80)
	lines := plainLines(r.RenderHeader(HeaderState{
		SessionName: "bat",
		Version:     "v0.3.1",
		Provider:    "ollama-cloud",
		Model:       "qwen3-coder:480b-cloud",
		AuthKind:    "api-key",
		Cwd:         "~/projects/evilcode",
		Branch:      "main",
		Providers: []ProviderStatus{
			{Name: "ollama-cloud", Ready: true},
			{Name: "openrouter", Ready: false},
		},
	}))
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		"evilcode", "v0.3.1", "Bat 🦇", "/model to switch",
		"api-key:ollama-cloud", "qwen3-coder:480b-cloud",
		"● ollama-cloud", "○ openrouter", "~/projects/evilcode (main)",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("header is missing %q:\n%s", want, joined)
		}
	}
}

func TestWelcomeScreen(t *testing.T) {
	r := testRenderer(80)
	joined := strings.Join(plainLines(r.RenderWelcome(0, nil)), "\n")
	if !strings.Contains(joined, WelcomeMessage) {
		t.Errorf("welcome missing its greeting:\n%s", joined)
	}
	if !strings.Contains(joined, "◖") || !strings.Contains(joined, "◗") {
		t.Errorf("welcome missing suggestion chips:\n%s", joined)
	}
}

func TestWelcomeChipSelectionUsesFilledBackground(t *testing.T) {
	r := testRenderer(80)
	selected := strings.Join(r.RenderWelcome(0, nil), "\n")
	plain := strings.Join(r.RenderWelcome(-1, nil), "\n")
	if !strings.Contains(selected, "48;2;") {
		t.Fatalf("selected welcome chip has no filled background: %q", selected)
	}
	if selected == plain {
		t.Fatal("focused and unfocused welcome chips render identically")
	}
}

func TestTipRotation(t *testing.T) {
	// Tips are visible for part of the period and quiet for the rest.
	if got := TipAt(0, 80); got == "" {
		t.Error("a tip should show at the start of a period")
	}
	if got := TipAt(TipVisible+time.Second, 80); got != "" {
		t.Errorf("tip = %q, want silence after the visible window", got)
	}
	// A narrow terminal has better uses for the row.
	if got := TipAt(0, MinTipWidth-1); got != "" {
		t.Errorf("tip = %q, want none on a narrow terminal", got)
	}
	// Successive periods rotate.
	first := TipAt(0, 80)
	second := TipAt(TipPeriod, 80)
	if len(Tips) > 1 && first == second {
		t.Error("tips should rotate between periods")
	}
}

func TestWrapPlainBreaksLongWords(t *testing.T) {
	// A single unbreakable token must not push the layout sideways.
	lines := wrapPlain(strings.Repeat("x", 50), 10)
	for i, l := range lines {
		if lipgloss.Width(l) > 10 {
			t.Errorf("line %d is %d cells wide, want at most 10", i, lipgloss.Width(l))
		}
	}
	if strings.Count(strings.Join(lines, ""), "x") != 50 {
		t.Error("wrapping lost characters")
	}
}

func TestWrapPlainPreservesBlankLines(t *testing.T) {
	lines := wrapPlain("a\n\nb", 20)
	if len(lines) != 3 || lines[1] != "" {
		t.Errorf("lines = %q, want the blank line preserved", lines)
	}
}

func TestTruncateCellsRespectsWideGlyphs(t *testing.T) {
	// The bat is two cells; truncating to 3 must keep one whole glyph, not
	// half of one.
	got := truncateCells("🦇🦇", 3)
	if lipgloss.Width(got) > 3 {
		t.Errorf("truncated to %d cells", lipgloss.Width(got))
	}
	if got != "🦇" {
		t.Errorf("got %q, want a whole glyph", got)
	}
}

func TestBlockCacheInvalidatesOnContentChange(t *testing.T) {
	r := testRenderer(40)
	b := Block{Kind: BlockAssistant, Text: "first"}
	first := strings.Join(r.Lines(&b), "\n")

	b.Text = "second"
	second := strings.Join(r.Lines(&b), "\n")
	if first == second {
		t.Error("the cache did not notice the content changed")
	}
}

func TestBlockCacheInvalidatesOnResize(t *testing.T) {
	r := testRenderer(60)
	b := Block{Kind: BlockUser, Text: "hello", Number: 0}
	r.Lines(&b)

	r.SetWidth(30)
	lines := r.Lines(&b)
	if w := lipgloss.Width(lines[0]); w != 30 {
		t.Errorf("width = %d after resize, want 30 — the cache was stale", w)
	}
}

func TestBlockCacheHoldsBothWrapWidths(t *testing.T) {
	// The scrollbar hysteresis renders the whole transcript at the alternate
	// width every frame to decide whether the bar flips. With one cache slot
	// that probe evicted the real render and the real render evicted the probe,
	// so every frame rendered every block twice — 54ms per wheel event on a
	// long transcript.
	r := testRenderer(60)
	b := Block{Kind: BlockUser, Text: "hello", Number: 0}
	r.Lines(&b)
	r.SetWidth(59)
	r.Lines(&b)

	widths := map[int]bool{}
	for _, c := range b.cache {
		if c.valid {
			widths[c.width] = true
		}
	}
	if !widths[60] || !widths[59] {
		t.Errorf("cached widths = %v, want both 60 and 59 held at once", widths)
	}

	r.SetWidth(60)
	if w := lipgloss.Width(r.Lines(&b)[0]); w != 60 {
		t.Errorf("width = %d back at 60, want 60 — the probe corrupted the cache", w)
	}
}

func TestStreamingBlockIsNotCached(t *testing.T) {
	r := testRenderer(40)
	b := Block{Kind: BlockAssistant, Text: "partial", Streaming: true}
	r.Lines(&b)
	if b.cache[0].valid || b.cache[1].valid {
		t.Error("a streaming block must not be cached; it changes every frame")
	}
}

func TestGapsSeparateSubjectsNotBlocks(t *testing.T) {
	// A blank line marks a change of subject. Tool activity — the call row, an
	// error it produced, the todo delta under it — is one subject, so a batch of
	// calls stays packed instead of being spread across three times the height.
	tests := []struct {
		name     string
		kinds    []BlockKind
		wantGaps []bool // gap after each block except the last
	}{
		{
			"consecutive tool rows stay packed",
			[]BlockKind{BlockTool, BlockTool, BlockTool, BlockAssistant},
			[]bool{false, false, true},
		},
		{
			"an error stays with the call that produced it",
			[]BlockKind{BlockTool, BlockError, BlockTool, BlockAssistant},
			[]bool{false, false, true},
		},
		{
			"a todo delta stays with its tool row",
			[]BlockKind{BlockTool, BlockTodoDelta, BlockAssistant},
			[]bool{false, true},
		},
		{
			"prose and prompts each get room",
			[]BlockKind{BlockUser, BlockAssistant, BlockUser},
			[]bool{true, true},
		},
		{
			"prose before a tool row is separated",
			[]BlockKind{BlockAssistant, BlockTool},
			[]bool{true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blocks := make([]Block, len(tt.kinds))
			for i, k := range tt.kinds {
				blocks[i] = Block{Kind: k}
			}
			for i, want := range tt.wantGaps {
				if got := needsGapAfter(blocks, i); got != want {
					t.Errorf("gap after block %d (%v -> %v) = %v, want %v",
						i, tt.kinds[i], tt.kinds[i+1], got, want)
				}
			}
			// Never a trailing gap: the composer provides its own separation.
			if needsGapAfter(blocks, len(blocks)-1) {
				t.Error("the last block should not be followed by a gap")
			}
		})
	}
}
