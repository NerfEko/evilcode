package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"evilcode/internal/agent"
	"evilcode/internal/lsp"
)

// WithAdvisor attaches the advisor and the language-server manager, which are
// the two Phase 5 subsystems the commands below expose.
func (m *Model) WithAdvisor(a *agent.Advisor, servers *lsp.Manager) *Model {
	m.advisor, m.lsp = a, servers
	return m
}

// advisorCommand implements `/advisor on|off|status` (plan.md §21).
func (m *Model) advisorCommand(arg string) tea.Cmd {
	if m.advisor == nil {
		m.notice = "the advisor is not configured for this session"
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "on":
		m.advisor.SetEnabled(true)
		m.notice = "ⓘ Advisor ON · a second model will raise at most one concern per turn"
	case "off":
		m.advisor.SetEnabled(false)
		m.notice = "ⓘ Advisor OFF"
	default:
		m.notice = m.advisor.Status()
	}
	return nil
}

// lspCommand implements `/lsp status`.
func (m *Model) lspCommand(arg string) tea.Cmd {
	if m.lsp == nil {
		m.notice = "no language servers are configured for this session"
		return nil
	}
	statuses := m.lsp.Status()
	if len(statuses) == 0 {
		m.notice = "no language servers are configured"
		return nil
	}

	var b strings.Builder
	b.WriteString("Language servers:\n")
	for _, s := range statuses {
		switch {
		case s.Running:
			fmt.Fprintf(&b, "● %-11s %s — running\n", s.Language, s.Command)
		case s.Err != "":
			// Named plainly rather than hidden: a server that is not installed
			// is the usual reason the tool does nothing, and the fix is to
			// install it.
			fmt.Fprintf(&b, "○ %-11s %s — %s\n", s.Language, s.Command, s.Err)
		default:
			fmt.Fprintf(&b, "○ %-11s %s — ready, not started\n", s.Language, s.Command)
		}
	}
	b.WriteString("\nServers start on first use, not at boot.")

	m.blocks = append(m.blocks, Block{Kind: BlockNotice, Text: strings.TrimRight(b.String(), "\n")})
	m.scroll.FollowBottom()
	return nil
}
