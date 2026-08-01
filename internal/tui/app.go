package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"evilcode/internal/agent"
	"evilcode/internal/config"
	"evilcode/internal/core"
	"evilcode/internal/graphics"
	"evilcode/internal/lsp"
	"evilcode/internal/memory"
	"evilcode/internal/provider"
	"evilcode/internal/session"
	"evilcode/internal/theme"
	"evilcode/internal/todo"
	"evilcode/internal/tools"
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

	// transcriptCache is the assembled current-width transcript. A tick changes
	// status widgets, not settled history, so retaining this one frame-sized
	// slice avoids rebuilding every old row twelve times a second. It is cleared
	// before state-changing messages and never populated for live streaming or
	// entry animation.
	transcriptCache      Rows
	transcriptCacheWidth int
	transcriptCacheValid bool

	editor Editor

	// pastes holds collapsed paste contents, restored on send.
	pastes []Paste

	// lastPaste times the most recent paste, for the trailing-Enter guard.
	lastPaste time.Time

	processing bool
	queueMode  bool

	status  StatusState
	header  HeaderState
	notice  string
	started time.Time
	turnAt  time.Time

	// promptCount is how many user prompts have been submitted, which drives
	// the rainbow numbering.
	promptCount int

	// streamingIdx points at the assistant block currently being appended to,
	// or -1 when nothing is streaming. reasoningIdx is its thinking twin.
	streamingIdx int
	reasoningIdx int

	pending []PendingMessage

	// palette is the slash-command overlay. It reserves no layout height:
	// opening it must never move the transcript (plan.md invariant 3).
	palette PaletteState

	// ask holds a pending `ask` tool question. While one is open the composer
	// is an answer box, and the slash palette suppresses itself (§5.1).
	ask       *tools.AskRequest
	askCursor int
	askChosen map[int]bool

	// picker is the inline model picker. Unlike the palette it *does* reserve
	// layout height, because it is a surface you interact with (plan.md §5.3).
	picker     PickerState
	pickerOpen bool

	// models supplies picker entries; it is set once the provider answers.
	models []ModelEntry

	// providers is the configured provider list, so the picker can list models
	// across every provider and rebuild the live one when a selection crosses
	// providers. Empty falls back to the active provider only.
	providers []config.ProviderConfig

	// sawEscapeHint keeps the trailing-backslash tip to once per session.
	sawEscapeHint bool

	// commandArg holds the argument of the command being run.
	commandArg string

	// loginMode owns the composer while a cloud key is entered. The editor is
	// reset completely when it ends so undo/stash cannot resurrect the secret.
	// loginProvider names the provider the entered key is saved to.
	loginMode     bool
	loginProvider string

	// loginPicker is the provider selector shown by `/login` with no argument,
	// so you can choose which provider's key you are entering before the masked
	// composer takes over. `/login <provider>` skips it and goes straight in.
	loginPicker     LoginPickerState
	loginPickerOpen bool

	// resumeTarget is set when the picker chose a session to switch to; the
	// caller re-execs into it after the program exits.
	resumeTarget string

	// attachments are images staged for the next message (§6.6), and vision
	// records whether the active model can actually accept them.
	attachments []Attachment
	vision      bool

	// compactor summarises and replaces the conversation when it gets long.
	compactor *agent.Compactor

	// sessionTitle is the last title written, so an unchanged one is not
	// re-appended to the log every turn.
	sessionTitle string

	// placements is the last frame's widget geometry, for hit-testing clicks.
	placements []Placement

	// Widget airtime and change state keep the one dock slot from becoming a
	// permanent ModelInfo billboard. They are bounded by WidgetKind, not by
	// transcript length.
	widgetClock       uint64
	widgetLastShown   map[WidgetKind]uint64
	widgetLastChanged map[WidgetKind]uint64
	widgetHashes      map[WidgetKind]uint64

	// dock places widgets in the transcript's negative space, and widgetsOn
	// is the Alt+I toggle.
	dock      *Dock
	widgetsOn bool

	// centered is the Alt+C layout toggle, and overscroll drives the elastic
	// pull-to-reveal facts line (§4.4).
	centered   bool
	overscroll Overscroll

	// typingLock keeps the view where it is while typing (Alt+S, §4.5).
	typingLock bool

	// entryAnim is the ~600ms flourish on a just-submitted prompt (§10.2).
	entryAnim EntryAnimation

	// welcomeChip is the focused starter prompt while the transcript is empty.
	welcomeChip  int
	welcomeFocus bool

	// keymap resolves chords, and hotkeys drives the rare-chord and near-miss
	// feedback of §6.8.
	keymap    *Keymap
	hotkeys   *HotkeyUsage
	showHints bool

	// thinking is the reasoning display mode (§9.7).
	thinking ThinkingMode

	// Self-test state (§14): frame capture, layout overlays, and the
	// anchor-stability recorder behind /smoothness.
	recording      bool
	screenshotMode bool
	debugVisual    bool
	recordSeq      int
	lastFrameHash  uint64
	smoothness     SmoothnessReport
	anchorHashes   map[string]int

	// diffMode cycles Off → Inline → Pinned → File (Alt+G, §9.3), and panel
	// holds what the side pane is showing.
	diffMode   DiffMode
	panel      PanelContent
	panelOpen  bool
	panelRatio int

	// quickView is the transient click-to-look overlay (§3.2). Non-nil means it
	// is showing; Esc clears it and the persistent /diff panel underneath is
	// untouched. It is rendered in preference to m.panel and opens the pane
	// regardless of m.panelOpen/m.diffMode, but it never writes any of them.
	quickView *PanelContent

	// sessions is the full-screen picker, and dataDir is where session state
	// lives so the picker can act on it.
	sessions     SessionPickerState
	sessionsOpen bool
	dataDir      string
	store        *session.Store
	cwd          string

	// artVariant is chosen once per process so the welcome animation stays the
	// same one for a session, and decorate gates all decorative animation.
	artVariant Variant
	decorate   bool

	// helpOpen and helpScroll drive the full-screen help overlay (§5.5).
	helpOpen   bool
	helpScroll int

	// pendingAsk is the slot the ask tool parks its question in.
	pendingAsk tools.PendingAsk

	// history is the Ctrl+R reverse search, and prompts is the recall source
	// merged across sessions (plan.md §5.2).
	history HistorySearch
	prompts *session.History

	// poke is the auto-poke hook, when the session has one.
	poke *agent.PokeHook

	// todos is the session's todo state, for the inline card.
	todos        *todo.Store
	showTodoCard bool

	// memory is the long-term memory bank (plan.md §19), nil when the feature
	// is off.
	memory *memory.Manager

	// graphics is the image protocol this terminal speaks, and imagesOn is the
	// Alt+Shift+I toggle. pendingImages holds escape sequences to emit after
	// the next frame — they carry no printable cells, so they must not be part
	// of any row the layout measures.
	graphics      graphics.Protocol
	imagesOn      bool
	pendingImages string

	// diagrams maps mermaid source to its rendered PNG, so an unchanged
	// diagram is never re-rendered — mmdc starts a headless browser.
	diagrams   map[string]string
	diagramDir string
	// diagramMu guards the inbox and the diagrams map, both touched by render
	// goroutines and by the render loop.
	diagramMu sync.Mutex

	// diagramInbox is a buffered queue, not a slot. It was an atomic pointer,
	// so a second render finishing before the first was drained overwrote it —
	// and the lost render's source stays mapped to "" forever, which is the
	// sentinel meaning "already started". The diagram then never appears and
	// never retries.
	diagramInbox chan *mermaidRendered
	nextImageID  int

	// streamStart, streamChars and estimatedOut drive the live rate. Providers
	// report usage only in the final chunk, so without an estimate the status
	// line reads "0.0 tps · ↑0 ↓0" for the entire time text is arriving — which
	// is the whole window anyone is actually looking at it.
	streamStart  time.Time
	streamChars  int
	estimatedOut int

	// genMS accumulates generation time across a turn's requests, which is what
	// tokens-per-second is measured over.
	genMS int

	// sessionTokens are cumulative provider counts for /stats. StatusState is
	// deliberately reset at turn end because its counters drive the live
	// status line; keeping the session totals here prevents a completed turn
	// from making the session look unused.
	sessionTokensIn, sessionTokensOut int

	// ctxUsed is the newest request's context size, for the meter. It is not
	// the sum of the turn's requests — each one carries the whole conversation.
	ctxUsed int

	// cacheRead/cacheWrite accumulate DeepSeek KV-cache token counts across
	// the whole session, for the KvCache widget (plan.md §8.5-adjacent). They
	// stay zero for providers that do not report caching, which is what keeps
	// the widget away outside DeepSeek.
	cacheRead  int
	cacheWrite int

	// keepThinking leaves finished traces expanded (display.keep_thinking).
	keepThinking bool

	// modelsPending guards against a second fetch while one is in flight.
	modelsPending bool

	// queuedHidden is a harness prompt waiting for an interrupted turn to end.
	// Sending it immediately raced the turn it had just cancelled, and since
	// H2.3 the agent refuses the second one — silently, a hidden prompt having
	// nobody to report to.
	queuedHidden string

	// hiddenPrompt is the text of an injected turn that must not be drawn as a
	// user block, matched once when its turn starts.
	hiddenPrompt string

	// reloadTo is the session to resume after a `/reload`, or "".
	reloadTo string

	// overnight is the supervised long-run loop (§5), inert unless armed.
	overnight Overnight

	// advisor is the §21 second opinion, and lsp the language-server manager.
	// Both are nil when unconfigured, which every path has to survive.
	advisor *agent.Advisor
	lsp     *lsp.Manager

	// swarm is the other agents in the daemon, nil outside one (plan.md §20).
	swarm  *SwarmState
	summon SummonFunc

	// swarmDocked records whether the status widget found a slot last frame,
	// which is what the strip stands down against.
	swarmDocked bool

	// sideAnswer carries a finished `/btw` result from its goroutine into the
	// render loop, so the side call never touches model state directly.
	sideAnswer atomic.Pointer[sideAnswer]

	// bg is the background-task registry, and bgDone carries completions from
	// their goroutines into the render loop.
	bg     *tools.Background
	bgDone atomic.Pointer[bgCompletion]

	// lastFrame is the most recent rendered frame, which `/screenshot` writes
	// out. Capturing at render time is the only way to get exactly what the
	// user is looking at.
	lastFrame string

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
		agent:           a,
		renderer:        NewRenderer(p, 80),
		header:          h,
		started:         time.Now(),
		streamingIdx:    -1,
		reasoningIdx:    -1,
		dock:            NewDock(),
		widgetsOn:       true,
		widgetLastShown: map[WidgetKind]uint64{}, widgetLastChanged: map[WidgetKind]uint64{},
		widgetHashes: map[WidgetKind]uint64{},
		thinking:     ThinkingCurrent,
		diffMode:     DiffInline,
		panelRatio:   50,
		showHints:    true,
		overscroll:   Overscroll{Mode: OverscrollPull},
		welcomeFocus: true,
		artVariant:   PickVariant(h.SessionName),
		decorate:     os.Getenv("SSH_TTY") == "" && os.Getenv("SSH_CONNECTION") == "",
	}
}

// Asker returns the presenter the `ask` tool uses. The question is handed to
// the render loop and answered from the keyboard, so the tool blocks on the
// user rather than on a timer.
func (m *Model) Asker() tools.Asker {
	return tools.AskFunc(func(ctx context.Context, req *tools.AskRequest) ([]string, error) {
		m.pendingAsk.Set(req)
		select {
		case labels := <-req.Reply:
			return labels, nil
		case <-ctx.Done():
			// This question, not whichever one happens to be on screen: a
			// cancelled call may well be one waiting behind another.
			m.pendingAsk.Remove(req)
			return nil, ctx.Err()
		}
	})
}

// WithSessions attaches the session store and data directory, enabling the
// picker and the session commands.
func (m *Model) WithSessions(dataDir, cwd string, store *session.Store) *Model {
	m.dataDir, m.cwd, m.store = dataDir, cwd, store
	return m
}

// WithProviders attaches the configured provider list so the model picker can
// list models from every provider and switch the live provider on selection.
func (m *Model) WithProviders(provs []config.ProviderConfig) *Model {
	m.providers = provs
	return m
}

// bgCompletion is a finished background task waiting to be announced.
type bgCompletion struct {
	Label  string
	Failed bool
}

// WithBackground attaches the background-task registry.
func (m *Model) WithBackground(bg *tools.Background) *Model {
	m.bg = bg
	bg.OnDone = func(t *tools.BackgroundTask) {
		_, failed, _ := t.Snapshot()
		m.bgDone.Store(&bgCompletion{Label: t.Label, Failed: failed})
	}
	return m
}

// WithKeymap attaches the resolved keymap and hotkey usage tracking.
func (m *Model) WithKeymap(km *Keymap, usage *HotkeyUsage, hints bool) *Model {
	m.keymap, m.hotkeys, m.showHints = km, usage, hints
	return m
}

// WithHistory attaches the cross-session prompt history.
func (m *Model) WithHistory(h *session.History) *Model {
	m.prompts = h
	return m
}

// WithTodos attaches the session's todo state and auto-poke hook.
func (m *Model) WithTodos(store *todo.Store, poke *agent.PokeHook) *Model {
	m.todos, m.poke = store, poke
	return m
}

// WithMemory attaches the memory bank, which drives `/memory`, the activity
// widget, and semantic search in the session picker.
func (m *Model) WithMemory(mem *memory.Manager) *Model {
	m.memory = mem
	return m
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
		var e agent.Event
		ok := true
		select {
		case e = <-m.agent.Events():
		case <-m.agent.Done():
			ok = false
		}
		if !ok {
			return eventsClosedMsg{}
		}
		return eventMsg(e)
	}
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Tick only advances clocks. Every other message may change transcript
	// content, layout, or renderer settings, so invalidate once at the shared
	// boundary instead of trying to remember every mutation site.
	if _, ok := msg.(tickMsg); !ok {
		m.invalidateTranscriptCache()
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.applyWrapWidth()
		m.renderer.Graphics, m.renderer.ImagesOn = m.graphics, m.imagesOn
		m.drainDiagrams()
		return m, nil

	case compactDone:
		m.applyCompaction(msg)
		return m, nil

	case clipboardImage:
		m.applyClipboardImage(msg)
		return m, nil

	case modelsLoaded:
		m.applyModels(msg)
		return m, nil

	case semanticHits:
		m.applySemanticHits(msg)
		return m, nil

	case summonResult:
		m.applySummonResult(msg)
		return m, nil

	case rebuildResult:
		if msg.err != nil {
			m.blocks = append(m.blocks, Block{Kind: BlockError, Text: msg.err.Error()})
			m.notice = ""
			m.scroll.FollowBottom()
			return m, nil
		}
		m.notice = "🔄 Built and tested — restarting..."
		return m, m.reloadCommand()

	case reloadRequest:
		m.reloadTo = msg.session
		return m, nil

	case tickMsg:
		if m.processing {
			// Deterministic mode has no wall-clock text (invariant 5). Without
			// this a golden of any in-flight state — a running tool, a pending
			// question — races its own elapsed counter and never settles.
			if !Deterministic() {
				m.status.Elapsed = time.Since(m.turnAt)
			}
		}
		m.takeSideAnswer()
		if done := m.bgDone.Swap(nil); done != nil {
			if done.Failed {
				m.notice = "✗ background: " + done.Label + " failed"
			} else {
				m.notice = "✓ background: " + done.Label
			}
		}
		// A pending question is picked up here rather than pushed, so the tool
		// goroutine never touches model state.
		if m.ask == nil {
			if req := m.pendingAsk.Get(); req != nil {
				m.ask, m.askCursor = req, 0
				m.askChosen = map[int]bool{}
			}
		}
		return m, m.tick()

	case eventsClosedMsg:
		return m, nil

	case eventMsg:
		m.applyEvent(agent.Event(msg))
		return m, m.waitForEvent()

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case tea.MouseClickMsg:
		mouse := msg.Mouse()
		if mouse.Button != tea.MouseLeft {
			return m, nil
		}
		if m.dismissWidgetAt(mouse) {
			return m, nil
		}
		m.openQuickViewAt(mouse)

	case tea.PasteMsg:
		if m.loginMode {
			m.editor.Insert(msg.Content)
			return m, nil
		}
		// Bracketed paste never inspects the clipboard for images: on Wayland
		// a multi-MIME clipboard is routinely misidentified, and a stray image
		// attachment is worse than a missing one (plan.md §6.6).
		insert, stored := CollapsePaste(msg.Content)
		m.editor.Insert(insert)
		m.welcomeFocus = false
		if stored != nil {
			m.pastes = append(m.pastes, *stored)
		}
		m.lastPaste = time.Now()
		return m, nil

	case tea.MouseWheelMsg:
		return m.handleWheel(msg)
	}
	return m, nil
}

// applyEvent folds one agent event into the view.
func (m *Model) applyEvent(e agent.Event) {
	e = sanitizeEvent(e)
	switch e.Kind {
	case agent.EventTurnStart:
		m.processing = true
		m.turnAt = time.Now()
		m.status = StatusState{Phase: PhaseSending, Animate: !Deterministic()}
		m.genMS, m.estimatedOut, m.streamChars = 0, 0, 0
		m.streamStart = time.Time{}
		m.streamingIdx = -1
		// A turn this client did not start still has to show its prompt, or an
		// attached client renders answers to questions it never sees. The check
		// is against the last block rather than a flag, because the client that
		// typed it has already drawn it.
		// A hidden prompt is one this client injected on purpose — /plan,
		// auto-poke, overnight — and its full instruction text has no business
		// in the transcript. Without this check the attach path, which exists
		// so a client renders prompts it did not type, drew every one of them.
		if e.Text != "" && e.Text == m.hiddenPrompt {
			m.hiddenPrompt = ""
		} else if e.Text != "" && !m.lastBlockIsPrompt(e.Text) {
			m.promptCount++
			m.blocks = append(m.blocks, Block{Kind: BlockUser, Text: e.Text, Number: m.promptCount})
			m.renumberPrompts()
			m.followIfPinned()
		}

	case agent.EventTextDelta:
		m.status.Phase = PhaseStreaming
		if m.streamingIdx < 0 {
			// The answer starting is the end of the thinking that led to it.
			// Without this a trace stayed open underneath the whole reply and
			// only collapsed when the turn ended, which is far too late to be
			// useful and pushes the answer down the screen while it streams.
			m.finishReasoning()
			m.blocks = append(m.blocks, Block{Kind: BlockAssistant, Streaming: true})
			m.streamingIdx = len(m.blocks) - 1
		}
		m.blocks[m.streamingIdx].Text += e.Text
		m.observeStreamed(e.Text)
		m.followIfPinned()

	case agent.EventReasoningDelta:
		m.status.Phase = PhaseThinking
		if m.thinking == ThinkingOff {
			break
		}
		if m.reasoningIdx < 0 {
			m.blocks = append(m.blocks, Block{Kind: BlockReasoning, Streaming: true})
			m.reasoningIdx = len(m.blocks) - 1
		}
		m.blocks[m.reasoningIdx].Text += e.Text
		m.observeStreamed(e.Text)
		m.followIfPinned()

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
			ToolPath:   toolPath(e.Call.Args),
			ToolTokens: len(e.Output) / 4,
			Failed:     e.IsError(),
			Diff:       e.Diff,
		}
		if b.ToolPath != "" {
			b.ToolPathExists = toolPathExists(m.cwd, b.ToolPath)
			b.ToolPathMarkdown = b.ToolPathExists && isMarkdown(b.ToolPath)
		}
		if b.ToolName == "bash" {
			b.ToolCommand = truncateToolCommand(toolCommand(e.Call.Args))
			b.ToolOutput = tools.Truncate(e.Output)
		}
		if e.Intent != "" && !strings.Contains(e.Intent, b.ToolTarget) {
			b.ToolIntent = e.Intent
		}
		if e.DiffStat != nil {
			b.HasDiff = true
			b.Added, b.Removed = e.DiffStat.Added, e.DiffStat.Removed
		}
		// The panel always holds the newest diff, whether or not it is visible,
		// so toggling to it later shows something rather than nothing.
		//
		// The block keeps its diff too: whether the transcript draws it is the
		// renderer's call, which is what lets Alt+G re-render history instead
		// of only affecting what arrives next.
		if e.Diff != "" {
			m.panel = PanelContent{Title: b.ToolTarget, Path: b.ToolTarget, Diff: e.Diff}
			if m.diffMode.UsesPanel() {
				m.panelOpen = true
			}
		}
		m.blocks = append(m.blocks, b)
		if e.IsError() {
			m.blocks = append(m.blocks, Block{Kind: BlockError, Text: e.ErrText})
		}
		// A todo write re-arms the poke cycle and shows what changed. The
		// delta rides the event rather than a side channel, so it cannot race
		// the render loop.
		if e.Call.Name == "todo" && !e.IsError() {
			if m.poke != nil {
				m.poke.Rearm()
			}
			if d, ok := e.Display.(todo.Delta); ok && !d.Empty() {
				m.blocks = append(m.blocks, Block{Kind: BlockTodoDelta, TodoDelta: d})
			}
		}
		m.followIfPinned()

	case agent.EventTokenUsage:
		// Accumulated, not assigned. A turn with tool calls makes one request
		// per round and each reports only its own usage, so assigning here
		// showed the last round's tokens as if they were the turn's — which
		// also made the context meter read far below the truth.
		// Two different numbers, which is why this used to be wrong in two
		// directions at once. Spend is the sum across a turn's requests — a
		// turn with three tool rounds makes four, and you paid for all of them.
		// Context is the *latest* request's size, because prompt tokens already
		// are the whole conversation; summing them would double-count it.
		// The provider's own count replaces whatever the live estimate had
		// reached for this request, rather than adding to it.
		m.status.TokensOut -= m.estimatedOut
		m.estimatedOut, m.streamChars = 0, 0
		m.streamStart = time.Time{}

		m.status.TokensIn += e.Usage.In
		m.status.TokensOut += e.Usage.Out
		m.sessionTokensIn += e.Usage.In
		m.sessionTokensOut += e.Usage.Out
		m.ctxUsed = e.Usage.CtxUsed
		m.cacheRead += e.Usage.CacheRead
		m.cacheWrite += e.Usage.CacheWrite
		m.genMS += e.Usage.GenMS
		if m.genMS > 0 {
			// Over generation time only. Wall-clock counts tool execution as
			// generation and reports a rate that is not the model's.
			m.status.TokensPerSecond = float64(m.status.TokensOut) /
				(float64(m.genMS) / 1000)
		}

	case agent.EventNotice:
		// A notice marks a boundary between two things the model said. Without
		// closing the streaming block here, an auto-poked turn renders as one
		// paragraph — "…refresh path next.Done.Done.Done." — because the poke
		// continues the same Loop and so never emits a fresh turn start.
		m.streamingIdx = -1
		if e.Level == agent.LevelInfo || e.Level == "" {
			m.notice = e.Text
			break
		}
		// A warning goes in the transcript, not the status line. The status line
		// is cleared by the next thing the user types, and "another agent
		// rewrote the file you are working from" is not something to lose to a
		// keystroke.
		m.blocks = append(m.blocks, Block{Kind: BlockNotice, Text: e.Text})
		m.followIfPinned()

	case agent.EventMemoryRecall:
		// Drawn as a block rather than a status flash: what memory put in front
		// of the model is part of the turn's record, and a recall the user
		// scrolls back to is one they can still notice was wrong.
		if hits, ok := e.Display.([]memory.Hit); ok && len(hits) > 0 {
			m.blocks = append(m.blocks, Block{Kind: BlockMemory, Memories: hits})
			m.followIfPinned()
		}

	case agent.EventError:
		m.finishStreaming()
		m.blocks = append(m.blocks, Block{Kind: BlockError, Text: e.ErrText})
		m.followIfPinned()

	case agent.EventTurnEnd:
		m.finishStreaming()
		m.processing = false
		// Read before the reset: the status line is per-turn and is cleared in
		// the next statement, so the overnight budget was always told zero.
		spent := m.status.TokensIn + m.status.TokensOut
		m.status = StatusState{Phase: PhaseIdle}
		m.flushPending()
		// The unattended loop advances here, after everything the turn produced
		// has been folded in — so its stall detector reads the todo list as it
		// actually stands rather than as it stood mid-turn. Once: this was
		// called twice, which counted every turn twice against the cap and
		// could start two continuations on one agent.
		m.stepOvernight(spent)

		// A harness prompt that was queued behind an interrupted turn starts
		// here, once that turn has actually ended.
		if m.queuedHidden != "" && !m.processing {
			prompt := m.queuedHidden
			m.queuedHidden = ""
			m.submitHidden(prompt)
		}
	}
}

// sanitizeEvent strips terminal control sequences from everything an event
// carries into the UI.
//
// At ingress rather than at each renderer. The first pass at this sanitized the
// transcript renderer alone, and a review found five other paths — the status
// line's tool name, the side panel, the ask picker, the model list, the memory
// and todo widgets — each rendering event-derived text through its own code.
// Chasing renderers means finding all of them, and then finding the next one
// somebody adds. Everything here arrived from a provider, a tool, or a file.
func sanitizeEvent(e agent.Event) agent.Event {
	// Streaming deltas are the exception: they are fragments, and a control
	// sequence can span two of them. Sanitizing each fragment drops the
	// introducer from one and leaves the payload in the next as visible junk.
	// The transcript sanitizes each block's accumulated text at render, which
	// sees the whole sequence and consumes it — so the fragments pass through
	// here untouched and are cleaned once they are whole.
	switch e.Kind {
	case agent.EventTextDelta, agent.EventReasoningDelta:
	default:
		e.Text = core.SanitizeTerminal(e.Text)
	}
	e.ErrText = core.SanitizeTerminal(e.ErrText)
	e.Output = core.SanitizeTerminal(e.Output)
	e.Intent = core.SanitizeTerminal(e.Intent)
	e.Diff = core.SanitizeTerminal(e.Diff)
	if e.Call != nil {
		call := *e.Call
		call.Name = core.SanitizeTerminal(call.Name)
		e.Call = &call
	}
	e.Display = sanitizeDisplay(e.Display)
	return e
}

// sanitizeDisplay cleans the typed payloads widgets render directly.
//
// These never pass through the transcript, so the block-level sanitize does not
// see them: a remembered fact and a todo item are both model-authored text on
// their way to a widget that draws them itself.
func sanitizeDisplay(display any) any {
	switch v := display.(type) {
	case []memory.Hit:
		out := make([]memory.Hit, len(v))
		for i, h := range v {
			h.Text = core.SanitizeTerminal(h.Text)
			h.Session = core.SanitizeTerminal(h.Session)
			out[i] = h
		}
		return out
	case todo.Delta:
		return sanitizeTodoDelta(v)
	}
	return display
}

// sanitizeTodoDelta cleans the item text a todo card draws. The content is
// whatever the model wrote into its plan.
func sanitizeTodoDelta(d todo.Delta) todo.Delta {
	out := d
	out.Changes = make([]todo.ItemChange, len(d.Changes))
	for i, c := range d.Changes {
		c.Item.Content = core.SanitizeTerminal(c.Item.Content)
		if c.Item.Group != nil {
			group := core.SanitizeTerminal(*c.Item.Group)
			c.Item.Group = &group
		}
		out.Changes[i] = c
	}
	return out
}

// lastBlockIsPrompt reports whether the newest user block already holds this
// text, which is how the client that typed it avoids drawing it twice.
func (m *Model) lastBlockIsPrompt(text string) bool {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].Kind == BlockUser {
			return m.blocks[i].Text == text
		}
	}
	return false
}

// finishStreaming freezes the streaming block so it caches from now on.
func (m *Model) finishStreaming() {
	if m.streamingIdx >= 0 && m.streamingIdx < len(m.blocks) {
		m.blocks[m.streamingIdx].Streaming = false
		// A fence only becomes renderable once it closes, which is why this is
		// here rather than on every delta: mmdc on half a diagram fails slowly.
		m.renderDiagrams(m.blocks[m.streamingIdx].Text)
	}
	m.streamingIdx = -1

	m.finishReasoning()
}

// finishReasoning freezes the live thinking trace and collapses it, unless the
// reader has asked to keep traces open (display.keep_thinking).
func (m *Model) finishReasoning() {
	if m.reasoningIdx >= 0 && m.reasoningIdx < len(m.blocks) {
		m.blocks[m.reasoningIdx].Streaming = false
		m.blocks[m.reasoningIdx].Collapsed = !m.keepThinking
		m.blocks[m.reasoningIdx].dropCache()
	}
	m.reasoningIdx = -1
	m.collectReasoning()
}

// collectReasoning drops thinking traces that have scrolled safely out of view.
//
// It never runs while the reader has scrolled up: removing content someone may
// be reading is worse than holding a little more memory (plan.md §4.6).
func (m *Model) collectReasoning() {
	if m.thinking != ThinkingCurrent || m.scroll.Paused {
		return
	}
	viewport := m.transcriptHeight()
	total := m.contentHeight()

	kept := m.blocks[:0]
	for i := range m.blocks {
		b := m.blocks[i]
		if b.Kind == BlockReasoning && i < len(m.blocks)-1 {
			lines := len(m.renderer.Lines(&m.blocks[i]))
			if total-lines > viewport+2 {
				total -= lines
				continue
			}
		}
		kept = append(kept, b)
	}
	m.blocks = kept
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
	if m.loginMode {
		return m.handleLoginKey(key, msg)
	}
	if m.loginPickerOpen {
		return m.handleLoginPickerKey(key)
	}

	// A pending question owns the keyboard: the composer is an answer box
	// until it is resolved (plan.md §17).
	if m.ask != nil {
		return m.handleAskKey(key)
	}

	if m.history.Active {
		return m.handleHistoryKey(key, msg)
	}

	if m.sessionsOpen {
		return m.handleSessionKey(key, msg)
	}

	if m.helpOpen {
		switch key {
		case "esc", "q", "ctrl+c", "enter":
			m.helpOpen = false
		case "up", "k":
			m.helpScroll = max(m.helpScroll-1, 0)
		case "down", "j":
			m.helpScroll++
		case "pgup":
			m.helpScroll = max(m.helpScroll-PageLines, 0)
		case "pgdown", " ", "space":
			m.helpScroll += PageLines
		}
		return m, nil
	}

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
			// A typed command with arguments runs as typed; otherwise the
			// highlighted suggestion is taken.
			name, arg := splitCommand(m.editor.Text)
			if arg != "" {
				return m.runCommandWithArg(name, arg)
			}
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

	// Configurable bindings are resolved before the fixed keys, so a rebind
	// genuinely takes the chord away from its default (plan.md §11).
	if m.keymap != nil {
		if b, ok := m.keymap.Lookup(key); ok {
			if handled, model, cmd := m.runAction(b.Action); handled {
				m.noteHotkey(key, b.Desc)
				return model, cmd
			}
		}
	}

	// The welcome screen's chips take the arrows and Enter (§7, item 1). The
	// transcript is empty there, so there is no scroll to conflict with — but a
	// rebind still gets these chords first, which is why this sits below the
	// keymap rather than above it.
	if len(m.blocks) == 0 && m.editor.Text == "" && len(SuggestionChips) > 0 {
		switch key {
		case "up":
			m.welcomeFocus = true
			m.welcomeChip = (m.welcomeChip - 1 + len(SuggestionChips)) % len(SuggestionChips)
			return m, nil
		case "down":
			m.welcomeFocus = true
			m.welcomeChip = (m.welcomeChip + 1) % len(SuggestionChips)
			return m, nil
		case "enter":
			// Only when a chip is actually highlighted: with the focus dropped
			// there is nothing on screen to say which chip Enter would send.
			if m.welcomeFocus {
				m.editor.Text = SuggestionChips[m.welcomeChip%len(SuggestionChips)]
				m.editor.Cursor = len([]rune(m.editor.Text))
				return m.send(false)
			}
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

	case "ctrl+r":
		m.history.Open(m.editor.Text, m.editor.Cursor)
		return m, nil

	case "alt+c":
		m.centered = !m.centered
		m.renderer.Centered = m.centered
		m.applyWrapWidth()
		m.renderer.Graphics, m.renderer.ImagesOn = m.graphics, m.imagesOn
		m.drainDiagrams()
		if m.centered {
			m.notice = "Centered layout"
		} else {
			m.notice = "Left-aligned layout"
		}
		return m, nil

	case "alt+s":
		m.typingLock = !m.typingLock
		if m.typingLock {
			m.notice = "Typing scroll lock: ON - typing stays at current chat position"
		} else {
			m.notice = "Typing scroll lock: OFF - typing follows chat bottom"
		}
		return m, nil

	case "alt+g":
		m.diffMode = m.diffMode.Next()
		m.panelOpen = m.diffMode.UsesPanel() && !m.panel.Empty()
		m.applyWrapWidth()
		m.renderer.Graphics, m.renderer.ImagesOn = m.graphics, m.imagesOn
		m.drainDiagrams()
		m.notice = "Diff mode: " + m.diffMode.String()
		return m, nil

	case "alt+m":
		m.panelOpen = !m.panelOpen
		m.applyWrapWidth()
		m.renderer.Graphics, m.renderer.ImagesOn = m.graphics, m.imagesOn
		m.drainDiagrams()
		return m, nil

	case "ctrl+1", "ctrl+2", "ctrl+3", "ctrl+4":
		// Ctrl+1..4 snap the split to quarters (§11).
		m.panelRatio = 25 * (int(key[len(key)-1] - '0'))
		m.panelOpen = true
		m.applyWrapWidth()
		m.renderer.Graphics, m.renderer.ImagesOn = m.graphics, m.imagesOn
		m.drainDiagrams()
		m.notice = fmt.Sprintf("Side panel: %d%%", m.panelRatio)
		return m, nil

	case "ctrl+v", "alt+v":
		// Explicit only. Bracketed paste must never sniff the clipboard for
		// images — a Wayland clipboard advertises several MIME types at once
		// and is routinely misidentified (plan.md §6.6).
		return m, m.pasteImage()

	case "ctrl+up", "alt+up":
		// Pending messages come back for editing before prompt history does:
		// something you just staged is more likely what you meant to reach
		// than something from last week (plan.md §6.4).
		if m.editor.Text == "" && len(m.pending) > 0 {
			var texts []string
			for _, p := range drainPendingForEdit(m.pending) {
				texts = append(texts, p.Text)
			}
			n := len(m.pending)
			m.pending = nil
			m.editor.Text = strings.Join(texts, "\n\n")
			m.editor.Cursor = len([]rune(m.editor.Text))
			m.notice = fmt.Sprintf("Retrieved %d pending message(s) for editing", n)
			return m, nil
		}
		if m.editor.Text == "" && m.prompts != nil {
			if all := m.prompts.All(); len(all) > 0 {
				m.editor.Text = all[len(all)-1]
				m.editor.Cursor = len([]rune(m.editor.Text))
			}
		}
		return m, nil

	case "backspace":
		m.editor.Backspace()
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

	// An unhandled modified chord is a deliberate press that did nothing; say
	// so rather than swallowing it (plan.md §6.8). Checked after the text
	// resolution below would have claimed it, so AltGr symbols still type.
	if txt := msg.Key().Text; txt == "" && IsModifiedChord(key) {
		m.noteNearMiss(key)
		return m, nil
	}

	// Ordinary text input. Key.Text is the printable characters the key
	// produced — String() spells space as "space", which would drop it.
	if txt := msg.Key().Text; txt != "" {
		m.editor.Insert(txt)
		m.welcomeFocus = false
		m.confirmQuit = false
		// Typing normally follows the bottom; the lock keeps the reader where
		// they were (plan.md §4.5).
		if !m.typingLock {
			m.scroll.FollowBottom()
		}
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

// runAction executes a bound action. The bool reports whether it was handled,
// so an action the current context does not support falls through to the fixed
// keys rather than being swallowed.
func (m *Model) runAction(a Action) (bool, tea.Model, tea.Cmd) {
	switch a {
	case ActionScrollUp:
		m.scroll.Up(1, m.contentHeight(), m.transcriptHeight())
	case ActionScrollDown:
		m.scroll.Down(1)
	case ActionPageUp:
		m.scroll.Up(PageLines, m.contentHeight(), m.transcriptHeight())
	case ActionPageDown:
		m.scroll.Down(PageLines)
	case ActionPrevPrompt:
		m.jumpPrompt(-1)
	case ActionNextPrompt:
		m.jumpPrompt(1)
	case ActionScrollBookmark:
		if n := m.scroll.ToggleBookmark(); n != "" {
			m.notice = n
		}
	case ActionCenteredToggle:
		m.centered = !m.centered
		m.renderer.Centered = m.centered
		m.applyWrapWidth()
		m.renderer.Graphics, m.renderer.ImagesOn = m.graphics, m.imagesOn
		m.drainDiagrams()
		m.notice = map[bool]string{true: "Centered layout", false: "Left-aligned layout"}[m.centered]
	case ActionInfoWidgets:
		m.widgetsOn = !m.widgetsOn
		m.notice = map[bool]string{true: "Info widgets: ON", false: "Info widgets: OFF"}[m.widgetsOn]
	case ActionTodoCard:
		m.showTodoCard = !m.showTodoCard
	case ActionTypingLock:
		m.typingLock = !m.typingLock
		if m.typingLock {
			m.notice = "Typing scroll lock: ON - typing stays at current chat position"
		} else {
			m.notice = "Typing scroll lock: OFF - typing follows chat bottom"
		}
	case ActionQueueMode:
		m.queueMode = !m.queueMode
		if m.queueMode {
			m.notice = "Queue mode: messages wait until response completes"
		} else {
			m.notice = "Immediate mode: messages send next (no interrupt)"
		}
	case ActionAutoPoke:
		if m.poke == nil {
			return false, m, nil
		}
		m.poke.SetEnabled(!m.poke.Enabled())
		m.notice = map[bool]string{true: "Auto-poke ON", false: "Auto-poke OFF"}[m.poke.Enabled()]
	case ActionHistorySearch:
		m.history.Open(m.editor.Text, m.editor.Cursor)
	case ActionThinkingDisplay:
		m.thinking = m.thinking.Next()
		m.notice = "Thinking display: " + string(m.thinking)
	case ActionRetrievePending:
		return m.retrievePending()
	default:
		return false, m, nil
	}
	return true, m, nil
}

// retrievePending pulls staged messages back for editing (plan.md §6.4).
func (m *Model) retrievePending() (bool, tea.Model, tea.Cmd) {
	if m.editor.Text != "" {
		return false, m, nil
	}
	if len(m.pending) > 0 {
		var texts []string
		for _, p := range drainPendingForEdit(m.pending) {
			texts = append(texts, p.Text)
		}
		n := len(m.pending)
		m.pending = nil
		m.editor.Text = strings.Join(texts, "\n\n")
		m.editor.Cursor = len([]rune(m.editor.Text))
		m.notice = fmt.Sprintf("Retrieved %d pending message(s) for editing", n)
		return true, m, nil
	}
	if m.prompts != nil {
		if all := m.prompts.All(); len(all) > 0 {
			m.editor.Text = all[len(all)-1]
			m.editor.Cursor = len([]rune(m.editor.Text))
			return true, m, nil
		}
	}
	return false, m, nil
}

// userPromptRows returns the transcript line index of the first line of each
// user-prompt block, in document order.
//
// The indices share one coordinate system with contentHeight and scroll.Offset:
// they include the header chrome, the leading blank, and the conditional
// inter-block gaps, because they are read from transcriptLines' Owner
// provenance. Recomputing them from per-block line counts — a blanket +1 per
// block, no header — left them in a different frame from total/offset, so
// Prev/Next Prompt landed on the wrong line.
func (m *Model) userPromptRows() []int {
	tr := m.transcriptLines()
	var rows []int
	seen := make(map[int]bool)
	for i, owner := range tr.Owner {
		if owner < 0 || seen[owner] {
			continue
		}
		seen[owner] = true
		if m.blocks[owner].Kind == BlockUser {
			rows = append(rows, i)
		}
	}
	return rows
}

// jumpPrompt moves the view to the next or previous user prompt.
func (m *Model) jumpPrompt(dir int) {
	rows := m.userPromptRows()
	if len(rows) == 0 {
		return
	}

	viewport := m.transcriptHeight()
	total := m.contentHeight()
	current := total - viewport - m.scroll.Offset

	if dir < 0 {
		for i := len(rows) - 1; i >= 0; i-- {
			if rows[i] < current-1 {
				m.scroll.Offset = clamp(total-viewport-rows[i], 0, Max(total, viewport))
				m.scroll.Paused = m.scroll.Offset > 0
				return
			}
		}
		return
	}
	for _, r := range rows {
		if r > current+1 {
			m.scroll.Offset = clamp(total-viewport-r, 0, Max(total, viewport))
			m.scroll.Paused = m.scroll.Offset > 0
			return
		}
	}
	m.scroll.FollowBottom()
}

// noteHotkey shows the one-line explanation for a chord the user rarely uses.
func (m *Model) noteHotkey(key, desc string) {
	if !m.showHints || m.hotkeys == nil || desc == "" {
		return
	}
	if m.hotkeys.Record(key, time.Now()) {
		m.notice = m.renderer.RenderHotkeyHint(key, desc)
	}
}

// noteNearMiss explains an unhandled modified chord instead of swallowing it.
func (m *Model) noteNearMiss(key string) {
	if !m.showHints || m.keymap == nil || m.hotkeys == nil {
		return
	}
	if !IsModifiedChord(key) {
		return
	}
	if !m.hotkeys.AllowNearMiss(key, time.Now()) {
		return
	}
	nearest, found := m.keymap.NearestBinding(key)
	m.notice = m.renderer.RenderNearMiss(key, nearest, found)
}

// handleWheel applies the momentum scrolling of §4.1.
func (m *Model) handleWheel(msg tea.MouseWheelMsg) (tea.Model, tea.Cmd) {
	now := time.Now()
	switch msg.Button {
	case tea.MouseWheelUp:
		m.scroll.WheelUp(now, m.contentHeight(), m.transcriptHeight())
		// Scrolling up cancels the reveal instantly; it is a downward gesture.
		m.overscroll.Cancel()
	case tea.MouseWheelDown:
		atBottom := m.scroll.AtBottom()
		m.scroll.WheelDown(now)
		m.overscroll.Tick(now, atBottom)
	}
	return m, nil
}

// handleHistoryKey drives the Ctrl+R reverse search.
func (m *Model) handleHistoryKey(key string, msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key {
	case "esc", "ctrl+g", "ctrl+c":
		// Cancelling restores exactly the draft that was there before.
		draft, cursor := m.history.Close()
		m.editor.Text, m.editor.Cursor = draft, cursor
		return m, nil

	case "enter":
		m.history.Close()
		return m, nil

	case "up", "ctrl+r":
		m.history.Move(1)
		m.previewHistory()
		return m, nil

	case "down":
		m.history.Move(-1)
		m.previewHistory()
		return m, nil

	case "backspace":
		if r := []rune(m.history.Query); len(r) > 0 {
			m.history.Query = string(r[:len(r)-1])
			m.refreshHistory()
		}
		return m, nil
	}

	if txt := msg.Key().Text; txt != "" {
		m.history.Query += txt
		m.refreshHistory()
	}
	return m, nil
}

// refreshHistory re-runs the search and previews the top match.
func (m *Model) refreshHistory() {
	m.history.Selected = 0
	if m.prompts == nil || m.history.Query == "" {
		m.history.Matches = nil
		return
	}
	m.history.Matches = m.prompts.Search(m.history.Query, 50)
	m.previewHistory()
}

// previewHistory writes the selected match into the composer, which is what
// makes the search usable without a separate preview pane.
func (m *Model) previewHistory() {
	if got := m.history.Current(); got != "" {
		m.editor.Text = got
		m.editor.Cursor = len([]rune(got))
	}
}

// handleAskKey drives the ask tool's option picker.
func (m *Model) handleAskKey(key string) (tea.Model, tea.Cmd) {
	n := len(m.ask.Options)
	switch key {
	case "up", "ctrl+k":
		m.askCursor = MovePaletteSelection(m.askCursor, -1, n)
		return m, nil
	case "down", "ctrl+j":
		m.askCursor = MovePaletteSelection(m.askCursor, 1, n)
		return m, nil
	case " ", "space":
		if m.ask.Multi {
			m.askChosen[m.askCursor] = !m.askChosen[m.askCursor]
		}
		return m, nil
	case "enter":
		var labels []string
		if m.ask.Multi && len(m.askChosen) > 0 {
			for i, opt := range m.ask.Options {
				if m.askChosen[i] {
					labels = append(labels, opt.Label)
				}
			}
		} else {
			labels = []string{m.ask.Options[clamp(m.askCursor, 0, n-1)].Label}
		}
		m.pendingAsk.Answer(labels)
		m.ask = nil
		return m, nil
	case "esc", "ctrl+c":
		// Declining is an answer: the tool reports that nothing was chosen
		// rather than hanging.
		m.pendingAsk.Answer(nil)
		m.ask = nil
		return m, nil
	}
	return m, nil
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
			if sel.Provider != "" && sel.Provider != m.header.Provider {
				// Crossing providers rebuilds the live client. The picker lists
				// models from every configured provider, so a selection can name
				// one the session did not start on.
				if pc := m.providerConfig(sel.Provider); pc != nil {
					if p, err := pc.Build(); err == nil {
						m.agent.Provider = p
						m.header.Provider = sel.Provider
					}
				}
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

// modelsLoaded carries a fetched model list back into the update loop.
type modelsLoaded struct{ entries []ModelEntry }

// openPicker shows the picker, fetching the model list in the background.
//
// The fetch is a network call with a five-second timeout, and it used to happen
// inside Update — so opening the picker froze every frame until the provider
// answered. It opens immediately now with whatever is known, and fills in.
func (m *Model) openPicker() tea.Cmd {
	m.showPicker(m.models)
	if len(m.models) > 0 || m.modelsPending {
		return nil
	}
	m.picker.Entries = []ModelEntry{{
		Name: m.header.Model, Provider: m.header.Provider, Current: true, Detail: "loading…",
	}}

	// Captured here, on the update loop. The command runs on its own goroutine
	// and must not read Model fields — the user can switch models while the
	// fetch is in flight, and reading m.header from there is a race.
	prov, current, providerName := m.agent.Provider, m.header.Model, m.header.Provider
	provs := m.providers
	m.modelsPending = true
	return func() tea.Msg {
		if len(provs) > 0 {
			return modelsLoaded{entries: fetchAllModels(provs, current, providerName)}
		}
		return modelsLoaded{entries: fetchModels(prov, current, providerName)}
	}
}

// applyModels fills the picker in once the provider has answered.
func (m *Model) applyModels(msg modelsLoaded) {
	m.modelsPending = false
	m.models = msg.entries
	if m.pickerOpen {
		m.showPicker(m.models)
	}
}

func (m *Model) showPicker(entries []ModelEntry) {
	m.picker = PickerState{Entries: entries, Height: DefaultPickerHeight}
	for i, e := range m.picker.Entries {
		if e.Name == m.header.Model {
			m.picker.Selected = i
		}
	}
	m.pickerOpen = true
}

// providerConfig looks up a configured provider by name for the picker's
// cross-provider rebuild.
func (m *Model) providerConfig(name string) *config.ProviderConfig {
	for i := range m.providers {
		if m.providers[i].Name == name {
			return &m.providers[i]
		}
	}
	return nil
}

// fetchModels asks the provider for its model list. It takes everything it
// needs as arguments: it runs off the update loop, where Model is not safe to
// touch.
func fetchModels(prov provider.Provider, current, providerName string) []ModelEntry {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	infos, err := prov.Models(ctx)
	if err != nil || len(infos) == 0 {
		return []ModelEntry{{
			Name:     current,
			Provider: providerName,
			Current:  true,
			Default:  true,
		}}
	}
	out := make([]ModelEntry, 0, len(infos))
	for _, info := range infos {
		e := ModelEntry{
			Name:     info.Name,
			Provider: providerName,
			Current:  info.Name == current,
		}
		if info.Size != "" {
			e.Detail = info.Size
		}
		out = append(out, e)
	}
	return out
}

// fetchAllModels lists models from every configured provider concurrently, so
// the picker surfaces DeepSeek alongside Ollama without a config file. A
// provider that errors or returns nothing (no key, unreachable) contributes no
// rows rather than blanking the picker: a missing catalog is not a failure.
func fetchAllModels(provs []config.ProviderConfig, current, currentProvider string) []ModelEntry {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	type result struct {
		name     string
		infos    []provider.ModelInfo
		hasKey   bool
		needsKey bool
	}

	results := make(chan result, len(provs))
	for _, pc := range provs {
		pc := pc
		go func() {
			p, err := pc.Build()
			if err != nil {
				results <- result{name: pc.Name}
				return
			}
			infos, err := p.Models(ctx)
			results <- result{
				name: pc.Name, infos: infos,
				hasKey:   pc.APIKeyValue() != "",
				needsKey: pc.APIKeyEnv != "",
			}
		}()
	}

	var out []ModelEntry
	for range provs {
		r := <-results
		via := "local"
		if r.needsKey {
			if r.hasKey {
				via = "api-key"
			} else {
				via = "no key"
			}
		}
		for _, info := range r.infos {
			e := ModelEntry{
				Name:     info.Name,
				Provider: r.name,
				Via:      via,
				Current:  r.name == currentProvider && info.Name == current,
			}
			if info.Size != "" {
				e.Detail = info.Size
			}
			out = append(out, e)
		}
	}
	if len(out) == 0 {
		return []ModelEntry{{Name: current, Provider: currentProvider, Current: true, Default: true}}
	}
	return out
}

// runCommand executes a slash command. Unknown commands fall through to a
// notice rather than being sent to the model, which would waste a turn.
func (m *Model) runCommand(name string) (tea.Model, tea.Cmd) {
	return m.runCommandWithArg(name, "")
}

// splitCommand separates `/name rest` into its parts.
func splitCommand(input string) (name, arg string) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(input), "/")
	name, arg, _ = strings.Cut(trimmed, " ")
	return name, strings.TrimSpace(arg)
}

func (m *Model) runCommandWithArg(name, arg string) (tea.Model, tea.Cmd) {
	m.commandArg = arg
	m.editor.Clear()
	m.palette.Selected = 0

	switch name {
	case "quit":
		m.quitting = true
		return m, tea.Quit

	case "clear", "cls":
		m.blocks = nil
		m.dock.Reset()
		m.scroll.ClearSlack()
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
		return m, m.openPicker()

	case "plan":
		// /plan is a one-shot synthetic user turn, not a mode: no flag, no
		// permission gate. The plan→execution handoff is conversational
		// (plan.md §12.1).
		goal := strings.TrimSpace(m.commandArg)
		if goal == "" {
			goal = BarePlanGoal
		}
		prompt := fmt.Sprintf(PlanPrompt, goal)
		if m.processing {
			// Queued, not sent: the cancelled turn is still unwinding, and a
			// second Run against the same agent is refused (H2.3) — silently,
			// since a hidden prompt has nobody to report to. It starts when the
			// turn it interrupted actually ends.
			m.notice = "👉 Interrupting and planning..."
			m.queuedHidden = prompt
			m.interrupt(false)
			return m, nil
		}
		m.notice = "🧭 Planning " + truncateCells(goal, 40) + "... (plan-only; no edits)"
		m.submitHidden(prompt)
		return m, nil

	case "review":
		target := strings.TrimSpace(arg)
		if target == "" {
			target = "this branch"
		}
		m.submitHidden(fmt.Sprintf("Review %s. Read the diff or named path first. Report correctness issues, then clarity issues, then anything genuinely dangerous. No praise, no scope creep, and one line per finding with file:line.", target))
		m.notice = "🔎 Reviewing " + truncateCells(target, 40)
		return m, nil

	case "bugfix":
		if strings.TrimSpace(arg) == "" {
			m.notice = "usage: /bugfix <symptom>"
			return m, nil
		}
		m.submitHidden(fmt.Sprintf("Bug symptom: %s\n\nReproduce this first by writing the smallest failing test and explicitly report that it failed. Grep every caller to find the root cause. Fix the root cause, then run the test again and show it passing. Do not claim done without the fail-then-pass pair.", arg))
		m.notice = "🛠 Reproducing bug: " + truncateCells(arg, 40)
		return m, nil

	case "describe":
		target := strings.TrimSpace(arg)
		if target == "" {
			target = m.cwd
			if target == "" {
				target = "."
			}
		}
		m.submitHidden(fmt.Sprintf("Explain %s for someone who has never seen this codebase. Start with the structure and how the pieces fit together, then cover the important behavior. Do not narrate every line.", target))
		m.notice = "🧭 Describing " + truncateCells(target, 40)
		return m, nil

	case "poke":
		if m.poke == nil {
			m.notice = "auto-poke is not configured for this session"
			return m, nil
		}
		switch strings.TrimSpace(m.commandArg) {
		case "off":
			m.poke.SetEnabled(false)
			m.notice = "Auto-poke OFF"
		case "on":
			m.poke.SetEnabled(true)
			m.notice = "Auto-poke ON"
		default:
			if m.poke.Enabled() {
				m.notice = "Auto-poke is ON · /poke off to stop"
			} else {
				m.notice = "Auto-poke is OFF · /poke on to enable"
			}
		}
		return m, nil

	case "memory":
		return m, m.memoryCommand(strings.TrimSpace(m.commandArg))

	case "selfdev":
		return m, m.selfdevCommand()

	case "rebuild":
		return m, m.rebuildCommand()

	case "reload":
		return m, m.reloadCommand()

	case "overnight":
		return m, m.overnightCommand(strings.TrimSpace(m.commandArg))

	case "context":
		return m, m.contextCommand()

	case "stats":
		return m, m.statsCommand()

	case "login":
		return m, m.loginCommand(strings.TrimSpace(arg))

	case "productivity":
		return m, m.productivityCommand()

	case "advisor":
		return m, m.advisorCommand(strings.TrimSpace(m.commandArg))

	case "lsp":
		return m, m.lspCommand(strings.TrimSpace(m.commandArg))

	case "summon":
		return m, m.summonCommand(strings.TrimSpace(m.commandArg))

	case "agents", "swarm":
		return m, m.agentsCommand()

	case "todos", "todo":
		m.showTodoCard = !m.showTodoCard
		return m, nil

	case "terminal-setup":
		m.blocks = append(m.blocks, Block{Kind: BlockNotice, Text: terminalSetupText()})
		m.scroll.FollowBottom()
		return m, nil

	case "screenshot-mode":
		return m.runScreenshotMode()

	case "record":
		return m.runRecord()

	case "debug-visual":
		return m.runDebugVisual()

	case "smoothness":
		return m.runSmoothness(arg)

	case "onboarding-sim":
		for i, frame := range []int{0, 1, 2} {
			m.blocks = append(m.blocks, Block{
				Kind: BlockNotice,
				Text: fmt.Sprintf("— welcome screen %d —\n%s", i+1,
					strings.Join(plainRows(m.renderer.RenderWelcome(frame, nil)), "\n")),
			})
		}
		m.scroll.FollowBottom()
		return m, nil

	case "diff":
		m.diffMode = m.diffMode.Next()
		m.panelOpen = m.diffMode.UsesPanel() && !m.panel.Empty()
		m.applyWrapWidth()
		m.renderer.Graphics, m.renderer.ImagesOn = m.graphics, m.imagesOn
		m.drainDiagrams()
		m.notice = "Diff mode: " + m.diffMode.String()
		return m, nil

	case "alignment":
		m.centered = !m.centered
		m.renderer.Centered = m.centered
		m.applyWrapWidth()
		m.renderer.Graphics, m.renderer.ImagesOn = m.graphics, m.imagesOn
		m.drainDiagrams()
		if m.centered {
			m.notice = "Centered layout"
		} else {
			m.notice = "Left-aligned layout"
		}
		return m, nil

	case "thinking-display":
		switch strings.TrimSpace(arg) {
		case "off":
			m.thinking = ThinkingOff
		case "full":
			m.thinking = ThinkingFull
		case "current":
			m.thinking = ThinkingCurrent
		default:
			m.notice = "thinking-display is " + string(m.thinking) + " · off|full|current"
			return m, nil
		}
		m.notice = "thinking-display: " + string(m.thinking)
		return m, nil

	case "tool-call-details":
		m.renderer.ToolDetails = !m.renderer.ToolDetails
		for i := range m.blocks {
			if m.blocks[i].Kind == BlockTool {
				m.blocks[i].dropCache()
			}
		}
		if m.renderer.ToolDetails {
			m.notice = "tool call details: ON"
		} else {
			m.notice = "tool call details: OFF"
		}
		return m, nil

	case "theme", "color":
		m.blocks = append(m.blocks, Block{Kind: BlockNotice, Text: m.runTheme(arg)})
		m.scroll.FollowBottom()
		return m, nil

	case "resume", "graveyard", "sessions":
		m.openSessions()
		return m, nil

	case "save", "unsave":
		if m.store == nil {
			m.notice = "no session to pin"
			return m, nil
		}
		if err := session.Save(m.dataDir, m.store.Name, name == "save"); err != nil {
			m.notice = err.Error()
		} else if name == "save" {
			m.notice = "📌 Saved " + m.store.Name
		} else {
			m.notice = "Unpinned " + m.store.Name
		}
		return m, nil

	case "rename":
		if m.store == nil || arg == "" {
			m.notice = "usage: /rename <new-name>"
			return m, nil
		}
		if err := m.store.Rename(m.dataDir, arg); err != nil {
			m.notice = err.Error()
			return m, nil
		}
		m.header.SessionName = arg
		m.notice = "Renamed to " + arg
		return m, nil

	case "fork":
		if m.store == nil || arg == "" {
			m.notice = "usage: /fork <new-name>"
			return m, nil
		}
		if err := session.Fork(m.dataDir, m.store.Name, arg); err != nil {
			m.notice = err.Error()
		} else {
			m.notice = "Forked to " + arg
		}
		return m, nil

	case "checkpoint":
		if m.store == nil {
			m.notice = "no session to checkpoint"
			return m, nil
		}
		label := arg
		if label == "" {
			label = fmt.Sprintf("checkpoint-%d", m.promptCount)
		}
		if err := m.store.WriteCheckpoint(label); err != nil {
			m.notice = err.Error()
		} else {
			m.notice = "Checkpoint " + label
		}
		return m, nil

	case "rewind":
		return m.runRewind(arg)

	case "compact":
		return m.runCompact()

	case "fix":
		// A recovery prompt for when the model has stalled or lost the thread.
		m.submitHidden("You seem to have stalled or lost track of the task. " +
			"Re-read the most recent request and the current state of the files you were " +
			"changing, say in one line what you believe is left to do, and continue. " +
			"If something is blocking you, name it concretely instead of retrying.")
		m.notice = "🔄 Asked the model to recover"
		return m, nil

	case "btw":
		if arg == "" {
			m.notice = "usage: /btw <question>"
			return m, nil
		}
		// A side question answered without touching the main conversation, so
		// asking it costs nothing in context (plan.md §13).
		return m.runSideQuestion(arg)

	case "screenshot":
		return m.runScreenshot()

	case "version":
		m.notice = "evilcode " + m.header.Version
		return m, nil

	case "info":
		m.notice = m.header.SessionName + " · " + m.header.Model + " · " + m.header.Provider
		return m, nil

	case "help", "?", "commands", "keys", "hotkeys":
		if arg != "" {
			if c, ok := FindCommand(strings.TrimPrefix(arg, "/")); ok {
				detail := c.Long
				if detail == "" {
					detail = c.Help
				}
				m.blocks = append(m.blocks, Block{
					Kind: BlockNotice, Text: "/" + c.Name + "\n\n" + detail,
				})
			} else {
				m.notice = "no command named /" + strings.TrimPrefix(arg, "/")
			}
			m.scroll.FollowBottom()
			return m, nil
		}
		m.helpOpen = true
		m.helpScroll = 0
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
	case m.quickView != nil:
		// Closing something you are looking at is what Esc means in every other
		// overlay here, and a user who opened a quick view mid-turn meant "close
		// this", not "kill the turn" (§3.3). First rung, above interrupt.
		m.quickView = nil
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

// interrupt cancels the turn. disarmPoke distinguishes the two keys that get
// here: Esc means "stop", so the harness must not immediately re-poke, while
// Ctrl+C means "skip this" and leaves the cycle armed (plan.md §6.7).
func (m *Model) interrupt(disarmPoke bool) {
	if m.cancelTurn != nil {
		m.cancelTurn()
	}
	// Staged interleaves were aimed at the turn being cancelled.
	m.pending = nil

	if disarmPoke && m.poke != nil && m.poke.Enabled() {
		m.poke.Disarm()
		m.notice = "Interrupting... Auto-poke OFF"
		return
	}
	m.notice = "Interrupting..."
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
		Number: m.promptCount + 1,
	})
	m.promptCount++
	m.renumberPrompts()
	if !Deterministic() {
		// The flourish is decorative, so it is frozen in test mode along with
		// everything else animated (invariant 5).
		m.entryAnim = NewEntryAnimation(len(m.blocks) - 1)
	}
	if m.prompts != nil {
		_ = m.prompts.Add(text)
	}
	m.editor.Clear()
	m.scroll.FollowBottom()
	m.notice = ""

	// Attachments ride with this message and are cleared by taking them, so a
	// second prompt does not silently resend the first one's images.
	if images := m.TakeAttachments(); len(images) > 0 {
		if m.visionOK() {
			m.agent.Attach(images)
		} else {
			m.blocks = append(m.blocks, Block{Kind: BlockError, Text: fmt.Sprintf(
				"%s cannot see images. Set `vision = true` on the model in your config, "+
					"or switch with /model.", m.header.Model)})
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.cancelTurn = cancel
	go func() {
		defer cancel()
		// A turn already running takes the text as an interjection rather than
		// dropping it. The agent refuses a second concurrent turn (H2.3), and
		// the one caller that can reach it anyway is the queue flushing at turn
		// end alongside an unattended continuation.
		if err := m.agent.Run(ctx, text); errors.Is(err, agent.ErrBusy) {
			m.agent.Interject(agent.Interrupt{Source: agent.SourceUser, Text: text})
		}
	}()
}

// visionOK reports whether the active model accepts images.
//
// Configured rather than inferred from the model name: a guess that says no to a
// capable model is invisible, and a guess that says yes to a text-only one fails
// deep in the provider with a message that explains nothing.
func (m *Model) visionOK() bool { return m.vision }

// drainPendingForEdit orders staged messages for retrieval: soft-interrupts
// first, then interleaves, then queued, so the text comes back in the order it
// would have reached the model (plan.md §6.4).
func drainPendingForEdit(pending []PendingMessage) []PendingMessage {
	order := []PendingKind{PendingSent, PendingInterleave, PendingQueued}
	var out []PendingMessage
	for _, kind := range order {
		for _, p := range pending {
			if p.Kind == kind {
				out = append(out, p)
			}
		}
	}
	return out
}

// submitHidden starts a turn whose prompt is harness-authored, so it drives the
// model without appearing as something the user typed.
func (m *Model) submitHidden(text string) {
	if m.processing || (m.agent != nil && m.agent.Running()) {
		// Hidden turns have no user watching their error path. Queue them behind
		// the active turn instead of starting a second Agent.Run and silently
		// losing the prompt to ErrBusy.
		m.queuedHidden = text
		m.interrupt(false)
		return
	}
	m.hiddenPrompt = text
	m.editor.Clear()
	m.scroll.FollowBottom()

	ctx, cancel := context.WithCancel(context.Background())
	m.cancelTurn = cancel
	go func() {
		defer cancel()
		_ = m.agent.Run(ctx, text)
	}()
}

// renumberPrompts recomputes each user block's distance from the newest, which
// is what the rainbow ramp is indexed by.
// renumberPrompts recomputes each user block's distance from the newest, which
// is what the rainbow ramp is indexed by (§7.7). It does not touch Number: a
// prompt's ordinal is assigned once and never changes, because renumbering the
// history every turn is how prompt 1 ended up labelled with the highest number.
func (m *Model) renumberPrompts() {
	seen := 0
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].Kind == BlockUser {
			m.blocks[i].Decay = seen
			m.blocks[i].dropCache()
			seen++
		}
	}
}

func (m *Model) transcriptHeight() int {
	res := m.stack().Resolve()
	return res.Transcript
}

func (m *Model) contentHeight() int {
	return len(m.transcriptLines().Lines)
}

func (m *Model) invalidateTranscriptCache() {
	m.transcriptCache = Rows{}
	m.transcriptCacheWidth = 0
	m.transcriptCacheValid = false
}

// transcriptLines renders every block to lines plus the provenance of each line
// (§1.2). Owner[i] is the index into m.blocks that rendered Lines[i], or -1 for
// chrome (the header, inter-block gaps, welcome art, todo card). The
// per-block render cache is untouched: provenance is recorded around the cache,
// not inside it, so a cache hit still costs nothing.
func (m *Model) transcriptLines() Rows {
	if m.renderer.DiffMode != m.diffMode {
		m.renderer.DiffMode = m.diffMode
		for i := range m.blocks {
			if m.blocks[i].Diff != "" {
				m.blocks[i].dropCache()
			}
		}
		m.invalidateTranscriptCache()
	}
	animT, animating := m.entryAnim.Progress(time.Now())
	cacheable := len(m.blocks) > 0 && !animating
	for i := range m.blocks {
		if m.blocks[i].Streaming {
			cacheable = false
			break
		}
	}
	if cacheable && m.transcriptCacheValid && m.transcriptCacheWidth == m.renderer.Width {
		return m.transcriptCache
	}

	if len(m.blocks) == 0 {
		welcome := m.renderer.RenderWelcome(m.welcomeIndex(), m.idleArt())
		owner := make([]int, len(welcome))
		for i := range owner {
			owner[i] = -1
		}
		return Rows{Lines: welcome, Owner: owner}
	}
	var out []string
	var owner []int
	addChrome := func(lines []string) {
		out = append(out, lines...)
		for range lines {
			owner = append(owner, -1)
		}
	}
	addOwned := func(idx int, lines []string) {
		out = append(out, lines...)
		for range lines {
			owner = append(owner, idx)
		}
	}

	addChrome(m.renderer.RenderHeader(m.header))
	addChrome([]string{""})
	for i := range m.blocks {
		lines := m.renderer.Lines(&m.blocks[i])
		if animating && i == m.entryAnim.Block {
			lines = m.renderer.animateEntry(lines, animT)
		}
		addOwned(i, lines)

		// Blank lines separate *ideas*, not every block. A batch of tool calls
		// is one idea, so consecutive tool rows stay packed; a gap between each
		// one triples the height of a five-call batch and reads as five
		// unrelated events.
		if len(lines) > 0 && needsGapAfter(m.blocks, i) {
			addChrome([]string{""})
		}
	}

	// The inline todo card is a single card pinned to the transcript tail, so
	// re-toggling moves it to the bottom rather than leaving copies behind
	// (plan.md §12.5).
	if m.showTodoCard && m.todos != nil {
		addChrome(m.renderer.RenderTodoCard(TodoCardState{
			Items: m.todos.Items(),
			Plan:  m.todos.Plan(),
			Goals: m.todos.Goals(),
		}))
		addChrome([]string{""})
	}

	// The one invariant every consumer depends on: a mismatch here misplaces
	// every widget and every click.
	if len(out) != len(owner) {
		panic(fmt.Sprintf("transcriptLines: len(Lines)=%d != len(Owner)=%d", len(out), len(owner)))
	}
	rows := Rows{Lines: out, Owner: owner}
	if cacheable && (!m.transcriptCacheValid || m.transcriptCacheWidth == m.renderer.Width) {
		m.transcriptCache = rows
		m.transcriptCacheWidth = m.renderer.Width
		m.transcriptCacheValid = true
	}
	return rows
}

// applyWrapWidth sets the renderer's wrap width, reserving a column for the
// scrollbar when the previous frame showed one (plan.md §3.5).
func (m *Model) applyWrapWidth() {
	width, _ := ContentWidth(m.chatWidth(), m.centered)
	if m.scrollbarOn {
		width--
	}
	if width != m.renderer.Width {
		m.renderer.SetWidth(max(width, 1))
		m.invalidateTranscriptCache()
	}
}

// stack builds the vertical layout request.
func (m *Model) stack() Stack {
	return m.stackFor(len(m.transcriptLines().Lines))
}

func (m *Model) stackFor(contentHeight int) Stack {
	s := Stack{Available: m.height, ContentHeight: contentHeight}
	s.Heights[SlotStatus] = 1
	if m.notice != "" {
		s.Heights[SlotNotice] = 1
	}
	s.Heights[SlotQueued] = min(len(m.pending), MaxPendingRows)

	if m.ask != nil {
		s.Heights[SlotPicker] = len(m.renderer.RenderAsk(m.ask, m.askCursor, m.askChosen))
		s.Heights[SlotPickerGap] = 1
	}
	if m.pickerOpen {
		s.Heights[SlotPicker] += len(m.renderer.RenderPicker(m.picker))
		s.Heights[SlotPickerGap] = 1
	}
	if m.loginPickerOpen {
		s.Heights[SlotPicker] += len(m.renderer.RenderLoginPicker(m.loginPicker))
		s.Heights[SlotPickerGap] = 1
	}

	composer := m.renderer.RenderComposer(m.composerState())
	s.Heights[SlotComposer] = len(composer)
	return s
}

// contentHeightAtWidth probes scrollbar feedback without allocating a second
// full Rows value. The old path built every line and every Owner slice again
// just to count them, which made a long transcript increasingly expensive on
// every 80ms tick.
func (m *Model) contentHeightAtWidth(width int) int {
	old := m.renderer.Width
	if old != width {
		m.renderer.SetWidth(max(width, 1))
	}
	height := m.transcriptHeightOnly()
	if old != width {
		m.renderer.SetWidth(old)
	}
	return height
}

func (m *Model) transcriptHeightOnly() int {
	if len(m.blocks) == 0 {
		return len(m.renderer.RenderWelcome(m.welcomeIndex(), m.idleArt()))
	}
	height := len(m.renderer.RenderHeader(m.header)) + 1
	for i := range m.blocks {
		lines := m.renderer.Lines(&m.blocks[i])
		height += len(lines)
		if len(lines) > 0 && needsGapAfter(m.blocks, i) {
			height++
		}
	}
	if m.showTodoCard && m.todos != nil {
		height += len(m.renderer.RenderTodoCard(TodoCardState{
			Items: m.todos.Items(), Plan: m.todos.Plan(), Goals: m.todos.Goals(),
		})) + 1
	}
	return height
}

func (m *Model) composerState() ComposerState {
	return ComposerState{
		Text:         m.editor.Text,
		Cursor:       m.editor.Cursor,
		PromptNumber: m.promptCount,
		Model:        m.header.Model,
		CtxUsed:      m.ctxUsed,
		CtxMax:       m.contextMax(),
		Session:      m.header.SessionName,
		Processing:   m.processing,
		QueueMode:    m.queueMode,
		PaletteOpen:  m.paletteOpen(),
		Masked:       m.loginMode,
	}
}

func (m *Model) welcomeIndex() int {
	if !m.welcomeFocus || len(SuggestionChips) == 0 {
		return -1
	}
	return m.welcomeChip % len(SuggestionChips)
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
	m.renderer.Graphics, m.renderer.ImagesOn = m.graphics, m.imagesOn
	m.drainDiagrams()

	if m.sessionsOpen {
		v := tea.NewView(strings.Join(
			m.renderer.RenderSessionPicker(m.sessions, m.width, m.height), "\n"))
		v.AltScreen = true
		return v
	}

	if m.helpOpen {
		v := tea.NewView(strings.Join(
			m.renderer.RenderHelp(m.helpScroll, m.width, m.height), "\n"))
		v.AltScreen = true
		return v
	}

	tr := m.transcriptLines()
	res := m.stackFor(len(tr.Lines)).Resolve()
	content := tr.Lines
	owner := tr.Owner

	// A shrink becomes slack rather than a downward haul: when a thinking trace
	// collapses, the gap it leaves is kept below the text and spent by whatever
	// arrives next, so the view only ever moves one way (invariant 4).
	m.scroll.Observe(len(content), res.Transcript)

	// Window the transcript to its resolved height, honoring the scroll offset.
	// The window is measured against the content *plus* any slack, which is what
	// holds the text where it was instead of pulling it back down.
	// Clamped to the content: slack extends the *window*, not the content, so
	// without this the start can run past the last line and the slice panics.
	start := clamp(len(content)+m.scroll.Slack()-res.Transcript-m.scroll.Offset,
		0, len(content))
	end := min(start+res.Transcript, len(content))
	if end < start {
		end = start
	}
	visible := content[start:end]

	// The slack itself is blank rows below the text. They are real rows, so the
	// dock and the scrollbar see a stable region rather than one that shrinks
	// and regrows under them.
	if gap := min(m.scroll.Slack(), res.Transcript-len(visible)); gap > 0 {
		visible = append(append([]string(nil), visible...), make([]string, gap)...)
	}

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
	// Measure the alternate wrap width too. Passing the current height for both
	// layouts made the helper's hysteresis meaningless and left the bar stuck on
	// or off after a line wrapped across its reserved column.
	withoutBar, withBar := len(content), len(content)
	width := m.renderer.Width
	if m.scrollbarOn {
		withoutBar = m.contentHeightAtWidth(width + 1)
	} else {
		withBar = m.contentHeightAtWidth(width - 1)
	}
	m.scrollbarOn = ScrollbarVisible(m.scrollbarOn, withBar, withoutBar, res.Transcript)

	// Widgets dock here — before the scrollbar, the side panel and the centering
	// inset are painted into these rows. Docking last meant measuring rows that
	// were already full width and concluding there was nowhere to go, which is
	// why the boxes vanished (plan.md §8.3).
	rows = m.dockWidgets(rows, content, res.Transcript, start, owner)

	if m.scrollbarOn && res.Transcript > 0 {
		bar := m.renderer.RenderScrollbar(m.scroll.Offset, len(content), res.Transcript, !m.scroll.Paused)
		// The bar owns the last column of the *chat* area, not of the terminal.
		// With the side pane open those are different columns, and painting at
		// the terminal edge made every row wider than the chat, which shoved the
		// pane off screen and wrapped the frame.
		_, lead := ContentWidth(m.width, m.centered)
		avail := max(m.chatWidth()-lead-1, 0)
		for i := 0; i < res.Transcript && i < len(bar) && i < len(rows); i++ {
			// Truncate, not just pad: a row wider than its column — an untrimmed
			// tool row, a docked widget — used to push the bar past the edge
			// instead of being cut off at it.
			row := truncateCells(rows[i], avail)
			pad := max(avail-lipgloss.Width(row), 0)
			rows[i] = row + strings.Repeat(" ", pad) + bar[i]
		}
	}

	rows = append(rows, m.renderer.RenderPending(m.pending)...)

	m.status.Animate = !Deterministic()
	if m.status.Phase == PhaseIdle && m.notice == "" {
		m.status.Tip = TipAt(time.Since(m.started), m.width)
	}
	m.status.Queued = len(m.pending)
	rows = append(rows, m.renderer.RenderStatus(m.status))

	// The swarm strip is the fallback for when the dock widget cannot find a
	// slot. Whether the widget is up is only known after docking, so the strip
	// reads last frame's answer through the hysteresis — which is the point:
	// deciding from the current frame would make the two flicker against each
	// other every time a wide line slid under the widget.
	if m.swarm != nil {
		if strip := m.renderer.RenderSwarmStrip(m.swarm,
			time.Since(m.started)); strip != "" && m.swarm.StripVisible() {
			rows = append(rows, strip)
		}
	}

	if m.notice != "" {
		// Sanitized at the draw rather than at each of the hundred-odd
		// assignments: a notice is usually ours, but some carry text straight
		// from elsewhere — a renderer's stderr, a provider's error, a tool's
		// message — and the ones that do are not marked.
		rows = append(rows, m.renderer.style(theme.RoleSystem).
			Render(core.SanitizeTerminal(m.notice)))
	}

	if m.ask != nil {
		rows = append(rows, m.renderer.RenderAsk(m.ask, m.askCursor, m.askChosen)...)
		rows = append(rows, "")
	}
	if m.pickerOpen {
		rows = append(rows, m.renderer.RenderPicker(m.picker)...)
		rows = append(rows, "")
	}
	if m.loginPickerOpen {
		rows = append(rows, m.renderer.RenderLoginPicker(m.loginPicker)...)
		rows = append(rows, "")
	}

	rows = append(rows, m.renderer.RenderComposer(m.composerState())...)

	// The elastic facts line lives below the composer and owns the same facts
	// as the fact stack, so only one of them shows at a time (§4.4, §8.6).
	now := time.Now()
	if m.overscroll.Visible(now, m.scroll.AtBottom()) {
		rows = append(rows, m.renderer.RenderOverscrollFacts(
			m.factStack(), m.overscroll.Remaining(now)))
	}

	// Carve the side pane off the right before anything else measures width
	// (plan.md §3.1).
	rows = m.attachSidePanel(rows, res.Transcript)

	// Centering is literal left padding rather than per-line centering, which
	// keeps copy and column math sane (plan.md Phase 2). It is applied before
	// docking so widgets measure the real frame, not the pre-padded one.
	// ContentWidth already accounts for the left gutter, so pad is the whole
	// left offset — adding Inset again would double-count it.
	_, pad := ContentWidth(m.width, m.centered)
	inset := strings.Repeat(" ", pad)
	for i, row := range rows {
		rows[i] = inset + row
	}

	// Now that docking has run, the strip knows whether the widget got a slot.
	// Feeding it here — after the fact, through the hysteresis — is what stops
	// the two from flickering against each other as lines slide underneath.
	if m.swarm != nil {
		m.swarm.ObserveDock(m.swarmDocked, time.Now())
	}

	// Overlays are spliced over the finished frame rather than added to it, so
	// opening one reserves no layout height and the transcript never moves
	// (plan.md invariant 3).
	rows = m.overlayPalette(rows, inset)
	rows = m.overlayHistory(rows, inset)

	rows = m.debugOverlay(rows, res.Transcript)

	// The anchor recorder runs on the finished frame, so what it measures is
	// exactly what the reader saw (plan.md §13).
	// Only the transcript is measured. The composer and status line move down
	// as a packed layout grows, which is the layout working, not the screen
	// jumping.
	m.observeSmoothness(rows[:min(res.Transcript, len(rows))],
		max(len(content)-res.Transcript-m.scroll.Offset, 0))

	frame := strings.Join(rows, "\n")
	// Image escape sequences ride after the frame, never inside it: they carry
	// no printable cells, so a row holding one would measure wrong everywhere
	// the layout looks at widths. The terminal draws them at the cursor, which
	// is wherever the frame left it.
	if m.pendingImages != "" {
		frame += m.pendingImages
		m.pendingImages = ""
	}
	m.lastFrame = frame
	m.autoCapture(frame)

	v := tea.NewView(frame)
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

// overlayHistory splices the reverse search over the finished frame. It is
// drawn after the palette so it wins when both could apply (plan.md §5.2).
func (m *Model) overlayHistory(rows []string, inset string) []string {
	return spliceOverlay(rows, indent(m.renderer.RenderHistorySearch(&m.history), inset),
		m.height, len(m.renderer.RenderComposer(m.composerState())))
}

// indent prefixes overlay rows so they line up with the padded frame.
func indent(rows []string, prefix string) []string {
	if prefix == "" {
		return rows
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = prefix + r
	}
	return out
}

// overlayPalette splices the command list over the finished frame.
func (m *Model) overlayPalette(rows []string, inset string) []string {
	if !m.paletteOpen() {
		return rows
	}
	// The query is derived from the input rather than mirrored into state, so
	// the two can never drift apart.
	state := m.palette
	state.Query = strings.TrimPrefix(m.editor.Text, "/")
	overlay := indent(m.renderer.RenderPalette(state, VisibleCommands()), inset)
	return spliceOverlay(rows, overlay, m.height,
		len(m.renderer.RenderComposer(m.composerState())))
}

// spliceOverlay draws overlay rows over existing ones, covering them rather
// than displacing them — which is what lets an overlay reserve zero layout
// height and never move the transcript (plan.md invariant 3).
//
// Placement prefers the rows directly below the composer. When the frame ends
// before that — which it does whenever the layout is packed — the overlay flips
// above the composer and covers the transcript tail instead.
func spliceOverlay(rows, overlay []string, screenHeight, composerRows int) []string {
	if len(overlay) == 0 {
		return rows
	}

	composerEnd := len(rows)
	composerStart := max(composerEnd-composerRows, 0)

	var at int
	if screenHeight-composerEnd >= len(overlay) {
		// Room underneath: grow into rows the terminal already shows blank.
		at = composerEnd
		for len(rows) < at+len(overlay) {
			rows = append(rows, "")
		}
	} else {
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
		// Assignment rather than overlay-onto: a shorter overlay row must not
		// leave the tail of whatever was underneath visible.
		rows[idx] = line
	}
	return rows
}

// needsGapAfter reports whether a blank line belongs after block i.
//
// The rule is that a gap marks a change of subject. Tool activity — the call
// row, an error it produced, the todo delta under it — is one subject, so those
// stay packed together; prose, prompts, and cards each get their own breathing
// room.
func needsGapAfter(blocks []Block, i int) bool {
	if i+1 >= len(blocks) {
		return false
	}
	return !sameSubject(blocks[i].Kind, blocks[i+1].Kind)
}

// sameSubject reports whether two adjacent block kinds are part of one thought.
func sameSubject(a, b BlockKind) bool {
	toolish := func(k BlockKind) bool {
		switch k {
		case BlockTool, BlockError, BlockTodoDelta:
			return true
		}
		return false
	}
	return toolish(a) && toolish(b)
}

// sidePaneOpen reports whether the side pane is showing anything — the
// persistent /diff panel, or the transient quick view on top of it. Both layout
// splits and attachSidePanel route through here so a quick view opens the pane
// without touching m.panelOpen (§3.2).
func (m *Model) sidePaneOpen() bool {
	return m.panelOpen || m.quickView != nil
}

// chatWidth is the width left for the chat column once the side pane is carved
// off (plan.md §3.1).
func (m *Model) chatWidth() int {
	chat, _ := Horizontal{
		Width: m.width, SidePaneRatio: m.panelRatio, SidePaneOpen: m.sidePaneOpen(),
	}.Split()
	return chat
}

// attachSidePanel paints the pane to the right of the transcript rows.
func (m *Model) attachSidePanel(rows []string, transcriptRows int) []string {
	chat, side := Horizontal{
		Width: m.width, SidePaneRatio: m.panelRatio, SidePaneOpen: m.sidePaneOpen(),
	}.Split()
	if side == 0 {
		return rows
	}

	// The panel gets the whole terminal, not the height of the chat beside it.
	// A short conversation next to a long file used to cut the panel off at the
	// composer — the top of the diff rendered and everything below it, divider
	// included, silently vanished.
	height := max(len(rows), transcriptRows)
	if m.height > height {
		height = m.height
	}
	// The quick view sits on top of the persistent panel: when it is open, it
	// is what the pane draws, and the /diff state underneath is untouched (§3.2).
	content, mode := m.panel, m.diffMode
	if m.quickView != nil {
		content, mode = *m.quickView, DiffInline
	}
	panel := m.renderer.RenderSidePanel(content, mode, side, height, m.quickView != nil)

	// Grow the frame so every panel row has something to attach to. Blank chat
	// rows below the composer are empty screen either way; without them the
	// panel is truncated to whatever the conversation happens to be tall.
	for len(rows) < len(panel) && len(rows) < m.height {
		rows = append(rows, "")
	}

	// Cut the chat off at its column. A row wider than the chat — a long tool
	// row, a wide code line — used to leak straight across the divider and push
	// the panel off screen, which wrapped the frame and left half of it blank.
	for i := 0; i < len(rows) && i < len(panel); i++ {
		row := truncateCells(rows[i], chat)
		pad := max(chat-lipgloss.Width(row), 0)
		rows[i] = row + strings.Repeat(" ", pad) + panel[i]
	}
	return rows
}

// idleArt renders the welcome art, sized to the space actually available so it
// never pushes the composer off a short terminal.
//
// When decorative animation is gated off the art still draws — frozen at frame
// zero. What SSH and deterministic mode are protecting against is the repaint
// cost of animating, not the picture itself; omitting it entirely would remove
// the welcome screen rather than quiet it (plan.md §10).
func (m *Model) idleArt() []string {
	// A demo-only escape hatch, distinct from deterministic mode's "freeze,
	// don't omit" guarantee above: a screen recording wants a clean, tight
	// welcome band, not the full art.
	if os.Getenv("EVILCODE_DEMO_NO_LOGO") != "" {
		return nil
	}
	rows := min(ArtHeight, max(m.height-12, 0))
	if rows < 6 || m.width < 40 {
		return nil
	}
	width, _ := ContentWidth(m.width, m.centered)

	elapsed := 0.0
	if m.animate() {
		elapsed = time.Since(m.started).Seconds()
	}
	return RenderArt(SamplerFor(m.artVariant), min(width, 72), rows, elapsed, m.animate())
}

// animate reports whether decorative animation is on. It is gated by
// deterministic mode (so golden frames stay reproducible), by SSH (a remote
// terminal pays for every repaint), and by config (plan.md §10).
func (m *Model) animate() bool {
	return m.decorate && !Deterministic()
}

// factStack gathers the always-true, never-urgent facts (§8.6).
func (m *Model) factStack() FactStack {
	return FactStack{
		Provider: m.header.Provider,
		Auth:     m.header.AuthKind,
		Model:    m.header.Model,
		Cwd:      m.header.Cwd,
		Branch:   m.header.Branch,
		Used:     m.ctxUsed,
		Total:    m.contextMax(),
	}
}

// The salience knobs. Salience decides one thing only: which widget moves in
// when the dock is empty and a pocket opens. It has no say over a widget that is
// already resident — that one stays until it scrolls away (see Dock). Both are
// feel values and the first set will be wrong.
const (
	// WidgetAirtimeCap bounds the pressure a widget accrues while it is not the
	// one on screen. It is what spreads spawns across the kinds instead of
	// handing every one to whichever ranks highest, and it only ever applies at
	// the moment of spawning.
	WidgetAirtimeCap = 12

	// WidgetChangeBoost favours a widget whose rendered content just changed, so
	// the one with news is the one that moves in.
	WidgetChangeBoost = 8
)

// activeWidgets builds the widget list, dropping empty boxes and ranking the
// survivors by urgency plus airtime. A static kind sort made ModelInfo win the
// only slot forever, so the list itself carries the decision into Dock.Layout.
func (m *Model) activeWidgets() []Widget {
	if m.widgetLastShown == nil {
		m.widgetLastShown = map[WidgetKind]uint64{}
	}
	if m.widgetLastChanged == nil {
		m.widgetLastChanged = map[WidgetKind]uint64{}
	}
	if m.widgetHashes == nil {
		m.widgetHashes = map[WidgetKind]uint64{}
	}
	var out []Widget
	add := func(w Widget) {
		if len(w.Lines) > 0 {
			out = append(out, w)
		}
	}

	if m.todos != nil {
		add(m.renderer.TodosWidget(m.todos.Items(), m.todos.Goals(), 4))
	}
	if m.ctxUsed > 0 && m.contextMax() > 0 {
		add(m.renderer.ContextWidget(m.ctxUsed, m.contextMax()))
	}
	// The KV-cache widget only means something for providers that report
	// prompt-cache tokens — DeepSeek today. Ollama (local or cloud) has no
	// such concept, so even if counts somehow accumulated the widget stays
	// away rather than advertising a cache that does not exist.
	if m.cacheProviderActive() && (m.cacheRead > 0 || m.cacheWrite > 0) {
		add(m.renderer.KvCacheWidget(m.cacheRead, m.cacheWrite))
	}
	add(m.renderer.ModelInfoWidget(m.header, 1))
	if m.bg != nil {
		var tasks []BackgroundTask
		for _, t := range m.bg.Tasks() {
			done, failed, _ := t.Snapshot()
			tasks = append(tasks, BackgroundTask{Label: t.Label, Done: done, Err: failed})
		}
		add(m.renderer.BackgroundTasksWidget(tasks, int(time.Since(m.started)/SpinnerInterval)))
	}
	if m.memory != nil {
		act := m.memory.Activity()
		// Wall-clock text is frozen in deterministic mode, or the golden churns
		// once a second (invariant 5).
		elapsed := 0
		if !Deterministic() && !act.Since.IsZero() {
			elapsed = int(time.Since(act.Since).Seconds())
		}
		add(m.renderer.MemoryActivityWidget(act, elapsed))
	}
	if m.swarm != nil {
		add(m.renderer.SwarmStatusWidget(m.swarm, time.Since(m.started)))
	}
	// GitStatusWidget (widgets.go) is still not added: it needs staged/unstaged
	// counts, and nothing polls `git status` today. Adding a poller is a new
	// data path rather than part of making widgets stay put, and a widget
	// showing 0/0/0 would be worse than one that is absent.
	if tip := TipAt(time.Since(m.started), m.width); tip != "" {
		add(m.renderer.TipsWidget(tip))
	}
	if !Deterministic() {
		m.widgetClock++
	}
	for i := range out {
		w := &out[i]
		hash := frameHash(strings.Join(w.Lines, "\n"))
		if !Deterministic() {
			if old, ok := m.widgetHashes[w.Kind]; ok && old != hash {
				m.widgetLastChanged[w.Kind] = m.widgetClock
			}
		}
		m.widgetHashes[w.Kind] = hash

		age := m.widgetClock
		if shown, ok := m.widgetLastShown[w.Kind]; ok && age >= shown {
			age -= shown
		}
		airtime := minFloat(float64(age)/12, WidgetAirtimeCap)
		change := 0.0
		if !Deterministic() {
			if changed, ok := m.widgetLastChanged[w.Kind]; ok && m.widgetClock >= changed {
				change = maxFloat(WidgetChangeBoost-float64(m.widgetClock-changed)/6, 0)
			}
		}
		// No incumbent bonus and no dwell: the dock does not consult this
		// ranking for a widget that is already resident, so there is nothing to
		// defend a sitting widget against. Scoring one was how a timer ended up
		// deciding what was on screen.
		w.Salience = m.widgetUrgency(*w) + airtime + change
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Salience != out[j].Salience {
			return out[i].Salience > out[j].Salience
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func (m *Model) widgetUrgency(w Widget) float64 {
	switch w.Kind {
	case WidgetContextUsage:
		total := m.contextMax()
		if total <= 0 {
			return 0
		}
		ratio := float64(m.ctxUsed) / float64(total)
		switch {
		case ratio >= .9:
			return 120
		case ratio >= .8:
			return 30
		case ratio >= .65:
			return 8
		}
	case WidgetBackgroundTasks:
		if m.bg != nil {
			for _, t := range m.bg.Tasks() {
				done, failed, _ := t.Snapshot()
				if failed {
					return 24
				}
				if !done {
					return 12
				}
			}
		}
	case WidgetTodos:
		return 3
	case WidgetKvCache:
		// Informational, not urgent, but it only exists once DeepSeek has
		// reported cache tokens — so when it is present it has real news.
		return 4
	case WidgetSwarmStatus:
		if m.swarm != nil && len(m.swarm.Live()) > 0 {
			return 12
		}
	case WidgetMemoryActivity:
		return 2
	}
	return 0
}

// contextMax is the model's window, falling back to a common default so the
// meter is useful before the provider has reported one.
func (m *Model) contextMax() int {
	if m.agent != nil && m.agent.NumCtx > 0 {
		return m.agent.NumCtx
	}
	return 200_000
}

// cacheProviderActive reports whether the active provider is one that reports
// KV prompt-cache token counts. DeepSeek does (prompt_cache_hit_tokens /
// prompt_cache_miss_tokens); Ollama — local or cloud — does not, and a cache
// widget there would advertise a cache that does not exist.
func (m *Model) cacheProviderActive() bool {
	return m.header.Provider == "deepseek"
}

// dockWidgets paints widgets into the transcript's blank margin.
//
// Widgets are suppressed entirely while the welcome art is showing: an empty
// screen decorated with status boxes is busier than the thing it decorates.
func (m *Model) dockWidgets(rows, content []string, transcriptRows, scrollTop int, owner []int) []string {
	if !m.widgetsOn || len(m.blocks) == 0 || transcriptRows <= 0 {
		m.placements = nil
		return rows
	}
	blocks := m.blocks
	kindOf := func(idx int) BlockKind {
		if idx < 0 || idx >= len(blocks) {
			return BlockAssistant // treat unknown as undockable, never place on it
		}
		return blocks[idx].Kind
	}

	widgets := m.activeWidgets()
	render := make(map[WidgetKind]Widget, len(widgets)+len(m.dock.residents))
	for _, w := range widgets {
		render[w.Kind] = w
	}
	for _, kind := range m.dock.ResidentKinds() {
		if _, ok := render[kind]; !ok {
			render[kind] = m.renderer.EmptyWidget(kind)
		}
	}
	candidates := widgets
	if m.showTodoCard {
		candidates = make([]Widget, 0, len(widgets))
		for _, w := range widgets {
			if w.Kind != WidgetTodos {
				candidates = append(candidates, w)
			}
		}
	}

	// Neither the scrollbar nor the centering inset has been painted yet, but
	// both will be, so both are reserved here. Measuring one width and painting
	// against another is what let the painter drop boxes the dock believed it
	// had placed — and leaving out the inset cost the box its right corner.
	usable := m.chatWidth() - Inset(m.centered)
	if m.scrollbarOn {
		usable -= ScrollbarReserve
	}

	// The streaming tail is the live block whose rows are still changing — the
	// boundary of the settled region (§2.3). At most one block is Streaming at a
	// time (the newest), and -1 means the turn is finished.
	streamingBlock := -1
	for i := range blocks {
		if blocks[i].Streaming {
			streamingBlock = i
		}
	}

	placements := m.dock.Layout(render, candidates, content, owner, kindOf,
		streamingBlock, usable, scrollTop, transcriptRows)

	m.swarmDocked = false
	for _, p := range placements {
		if p.Kind == WidgetSwarmStatus {
			m.swarmDocked = true
		}
		paintWidget(rows, m.renderer.RenderWidget(render[p.Kind]),
			p.Row, p.Col, min(transcriptRows, len(rows)))
	}
	// Airtime is pressure accrued while a widget is *not* the one on screen, so
	// the resident's resets every frame it is up and it starts from zero when it
	// finally scrolls away.
	for _, p := range placements {
		m.widgetLastShown[p.Kind] = m.widgetClock
	}
	m.placements = placements
	return rows
}

// paintWidget draws a widget's lines into the frame at one column.
//
// One column for the whole box, taken from the widest row it covers. Padding
// each line against its own row is what tore a widget into fragments: a line
// over prose started two cells past that prose while its blank neighbours
// started at col, so one box came out at three different columns.
//
// A box that no longer fits is dropped rather than drawn past the right edge,
// where the terminal would wrap it and push the whole frame down.
// paintWidget writes a widget's box into the frame at a fixed column.
//
// The box overlays whatever is underneath: each row is cut at the widget's
// column and the box written there. glamour pads every paragraph out to the
// wrap width, so a painter that appended produced rows far wider than the
// terminal, which then wrapped and tore the frame apart — the symptom that
// looked like "the widget vanished".
//
// It no longer recomputes the column or refuses to draw. Layout owns that
// decision now, and a painter that silently declined left the dock believing it
// had placed a widget that was never on screen and would never be re-homed.
func paintWidget(rows, lines []string, top, col, limit int) {
	for i, line := range lines {
		row := top + i
		if row < 0 || row >= limit || row >= len(rows) {
			continue
		}
		// Cut the row at the widget's column, then pad back out to it. Cutting
		// rather than trimming is what makes this an overlay: a row longer than
		// col would otherwise push the box further right, which is the
		// appending behaviour that used to send boxes past the frame edge.
		base := truncateCells(rows[row], col)
		rows[row] = base + strings.Repeat(" ", max(col-lipgloss.Width(base), 0)) + line
	}
}

func toolArgs(raw json.RawMessage) map[string]any {
	var args map[string]any
	if json.Unmarshal(raw, &args) != nil {
		return nil
	}
	return args
}

func toolArg(raw json.RawMessage, key string) string {
	if v, ok := toolArgs(raw)[key].(string); ok {
		return v
	}
	return ""
}

// toolTarget pulls the one argument worth showing beside a tool name. The
// display target is intentionally short; ToolPath and ToolCommand retain the
// bounded full values for quick views.
func toolTarget(raw json.RawMessage) string {
	for _, key := range []string{"path", "pattern", "cmd", "query"} {
		if v := toolArg(raw, key); v != "" {
			return truncateCells(core.SanitizeTerminal(strings.ReplaceAll(v, "\n", " ")), 60)
		}
	}
	return ""
}

func toolPath(raw json.RawMessage) string    { return strings.TrimSpace(toolArg(raw, "path")) }
func toolCommand(raw json.RawMessage) string { return toolArg(raw, "cmd") }

func resolveToolPath(root, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	if root == "" {
		root, _ = os.Getwd()
	}
	return filepath.Clean(filepath.Join(root, path))
}

func toolPathExists(root, path string) bool {
	info, err := os.Stat(resolveToolPath(root, path))
	return err == nil && info.Mode().IsRegular()
}

// truncateToolCommand keeps an unusually large shell script from becoming a
// transcript retention leak while preserving the useful beginning verbatim.
func truncateToolCommand(s string) string {
	if len(s) <= tools.MaxResultBytes {
		return s
	}
	const marker = "\n\n… command truncated …\n\n"
	keep := tools.MaxResultBytes - len(marker)
	for keep > 0 && !utf8.RuneStart(s[keep]) {
		keep--
	}
	return s[:keep] + marker
}

func (m *Model) openQuickViewAt(mouse tea.Mouse) {
	idx := m.transcriptBlockAt(mouse)
	if idx < 0 || idx >= len(m.blocks) || m.blocks[idx].Kind != BlockTool {
		return
	}
	b := m.blocks[idx]

	switch strings.ToLower(b.ToolName) {
	case "read":
		m.quickView = m.readQuickView(b.ToolPath)
	case "write", "edit":
		body := []string(nil)
		if b.Diff == "" {
			body = []string{"no diff captured for this edit"}
		}
		m.quickView = &PanelContent{Title: b.ToolTarget, Path: b.ToolPath, Diff: b.Diff, Body: body}
	case "bash":
		command := b.ToolCommand
		if command == "" {
			command = b.ToolTarget
		}
		body := strings.Split("> "+command, "\n")
		output := b.ToolOutput
		if output == "" {
			output = "(no output)"
		}
		body = append(body, strings.Split(strings.TrimSuffix(output, "\n"), "\n")...)
		m.quickView = &PanelContent{Title: "bash", Body: body}
	}
}

func (m *Model) readQuickView(path string) *PanelContent {
	content := &PanelContent{Title: path, Path: path, Code: true}
	if path == "" {
		content.Code = false
		content.Body = []string{"read did not include a file path"}
		return content
	}

	file, err := os.Open(resolveToolPath(m.cwd, path))
	if err != nil {
		content.Code = false
		content.Body = []string{"error: " + err.Error()}
		return content
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(tools.MaxResultBytes)+1))
	if err != nil {
		content.Code = false
		content.Body = []string{"error reading file: " + err.Error()}
		return content
	}
	content.Body = strings.Split(tools.Truncate(string(data)), "\n")
	return content
}

// Run starts the TUI over a bare agent.
func Run(a *agent.Agent, h HeaderState) error {
	return RunModel(NewModel(a, h))
}

// RunModel starts the TUI over a model the caller has configured.
func RunModel(m *Model) error {
	_, err := tea.NewProgram(m).Run()
	return err
}

// WithDisplay applies the `[display]` block.
//
// It exists because these settings were reaching the model one at a time or not
// at all: thinking_display was parsed, defaulted, documented, and then never
// read by anything.
func (m *Model) WithDisplay(d config.Display) *Model {
	// display.theme was parsed, defaulted and documented, and then never read:
	// NewModel hardcoded dracula. Same class of gap as thinking_display.
	if d.Theme != "" {
		m.setPalette(theme.ByName(d.Theme))
	}
	m.centered = d.Centered
	m.renderer.Centered = d.Centered
	if mode := ThinkingMode(d.ThinkingDisplay); mode.Valid() {
		m.thinking = mode
	}
	m.keepThinking = d.KeepThinking
	m.renderer.ThinkingLines = d.ThinkingLines
	if !d.InlineDiffs {
		m.renderer.DiffMode = DiffOff
	}
	return m
}

// observeStreamed keeps a live token estimate while a response arrives.
//
// It is an estimate — four characters to a token, the same rule the tool rows
// use — and it is replaced by the provider's exact count the moment that
// arrives. The alternative is what was there before: a rate and a token count
// that both sit at zero until the response is already finished.
func (m *Model) observeStreamed(text string) {
	if text == "" {
		return
	}
	if m.streamStart.IsZero() {
		m.streamStart = time.Now()
	}
	m.streamChars += len(text)

	estimate := m.streamChars / 4
	m.status.TokensOut += estimate - m.estimatedOut
	m.estimatedOut = estimate

	if secs := time.Since(m.streamStart).Seconds(); secs > 0.2 {
		m.status.TokensPerSecond = float64(estimate) / secs
	}
}

// dismissWidgetAt hides the docked widget under a click, and reports whether one
// was there.
//
// Widgets can cover prose now, so a way to swat one away is part of the deal
// rather than a nicety. Dismissal lasts the session: a box you dismissed should
// not return on the next tick, but it is not worth writing to config either.
func (m *Model) dismissWidgetAt(mouse tea.Mouse) bool {
	if !m.widgetsOn || len(m.placements) == 0 {
		return false
	}
	// Placements are in transcript-region coordinates, and the region starts at
	// the top of the frame, so the mouse row maps straight through.
	_, pad := ContentWidth(m.width, m.centered)
	p, ok := m.dock.Hit(m.placements, mouse.X-pad, mouse.Y)
	if !ok {
		return false
	}
	m.dock.Dismiss(p.Index)
	m.notice = "Widget dismissed"
	return true
}

// transcriptBlockAt maps a screen click back through the same windowing math
// View uses. Rows.Owner is the source of truth; re-rendering or guessing from
// block heights would drift as wrapping, scrolling, and slack change.
func (m *Model) transcriptBlockAt(mouse tea.Mouse) int {
	_, pad := ContentWidth(m.width, m.centered)
	x := mouse.X - pad
	chat, _ := Horizontal{
		Width: m.width, SidePaneRatio: m.panelRatio, SidePaneOpen: m.sidePaneOpen(),
	}.Split()
	if x < 0 || x >= chat || (m.scrollbarOn && x >= chat-ScrollbarReserve) || mouse.Y < 0 {
		return -1
	}

	rows := m.transcriptLines()
	res := m.stackFor(len(rows.Lines)).Resolve()
	if mouse.Y >= res.Transcript {
		return -1
	}
	start := clamp(len(rows.Lines)+m.scroll.Slack()-res.Transcript-m.scroll.Offset,
		0, len(rows.Lines))
	line := start + mouse.Y
	if line < 0 || line >= len(rows.Owner) {
		return -1
	}
	return rows.Owner[line]
}
