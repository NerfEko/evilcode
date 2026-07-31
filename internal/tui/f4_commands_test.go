package tui

import (
	"strings"
	"testing"

	"evilcode/internal/agent"
)

func TestF4CommandsRegistered(t *testing.T) {
	for _, name := range []string{"review", "bugfix", "describe", "stats", "login"} {
		cmd, ok := FindCommand(name)
		if !ok || cmd.Help == "" || (name != "stats" && cmd.Long == "") {
			t.Fatalf("%s is not fully registered: %+v, found=%v", name, cmd, ok)
		}
	}
	for _, name := range []string{"review", "bugfix", "describe", "stats", "login"} {
		covered := false
		for _, section := range HelpSections {
			for _, listed := range section.Names {
				if listed == name {
					covered = true
				}
			}
		}
		if !covered {
			t.Fatalf("%s is missing from help sections", name)
		}
	}
}

func TestStatsCommandUsesCurrentSessionState(t *testing.T) {
	m := &Model{
		header:           HeaderState{SessionName: "s", Model: "m", Provider: "p"},
		promptCount:      2,
		blocks:           []Block{{Kind: BlockTool}, {Kind: BlockAssistant}},
		sessionTokensIn:  11,
		sessionTokensOut: 7,
		ctxUsed:          19,
		genMS:            123,
	}
	m.statsCommand()
	if len(m.blocks) != 3 || m.blocks[2].Kind != BlockNotice {
		t.Fatalf("stats did not append one notice: %+v", m.blocks)
	}
	for _, want := range []string{"session: s", "prompts: 2", "tool calls: 1", "tokens: in 11 · out 7", "123ms"} {
		if !strings.Contains(m.blocks[2].Text, want) {
			t.Errorf("stats omitted %q: %s", want, m.blocks[2].Text)
		}
	}
}

func TestStatsKeepsProviderCountsAfterTurnEnds(t *testing.T) {
	m := newTestModel(t)
	m.applyEvent(agent.Event{Kind: agent.EventTurnStart, Text: "count this"})
	m.applyEvent(agent.Event{Kind: agent.EventTokenUsage,
		Usage: &agent.Usage{In: 41, Out: 9, CtxUsed: 50}})
	m.applyEvent(agent.Event{Kind: agent.EventTurnEnd})
	m.statsCommand()
	if got := m.blocks[len(m.blocks)-1].Text; !strings.Contains(got, "tokens: in 41 · out 9") {
		t.Fatalf("completed-turn stats lost provider counts: %s", got)
	}
}
