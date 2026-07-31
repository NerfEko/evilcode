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

	{Name: "clear", Help: "Clear the transcript"},
	{Name: "cls", Help: "Clear the transcript", Hidden: true},

	{Name: "context", Help: "Show context usage"},
	{Name: "info", Help: "Show session info"},
	{Name: "version", Help: "Show version"},
	{Name: "config", Help: "Show the loaded configuration"},

	{Name: "resume", Help: "Resume a previous session"},
	{Name: "graveyard", Help: "Resume a previous session", Hidden: true},
	{Name: "sessions", Help: "List sessions"},
	{Name: "rename", Help: "Rename this session"},

	{Name: "quit", Help: "Exit evilcode"},
	{Name: "cancel", Help: "Cancel the current turn"},

	{Name: "terminal-setup", Help: "Fix Shift+Enter in this terminal"},
	{Name: "keys", Help: "Show the keymap"},
	{Name: "hotkeys", Help: "Show the keymap", Hidden: true},

	{Name: "screenshot", Help: "Write the current frame to a PNG"},
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
