package tui

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

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

	input  string
	cursor int

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
		width, _ := ContentWidth(m.width, false)
		m.renderer.SetWidth(width)
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
				m.input = "/" + suggestions[sel].Name
				m.cursor = len([]rune(m.input))
			}
			return m, nil
		case "enter":
			if len(suggestions) > 0 {
				sel := clamp(m.palette.Selected, 0, len(suggestions)-1)
				return m.runCommand(suggestions[sel].Name)
			}
			return m, nil
		case "esc":
			m.input, m.cursor, m.palette.Selected = "", 0, 0
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
		if m.confirmQuit || m.input == "" {
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
		m.input, m.cursor = "", 0
		return m, nil

	case "ctrl+g":
		if n := m.scroll.ToggleBookmark(); n != "" {
			m.notice = n
		}
		return m, nil

	case "backspace":
		if m.cursor > 0 {
			runes := []rune(m.input)
			m.input = string(runes[:m.cursor-1]) + string(runes[m.cursor:])
			m.cursor--
		}
		m.syncShellMode()
		return m, nil

	case "pgup":
		m.scroll.Up(PageLines, m.contentHeight(), m.transcriptHeight())
		return m, nil

	case "pgdown":
		m.scroll.Down(PageLines)
		return m, nil

	case "up":
		if m.input == "" {
			m.scroll.Up(1, m.contentHeight(), m.transcriptHeight())
		}
		return m, nil

	case "down":
		if m.input == "" {
			m.scroll.Down(1)
		}
		return m, nil
	}

	// Ordinary text input. Key.Text is the printable characters the key
	// produced — String() spells space as "space", which would drop it.
	if txt := msg.Key().Text; txt != "" {
		runes := []rune(m.input)
		m.input = string(runes[:m.cursor]) + txt + string(runes[m.cursor:])
		m.cursor += len([]rune(txt))
		m.confirmQuit = false
		m.syncShellMode()
	}
	return m, nil
}

// paletteOpen reports whether the composer is in slash mode.
func (m *Model) paletteOpen() bool {
	return strings.HasPrefix(m.input, "/") && !m.palette.Suppressed
}

// paletteSuggestions is the currently ranked list.
func (m *Model) paletteSuggestions() []Suggestion {
	if !m.paletteOpen() {
		return nil
	}
	return RankCommands(strings.TrimPrefix(m.input, "/"), VisibleCommands())
}

// syncShellMode tracks the `!` prefix that switches the composer to a shell.
func (m *Model) syncShellMode() {
	m.shellMode = strings.HasPrefix(m.input, "!")
}

// runCommand executes a slash command. Unknown commands fall through to a
// notice rather than being sent to the model, which would waste a turn.
func (m *Model) runCommand(name string) (tea.Model, tea.Cmd) {
	m.input, m.cursor, m.palette.Selected = "", 0, 0

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

// escape is the layered cancel of plan.md §6.7.
func (m *Model) escape() {
	switch {
	case m.processing:
		// Esc means stop, which is why it also disarms auto-poke — unlike
		// Ctrl+C, which means "skip this".
		m.interrupt(true)
	default:
		m.scroll.FollowBottom()
		m.input, m.cursor = "", 0
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
	text := strings.TrimSpace(m.input)
	if text == "" {
		return m, nil
	}

	switch SendActionFor(m.processing, m.queueMode, m.input, alternate) {
	case Queue:
		m.pending = append(m.pending, PendingMessage{Kind: PendingQueued, Text: text})
		m.input, m.cursor = "", 0
		return m, nil

	case Interleave:
		m.agent.Interject(agent.Interrupt{Source: agent.SourceUser, Text: text})
		m.pending = append(m.pending, PendingMessage{Kind: PendingSent, Text: text})
		m.notice = "⏭ Sending now (interleave)"
		m.input, m.cursor = "", 0
		return m, nil

	default:
		m.submit(text)
		return m, nil
	}
}

// submit starts a turn.
func (m *Model) submit(text string) {
	m.blocks = append(m.blocks, Block{
		Kind:   BlockUser,
		Text:   text,
		Number: m.promptCount,
	})
	m.promptCount++
	m.renumberPrompts()
	m.input, m.cursor = "", 0
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

// stack builds the vertical layout request.
func (m *Model) stack() Stack {
	s := Stack{Available: m.height, ContentHeight: len(m.transcriptLines())}
	s.Heights[SlotStatus] = 1
	if m.notice != "" {
		s.Heights[SlotNotice] = 1
	}
	s.Heights[SlotQueued] = min(len(m.pending), MaxPendingRows)

	composer := m.renderer.RenderComposer(m.composerState())
	s.Heights[SlotComposer] = len(composer)
	return s
}

func (m *Model) composerState() ComposerState {
	return ComposerState{
		Text:         m.input,
		Cursor:       m.cursor,
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
	// Alt-screen is a view property in Bubble Tea v2, not a program option.
	v.AltScreen = true
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
	state.Query = strings.TrimPrefix(m.input, "/")
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
