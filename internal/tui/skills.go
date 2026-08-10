package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"evilcode/internal/agent"
	"evilcode/internal/tools"
)

func skillPromptEntries(skills *tools.SkillSet) []agent.Skill {
	if skills == nil {
		return nil
	}
	index := skills.Index()
	out := make([]agent.Skill, 0, len(index))
	for _, skill := range index {
		out = append(out, agent.Skill{Name: skill.Name, Desc: skill.Desc, Path: skill.Path})
	}
	return out
}

// skillsCommand implements /skills and /skills reload.
func (m *Model) skillsCommand(arg string) tea.Cmd {
	if m.skills == nil {
		m.notice = "skills are not configured for this session"
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "", "list":
		m.blocks = append(m.blocks, Block{Kind: BlockNotice, Text: formatSkills(m.skills)})
		m.followIfPinned()
	case "reload":
		m.skills.Reload()
		m.header.Skills = m.skills.Names()
		if m.agent != nil && m.agent.Conv != nil {
			m.agent.Conv.SetSystemPrompt(agent.BuildSystemPrompt(
				m.skillContext, skillPromptEntries(m.skills), ""))
		}
		m.notice = fmt.Sprintf("Skills reloaded · %d available", len(m.skills.Index()))
	default:
		m.notice = "usage: /skills [reload]"
	}
	return nil
}

func formatSkills(skills *tools.SkillSet) string {
	index := skills.Index()
	if len(index) == 0 {
		return "No skills found."
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Skills (%d)\n", len(index)))
	for _, skill := range index {
		fmt.Fprintf(&b, "- %s — %s\n  source: %s\n",
			skill.Name, strings.Join(strings.Fields(skill.Desc), " "), skill.Path)
	}
	return strings.TrimRight(b.String(), "\n")
}
