package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"evilcode/internal/agent"
)

// WithOrchestrate attaches the fan-out keyword hook for local sessions.
// Attached clients leave it nil and forward /orchestrate to the daemon,
// which owns the hook there.
func (m *Model) WithOrchestrate(h *agent.OrchestrateHook) *Model {
	m.orchestrate = h
	if h != nil {
		h.SetOnChange(m.setOrchestratorCapabilities)
	}
	return m
}

// orchestratorActive reports the effective mode for command surfaces. The hook
// is included because its keyword detector runs on the agent goroutine.
func (m *Model) orchestratorActive() bool {
	return m.orchestrator || m.orchestrate != nil && m.orchestrate.Active()
}

// setOrchestratorCapabilities is safe for hook callbacks that run outside the
// Bubble Tea loop. It withdraws todo and auto-poke while delegation owns the
// control loop.
func (m *Model) setOrchestratorCapabilities(on bool) {
	m.orchestratorMu.Lock()
	defer m.orchestratorMu.Unlock()
	if m.agent != nil {
		m.agent.SetToolBlocked("todo", on)
	}
	if m.poke == nil {
		return
	}
	if on {
		if !m.orchestratorPokeSaved {
			m.orchestratorPokeEnabled = m.poke.Enabled()
			m.orchestratorPokeSaved = true
		}
		m.poke.SetEnabled(false)
	} else if m.orchestratorPokeSaved {
		m.poke.SetEnabled(m.orchestratorPokeEnabled)
		m.orchestratorPokeSaved = false
	}
}

// setOrchestrator flips the visible half of orchestrator mode: the rainbow
// composer tint and the per-worker roster colors. Capability synchronization is
// repeated here because explicit commands run on the update loop.
func (m *Model) setOrchestrator(on bool) {
	m.orchestrator = on
	if m.swarm != nil {
		m.swarm.Orchestrator = on
	}
	m.setOrchestratorCapabilities(on)
}

// orchestrateCommand implements `/orchestrate on|off|status` (fan-out D5).
func (m *Model) orchestrateCommand(arg string) tea.Cmd {
	if m.orchestrate == nil {
		if m.remoteCommand != nil {
			if err := m.remoteCommand("orchestrate", strings.TrimSpace(arg), ""); err != nil {
				m.notice = "could not update server orchestrator: " + err.Error()
			} else {
				switch strings.ToLower(strings.TrimSpace(arg)) {
				case "off":
					m.setOrchestrator(false)
					m.notice = "Orchestrator mode off"
				case "on":
					m.setOrchestrator(true)
					m.notice = "🌈 Orchestrator mode on · the fan-out contract is in context"
				default:
					m.notice = "Orchestrator request sent to server"
				}
			}
			return nil
		}
		m.notice = "orchestrator mode is not configured for this session"
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "on":
		m.orchestrate.Arm()
		m.setOrchestrator(true)
		m.notice = "🌈 Orchestrator mode on · the fan-out contract lands on the next turn"
	case "off":
		m.orchestrate.Disarm()
		m.setOrchestrator(false)
		m.notice = "Orchestrator mode off"
	case "", "status":
		if m.orchestrator || m.orchestrate.Active() {
			m.notice = "🌈 Orchestrator mode is on · /orchestrate off to stop"
		} else {
			m.notice = "Orchestrator mode is off · /orchestrate on to arm"
		}
	default:
		m.notice = "usage: /orchestrate [on|off|status]"
	}
	return nil
}

// armOrchestratorFromKeyword checks a just-submitted prompt for the standalone
// `orchestrate` word and arms the visible half. The contract itself is
// injected by the hook (locally) or the daemon (attached) at the next turn
// boundary, so the tint never promises guidance that is not coming.
func (m *Model) armOrchestratorFromKeyword(text string) {
	if m.orchestratorActive() || !agent.HasOrchestrateKeyword(text) {
		return
	}
	m.setOrchestrator(true)
	m.blocks = append(m.blocks, Block{Kind: BlockNotice, Text: fmt.Sprintf(
		"🌈 Orchestrator mode on\n%s",
		"Fan out independent work in one batch; the contract lands on the next turn. /orchestrate off to stop.")})
	m.scroll.FollowBottom()
}
