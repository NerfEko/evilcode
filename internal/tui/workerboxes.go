package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"evilcode/internal/session"
	"evilcode/internal/theme"
)

// WorkerBoxTailLines mirrors the daemon's WorkerTailLines: the roster row
// carries at most this many context lines, and the box is exactly this tall
// plus chrome. Fixed height is what makes click hit-testing arithmetic
// instead of a layout search.
const WorkerBoxTailLines = 14

// WorkerBoxRows is the full box height: title + tail + hint.
const WorkerBoxRows = WorkerBoxTailLines + 2

// ExpandedWorkerTailLines bounds the click-to-expand side view: a bigger
// start-screen-style look at the same worker, not the whole log.
const ExpandedWorkerTailLines = 120

// ExpandedWorkerRefresh is how often an open worker expansion re-reads the
// log while its worker runs. The boxes refresh on the roster poll; the
// expansion is a disk read, so it refreshes slower.
const ExpandedWorkerRefresh = 3 * time.Second

// RenderWorkerBoxes draws one live preview box per worker: an info header,
// the last WorkerBoxTailLines context lines, and the expand hint. Boxes
// stack, so N simultaneous workers show N boxes. Every box is exactly
// WorkerBoxRows tall (short tails pad), which keeps hit-testing exact.
func (r *Renderer) RenderWorkerBoxes(workers []SwarmAgent, width int) []string {
	amber := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.Hex(theme.RGB(255, 190, 100)))).Bold(true)
	dim := r.style(theme.RoleDim)
	border := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.Hex(theme.RGB(130, 130, 160))))

	var out []string
	for _, w := range workers {
		state := "idle"
		if w.Running {
			state = "working"
		}
		head := amber.Render("🔧 "+w.Name) + dim.Render(" · "+state)
		if w.Model != "" {
			head += dim.Render(" · " + w.Model)
		}
		if w.Tokens > 0 {
			head += dim.Render(fmt.Sprintf(" · %s tok", humanTokens(w.Tokens)))
		}
		if w.Task != "" {
			head += dim.Render(" · " + truncateCells(w.Task, max(width-40, 10)))
		}
		out = append(out, border.Render("┌")+truncateCells(head, max(width-2, 10)))

		tail := w.Tail
		if len(tail) > WorkerBoxTailLines {
			tail = tail[len(tail)-WorkerBoxTailLines:]
		}
		for i := 0; i < WorkerBoxTailLines; i++ {
			line := ""
			if i < len(tail) {
				line = tail[i]
			}
			out = append(out, border.Render("│")+" "+truncateCells(line, max(width-4, 10)))
		}
		out = append(out, border.Render("└")+dim.Render(" click to expand"))
	}
	return out
}

// myWorkerBoxes returns this session's live crew for the preview boxes.
// Nil-safe: local sessions without a daemon have no swarm and no boxes.
func (m *Model) myWorkerBoxes() []SwarmAgent {
	if m.swarm == nil {
		return nil
	}
	return m.swarm.MyWorkers(m.header.SessionName)
}

// workerBoxAt maps a click to the worker whose box it landed on: pure
// geometry over the last frame's recorded box top, so it is unit-testable
// without a terminal.
func workerBoxAt(names []string, boxTop, y, x, chatWidth int) string {
	if len(names) == 0 || x >= chatWidth {
		return ""
	}
	rel := y - boxTop
	if rel < 0 {
		return ""
	}
	if idx := rel / WorkerBoxRows; idx < len(names) {
		return names[idx]
	}
	return ""
}

// expandWorkerAt opens the clicked worker box in the side panel, the bigger
// start-screen-style view beside the chat — the same split the file diff
// uses. False when the click is not on a box.
func (m *Model) expandWorkerAt(y, x int) bool {
	_, pad := ContentWidth(m.width, m.centered)
	name := workerBoxAt(m.workerBoxNames, m.workerBoxTop, y, x-pad, m.chatWidth())
	if name == "" {
		return false
	}
	m.expandedWorker = name
	m.expandedAt = time.Time{}
	m.refreshExpandedWorker()
	m.panelOpen = true
	m.applyWrapWidth()
	m.renderer.Graphics, m.renderer.ImagesOn = m.graphics, m.imagesOn
	m.drainDiagrams()
	return true
}

// refreshExpandedWorker rebuilds the open worker expansion from the log,
// throttled: a disk read per frame would turn following a worker into the
// most expensive thing on screen.
func (m *Model) refreshExpandedWorker() {
	if m.expandedWorker == "" {
		return
	}
	if time.Since(m.expandedAt) < ExpandedWorkerRefresh {
		return
	}
	m.expandedAt = time.Now()
	m.panel = m.workerPanelContent(m.expandedWorker)
}

// workerPanelContent builds the expanded side view: worker header plus the
// recent transcript, newest last. It reads the persisted JSONL like the
// start-page preview does, so the expansion keeps working after the daemon
// forgets the worker.
func (m *Model) workerPanelContent(name string) PanelContent {
	var status, task, model string
	for _, w := range m.myWorkerBoxes() {
		if w.Name == name {
			status = "idle"
			if w.Running {
				status = "working"
			}
			task, model = w.Task, w.Model
		}
	}
	title := "🔧 " + name
	if status != "" {
		title += " · " + status
	}
	if model != "" {
		title += " · " + model
	}
	body := []string{}
	if task != "" {
		body = append(body, task, "")
	}
	if m.dataDir == "" {
		body = append(body, "(log unavailable: no session store)")
		return PanelContent{Title: title, Body: body}
	}
	msgs, err := session.Messages(filepath.Join(session.Dir(m.dataDir), name+".jsonl"))
	if err != nil {
		body = append(body, "(log unavailable)")
		return PanelContent{Title: title, Body: body}
	}
	var lines []string
	for _, msg := range msgs {
		text := strings.TrimSpace(string(msg.Content))
		if text == "" || msg.Role == "system" {
			continue
		}
		lines = append(lines, strings.Split(string(msg.Role)+": "+text, "\n")...)
	}
	if len(lines) > ExpandedWorkerTailLines {
		lines = lines[len(lines)-ExpandedWorkerTailLines:]
	}
	body = append(body, lines...)
	return PanelContent{Title: title, Body: body}
}
