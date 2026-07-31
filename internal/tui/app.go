package tui

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"evilcode/internal/agent"
	"evilcode/internal/theme"
)

// Deterministic reports whether the frozen test mode is on: fixed session
// name, animations at frame 0, no wall-clock text (plan.md invariant 5).
func Deterministic() bool { return os.Getenv("EVILCODE_DETERMINISTIC") == "1" }

// tickMsg drives animation at the spinner's cadence.
type tickMsg time.Time

// eventMsg carries one agent event into the bubbletea loop. The agent core
// knows nothing about this type — it just writes to a channel (invariant 1).
type eventMsg agent.Event

// eventsClosedMsg signals the agent stream ended.
type eventsClosedMsg struct{}

// Model is the bubbletea model.
type Model struct {
	agent    *agent.Agent
	renderer *Renderer

	width, height int

	blocks []Block
	scroll Scroll

	editor Editor

	// pastes holds collapsed paste contents, restored on send.
	pastes []Paste

	// lastPaste times the most recent paste, for the trailing-Enter guard.
	lastPaste time.Time

	processing bool
	queueMode  bool
	shellMode  bool

	status  StatusState
	header  HeaderState
	notice  string
	started time.Time
	turnAt  time.Time

	// promptCount is how many user prompts have been submitted, which drives
	// the rainbow numbering.
	promptCount int

	// streamingIdx points at the assistant block currently being appended to,
	// or -1 when nothing is streaming.
	streamingIdx int

	pending []PendingMessage

	// palette is the slash-command overlay. It reserves no layout height:
	// opening it must never move the transcript (plan.md invariant 3).
	palette PaletteState

	// picker is the inline model picker. Unlike the palette it *does* reserve
	// layout height, because it is a surface you interact with (plan.md §5.3).
	picker     PickerState
	pickerOpen bool

	// models supplies picker entries; it is set once the provider answers.
	models []ModelEntry

	// sawEscapeHint keeps the trailing-backslash tip to once per session.
	sawEscapeHint bool

	quitting     bool
	confirmQuit  bool
	scrollbarOn  bool
	cancelTurn   context.CancelFunc
	lastFrameLen int
}

// NewModel builds the TUI over an agent.
func NewModel(a *agent.Agent, h HeaderState) *Model {
	p := theme.Dracula()
	return &Model{
		agent:        a,
		renderer:     NewRenderer(p, 80),
		header:       h,
		started:      time.Now(),
		streamingIdx: -1,
	}
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.waitForEvent(), m.tick())
}

func (m *Model) tick() tea.Cmd {
	return tea.Tick(SpinnerInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// waitForEvent bridges the agent's channel into bubbletea's message loop.
func (m *Model) waitForEvent() tea.Cmd {
	return func() tea.Msg {
		e, ok := <-m.agent.Events()
		if !ok {
			return eventsClosedMsg{}
		}
		return eventMsg(e)
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.applyWrapWidth()
		return m, nil

	case tickMsg:
		if m.processing {
			m.status.Elapsed = time.Since(m.turnAt)
		}
		return m, m.tick()

	case eventsClosedMsg:
		return m, nil

	case eventMsg:
		m.applyEvent(agent.Event(msg))
		return m, m.waitForEvent()

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case tea.PasteMsg:
		// Bracketed paste never inspects the clipboard for images: on Wayland
		// a multi-MIME clipboard is routinely misidentified, and a stray image
		// attachment is worse than a missing one (plan.md §6.6).
		insert, stored := CollapsePaste(msg.Content)
		m.editor.Insert(insert)
		if stored != nil {
			m.pastes = append(m.pastes, *stored)
		}
		m.lastPaste = time.Now()
		m.syncShellMode()
		return m, nil

	case tea.MouseWheelMsg:
		return m.handleWheel(msg)
	}
	return m, nil
}

// applyEvent folds one agent event into the view.
func (m *Model) applyEvent(e agent.Event) {
	switch e.Kind {
	case agent.EventTurnStart:
		m.processing = true
		m.turnAt = time.Now()
		m.status = StatusState{Phase: PhaseSending, Animate: !Deterministic()}
		m.streamingIdx = -1

	case agent.EventTextDelta:
		m.status.Phase = PhaseStreaming
		if m.streamingIdx < 0 {
			m.blocks = append(m.blocks, Block{Kind: BlockAssistant, Streaming: true})
			m.streamingIdx = len(m.blocks) - 1
		}
		m.blocks[m.streamingIdx].Text += e.Text
		m.followIfPinned()

	case agent.EventReasoningDelta:
		m.status.Phase = PhaseThinking

	case agent.EventToolStart:
		m.status.Phase = PhaseRunningTool
		m.status.ToolName = e.Call.Name
		// A tool call closes the streaming text block; anything after it is a
		// new message.
		m.finishStreaming()

	case agent.EventToolResult:
		m.status.Phase = PhaseStreaming
		b := Block{
			Kind:       BlockTool,
			ToolName:   e.Call.Name,
			ToolTarget: toolTarget(e.Call.Args),
			ToolTokens: len(e.Output) / 4,
			Failed:     e.IsError(),
			Diff:       e.Diff,
		}
		if e.Intent != "" && !strings.Contains(e.Intent, b.ToolTarget) {
			b.ToolIntent = e.Intent
		}
		if e.DiffStat != nil {
			b.HasDiff = true
			b.Added, b.Removed = e.DiffStat.Added, e.DiffStat.Removed
		}
		m.blocks = append(m.blocks, b)
		if e.IsError() {
			m.blocks = append(m.blocks, Block{Kind: BlockError, Text: e.ErrText})
		}
		m.followIfPinned()

	case agent.EventTokenUsage:
		m.status.TokensIn, m.status.TokensOut = e.Usage.In, e.Usage.Out
		if secs := time.Since(m.turnAt).Seconds(); secs > 0 {
			m.status.TokensPerSecond = float64(e.Usage.Out) / secs
		}

	case agent.EventNotice:
		m.notice = e.Text

	case agent.EventError:
		m.finishStreaming()
		m.blocks = append(m.blocks, Block{Kind: BlockError, Text: e.ErrText})
		m.followIfPinned()

	case agent.EventTurnEnd:
		m.finishStreaming()
		m.processing = false
		m.status = StatusState{Phase: PhaseIdle}
		m.flushPending()
	}
}

// finishStreaming freezes the streaming block so it caches from now on.
func (m *Model) finishStreaming() {
	if m.streamingIdx >= 0 && m.streamingIdx < len(m.blocks) {
		m.blocks[m.streamingIdx].Streaming = false
	}
	m.streamingIdx = -1
}

// followIfPinned keeps the view at the bottom unless the reader scrolled up.
func (m *Model) followIfPinned() {
	if !m.scroll.Paused {
		m.scroll.Offset = 0
	}
}

// flushPending sends queued messages once a turn ends.
func (m *Model) flushPending() {
	if len(m.pending) == 0 {
		return
	}
	var texts []string
	for _, p := range m.pending {
		texts = append(texts, p.Text)
	}
	m.pending = nil
	m.submit(strings.Join(texts, "\n\n"))
}

func (m *Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// The picker owns the keyboard while it is open.
	if m.pickerOpen {
		return m.handlePickerKey(key)
	}

	// The palette owns navigation and accept keys while it is open.
	if m.paletteOpen() {
		suggestions := m.paletteSuggestions()
		switch key {
		case "up", "ctrl+k":
			m.palette.Selected = MovePaletteSelection(m.palette.Selected, -1, len(suggestions))
			return m, nil
		case "down":
			m.palette.Selected = MovePaletteSelection(m.palette.Selected, 1, len(suggestions))
			return m, nil
		case "tab":
			if len(suggestions) > 0 {
				sel := clamp(m.palette.Selected, 0, len(suggestions)-1)
				m.editor.Text = "/" + suggestions[sel].Name
				m.editor.Cursor = len([]rune(m.editor.Text))
			}
			return m, nil
		case "enter":
			if len(suggestions) > 0 {
				sel := clamp(m.palette.Selected, 0, len(suggestions)-1)
				return m.runCommand(suggestions[sel].Name)
			}
			return m, nil
		case "esc":
			m.editor.Clear()
			m.palette.Selected = 0
			return m, nil
		}
	}

	switch key {
	case "ctrl+c", "ctrl+d":
		// While processing this interrupts; idle, it quits — and quitting
		// takes two presses so a reflexive Ctrl+C does not lose the session.
		if m.processing {
			m.interrupt(false)
			return m, nil
		}
		if m.confirmQuit || m.editor.Text == "" {
			m.quitting = true
			return m, tea.Quit
		}
		m.confirmQuit = true
		m.notice = "Press Ctrl+C again to quit"
		return m, nil

	case "esc":
		m.escape()
		return m, nil

	case "enter":
		// A bare Enter right after a paste is the terminal ending the paste,
		// not the user submitting (plan.md §6.6).
		if time.Since(m.lastPaste) < TrailingEnterGuard {
			return m, nil
		}
		// The universal newline fallback: an odd number of trailing
		// backslashes escapes the Enter (plan.md §6.2).
		if EndsWithEscapedNewline(m.editor.Text) {
			m.editor.Text = StripEscapedNewline(m.editor.Text)
			m.editor.Insert("\n")
			if !m.sawEscapeHint {
				m.sawEscapeHint = true
				m.notice = "Tip: run /terminal-setup to make Shift+Enter insert newlines"
			}
			return m, nil
		}
		return m.send(false)

	case "ctrl+j":
		return m.send(true)

	case "ctrl+t":
		m.queueMode = !m.queueMode
		if m.queueMode {
			m.notice = "Queue mode: messages wait until response completes"
		} else {
			m.notice = "Immediate mode: messages send next (no interrupt)"
		}
		return m, nil

	case "ctrl+u":
		m.editor.KillToStart()
		return m, nil

	case "ctrl+k":
		m.editor.KillToEnd()
		return m, nil

	case "ctrl+w", "alt+backspace", "ctrl+backspace":
		m.editor.DeleteWord()
		return m, nil

	case "ctrl+a", "home":
		m.editor.Home()
		return m, nil

	case "ctrl+e", "end":
		m.editor.End()
		return m, nil

	case "left":
		m.editor.Left()
		return m, nil

	case "right":
		m.editor.Right()
		return m, nil

	case "ctrl+b", "ctrl+left", "alt+left":
		m.editor.WordLeft()
		return m, nil

	case "ctrl+f", "ctrl+right", "alt+right":
		m.editor.WordRight()
		return m, nil

	case "ctrl+z":
		if m.editor.Undo() {
			m.notice = "Restored"
		}
		return m, nil

	case "ctrl+s":
		if n := m.editor.Stash(); n != "" {
			m.notice = n
		}
		return m, nil

	case "delete":
		m.editor.Delete()
		return m, nil

	case "ctrl+g":
		if n := m.scroll.ToggleBookmark(); n != "" {
			m.notice = n
		}
		return m, nil

	case "backspace":
		m.editor.Backspace()
		m.syncShellMode()
		return m, nil

	case "pgup":
		m.scroll.Up(PageLines, m.contentHeight(), m.transcriptHeight())
		return m, nil

	case "pgdown":
		m.scroll.Down(PageLines)
		return m, nil

	case "up":
		if m.editor.Text == "" {
			m.scroll.Up(1, m.contentHeight(), m.transcriptHeight())
		}
		return m, nil

	case "down":
		if m.editor.Text == "" {
			m.scroll.Down(1)
		}
		return m, nil
	}

	// Newline chords insert instead of submitting (plan.md §6.2).
	if NewlineKeys[key] {
		m.editor.Insert("\n")
		return m, nil
	}

	// Ordinary text input. Key.Text is the printable characters the key
	// produced — String() spells space as "space", which would drop it.
	if txt := msg.Key().Text; txt != "" {
		m.editor.Insert(txt)
		m.confirmQuit = false
		m.syncShellMode()
	}
	return m, nil
}

// paletteOpen reports whether the composer is in slash mode.
func (m *Model) paletteOpen() bool {
	return strings.HasPrefix(m.editor.Text, "/") && !m.palette.Suppressed
}

// paletteSuggestions is the currently ranked list.
func (m *Model) paletteSuggestions() []Suggestion {
	if !m.paletteOpen() {
		return nil
	}
	return RankCommands(strings.TrimPrefix(m.editor.Text, "/"), VisibleCommands())
}

// handleWheel applies the momentum scrolling of §4.1.
func (m *Model) handleWheel(msg tea.MouseWheelMsg) (tea.Model, tea.Cmd) {
	now := time.Now()
	switch msg.Button {
	case tea.MouseWheelUp:
		m.scroll.WheelUp(now, m.contentHeight(), m.transcriptHeight())
	case tea.MouseWheelDown:
		m.scroll.WheelDown(now)
	}
	return m, nil
}

// syncShellMode tracks the `!` prefix that switches the composer to a shell.
func (m *Model) syncShellMode() {
	m.shellMode = strings.HasPrefix(m.editor.Text, "!")
}

// handlePickerKey drives the inline model picker.
func (m *Model) handlePickerKey(key string) (tea.Model, tea.Cmd) {
	entries, _ := m.picker.Filtered()

	switch key {
	case "esc", "ctrl+c":
		m.pickerOpen = false
		m.picker.Filter = ""
		return m, nil

	case "up", "ctrl+k":
		m.picker.Selected = MovePaletteSelection(m.picker.Selected, -1, len(entries))
		return m, nil

	case "down", "ctrl+j":
		m.picker.Selected = MovePaletteSelection(m.picker.Selected, 1, len(entries))
		return m, nil

	case "left":
		if m.picker.Column > ColModel {
			m.picker.Column--
		}
		return m, nil

	case "right":
		if m.picker.Column < ColVia {
			m.picker.Column++
		}
		return m, nil

	case "enter":
		if len(entries) > 0 {
			sel := entries[clamp(m.picker.Selected, 0, len(entries)-1)]
			if sel.Unavailable {
				m.notice = sel.Name + " is unavailable"
				return m, nil
			}
			m.header.Model = sel.Name
			if sel.Provider != "" {
				m.header.Provider = sel.Provider
			}
			m.agent.Model = sel.Name
			m.notice = "Model: " + sel.Name
		}
		m.pickerOpen = false
		m.picker.Filter = ""
		return m, nil

	case "backspace":
		if r := []rune(m.picker.Filter); len(r) > 0 {
			m.picker.Filter = string(r[:len(r)-1])
			m.picker.Selected = 0
		}
		return m, nil
	}

	// Anything printable filters the list.
	if len(key) == 1 {
		m.picker.Filter += key
		m.picker.Selected = 0
	}
	return m, nil
}

// openPicker loads the provider's model list into the picker. The list is
// fetched lazily and failures degrade to the configured model alone, because a
// provider being unreachable should not make the picker unusable.
func (m *Model) openPicker() {
	if len(m.models) == 0 {
		m.models = m.loadModels()
	}
	m.picker = PickerState{Entries: m.models, Height: DefaultPickerHeight}
	for i, e := range m.picker.Entries {
		if e.Name == m.header.Model {
			m.picker.Selected = i
		}
	}
	m.pickerOpen = true
}

func (m *Model) loadModels() []ModelEntry {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	infos, err := m.agent.Provider.Models(ctx)
	if err != nil || len(infos) == 0 {
		return []ModelEntry{{
			Name:     m.header.Model,
			Provider: m.header.Provider,
			Current:  true,
			Default:  true,
		}}
	}
	out := make([]ModelEntry, 0, len(infos))
	for _, info := range infos {
		e := ModelEntry{
			Name:     info.Name,
			Provider: m.header.Provider,
			Current:  info.Name == m.header.Model,
		}
		if info.Size != "" {
			e.Detail = info.Size
		}
		out = append(out, e)
	}
	return out
}

// runCommand executes a slash command. Unknown commands fall through to a
// notice rather than being sent to the model, which would waste a turn.
func (m *Model) runCommand(name string) (tea.Model, tea.Cmd) {
	m.editor.Clear()
	m.palette.Selected = 0

	switch name {
	case "quit":
		m.quitting = true
		return m, tea.Quit

	case "clear", "cls":
		m.blocks = nil
		m.promptCount = 0
		m.scroll.FollowBottom()
		m.notice = ""
		return m, nil

	case "cancel":
		if m.processing {
			m.interrupt(false)
		}
		return m, nil

	case "model", "models":
		m.openPicker()
		return m, nil

	case "terminal-setup":
		m.blocks = append(m.blocks, Block{Kind: BlockNotice, Text: terminalSetupText()})
		m.scroll.FollowBottom()
		return m, nil

	case "version":
		m.notice = "evilcode " + m.header.Version
		return m, nil

	case "info":
		m.notice = m.header.SessionName + " · " + m.header.Model + " · " + m.header.Provider
		return m, nil

	case "help", "?", "commands", "keys", "hotkeys":
		m.blocks = append(m.blocks, Block{Kind: BlockNotice, Text: helpText()})
		m.scroll.FollowBottom()
		return m, nil

	default:
		if _, ok := FindCommand(name); ok {
			m.notice = "/" + name + " is not implemented yet"
		} else {
			m.notice = "unknown command /" + name
		}
		return m, nil
	}
}

// helpText lists every registered command. Building it from the registry means
// a newly registered command can never be invisible (plan.md §5.5).
func helpText() string {
	var b strings.Builder
	b.WriteString("Commands\n")
	for _, c := range VisibleCommands() {
		b.WriteString("  /" + c.Name + strings.Repeat(" ", max(16-len(c.Name), 1)) + c.Help + "\n")
	}
	b.WriteString("\nKeys\n")
	for _, k := range [][2]string{
		{"Enter", "submit, or interleave while a turn is running"},
		{"Ctrl+Enter", "the opposite of the current send mode"},
		{"Ctrl+T", "toggle queue mode"},
		{"Esc", "cancel: close overlays, interrupt, or clear input"},
		{"Ctrl+C", "interrupt; twice when idle to quit"},
		{"Ctrl+G", "toggle a scroll bookmark"},
		{"PgUp/PgDn", "scroll a page"},
		{"!", "prefix a line to run it as a shell command"},
	} {
		b.WriteString("  " + k[0] + strings.Repeat(" ", max(16-len(k[0]), 1)) + k[1] + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// terminalSetupText explains how to make Shift+Enter insert a newline. It needs
// the kitty keyboard protocol, which several terminals gate behind a setting
// (plan.md §6.2).
func terminalSetupText() string {
	return strings.Join([]string{
		"Shift+Enter needs the kitty keyboard protocol.",
		"",
		"  tmux      set -g extended-keys on",
		"  WezTerm   enable_kitty_keyboard = true",
		"  kitty     supported out of the box",
		"  foot      supported out of the box",
		"",
		"Without it, use Alt+Enter, or end a line with a backslash.",
	}, "\n")
}

// escape is the layered cancel of plan.md §6.7.
func (m *Model) escape() {
	switch {
	case m.processing:
		// Esc means stop, which is why it also disarms auto-poke — unlike
		// Ctrl+C, which means "skip this".
		m.interrupt(true)
	default:
		m.scroll.FollowBottom()
		m.editor.Clear()
		m.notice = "Input cleared - Ctrl+Z to restore"
	}
}

func (m *Model) interrupt(disarmPoke bool) {
	if m.cancelTurn != nil {
		m.cancelTurn()
	}
	m.pending = nil
	if disarmPoke {
		m.notice = "Interrupting... Auto-poke OFF"
	} else {
		m.notice = "Interrupting..."
	}
}

// send applies the §6.3 send model.
func (m *Model) send(alternate bool) (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.editor.Text)
	if text == "" {
		return m, nil
	}

	switch SendActionFor(m.processing, m.queueMode, m.editor.Text, alternate) {
	case Queue:
		m.pending = append(m.pending, PendingMessage{Kind: PendingQueued, Text: text})
		m.editor.Clear()
		return m, nil

	case Interleave:
		m.agent.Interject(agent.Interrupt{Source: agent.SourceUser, Text: text})
		m.pending = append(m.pending, PendingMessage{Kind: PendingSent, Text: text})
		m.notice = "⏭ Sending now (interleave)"
		m.editor.Clear()
		return m, nil

	default:
		m.submit(text)
		return m, nil
	}
}

// submit starts a turn.
func (m *Model) submit(text string) {
	if len(m.pastes) > 0 {
		text = ExpandPastes(text, m.pastes)
		m.pastes = nil
	}
	m.blocks = append(m.blocks, Block{
		Kind:   BlockUser,
		Text:   text,
		Number: m.promptCount,
	})
	m.promptCount++
	m.renumberPrompts()
	m.editor.Clear()
	m.shellMode = false
	m.scroll.FollowBottom()
	m.notice = ""

	ctx, cancel := context.WithCancel(context.Background())
	m.cancelTurn = cancel
	go func() {
		defer cancel()
		_ = m.agent.Run(ctx, text)
	}()
}

// renumberPrompts recomputes each user block's distance from the newest, which
// is what the rainbow ramp is indexed by.
func (m *Model) renumberPrompts() {
	seen := 0
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].Kind == BlockUser {
			m.blocks[i].Number = seen
			m.blocks[i].cache = nil
			seen++
		}
	}
}

func (m *Model) transcriptHeight() int {
	res := m.stack().Resolve()
	return res.Transcript
}

func (m *Model) contentHeight() int {
	return len(m.transcriptLines())
}

// transcriptLines renders every block to lines.
func (m *Model) transcriptLines() []string {
	var out []string
	if len(m.blocks) == 0 {
		return m.renderer.RenderWelcome(0)
	}
	out = append(out, m.renderer.RenderHeader(m.header)...)
	out = append(out, "")
	for i := range m.blocks {
		lines := m.renderer.Lines(&m.blocks[i])
		out = append(out, lines...)
		if len(lines) > 0 {
			out = append(out, "")
		}
	}
	return out
}

// applyWrapWidth sets the renderer's wrap width, reserving a column for the
// scrollbar when the previous frame showed one (plan.md §3.5).
func (m *Model) applyWrapWidth() {
	width, _ := ContentWidth(m.width, false)
	if m.scrollbarOn {
		width--
	}
	if width != m.renderer.Width {
		m.renderer.SetWidth(max(width, 1))
	}
}

// stack builds the vertical layout request.
func (m *Model) stack() Stack {
	s := Stack{Available: m.height, ContentHeight: len(m.transcriptLines())}
	s.Heights[SlotStatus] = 1
	if m.notice != "" {
		s.Heights[SlotNotice] = 1
	}
	s.Heights[SlotQueued] = min(len(m.pending), MaxPendingRows)

	if m.pickerOpen {
		s.Heights[SlotPicker] = len(m.renderer.RenderPicker(m.picker))
		s.Heights[SlotPickerGap] = 1
	}

	composer := m.renderer.RenderComposer(m.composerState())
	s.Heights[SlotComposer] = len(composer)
	return s
}

func (m *Model) composerState() ComposerState {
	return ComposerState{
		Text:         m.editor.Text,
		Cursor:       m.editor.Cursor,
		PromptNumber: m.promptCount,
		Processing:   m.processing,
		QueueMode:    m.queueMode,
		ShellMode:    m.shellMode,
		PaletteOpen:  m.paletteOpen(),
	}
}

func (m *Model) View() tea.View {
	if m.width == 0 {
		v := tea.NewView("")
		v.AltScreen = true
		return v
	}

	// The previous frame's scrollbar decision picks this frame's wrap width, so
	// steady state wraps once. This is the feedback loop §3.6's hysteresis
	// exists to damp: a visible bar narrows the wrap width, which changes the
	// content height, which can change the decision.
	m.applyWrapWidth()

	res := m.stack().Resolve()
	content := m.transcriptLines()

	// Window the transcript to its resolved height, honoring the scroll offset.
	start := max(len(content)-res.Transcript-m.scroll.Offset, 0)
	end := min(start+res.Transcript, len(content))
	if end < start {
		end = start
	}
	visible := content[start:end]

	var rows []string
	rows = append(rows, visible...)

	// Pad so the composer sits at the bottom once the transcript is scrolling.
	if res.Scrolling {
		for len(rows) < res.Transcript {
			rows = append(rows, "")
		}
	}

	// The scrollbar decision is hysteretic because it feeds back into layout:
	// a visible bar narrows the wrap width, which changes the content height,
	// which can change the decision (plan.md §3.6).
	m.scrollbarOn = ScrollbarVisible(m.scrollbarOn, len(content), len(content), res.Transcript)
	if m.scrollbarOn && res.Transcript > 0 {
		bar := m.renderer.RenderScrollbar(m.scroll.Offset, len(content), res.Transcript, !m.scroll.Paused)
		for i := 0; i < res.Transcript && i < len(bar) && i < len(rows); i++ {
			pad := max(m.width-Inset(false)-1-lipgloss.Width(rows[i]), 0)
			rows[i] = rows[i] + strings.Repeat(" ", pad) + bar[i]
		}
	}

	rows = append(rows, m.renderer.RenderPending(m.pending)...)

	m.status.Animate = !Deterministic()
	if m.status.Phase == PhaseIdle && m.notice == "" {
		m.status.Tip = TipAt(time.Since(m.started), m.width)
	}
	m.status.Queued = len(m.pending)
	rows = append(rows, m.renderer.RenderStatus(m.status))

	if m.notice != "" {
		rows = append(rows, m.renderer.style(theme.RoleSystem).Render(m.notice))
	}

	if m.pickerOpen {
		rows = append(rows, m.renderer.RenderPicker(m.picker)...)
		rows = append(rows, "")
	}

	rows = append(rows, m.renderer.RenderComposer(m.composerState())...)

	// The palette is spliced over the finished frame rather than added to it,
	// so opening it reserves no layout height and the transcript never moves
	// (plan.md invariant 3). It runs before the inset so the overlay shares the
	// same gutter as every other row.
	rows = m.overlayPalette(rows)

	// The left inset is applied here rather than per-widget, so every row
	// shares one gutter (plan.md §3.4).
	inset := strings.Repeat(" ", Inset(false))
	for i, row := range rows {
		rows[i] = inset + row
	}

	v := tea.NewView(strings.Join(rows, "\n"))
	// These are view properties in Bubble Tea v2, not program options.
	v.AltScreen = true
	// Cell motion is enough for the wheel and is better supported than all
	// motion, which would flood the loop with movement events we ignore.
	v.MouseMode = tea.MouseModeCellMotion
	// Shift+Enter needs the kitty keyboard protocol to be distinguishable from
	// a plain Enter. Terminals without it fall back to Alt+Enter or the
	// trailing backslash (plan.md §6.2).
	v.KeyboardEnhancements = tea.KeyboardEnhancements{ReportAlternateKeys: true}
	return v
}

// overlayPalette splices the command list over existing rows, covering them
// rather than displacing them.
//
// Placement prefers the rows directly below the composer. When the frame ends
// before that — which it does whenever the layout is packed — the list flips
// above the composer and covers the transcript tail instead. Either way the
// row count is unchanged.
func (m *Model) overlayPalette(rows []string) []string {
	if !m.paletteOpen() {
		return rows
	}
	// The query is derived from the input rather than mirrored into state, so
	// the two can never drift apart.
	state := m.palette
	state.Query = strings.TrimPrefix(m.editor.Text, "/")
	overlay := m.renderer.RenderPalette(state, VisibleCommands())
	if len(overlay) == 0 {
		return rows
	}

	// The composer's last row is the anchor.
	composerRows := len(m.renderer.RenderComposer(m.composerState()))
	composerEnd := len(rows)
	composerStart := max(composerEnd-composerRows, 0)

	below := m.height - composerEnd
	var at int
	if below >= len(overlay) {
		// Room underneath: grow the frame into the empty rows the terminal
		// already shows.
		at = composerEnd
		for len(rows) < at+len(overlay) {
			rows = append(rows, "")
		}
	} else {
		// Flip above the composer, covering the transcript tail.
		at = max(composerStart-len(overlay), 0)
	}

	for i, line := range overlay {
		idx := at + i
		if idx < 0 {
			continue
		}
		for len(rows) <= idx {
			rows = append(rows, "")
		}
		// Explicitly clear the covered cells; a shorter overlay row must not
		// leave the tail of whatever was underneath visible.
		rows[idx] = line
	}
	return rows
}

// toolTarget pulls the one argument worth showing beside a tool name.
func toolTarget(raw json.RawMessage) string {
	var args map[string]any
	if json.Unmarshal(raw, &args) != nil {
		return ""
	}
	for _, key := range []string{"path", "pattern", "cmd", "query"} {
		if v, ok := args[key].(string); ok && v != "" {
			return truncateCells(strings.ReplaceAll(v, "\n", " "), 60)
		}
	}
	return ""
}

// Run starts the TUI.
func Run(a *agent.Agent, h HeaderState) error {
	m := NewModel(a, h)
	_, err := tea.NewProgram(m).Run()
	return err
}
