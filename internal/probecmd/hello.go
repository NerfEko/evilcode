package probecmd

import (
	"flag"
	"fmt"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// runHello boots a minimal bubbletea program. It is the Phase 0 rig smoke test:
// enough color, weight, and emoji on screen that a rendered PNG proves the whole
// chain works — tmux capture, SGR parsing, glyph metrics, wide-glyph advance.
func runHello(args []string) error {
	fs := flag.NewFlagSet("probe hello", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	_, err := tea.NewProgram(helloModel{}).Run()
	return err
}

// deterministic reports whether the frozen, reproducible mode is on
// (plan.md invariant 5).
func deterministic() bool { return os.Getenv("EVILCODE_DETERMINISTIC") == "1" }

type helloModel struct {
	keys  int
	width int
}

func (m helloModel) Init() tea.Cmd { return nil }

func (m helloModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		}
		m.keys++
	}
	return m, nil
}

// The dracula defaults from plan.md §7.1, enough of them to prove truecolor
// survives the round trip.
var (
	styleAccent = lipgloss.NewStyle().Foreground(lipgloss.Color("#ff79c6")).Bold(true)
	styleUser   = lipgloss.NewStyle().Foreground(lipgloss.Color("#bd93f9"))
	styleAI     = lipgloss.NewStyle().Foreground(lipgloss.Color("#50fa7b"))
	styleDim    = lipgloss.NewStyle().Foreground(lipgloss.Color("#505050"))
	styleBand   = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#f8f8f2")).
			Background(lipgloss.Color("#2a2440"))
	styleItalic = lipgloss.NewStyle().Foreground(lipgloss.Color("#8c8c8c")).Italic(true)
)

func (m helloModel) View() tea.View {
	clock := "frozen"
	if !deterministic() {
		clock = time.Now().Format("15:04:05")
	}

	lines := []string{
		styleAccent.Render("Welcome to evilcode 🦇"),
		"",
		styleBand.Render(" 1› probe rig smoke test "),
		styleUser.Render("user purple") + styleDim.Render(" · ") + styleAI.Render("ai green"),
		styleDim.Render("┌─ go"),
		styleDim.Render("│ ") + "func main() {}",
		styleDim.Render("└─"),
		styleItalic.Render(fmt.Sprintf("  dim italic · keys:%d · width:%d · %s", m.keys, m.width, clock)),
		"",
		styleDim.Render("q to quit"),
	}

	out := ""
	for i, l := range lines {
		if i > 0 {
			out += "\n"
		}
		out += l
	}
	return tea.NewView(out)
}
