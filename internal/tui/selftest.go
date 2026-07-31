package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"evilcode/internal/agent"
	"evilcode/internal/ansirender"
	"evilcode/internal/config"
)

// runCompact summarizes the conversation and starts a fresh context from it.
//
// This is the one sanctioned rewrite of the append-only rule (invariant 2), and
// it bumps the context epoch so anything caching by message index starts over.
// The summary is produced by the `smol` role, because paying the main model to
// summarize its own history is exactly the ambient work role routing exists to
// prevent (plan.md §16).
// compactDone carries a finished compaction back into the render loop.
type compactDone struct {
	summary string
	before  int
	err     error
}

// runCompact summarises the conversation and replaces it with the summary.
//
// It runs off the render loop. It used to do a synchronous 60-second side call
// from the update handler, which froze the UI for the duration — so the
// "📦 Compacting…" notice it set was never painted, because the same function
// overwrote it before Bubbletea got control back.
func (m *Model) runCompact() (tea.Model, tea.Cmd) {
	// Before anything else: a turn in flight keeps appending across the reset,
	// and its messages land after the rewrite — in a conversation that no
	// longer holds what they were answering.
	if m.processing {
		m.notice = "⏳ Finish or interrupt the current turn first"
		return m, nil
	}
	if m.compactor == nil || !m.compactor.Enabled() {
		m.notice = "compaction is not configured for this session"
		return m, nil
	}
	if m.agent.Conv.Len() == 0 {
		m.notice = "nothing to compact"
		return m, nil
	}

	before := m.agent.Conv.Len()
	m.notice = "📦 Compacting…"

	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), CompactTimeout)
		defer cancel()
		summary, err := m.compactor.Compact(ctx, m.agent.Conv)
		return compactDone{summary: summary, before: before, err: err}
	}
}

// CompactTimeout bounds the summarising side-call.
const CompactTimeout = 60 * time.Second

// applyCompaction folds a finished compaction into the transcript.
func (m *Model) applyCompaction(done compactDone) {
	m.notice = ""
	if done.err != nil {
		m.blocks = append(m.blocks, Block{Kind: BlockError,
			Text: "could not compact: " + done.err.Error()})
		m.scroll.FollowBottom()
		return
	}

	m.blocks = nil
	m.scroll.ClearSlack()
	m.promptCount = 0
	// The meter reflected the pre-compaction size until the next turn reported
	// usage, which made a compaction look like it had done nothing.
	m.ctxUsed = 0
	m.blocks = append(m.blocks, Block{
		Kind: BlockNotice,
		Text: fmt.Sprintf("📦 Compacted %d messages into a summary (context epoch %d)\n\n%s",
			done.before, m.agent.Conv.Epoch(), done.summary),
	})
	m.scroll.FollowBottom()
}

// runSideQuestion answers a question in the side panel without touching the
// main conversation, so asking costs nothing in context.
func (m *Model) runSideQuestion(question string) (tea.Model, tea.Cmd) {
	cfg, err := config.Load()
	if err != nil {
		m.notice = err.Error()
		return m, nil
	}
	m.notice = "🔎 Asking on the side…"

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()

		answer, err := cfg.Router().SideCall(ctx, config.RoleSmol,
			"Answer briefly and concretely. You are a side channel: the developer asked "+
				"this without wanting it in their main conversation.",
			question)
		if err != nil {
			answer = "could not answer: " + err.Error()
		}
		m.sideAnswer.Store(&sideAnswer{Question: question, Answer: answer})
	}()
	return m, nil
}

// sideAnswer is a finished `/btw` result waiting to be picked up by the render
// loop. It is handed over through an atomic rather than written directly,
// because the side call runs on its own goroutine.
type sideAnswer struct {
	Question string
	Answer   string
}

// takeSideAnswer moves a finished side answer into the panel.
func (m *Model) takeSideAnswer() {
	got := m.sideAnswer.Swap(nil)
	if got == nil {
		return
	}
	m.panel = PanelContent{
		Title: "btw: " + truncateCells(oneLine(got.Question), 40),
		Body:  wrapPlain(got.Answer, 60),
	}
	m.panelOpen = true
	m.notice = ""
}

// runScreenshot writes the current frame to a PNG, which is how an agent
// verifies its own output without a human watching (plan.md §14).
func (m *Model) runScreenshot() (tea.Model, tea.Cmd) {
	dir := filepath.Join(config.DataDir(), "shots")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		m.notice = err.Error()
		return m, nil
	}

	name := fmt.Sprintf("shot-%d", time.Now().UnixNano())
	if Deterministic() {
		name = "shot"
	}
	ansiPath := filepath.Join(dir, name+".ansi")
	pngPath := filepath.Join(dir, name+".png")

	frame := m.lastFrame
	if err := os.WriteFile(ansiPath, []byte(frame), 0o644); err != nil {
		m.notice = err.Error()
		return m, nil
	}
	if err := ansirender.RenderFile(ansiPath, pngPath); err != nil {
		m.notice = "rendered ANSI but not PNG: " + err.Error()
		return m, nil
	}
	m.notice = "📸 " + pngPath
	return m, nil
}

// plainRows strips styling from a set of rows, for text the transcript embeds
// rather than renders.
func plainRows(rows []string) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = plainText(r)
	}
	return out
}

// SmoothnessReport is the objective form of "the screen never jumps"
// (plan.md §13, §4). The motions are counted separately because they have
// different causes: a mass reflow is one layout decision, while scattered
// off-anchor motion is many small ones.
type SmoothnessReport struct {
	Frames int

	// OffAnchor counts rows that moved without their content changing.
	OffAnchor int

	// DownwardPush counts rows shoved down by content inserted above them,
	// which is the motion a reader actually notices.
	DownwardPush int

	// Reflows counts frames where most rows moved at once.
	Reflows int
}

// Clean reports whether nothing objectionable happened.
func (r SmoothnessReport) Clean() bool {
	return r.OffAnchor == 0 && r.DownwardPush == 0 && r.Reflows == 0
}

func (r SmoothnessReport) String() string {
	if r.Frames == 0 {
		return "no frames observed yet"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d frames observed\n", r.Frames)
	fmt.Fprintf(&b, "  off-anchor motion  %d\n", r.OffAnchor)
	fmt.Fprintf(&b, "  downward pushes    %d\n", r.DownwardPush)
	fmt.Fprintf(&b, "  mass reflows       %d", r.Reflows)
	if r.Clean() {
		b.WriteString("\n\nnothing jumped.")
	}
	return b.String()
}

// observeSmoothness folds one frame into the report.
//
// Rows are keyed by their content and compared at absolute position, so motion
// caused by scrolling is not counted — only motion the reader did not ask for.
// Excluding expected motion is the whole difficulty: counting every position
// change would report every scroll as a defect.
func (m *Model) observeSmoothness(rows []string, origin int) {
	if m.anchorHashes == nil {
		m.anchorHashes = map[string]int{}
	}
	m.smoothness.Frames++

	current := make(map[string]int, len(rows))
	for i, row := range rows {
		key := plainText(row)
		if strings.TrimSpace(key) == "" {
			continue
		}
		// Absolute transcript line, not viewport row. Keying on the viewport
		// counts ordinary streaming as motion: content arriving at the bottom
		// shifts every row up, which the reader asked for. Excluding expected
		// motion is the entire difficulty of this metric.
		current[key] = i + origin
	}

	moved, pushed := 0, 0
	for key, pos := range current {
		prev, seen := m.anchorHashes[key]
		if !seen || prev == pos {
			continue
		}
		moved++
		if pos > prev {
			pushed++
		}
	}

	// A frame where most rows moved is one reflow, not fifty separate ones.
	if len(current) > 0 && moved*2 > len(current) {
		m.smoothness.Reflows++
	} else {
		m.smoothness.OffAnchor += moved
		m.smoothness.DownwardPush += pushed
	}

	m.anchorHashes = current
}

// runSmoothness reports the anchor-stability numbers (plan.md §13).
func (m *Model) runSmoothness(arg string) (tea.Model, tea.Cmd) {
	if strings.TrimSpace(arg) == "reset" {
		m.smoothness = SmoothnessReport{}
		m.anchorHashes = nil
		m.notice = "smoothness counters reset"
		return m, nil
	}
	m.blocks = append(m.blocks, Block{Kind: BlockNotice, Text: m.smoothness.String()})
	m.scroll.FollowBottom()
	return m, nil
}

// runRecord toggles capturing a frame whenever the frame changes.
func (m *Model) runRecord() (tea.Model, tea.Cmd) {
	m.recording = !m.recording
	if m.recording {
		m.notice = "⏺ recording frames to " + filepath.Join(config.DataDir(), "shots")
	} else {
		m.notice = fmt.Sprintf("recorded %d frames", m.recordSeq)
		m.recordSeq = 0
	}
	return m, nil
}

// runScreenshotMode toggles auto-capture on every frame change.
func (m *Model) runScreenshotMode() (tea.Model, tea.Cmd) {
	m.screenshotMode = !m.screenshotMode
	if m.screenshotMode {
		m.notice = "screenshot mode ON — every changed frame is written"
	} else {
		m.notice = "screenshot mode OFF"
	}
	return m, nil
}

// runDebugVisual toggles layout rectangle overlays, so a misplaced region is
// visible rather than inferred.
func (m *Model) runDebugVisual() (tea.Model, tea.Cmd) {
	m.debugVisual = !m.debugVisual
	if m.debugVisual {
		m.notice = "layout overlay ON"
	} else {
		m.notice = "layout overlay OFF"
	}
	return m, nil
}

// autoCapture writes a frame when recording or screenshot mode is on and the
// frame actually changed. Dumping identical frames fills a directory with
// nothing.
func (m *Model) autoCapture(frame string) {
	if !m.recording && !m.screenshotMode {
		return
	}
	h := frameHash(frame)
	if h == m.lastFrameHash {
		return
	}
	m.lastFrameHash = h

	dir := filepath.Join(config.DataDir(), "shots")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	name := fmt.Sprintf("frame-%04d", m.recordSeq)
	m.recordSeq++
	ansiPath := filepath.Join(dir, name+".ansi")
	if err := os.WriteFile(ansiPath, []byte(frame), 0o644); err != nil {
		return
	}
	_ = ansirender.RenderFile(ansiPath, filepath.Join(dir, name+".png"))
}

// frameHash is FNV-1a inline: this runs per frame, and a collision costs one
// skipped screenshot.
func frameHash(s string) uint64 {
	var h uint64 = 14695981039346656037
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}

// debugOverlay paints layout boundaries over a frame.
func (m *Model) debugOverlay(rows []string, transcriptRows int) []string {
	if !m.debugVisual {
		return rows
	}
	label := rgbStyle(255, 80, 200)
	mark := func(idx int, text string) {
		if idx < 0 || idx >= len(rows) {
			return
		}
		width := m.chatWidth()
		pad := max(width-len([]rune(plainText(rows[idx])))-len([]rune(text)), 1)
		rows[idx] = rows[idx] + strings.Repeat(" ", pad) + label.Render(text)
	}
	mark(0, "◤ transcript")
	mark(transcriptRows-1, "◣ transcript")
	if transcriptRows < len(rows) {
		mark(transcriptRows, "◤ status")
	}
	mark(len(rows)-1, "◣ composer")
	return rows
}

// WithCompactor attaches the compaction engine.
func (m *Model) WithCompactor(c *agent.Compactor) *Model {
	m.compactor = c
	return m
}

// contextCommand implements `/context`.
//
// It was registered in the command table and had no case in the dispatch switch,
// so typing it printed "not implemented yet".
func (m *Model) contextCommand() tea.Cmd {
	used, window := m.ctxUsed, m.contextMax()

	var b strings.Builder
	fmt.Fprintf(&b, "Context %s of %s", humanTokens(used), roundTokens(window))
	if used > 0 && window > 0 {
		fmt.Fprintf(&b, " · %d%%", used*100/window)
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "%d messages · epoch %d\n", m.agent.Conv.Len(), m.agent.Conv.Epoch())

	if n := m.compactor.Count(); n > 0 {
		fmt.Fprintf(&b, "compacted %d %s this session\n", n, timeNoun(n))
	}
	if m.compactor.Enabled() {
		fmt.Fprintf(&b, "\nAuto-compacts past %d%% · /compact to do it now",
			int(agent.CompactThreshold*100))
	} else {
		b.WriteString("\nCompaction is not configured")
	}

	m.blocks = append(m.blocks, Block{Kind: BlockNotice, Text: strings.TrimRight(b.String(), "\n")})
	m.scroll.FollowBottom()
	return nil
}

func timeNoun(n int) string {
	if n == 1 {
		return "time"
	}
	return "times"
}
