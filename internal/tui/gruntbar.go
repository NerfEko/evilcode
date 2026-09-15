package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"evilcode/internal/theme"
)

// EnterObserveMode attaches in grunt observe mode: details bar, no input.
// Called once, from attach, when the snapshot belongs to a worker session.
func (m *Model) EnterObserveMode(name, task, model string) {
	m.observeGrunt = true
	m.observeHinted = false
	m.gruntName = name
	m.gruntTask = task
	m.gruntModel = model
	m.clearEditor()
}

// setObserveGrunt flips observe mode. Promoting keeps the transcript and the
// session; it only swaps the bottom bar back to the composer.
func (m *Model) setObserveGrunt(on bool) {
	m.observeGrunt = on
	m.observeHinted = false
	if on {
		m.clearEditor()
	} else {
		m.notice = "Normal session — type below to talk to " + m.gruntName
	}
}

// bottomBar is the input row: the composer, or the grunt details bar while
// observing a worker session. Both call sites (frame layout and its height
// probe) go through here so the two can never disagree about the height.
func (m *Model) bottomBar() []string {
	if m.observeGrunt {
		return m.renderer.RenderGruntBar(GruntDetails{
			Name: m.gruntName, Task: m.gruntTask, Model: m.gruntModel,
			Running: m.processing,
		})
	}
	return m.renderer.RenderComposer(m.composerState())
}

// observeSwallows reports whether a key would type into the hidden composer.
// Navigation, scroll, quit, help, and the side panel keep working in observe
// mode; only composer-bound keys are swallowed, with a hint the first time.
func (m *Model) observeSwallows(key string, text string) bool {
	switch key {
	case "enter", "backspace", "delete",
		"ctrl+u", "ctrl+k", "ctrl+w", "ctrl+a", "ctrl+e", "ctrl+z", "ctrl+s",
		"ctrl+j", "ctrl+r", "ctrl+v", "alt+v", "ctrl+up", "alt+up",
		"ctrl+backspace", "alt+backspace":
		return true
	}
	if NewlineKeys[key] {
		return true
	}
	return text != ""
}

// observeGate runs before the keymap while observing a grunt. Alt+O promotes
// to a normal session; composer-bound keys are swallowed with a hint;
// everything else falls through to normal handling.
func (m *Model) observeGate(key string, text string) (handled bool) {
	if !m.observeGrunt {
		return false
	}
	if key == "alt+o" {
		m.setObserveGrunt(false)
		return true
	}
	if m.observeSwallows(key, text) {
		if !m.observeHinted {
			m.observeHinted = true
			m.notice = fmt.Sprintf("Observing %s — %s to take over and make this a normal session",
				m.gruntName, PrettyKey("alt+o"))
		}
		return true
	}
	return false
}

// GruntDetails is what the bottom bar shows while observing a worker session:
// identity and brief, not an input box.
type GruntDetails struct {
	Name    string
	Task    string
	Model   string
	Running bool
}

// RenderGruntBar draws the bottom bar in grunt observe mode: session details
// plus the takeover keybind, where the composer would be. No input box —
// typing at a worker is how accidents happen; Alt+O promotes to a normal
// session when that is really what is wanted.
func (r *Renderer) RenderGruntBar(d GruntDetails) []string {
	amber := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.Hex(theme.RGB(255, 190, 100)))).Bold(true)
	dim := r.style(theme.RoleDim)

	state := "idle"
	if d.Running {
		state = "working"
	}
	head := amber.Render("🔧 "+d.Name) + dim.Render(" · "+state)
	if d.Model != "" {
		head += dim.Render(" · "+d.Model)
	}
	task := strings.TrimSpace(d.Task)
	if task == "" {
		task = "no brief recorded"
	}
	return []string{
		head,
		dim.Render("  " + truncateCells(task, 120)),
		dim.Render(fmt.Sprintf("  %s to take over and make this a normal session · scroll freely meanwhile",
			PrettyKey("alt+o"))),
	}
}
