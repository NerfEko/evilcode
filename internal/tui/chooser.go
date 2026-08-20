package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"evilcode/internal/theme"
)

// ChooseSessionResult describes the first-screen decision.
type ChooseSessionResult struct {
	Name       string
	NewSession bool
	Cancelled  bool
}

// ChooseSession presents the active-first session chooser used before a
// default client attaches. It intentionally returns before an agent is
// created, so choosing a stored session never hydrates a duplicate process.
func ChooseSession(rows []SessionDescriptor) (ChooseSessionResult, error) {
	m := &chooserModel{
		picker:   SessionPickerState{Rows: SessionRows(rows)},
		renderer: NewRenderer(theme.Dracula(), 80),
	}
	_, err := tea.NewProgram(m).Run()
	return m.result, err
}

type chooserModel struct {
	picker   SessionPickerState
	renderer *Renderer
	width    int
	height   int
	result   ChooseSessionResult
}

func (m *chooserModel) Init() tea.Cmd { return nil }

func (m *chooserModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.renderer.SetWidth(max(msg.Width, 1))
	case tea.KeyPressMsg:
		key := msg.String()
		if m.picker.Editing {
			switch key {
			case "esc":
				m.picker.Editing = false
				m.picker.Filter = ""
				m.picker.Selected = 0
			case "enter":
				m.picker.Editing = false
			case "backspace":
				if r := []rune(m.picker.Filter); len(r) > 0 {
					m.picker.Filter = string(r[:len(r)-1])
					m.picker.Selected = 0
				}
			default:
				if text := msg.Key().Text; text != "" {
					m.picker.Filter += text
					m.picker.Selected = 0
				}
			}
			return m, nil
		}

		rows := m.picker.Filtered()
		switch key {
		case "esc", "q", "ctrl+c":
			m.result.Cancelled = true
			return m, tea.Quit
		case "/":
			m.picker.Editing = true
		case "n":
			m.result.NewSession = true
			return m, tea.Quit
		case "up", "k":
			m.picker.Selected = max(m.picker.Selected-1, 0)
		case "down", "j":
			m.picker.Selected = min(m.picker.Selected+1, max(len(rows)-1, 0))
		case "enter":
			if len(rows) == 0 {
				m.result.NewSession = true
				return m, tea.Quit
			}
			m.result.Name = rows[clamp(m.picker.Selected, 0, len(rows)-1)].Info.Name
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *chooserModel) View() tea.View {
	width, height := m.width, m.height
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	rows := m.renderer.RenderSessionPicker(m.picker, width, max(height-2, 1))
	rows = append(rows,
		lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Hex(theme.RGB(120, 120, 140)))).Render(
			"↑↓ select · Enter resume · n new session · / filter · q quit"),
	)
	return tea.NewView(strings.Join(rows, "\n"))
}
