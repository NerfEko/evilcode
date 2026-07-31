package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"evilcode/internal/memory"
	"evilcode/internal/provider"
	"evilcode/internal/session"
	"evilcode/internal/todo"
)

// handleSessionKey drives the full-screen session picker.
func (m *Model) handleSessionKey(key string, msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	rows := m.sessions.Filtered()

	// A pending confirmation owns the keyboard until it is answered.
	if m.sessions.Confirm != "" {
		switch key {
		case "enter", "y":
			m.sessions.Confirm = ""
			return m.resumeSelected(rows)
		case "esc", "n", "ctrl+c":
			m.sessions.Confirm = ""
		}
		return m, nil
	}

	if m.sessions.Editing {
		switch key {
		case "esc":
			m.sessions.Filter, m.sessions.Editing = "", false
			return m, nil
		case "enter":
			m.sessions.Editing = false
			return m, nil
		case "backspace":
			if r := []rune(m.sessions.Filter); len(r) > 0 {
				m.sessions.Filter = string(r[:len(r)-1])
				m.sessions.Selected = 0
				m.recallSessions()
			}
			return m, nil
		}
		if txt := msg.Key().Text; txt != "" {
			m.sessions.Filter += txt
			m.sessions.Selected = 0
			m.recallSessions()
		}
		return m, nil
	}

	switch key {
	case "esc", "q", "ctrl+c":
		m.sessionsOpen = false

	case "/":
		m.sessions.Editing = true

	case "up", "k":
		m.sessions.Selected = max(m.sessions.Selected-1, 0)
		m.loadPreview()

	case "down", "j":
		m.sessions.Selected = min(m.sessions.Selected+1, max(len(rows)-1, 0))
		m.loadPreview()

	case " ", "space":
		if len(rows) > 0 {
			sel := rows[clamp(m.sessions.Selected, 0, len(rows)-1)]
			for i := range m.sessions.Rows {
				if m.sessions.Rows[i].Info.Name == sel.Info.Name {
					m.sessions.Rows[i].Marked = !m.sessions.Rows[i].Marked
				}
			}
		}

	case "enter":
		if len(rows) == 0 {
			return m, nil
		}
		sel := rows[clamp(m.sessions.Selected, 0, len(rows)-1)]
		if sel.Current {
			m.sessionsOpen = false
			return m, nil
		}
		// Switching sessions replaces the whole conversation, so it asks first.
		m.sessions.Confirm = "Resume " + sel.Info.Name +
			"?\n\nThe current session stays on disk and can be resumed later."
	}
	return m, nil
}

// resumeSelected switches to the chosen session.
//
// It exits with a resume target rather than swapping state in place: the agent,
// the todo store, the prompt history, and the poke breakers are each bound to
// one session, and re-entering the process is a far smaller surface than
// rebuilding all of them consistently.
func (m *Model) resumeSelected(rows []SessionRow) (tea.Model, tea.Cmd) {
	if len(rows) == 0 {
		m.sessionsOpen = false
		return m, nil
	}
	sel := rows[clamp(m.sessions.Selected, 0, len(rows)-1)]
	m.sessionsOpen = false
	m.resumeTarget = sel.Info.Name
	m.quitting = true
	return m, tea.Quit
}

// ResumeTarget reports the session the picker chose, or "" if none.
func (m *Model) ResumeTarget() string { return m.resumeTarget }

// openSessions loads the picker.
func (m *Model) openSessions() {
	if m.dataDir == "" {
		m.notice = "session storage is not configured"
		return
	}
	infos, err := session.List(m.dataDir)
	if err != nil {
		m.notice = "could not list sessions: " + err.Error()
		return
	}
	m.sessions = SessionPickerState{}
	for _, info := range infos {
		m.sessions.Rows = append(m.sessions.Rows, SessionRow{
			Info:    info,
			Current: m.store != nil && info.Name == m.store.Name,
			Here:    info.Cwd != "" && info.Cwd == m.cwd,
		})
	}
	m.sessionsOpen = true
	m.loadPreview()
}

// runRewind lists rewind points, or collapses back to one.
func (m *Model) runRewind(arg string) (tea.Model, tea.Cmd) {
	if m.store == nil {
		m.notice = "no session to rewind"
		return m, nil
	}
	points, err := session.RewindPoints(m.store.Path)
	if err != nil {
		m.notice = err.Error()
		return m, nil
	}
	if len(points) == 0 {
		m.notice = "nothing to rewind to yet"
		return m, nil
	}

	if arg == "" {
		var b strings.Builder
		b.WriteString("Rewind points — /rewind N to collapse back to one\n")
		for _, p := range points {
			fmt.Fprintf(&b, "  %d  %s\n", p.Index, truncateCells(oneLine(p.Prompt), 60))
		}
		m.blocks = append(m.blocks, Block{
			Kind: BlockNotice, Text: strings.TrimRight(b.String(), "\n"),
		})
		m.scroll.FollowBottom()
		return m, nil
	}

	n := 0
	if _, err := fmt.Sscanf(arg, "%d", &n); err != nil || n < 1 || n > len(points) {
		m.notice = fmt.Sprintf("usage: /rewind 1..%d", len(points))
		return m, nil
	}
	target := points[n-1]

	before := m.agent.Conv.Messages()
	kept, err := session.Rewind(m.dataDir, m.store.Name, target.Entry)
	if err != nil {
		m.notice = err.Error()
		return m, nil
	}

	// Collapse-and-report: the model is told what was pruned rather than
	// silently losing it (plan.md §18).
	discarded := before
	if len(before) > len(kept) {
		discarded = before[len(kept):]
	}
	summary := session.CollapseSummary(discarded)

	m.agent.Conv.Reset(kept)
	if summary != "" {
		m.agent.Conv.Append(provider.Message{Role: provider.RoleUser, Content: summary})
	}

	m.blocks = nil
	m.promptCount = 0
	m.rebuildFromMessages(kept)
	m.notice = fmt.Sprintf("Rewound to point %d · a summary of what was pruned was kept", n)
	m.scroll.FollowBottom()
	return m, nil
}

// rebuildFromMessages repopulates the transcript from a message list, which a
// rewind and a resume both need.
// BlocksFromMessages turns a conversation into transcript blocks.
//
// Free of model state so the session picker can render a preview through the
// same path the transcript uses — a preview that looks like the thing it is
// previewing costs nothing extra when the renderer is already there.
func BlocksFromMessages(msgs []provider.Message) []Block {
	var out []Block
	prompts := 0
	for _, msg := range msgs {
		switch msg.Role {
		case provider.RoleUser:
			// A harness continuation persists as user-role but renders as a
			// system line (plan.md §12.4).
			if todo.IsAutomated(msg.Content) {
				out = append(out, Block{Kind: BlockNotice, Text: msg.Content})
				continue
			}
			prompts++
			out = append(out, Block{Kind: BlockUser, Text: msg.Content, Number: prompts})

		case provider.RoleAssistant:
			if strings.TrimSpace(msg.Content) != "" {
				out = append(out, Block{Kind: BlockAssistant, Text: msg.Content})
			}

		case provider.RoleTool:
			out = append(out, Block{
				Kind: BlockTool, ToolName: msg.ToolName, Failed: msg.IsError,
			})
		}
	}
	return out
}

func (m *Model) rebuildFromMessages(msgs []provider.Message) {
	for _, b := range BlocksFromMessages(msgs) {
		if b.Kind == BlockUser {
			m.promptCount++
			b.Number = m.promptCount
		}
		m.blocks = append(m.blocks, b)
	}
	m.renumberPrompts()
}

// RebuildFrom repopulates the transcript when resuming a session.
func (m *Model) RebuildFrom(msgs []provider.Message) {
	m.blocks = nil
	m.promptCount = 0
	m.rebuildFromMessages(msgs)
}

func oneLine(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
}

// SemanticSearchMinLen is how much has to be typed before session RAG runs. A
// two-character filter describes nothing, and embedding it would spend a
// round-trip to rank every session equally badly.
const SemanticSearchMinLen = 4

// recallSessions fills the picker's semantic matches from the memory bank.
//
// It runs only when the literal filter has already come up empty, which keeps
// it off the keystroke path for every search that was going to work anyway.
func (m *Model) recallSessions() {
	m.sessions.Semantic = nil
	q := strings.TrimSpace(m.sessions.Filter)
	if m.memory == nil || !m.memory.Enabled() || len(q) < SemanticSearchMinLen {
		return
	}
	if len(m.sessions.Filtered()) > 0 {
		return
	}

	// Synchronous, but bounded by the manager's embed timeout and only reached
	// after a search that found nothing — the alternative is results that
	// arrive after the user has already typed the next character.
	hits := m.memory.SearchSessions(context.Background(), q, 8)
	if len(hits) == 0 {
		return
	}
	m.sessions.Semantic = make(map[string]string, len(hits))
	for _, h := range hits {
		if _, seen := m.sessions.Semantic[h.Session]; !seen {
			m.sessions.Semantic[h.Session] = memory.Truncate(h.Text, 60)
		}
	}
}

// PreviewMessages is how much of a session's tail the picker renders. Enough to
// recognise it, few enough that a long session is not fully parsed to draw a box.
const PreviewMessages = 12

// loadPreview fills the selected row's preview, once.
//
// Lazy and cached: reading a JSONL on every arrow key would make the picker
// crawl on a store with any history in it, and the rows outlive the keystroke.
func (m *Model) loadPreview() {
	rows := m.sessions.Filtered()
	if len(rows) == 0 || m.dataDir == "" {
		return
	}
	sel := rows[clamp(m.sessions.Selected, 0, len(rows)-1)]
	if sel.Preview != nil {
		return
	}

	msgs, err := session.Messages(filepath.Join(session.Dir(m.dataDir), sel.Info.Name+".jsonl"))
	if err != nil {
		return
	}
	if len(msgs) > PreviewMessages {
		msgs = msgs[len(msgs)-PreviewMessages:]
	}
	blocks := BlocksFromMessages(msgs)
	if blocks == nil {
		// Distinguish "loaded and empty" from "not loaded yet", or an empty
		// session is re-read on every frame.
		blocks = []Block{}
	}
	for i := range m.sessions.Rows {
		if m.sessions.Rows[i].Info.Name == sel.Info.Name {
			m.sessions.Rows[i].Preview = blocks
		}
	}
}

// TitleMaxLen keeps a derived session title to something a picker row can carry.
const TitleMaxLen = 60

// updateSessionTitle records what this session is about, so the picker is
// labelled by the work rather than by a creature name (plan.md §5.4).
//
// The plan derives it from the in-progress todo's group, then the plan's stated
// user intention, then the todo content — the list is labelled by what the agent
// understood you wanted. It falls back to the first prompt, which is what a
// session without a todo list has to offer.
//
// MetaTitle was read by the store and written by nothing, so every title was
// empty and the whole §5.4 titling feature was invisible.
func (m *Model) updateSessionTitle() {
	if m.store == nil {
		return
	}
	title := m.deriveTitle()
	if title == "" || title == m.sessionTitle {
		return
	}
	m.sessionTitle = title
	_ = m.store.WriteMeta(session.Meta{Kind: session.MetaTitle, Note: title})
}

// deriveTitle picks the best available description of the current work.
func (m *Model) deriveTitle() string {
	if m.todos != nil {
		for _, it := range m.todos.Items() {
			if it.Status == todo.StatusInProgress {
				if it.Group != nil && strings.TrimSpace(*it.Group) != "" {
					return truncateCells(oneLine(*it.Group), TitleMaxLen)
				}
				return truncateCells(oneLine(it.Content), TitleMaxLen)
			}
		}
		if intent := m.todos.Plan().UserIntention; intent != nil &&
			strings.TrimSpace(*intent) != "" {
			return truncateCells(oneLine(*intent), TitleMaxLen)
		}
		if items := m.todos.Items(); len(items) > 0 {
			return truncateCells(oneLine(items[0].Content), TitleMaxLen)
		}
	}
	// No list yet: the first thing asked for is the best available label.
	for _, b := range m.blocks {
		if b.Kind == BlockUser && strings.TrimSpace(b.Text) != "" {
			return truncateCells(oneLine(b.Text), TitleMaxLen)
		}
	}
	return ""
}
