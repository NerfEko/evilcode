package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"evilcode/internal/core"
	"evilcode/internal/provider"
	"evilcode/internal/theme"
)

// HeaderState describes the session for the top-of-transcript header.
type HeaderState struct {
	SessionName string
	Version     string

	Provider string
	Model    string
	AuthKind string

	// ReasoningEffort is shown when the active provider supports live effort
	// control. It stays beside the model so the setting is visible without
	// spending another header row.
	ReasoningEffort provider.ReasoningEffort
	// ReasoningEfforts is the active model's ordered capability list. It is
	// optional for ordinary local construction and is useful to attached TUI
	// clients that learn capabilities from the daemon snapshot.
	ReasoningEfforts []provider.ReasoningEffort

	// Cwd and Branch describe the workspace.
	Cwd    string
	Branch string

	// Providers lists configured providers and whether each is authenticated,
	// for the status dots.
	Providers []ProviderStatus

	// Skills and MCP servers, when present.
	Skills []string
	MCP    []MCPStatus

	// Attached is the daemon socket this TUI is driving through, empty for a
	// local session. ClientName names this client (plan.md §20): with two
	// terminals on one session, "which of these am I" is the first question,
	// and the header is where it gets answered.
	Attached   string
	ClientName string
}

// MCPStatus is one connected server, for the header line.
type MCPStatus struct {
	Name  string
	Tools int
}

// ProviderStatus is one provider dot.
type ProviderStatus struct {
	Name  string
	Ready bool
}

// RenderHeader draws the borderless, all-dim header of plan.md §8.7.
func (r *Renderer) RenderHeader(h HeaderState) []string {
	dim := r.style(theme.RoleDim)
	name := lipgloss.NewStyle().
		Foreground(lipgloss.Color(r.Palette.Hex(theme.RoleHeaderName))).Bold(true)
	system := r.style(theme.RoleSystem)
	sessionStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(r.Palette.Hex(theme.RoleHeaderSession)))

	var out []string

	first := name.Render("evilcode")
	if h.Version != "" {
		first += dim.Render(" · " + h.Version)
	}
	out = append(out, first)

	if h.SessionName != "" {
		emoji := core.CreatureEmoji(h.SessionName)
		title := sessionStyle.Render(core.Title(h.SessionName, emoji))
		if h.Attached != "" {
			// Attached mode names both ends. The session belongs to the daemon,
			// so calling it "session:" as though it were local would be a lie
			// the moment a second terminal attaches to the same one.
			line := dim.Render("server: ") + title
			if h.ClientName != "" {
				line += dim.Render(" · client: ") +
					sessionStyle.Render(core.Title(h.ClientName, core.CreatureEmoji(h.ClientName)))
			}
			out = append(out, line)
		} else {
			out = append(out, dim.Render("session: ")+title)
		}
	}

	modelLine := dim.Render("/model to switch")
	if h.AuthKind != "" && h.Provider != "" {
		modelLine += dim.Render(" · " + h.AuthKind + ":" + h.Provider)
	}
	if h.Model != "" {
		modelLine += dim.Render(" · ") + system.Render(h.Model)
	}
	if h.ReasoningEffort.Valid() {
		modelLine += dim.Render(" · effort:") + system.Render(string(h.ReasoningEffort))
	}
	out = append(out, modelLine)

	// Provider dots: filled and colored when authenticated, hollow and dim
	// when not, so an unconfigured provider is visible without being loud.
	if len(h.Providers) > 0 {
		var dots []string
		for _, p := range h.Providers {
			if p.Ready {
				dots = append(dots, r.style(theme.RoleSuccess).Render("●")+dim.Render(" "+p.Name))
			} else {
				dots = append(dots, dim.Render("○ "+p.Name))
			}
		}
		out = append(out, strings.Join(dots, dim.Render("  ")))
	}

	if len(h.MCP) > 0 {
		var parts []string
		shown := h.MCP
		suffix := ""
		if len(shown) > 3 {
			suffix = fmt.Sprintf(" +%d more", len(shown)-3)
			shown = shown[:3]
		}
		for _, s := range shown {
			parts = append(parts, fmt.Sprintf("%s (%d tools)", s.Name, s.Tools))
		}
		out = append(out, dim.Render("mcp: "+strings.Join(parts, ", ")+suffix))
	}

	if len(h.Skills) > 0 {
		shown := h.Skills
		suffix := ""
		if len(shown) > 4 {
			suffix = fmt.Sprintf(" +%d more", len(shown)-4)
			shown = shown[:4]
		}
		out = append(out, dim.Render("skills: /"+strings.Join(shown, " /")+suffix))
	}

	loc := h.Cwd
	if h.Branch != "" {
		loc += fmt.Sprintf(" (%s)", h.Branch)
	}
	if loc != "" {
		out = append(out, dim.Render(loc))
	}
	return out
}

// WelcomeMessage is the one piece of flavor on the welcome screen (§2.1).
const WelcomeMessage = "Welcome to evilcode 🦇"

// SuggestionChips are the rotating starter prompts.
var SuggestionChips = []string{
	"explain this codebase",
	"find the bug in the scroll math",
	"add a test for the parser",
	"what changed on this branch?",
}

// RenderWelcome draws the empty-transcript screen with its idle art.
//
// Widgets are suppressed while this is showing: an empty screen decorated with
// status boxes is busier than the thing it decorates (plan.md §8.3).
func (r *Renderer) RenderWelcome(chipIndex int, art []string) []string {
	out := r.welcomeText(chipIndex)
	if len(art) == 0 {
		return out
	}
	return append(append(art, ""), out...)
}

func (r *Renderer) welcomeText(chipIndex int) []string {
	accent := lipgloss.NewStyle().
		Foreground(lipgloss.Color(r.Palette.Hex(theme.RoleAccent))).Bold(true)
	inactiveCap := lipgloss.NewStyle().
		Foreground(lipgloss.Color(r.Palette.Hex(theme.RoleDim)))
	inactiveChip := lipgloss.NewStyle().
		Foreground(lipgloss.Color(r.Palette.Hex(theme.RoleAIText))).
		Background(lipgloss.Color(r.Palette.Hex(theme.RoleDim)))
	selectedCap := lipgloss.NewStyle().
		Foreground(lipgloss.Color(r.Palette.Hex(theme.RoleAccent)))
	selectedChip := lipgloss.NewStyle().
		Foreground(lipgloss.Color(r.Palette.Hex(theme.RoleAIText))).
		Background(lipgloss.Color(r.Palette.Hex(theme.RoleAccent))).Bold(true)

	out := []string{accent.Render(WelcomeMessage), ""}

	if len(SuggestionChips) > 0 {
		focused := chipIndex >= 0
		idx := 0
		if focused {
			idx = ((chipIndex % len(SuggestionChips)) + len(SuggestionChips)) % len(SuggestionChips)
		}
		// Show a rotating window of chips rather than all of them; the point
		// is a nudge, not a menu.
		for i := 0; i < min(3, len(SuggestionChips)); i++ {
			chip := SuggestionChips[(idx+i)%len(SuggestionChips)]
			if focused && i == 0 {
				out = append(out, selectedCap.Render("  ◖")+
					selectedChip.Render(" "+chip+" ")+
					selectedCap.Render("◗"))
			} else {
				out = append(out, inactiveCap.Render("  ◖")+
					inactiveChip.Render(" "+chip+" ")+
					inactiveCap.Render("◗"))
			}
		}
	}
	return out
}
