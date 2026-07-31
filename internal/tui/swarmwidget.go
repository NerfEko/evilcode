package tui

import (
	"fmt"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"evilcode/internal/theme"
)

// SwarmAgent is one other agent, as the UI shows it.
type SwarmAgent struct {
	Name    string
	Task    string
	Worker  bool
	Running bool
	Since   time.Duration
}

// SwarmState is the live swarm, plus the hysteresis that keeps the strip and
// the widget from arguing about who owns the display.
type SwarmState struct {
	mu sync.Mutex

	// agents is written by the roster poller and read during render, so it is
	// behind a mutex rather than an exported field: a render that walked the
	// slice mid-replacement is the kind of crash that only shows up under a
	// real swarm.
	agents []SwarmAgent

	// stripSince is when the strip last became eligible to stand down.
	stripSince time.Time

	// stripDown is whether the strip is currently stood down.
	stripDown bool
}

// StandDownDelay is how long the dock widget must be showing the same agents
// before the strip stands down, and how long it must stop before the strip
// comes back (plan.md §10).
//
// Symmetric on purpose. An asymmetric delay produces the exact flicker the
// hysteresis exists to prevent: the strip vanishes the instant the widget
// appears and returns the instant it is briefly covered.
const StandDownDelay = 2 * time.Second

// Publish replaces the roster.
func (s *SwarmState) Publish(agents []SwarmAgent) {
	s.mu.Lock()
	s.agents = agents
	s.mu.Unlock()
}

// Agents returns the roster.
func (s *SwarmState) Agents() []SwarmAgent {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SwarmAgent, len(s.agents))
	copy(out, s.agents)
	return out
}

// Live returns the agents actually doing something.
func (s *SwarmState) Live() []SwarmAgent {
	var out []SwarmAgent
	for _, a := range s.Agents() {
		if a.Running {
			out = append(out, a)
		}
	}
	return out
}

// ObserveDock records whether the dock widget is showing the same agents, and
// reports whether the strip should draw.
//
// The strip is the fallback: it exists so a swarm is visible when the widget
// cannot dock. Both at once is the same information twice, so the widget wins —
// but only after it has held steady, and the strip returns only after the
// widget has been gone just as long.
func (s *SwarmState) ObserveDock(widgetShown bool, now time.Time) bool {
	if widgetShown == s.stripDown {
		// Already agreed; nothing is pending.
		s.stripSince = time.Time{}
		return !s.stripDown
	}
	if s.stripSince.IsZero() {
		s.stripSince = now
	}
	if now.Sub(s.stripSince) >= StandDownDelay {
		s.stripDown = widgetShown
		s.stripSince = time.Time{}
	}
	return !s.stripDown
}

// SwarmStatusWidget renders the live agents (plan.md §8.8-adjacent, §20):
//
//	⠹ bat · wiring auth · 42s
//
// Absent when nothing else is running: a box reading "no other agents" is the
// clutter §8.3 exists to prevent.
func (r *Renderer) SwarmStatusWidget(s *SwarmState, elapsed time.Duration) Widget {
	agents := s.Agents()
	if len(agents) == 0 {
		return Widget{Kind: WidgetSwarmStatus}
	}

	dim := r.style(theme.RoleDim)
	name := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.Hex(theme.RGB(140, 200, 255)))).Bold(true)
	doneStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.Hex(theme.RGB(100, 200, 100))))

	var lines []string
	for _, a := range agents {
		glyph := doneStyle.Render("✓")
		if a.Running {
			glyph = lipgloss.NewStyle().
				Foreground(lipgloss.Color(theme.Hex(theme.RGB(255, 200, 100)))).
				Render(SpinnerFrame(elapsed))
		}
		row := glyph + " " + name.Render(a.Name)
		if a.Task != "" {
			row += dim.Render(" · " + truncateCells(a.Task, 20))
		}
		row += dim.Render(" · " + humanSeconds(a.Since))
		lines = append(lines, row)
	}
	return Widget{Kind: WidgetSwarmStatus, Lines: lines}
}

// RenderSwarmStrip is the one-line fallback shown when the widget cannot dock.
func (r *Renderer) RenderSwarmStrip(s *SwarmState, elapsed time.Duration) string {
	if s == nil {
		return ""
	}
	live := s.Live()
	if len(live) == 0 {
		return ""
	}

	dim := r.style(theme.RoleDim)
	spin := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.Hex(theme.RGB(255, 200, 100)))).
		Render(SpinnerFrame(elapsed))

	names := make([]string, 0, len(live))
	for _, a := range live {
		names = append(names, a.Name)
	}
	const show = 3
	label := strings.Join(names[:min(show, len(names))], ", ")
	if len(names) > show {
		label += fmt.Sprintf(" +%d", len(names)-show)
	}
	return spin + dim.Render(fmt.Sprintf(" %d %s · %s",
		len(live), agentNoun(len(live)), label))
}

func agentNoun(n int) string {
	if n == 1 {
		return "agent"
	}
	return "agents"
}

// humanSeconds renders an age the way the strip and widget both want it.
func humanSeconds(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
}

// SummonFunc starts a worker. It is a function rather than an interface so the
// TUI keeps knowing nothing about the daemon: attach supplies one, a solo
// session supplies none.
//
// It returns the worker's name, which attach gets by opening its own short
// connection rather than borrowing the one the receive loop is reading — two
// readers on one socket would race for the reply.
type SummonFunc func(task string) (string, error)

// WithSwarm attaches swarm state and the summon hook.
func (m *Model) WithSwarm(s *SwarmState, summon SummonFunc) *Model {
	m.swarm, m.summon = s, summon
	return m
}

// summonResult carries a /summon round trip back into the update loop.
type summonResult struct {
	task string
	name string
	err  error
}

// summonCommand implements `/summon <task>` (plan.md §20).
//
// m.summon dials the daemon and waits for it to spawn the worker — a network
// round trip that used to run straight inside Update, freezing every frame
// until the daemon answered, with no read deadline to even bound the wait
// (H5.23). It runs in the returned tea.Cmd now, off the update loop.
func (m *Model) summonCommand(task string) tea.Cmd {
	if m.summon == nil {
		m.notice = "no daemon to summon into — start one with `evilcode serve`"
		return nil
	}
	if task == "" {
		m.notice = "usage: /summon <task> — write it as a complete brief"
		return nil
	}

	m.notice = "summoning…"
	return func() tea.Msg {
		name, err := m.summon(task)
		return summonResult{task: task, name: name, err: err}
	}
}

// applySummonResult reports how a /summon call landed, once the daemon
// answers.
func (m *Model) applySummonResult(r summonResult) {
	if r.err != nil {
		m.notice = "could not summon: " + r.err.Error()
		return
	}
	// A block rather than a notice: summoning is an action whose result arrives
	// later, and a flash that disappears leaves nothing to tie that result back
	// to when it does.
	m.blocks = append(m.blocks, Block{Kind: BlockNotice, Text: fmt.Sprintf(
		"👉 Summoned %s\n%s\n\nIts result will arrive as a message.", r.name, r.task)})
	m.scroll.FollowBottom()
}

// agentsCommand implements `/agents`.
func (m *Model) agentsCommand() tea.Cmd {
	agents := m.swarm.Agents()
	if len(agents) == 0 {
		m.notice = "no other agents are running"
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d in the swarm:\n", len(agents))
	for _, a := range agents {
		state := "idle"
		if a.Running {
			state = "working"
		}
		kind := "session"
		if a.Worker {
			kind = "worker"
		}
		fmt.Fprintf(&b, "%s (%s, %s, %s)", a.Name, kind, state, humanSeconds(a.Since))
		if a.Task != "" {
			fmt.Fprintf(&b, " — %s", a.Task)
		}
		b.WriteString("\n")
	}
	m.blocks = append(m.blocks, Block{Kind: BlockNotice, Text: strings.TrimRight(b.String(), "\n")})
	m.scroll.FollowBottom()
	return nil
}

// StripVisible reports the strip's current state without advancing it. The
// frame reads it; ObserveDock is what moves it, once per frame, after docking.
func (s *SwarmState) StripVisible() bool { return s != nil && !s.stripDown }
