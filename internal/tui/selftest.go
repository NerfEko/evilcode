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
	"evilcode/internal/provider"
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
	m.dock.Reset()
	m.scroll.ClearSlack()
	// The anchor map holds every distinct row ever seen; a compaction replaces
	// the transcript wholesale, and comparing a fresh one against rows from
	// before the rewrite would count the whole rewrite as motion — and keep
	// the old rows in memory forever.
	m.anchorHashes = nil
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
		fmt.Fprintf(&b, "\nAuto-compacts at %d%% or on projected growth · /compact to do it now",
			int(agent.CompactThreshold*100))
	} else {
		b.WriteString("\nCompaction is not configured")
	}

	m.blocks = append(m.blocks, Block{Kind: BlockNotice, Text: strings.TrimRight(b.String(), "\n")})
	m.scroll.FollowBottom()
	return nil
}

func (m *Model) statsCommand() tea.Cmd {
	toolCalls := 0
	for _, b := range m.blocks {
		if b.Kind == BlockTool {
			toolCalls++
		}
	}
	text := fmt.Sprintf("Session stats\n"+
		"session: %s · model: %s · provider: %s\n"+
		"prompts: %d · blocks: %d · tool calls: %d\n"+
		"tokens: in %d · out %d · context %s/%s\n"+
		"generation: %dms",
		m.header.SessionName, m.header.Model, m.header.Provider,
		m.promptCount, len(m.blocks), toolCalls,
		m.sessionTokensIn, m.sessionTokensOut,
		humanTokens(m.ctxUsed), humanTokens(m.contextMax()), m.genMS)
	m.blocks = append(m.blocks, Block{Kind: BlockNotice, Text: text})
	m.scroll.FollowBottom()
	return nil
}

func (m *Model) connectCommand(arg string) tea.Cmd {
	parts := strings.Fields(arg)
	if len(parts) == 0 {
		m.notice = "usage: /connect brave [status]"
		return nil
	}
	target := strings.ToLower(parts[0])
	status := false
	if target == "status" {
		if len(parts) != 1 {
			m.notice = "usage: /connect brave [status]"
			return nil
		}
		target = "brave"
		status = true
	} else if len(parts) > 1 {
		if len(parts) != 2 || strings.ToLower(parts[1]) != "status" {
			m.notice = "usage: /connect brave [status]"
			return nil
		}
		status = true
	}
	if target != "brave" {
		m.notice = "usage: /connect brave [status]"
		return nil
	}
	if status {
		cfg, err := config.Load()
		if err != nil {
			m.notice = "brave connection status unavailable"
			return nil
		}
		if cfg.BraveSearchAPIKey() != "" {
			m.notice = "brave: API key present"
		} else {
			m.notice = "brave: no API key configured"
		}
		return nil
	}
	if m.processing {
		m.notice = "Finish or interrupt the turn first, then /connect"
		return nil
	}
	m.loginMode = true
	m.loginProvider = "brave"
	m.editor = Editor{}
	m.notice = "Brave Search API key · input hidden · Enter saves · Esc cancels"
	return nil
}

func (m *Model) loginCommand(arg string) tea.Cmd {
	// `/login status [provider]` reports a key's presence without printing it.
	if arg == "status" || strings.HasPrefix(arg, "status ") {
		target := strings.TrimSpace(strings.TrimPrefix(arg, "status"))
		if target == "" {
			target = "ollama-cloud"
		}
		cfg, err := config.Load()
		if err != nil {
			m.notice = target + " login status unavailable"
			return nil
		}
		cfg.AddDiscoveredCodex()
		present := false
		for _, p := range cfg.Providers {
			if p.Name == target {
				if p.Kind == config.KindCodex {
					_, buildErr := p.Build()
					present = buildErr == nil
				} else {
					present = p.APIKeyValue() != ""
				}
				break
			}
		}
		if present {
			m.notice = target + " login: key present"
		} else {
			m.notice = target + " login: no key configured"
		}
		return nil
	}
	// `/login <provider>` targets a configured provider directly (e.g.
	// `deepseek`). `/login` with no argument opens a provider selector first, so
	// you can choose which key you are entering rather than defaulting silently.
	target := strings.TrimSpace(arg)
	if target == "" {
		if m.processing {
			m.notice = "Finish or interrupt the turn first, then /login"
			return nil
		}
		entries := m.loginPickerEntries()
		if len(entries) == 0 {
			m.notice = "no providers configured"
			return nil
		}
		m.loginPicker = LoginPickerState{Entries: entries}
		m.loginPickerOpen = true
		m.notice = "select a provider to enter a key for"
		return nil
	}
	cfg, err := config.Load()
	if err != nil {
		m.notice = target + " login unavailable: " + err.Error()
		return nil
	}
	cfg.AddDiscoveredCodex()
	pc := cfg.FindProvider(target)
	if pc == nil {
		m.notice = "usage: /login [provider] or /login status [provider]\nunknown provider: " + target
		return nil
	}
	if pc.Kind == config.KindCodex {
		if _, buildErr := pc.Build(); buildErr == nil {
			m.notice = "codex OAuth account detected; use `codex login` to change accounts"
		} else {
			m.notice = "codex OAuth account not found; run `codex login` first"
		}
		return nil
	}
	if m.processing {
		// Saving the key updates the live provider's APIKey field, which the
		// in-flight request reads from another goroutine. Waiting for the turn
		// is cheaper than putting a mutex around a field written once a month.
		m.notice = "Finish or interrupt the turn first, then /login"
		return nil
	}
	m.loginMode = true
	m.loginProvider = target
	m.editor = Editor{}
	m.notice = target + " API key · input hidden · Enter saves · Esc cancels"
	return nil
}

// loginPickerEntries builds the provider list for the `/login` selector from
// the configured providers, preferring the live list (which the picker can
// reach without re-reading the file) and falling back to a config load.
func (m *Model) loginPickerEntries() []LoginPickerEntry {
	provs := m.providers
	if len(provs) == 0 {
		if cfg, err := config.Load(); err == nil {
			provs = cfg.Providers
		}
	}
	entries := make([]LoginPickerEntry, 0, len(provs))
	for _, p := range provs {
		// A provider with no api_key_env is a local daemon (e.g. ollama-local)
		// that takes no key, so it has nothing to log in to.
		if p.APIKeyEnv == "" {
			continue
		}
		entries = append(entries, LoginPickerEntry{
			Name:   p.Name,
			Kind:   string(p.Kind),
			HasKey: p.APIKeyValue() != "",
		})
	}
	return entries
}

// handleLoginPickerKey drives the `/login` provider selector. Selecting a row
// transitions into the existing masked-key entry for that provider.
func (m *Model) handleLoginPickerKey(key string) (tea.Model, tea.Cmd) {
	entries, _ := m.loginPicker.Filtered()
	switch key {
	case "esc", "ctrl+c":
		m.loginPickerOpen = false
		m.loginPicker = LoginPickerState{}
		m.notice = "login cancelled"
		return m, nil
	case "up", "ctrl+k":
		m.loginPicker.Selected = MovePaletteSelection(m.loginPicker.Selected, -1, len(entries))
		return m, nil
	case "down", "ctrl+j":
		m.loginPicker.Selected = MovePaletteSelection(m.loginPicker.Selected, 1, len(entries))
		return m, nil
	case "enter":
		if len(entries) == 0 {
			return m, nil
		}
		sel := entries[clamp(m.loginPicker.Selected, 0, len(entries)-1)]
		m.loginPickerOpen = false
		m.loginPicker = LoginPickerState{}
		m.loginMode = true
		m.loginProvider = sel.Name
		m.editor = Editor{}
		m.notice = sel.Name + " API key · input hidden · Enter saves · Esc cancels"
		return m, nil
	case "backspace":
		if r := []rune(m.loginPicker.Filter); len(r) > 0 {
			m.loginPicker.Filter = string(r[:len(r)-1])
			m.loginPicker.Selected = 0
		}
		return m, nil
	}
	if len(key) == 1 {
		m.loginPicker.Filter += key
		m.loginPicker.Selected = 0
	}
	return m, nil
}

func (m *Model) handleLoginKey(key string, msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key {
	case "esc", "ctrl+c":
		target := m.loginProvider
		m.loginMode = false
		m.editor = Editor{}
		m.loginProvider = ""
		if target == "brave" {
			m.notice = "connect cancelled"
		} else {
			m.notice = "login cancelled"
		}
		return m, nil
	case "enter":
		keyText := m.editor.Text
		m.loginMode = false
		m.editor = Editor{}
		target := m.loginProvider
		if target == "" {
			target = "ollama-cloud" // back-compat for flows that set loginMode directly
		}
		m.loginProvider = ""
		if strings.TrimSpace(keyText) == "" {
			if target == "brave" {
				m.notice = "brave connection cancelled: no key entered"
			} else {
				m.notice = target + " login cancelled: no key entered"
			}
			return m, nil
		}
		if target == "brave" {
			keyText = strings.TrimSpace(keyText)
			if err := config.SaveBraveSearchAPIKey(keyText); err != nil {
				m.notice = "brave connection failed"
				return m, nil
			}
			activeKey := keyText
			envKey := config.BraveSearchAPIKey()
			if envKey != "" {
				activeKey = envKey
			}
			if m.braveSearch != nil {
				m.braveSearch.APIKey = activeKey
			}
			if envKey != "" {
				m.notice = "Brave Search API key saved · environment key remains active"
			} else {
				m.notice = "Brave Search API key saved"
			}
			return m, nil
		}
		if err := config.SaveProviderAPIKey(target, keyText); err != nil {
			m.notice = target + " login failed"
			return m, nil
		}
		// The model picker fetches models from the in-memory provider list, so a
		// key saved here must reach it too — otherwise the catalog stays empty
		// until restart even though the turn now works.
		m.updateProviderAPIKey(target, keyText)
		m.notice = target + " API key saved"
		if m.agent != nil && !m.processing {
			// Only while nothing is in flight: the request goroutine reads this
			// field, so writing it under a live turn is a data race. Both
			// wire-format clients carry an APIKey field set at their own edge.
			switch p := m.agent.Provider.(type) {
			case *provider.Ollama:
				if p.Name() == target {
					p.APIKey = keyText
				}
			case *provider.OpenAI:
				if p.Name() == target {
					p.APIKey = keyText
				}
			}
		} else if m.processing {
			m.notice = target + " API key saved · takes effect next turn"
		}
		return m, nil
	case "backspace":
		m.editor.Backspace()
	case "delete":
		m.editor.Delete()
	case "ctrl+u":
		m.editor.KillToStart()
	case "ctrl+k":
		m.editor.KillToEnd()
	case "ctrl+w", "ctrl+backspace", "alt+backspace":
		m.editor.DeleteWord()
	case "ctrl+a", "home":
		m.editor.Home()
	case "ctrl+e", "end":
		m.editor.End()
	case "left":
		m.editor.Left()
	case "right":
		m.editor.Right()
	case "ctrl+b", "ctrl+left", "alt+left":
		m.editor.WordLeft()
	case "ctrl+f", "ctrl+right", "alt+right":
		m.editor.WordRight()
	default:
		if text := msg.Key().Text; text != "" {
			m.editor.Insert(text)
		}
	}
	return m, nil
}

// updateProviderAPIKey writes a just-saved key into the in-memory provider
// list, so the model picker's fetch uses it without a restart.
func (m *Model) updateProviderAPIKey(name, key string) {
	for i := range m.providers {
		if m.providers[i].Name == name {
			m.providers[i].APIKey = key
			return
		}
	}
}

func timeNoun(n int) string {
	if n == 1 {
		return "time"
	}
	return "times"
}
