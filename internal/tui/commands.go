package tui

import "sort"

// Command is one entry in the slash registry (plan.md §13).
type Command struct {
	Name string
	Help string

	// Hidden keeps aliases out of the palette and help without making them
	// unusable.
	Hidden bool

	// Long is the expanded description shown by `/help <cmd>`.
	Long string
}

// Commands is the registry. Phase 1 ships the subset that actually does
// something; the rest arrive with the features they drive, so the palette never
// offers a command that does nothing.
var Commands = []Command{
	{Name: "help", Help: "Show help"},
	{Name: "?", Help: "Show help", Hidden: true},
	{Name: "commands", Help: "List all commands", Hidden: true},

	{Name: "model", Help: "Switch model",
		Long: "Open the model picker, or pass a model reference directly:\n" +
			"  /model qwen3-coder:480b-cloud@ollama-cloud"},
	{Name: "models", Help: "List available models"},
	{Name: "reasoning", Help: "Set reasoning effort",
		Long: "/reasoning [none|minimal|low|medium|high|xhigh|max]\n" +
			"  Values follow the active model's advertised capabilities. With no\n" +
			"  value, cycles those capabilities. Alt+R is the shortcut."},
	{Name: "effort", Help: "Set reasoning effort", Hidden: true},

	{Name: "plan", Help: "Plan before implementing",
		Long: "Injects a planning turn: the model researches and returns a plan card,\n" +
			"then stops. Approval is conversational — it starts work when you say so."},
	{Name: "review", Help: "Review a path or this branch",
		Long: "/review [path|this branch]\n\nReviews correctness first, then clarity, then genuinely dangerous issues."},
	{Name: "bugfix", Help: "Reproduce and fix a bug",
		Long: "/bugfix <symptom>\n\nReproduces with a failing test before fixing and proving it passes."},
	{Name: "describe", Help: "Explain code structure",
		Long: "/describe [path]\n\nExplains the structure and behavior for someone new to the codebase."},
	{Name: "todos", Help: "Toggle the todo card"},
	{Name: "todo", Help: "Toggle the todo card", Hidden: true},
	{Name: "poke", Help: "Auto-poke on/off/status"},
	{Name: "selfdev", Help: "Work on evilcode itself"},
	{Name: "rebuild", Help: "Build, test, and restart into the new binary"},
	{Name: "reload", Help: "Restart, keeping this session"},

	{Name: "productivity", Help: "What you have been doing, as a dashboard"},
	{Name: "overnight", Help: "Work the todo list unattended, under hard caps",
		Long: "  /overnight        arm it\n" +
			"  /overnight off    stop now\n" +
			"  /overnight status where it is\n" +
			"  /overnight report show the latest self-contained HTML report\n" +
			"It stops on turns, tokens, time, or a list that stopped moving — and says which."},
	{Name: "advisor", Help: "Second-opinion model on/off/status"},
	{Name: "lsp", Help: "Language server status"},

	{Name: "summon", Help: "Spawn a worker agent on a task",
		Long: "  /summon <task>   start a headless worker in the daemon\n" +
			"Its result arrives as a message when it finishes."},
	{Name: "agents", Help: "List the agents in the swarm"},
	{Name: "swarm", Help: "List the agents in the swarm", Hidden: true},

	{Name: "memory", Help: "Long-term memory on/off/status",
		Long: "  /memory          show what memory knows\n" +
			"  /memory on|off   stop or resume recalling and remembering\n" +
			"  /memory list [project|global]  show the selected memory view\n" +
			"  /memory forget <id>  drop one"},
	{Name: "skills", Help: "List or reload skills",
		Long: "  /skills         list names, descriptions, and source directories\n" +
			"  /skills reload  reread skill indexes and changed bodies"},

	{Name: "clear", Help: "Clear the transcript"},
	{Name: "cls", Help: "Clear the transcript", Hidden: true},

	{Name: "context", Help: "Show context usage"},
	{Name: "stats", Help: "Show current session statistics"},
	{Name: "info", Help: "Show session info"},
	{Name: "login", Help: "Set or inspect provider authentication",
		Long: "/login                  open a provider selector, then enter a masked key\n" +
			"/login [provider]       enter a masked API key for a provider (e.g. /login deepseek)\n" +
			"/login status [provider] report whether a key/account is present without printing it.\n" +
			"                         Codex OAuth is discovered from `codex login`; it does not accept an API key."},
	{Name: "connect", Help: "Connect an API service",
		Long: "/connect brave         enter a masked Brave Search API key\n" +
			"/connect brave status  report whether a Brave key is present without printing it."},
	{Name: "version", Help: "Show version"},
	{Name: "config", Help: "Show the loaded configuration"},
	{Name: "theme", Help: "Switch, score, or generate a palette",
		Long: "  /theme               list the built-in palettes\n" +
			"  /theme <name>        switch to one\n" +
			"  /theme score         score the current palette\n" +
			"  /theme generate #hex build a palette from a seed color"},
	{Name: "color", Help: "Switch, score, or generate a palette", Hidden: true},

	{Name: "resume", Help: "Resume a previous session"},
	{Name: "graveyard", Help: "Resume a previous session", Hidden: true},
	{Name: "sessions", Help: "List sessions"},
	{Name: "rename", Help: "Rename this session"},
	{Name: "save", Help: "Pin this session so the picker marks it"},
	{Name: "unsave", Help: "Unpin this session", Hidden: true},
	{Name: "fork", Help: "Copy this session under a new name"},
	{Name: "checkpoint", Help: "Mark a point to rewind back to"},
	{Name: "rewind", Help: "Collapse back to an earlier point",
		Long: "/rewind          list the points you can return to\n" +
			"/rewind N        collapse back to point N\n\n" +
			"Rewinding prunes exploratory context. Files already changed stay\n" +
			"changed, todos and memories survive, and a one-paragraph summary of\n" +
			"what was pruned is handed to the model."},

	{Name: "quit", Help: "Exit evilcode"},
	{Name: "cancel", Help: "Cancel the current turn"},

	{Name: "terminal-setup", Help: "Fix Shift+Enter in this terminal"},
	{Name: "keys", Help: "Show the keymap"},
	{Name: "hotkeys", Help: "Show the keymap", Hidden: true},

	{Name: "screenshot", Help: "Write the current frame to a PNG"},
	{Name: "screenshot-mode", Help: "Auto-capture every changed frame"},
	{Name: "record", Help: "Start or stop recording frames"},
	{Name: "debug-visual", Help: "Overlay layout boundaries"},
	{Name: "smoothness", Help: "Report anchor stability"},
	{Name: "onboarding-sim", Help: "Walk the welcome screens"},

	{Name: "diff", Help: "Cycle diff mode"},
	{Name: "alignment", Help: "Toggle centered layout"},
	{Name: "thinking-display", Help: "Reasoning display: off|full|current"},
	{Name: "tool-call-details", Help: "Toggle technical tool summaries"},
	{Name: "compact", Help: "Summarize the conversation into a fresh context",
		Long: "Replaces the history with a dense summary produced by the smol role,\n" +
			"and bumps the context epoch. This is the one sanctioned rewrite of the\n" +
			"append-only rule."},
	{Name: "fix", Help: "Nudge a stalled model back on track"},
	{Name: "btw", Help: "Ask a side question without touching the conversation",
		Long: "/btw <question>\n\nAnswered by the smol role in the side panel, so asking\n" +
			"costs nothing in your main context."},
}

// VisibleCommands returns the commands the palette and help may offer.
func VisibleCommands() []Command {
	out := make([]Command, 0, len(Commands))
	for _, c := range Commands {
		if !c.Hidden {
			out = append(out, c)
		}
	}
	return out
}

// FindCommand looks a command up by name, including hidden aliases.
func FindCommand(name string) (Command, bool) {
	for _, c := range Commands {
		if c.Name == name {
			return c, true
		}
	}
	return Command{}, false
}

// CommandNames returns every registered name, sorted.
func CommandNames() []string {
	out := make([]string, 0, len(Commands))
	for _, c := range Commands {
		out = append(out, c.Name)
	}
	sort.Strings(out)
	return out
}

// HelpSection is one hand-curated group in the help overlay.
type HelpSection struct {
	Title string
	Names []string
}

// HelpSections orders commands for reading rather than alphabetically. It is
// hand-curated, which risks drift — so RenderHelp computes the leftovers and
// shows them under "More commands". A newly registered command can therefore
// never be invisible (plan.md §5.5).
var HelpSections = []HelpSection{
	{"Getting around", []string{"help", "keys", "context", "stats", "info", "version"}},
	{"Models", []string{"model", "models", "reasoning"}},
	{"Working", []string{"plan", "review", "bugfix", "describe", "todos", "poke", "memory", "skills"}},
	{"Swarm", []string{"summon", "agents"}},
	{"Analysis", []string{"lsp", "advisor", "productivity", "overnight"}},
	{"Self-development", []string{"selfdev", "rebuild", "reload"}},
	{"Sessions", []string{"resume", "sessions", "rename", "save", "fork",
		"checkpoint", "rewind", "clear"}},
	{"System", []string{"config", "theme", "login", "connect", "terminal-setup", "screenshot",
		"compact", "fix", "btw", "cancel", "quit"}},
}

// UncoveredCommands returns visible commands no section lists.
func UncoveredCommands() []Command {
	covered := map[string]bool{}
	for _, sec := range HelpSections {
		for _, n := range sec.Names {
			covered[n] = true
		}
	}
	var out []Command
	for _, c := range VisibleCommands() {
		if !covered[c.Name] {
			out = append(out, c)
		}
	}
	return out
}

// HelpKeys is the keymap shown in the overlay.
var HelpKeys = [][2]string{
	{"Enter", "submit; queue while a turn is running"},
	{"Shift+Enter / Alt+Enter", "newline (or end a line with a backslash)"},
	{"Esc", "close overlays, interrupt (disarms auto-poke), then clear input"},
	{"Ctrl+C", "interrupt without disarming; twice when idle to quit"},
	{"Ctrl+T", "toggle queue mode"},
	{"Ctrl+R", "search prompt history"},
	{"Ctrl+Up", "retrieve staged messages for editing"},
	{"Ctrl+G", "toggle a scroll bookmark"},
	{"Ctrl+U / K / W", "kill to start / to end / previous word"},
	{"Ctrl+A / E", "start / end of line"},
	{"Ctrl+Z / S", "undo input / stash and restore a draft"},
	{"Alt+R", "cycle the active model's reasoning effort"},
	{"PgUp / PgDn", "scroll a page"},
}
