package tui

import (
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"

	"evilcode/internal/session"
)

// StartPageRefresh is how often the start page re-polls the roster while it is
// showing. The statuses it draws — "currently running", "completed 10m ago" —
// change on the scale of minutes, not frames, so a slow poll keeps them honest
// without spending a round trip per keystroke.
const StartPageRefresh = 3 * time.Second

// startSessionsMsg carries a freshly fetched roster into the update loop.
//
// Fetching runs in a goroutine (a tea.Cmd) because remoteSessions is a daemon
// round trip; doing it inside Update would freeze the start page on every poll.
type startSessionsMsg struct {
	rows []SessionRow
	err  error
}

// refreshStartSessions fetches the roster for the start page. It prefers the
// daemon's live roster (which carries running/pending state); the standalone
// TUI without a daemon falls back to the on-disk session list, which can still
// offer "completed Xm ago" rows.
//
// The current session is filtered out — you cannot resume the session you are
// already in, and on a fresh launch it is the empty session this start page
// belongs to.
func (m *Model) refreshStartSessions() tea.Cmd {
	m.startLoading = true
	return func() tea.Msg {
		if m.remoteSessions != nil {
			descriptors, err := m.remoteSessions()
			if err != nil {
				return startSessionsMsg{err: err}
			}
			return startSessionsMsg{rows: m.filterStartRows(SessionRows(descriptors))}
		}
		if m.dataDir != "" {
			infos, err := session.List(m.dataDir)
			if err != nil {
				return startSessionsMsg{err: err}
			}
			var rows []SessionRow
			for _, info := range infos {
				if info.Name == m.header.SessionName {
					continue
				}
				rows = append(rows, SessionRow{Info: info})
			}
			return startSessionsMsg{rows: rows}
		}
		return startSessionsMsg{}
	}
}

// filterStartRows drops the current session and trims to the most recent few so
// the start page is a short menu, not the whole roster.
func (m *Model) filterStartRows(rows []SessionRow) []SessionRow {
	out := make([]SessionRow, 0, len(rows))
	for _, r := range rows {
		if r.Info.Name == m.header.SessionName {
			continue
		}
		out = append(out, r)
	}
	// The daemon already sorts live-first, running-first, then by recency. Keep
	// the head of that ordering: the start page offers the last couple active
	// sessions, not the entire history.
	if len(out) > StartPageMaxRows {
		out = out[:StartPageMaxRows]
	}
	return out
}

// StartPageMaxRows caps the start page so it never grows past a short menu.
const StartPageMaxRows = 6

// applyStartSessions stores a fetched roster and re-clamps the selection.
func (m *Model) applyStartSessions(msg startSessionsMsg) {
	m.startLoading = false
	m.startLoadedAt = time.Now()
	if msg.err != nil {
		// A failed poll leaves the previous rows in place rather than blanking
		// the start page — a transient daemon hiccup should not erase the menu.
		return
	}
	m.startRows = msg.rows
	if m.startSelected >= len(m.startRows) {
		m.startSelected = max(len(m.startRows)-1, 0)
	}
	// Load the preview for the now-selected row so the box is populated on the
	// first frame after the roster arrives, not just after the first arrow.
	m.loadStartPreview()
}

// startPageVisible reports whether the empty-transcript start page is showing.
func (m *Model) startPageVisible() bool {
	return len(m.blocks) == 0
}

// needsStartRefresh reports whether the start page should poll the roster now.
func (m *Model) needsStartRefresh() bool {
	return m.startPageVisible() && !m.startLoading &&
		time.Since(m.startLoadedAt) >= StartPageRefresh
}

// resumeStartSelected resumes the session highlighted on the start page. It
// exits the program with a resume target so the attach path re-enters the
// chosen session, exactly as the full-screen picker does.
func (m *Model) resumeStartSelected() (tea.Model, tea.Cmd) {
	if len(m.startRows) == 0 {
		return m, nil
	}
	sel := m.startRows[clamp(m.startSelected, 0, len(m.startRows)-1)]
	m.resumeTarget = sel.Info.Name
	m.quitting = true
	return m, tea.Quit
}

// startPageBounds returns the width and height the start page should fill: the
// chat column width and the transcript slot height (terminal height minus the
// fixed composer/status rows). The start page sizes its preview box to this so
// the buttons sit just above the composer instead of floating mid-screen.
func (m *Model) startPageBounds() (int, int) {
	width := m.chatWidth()
	if width <= 0 {
		width = m.width
	}
	height := m.height - m.stackFor(0).Fixed()
	if height < 6 {
		height = 6
	}
	return width, height
}

// loadStartPreview fills the selected row's conversation preview from disk, once.
// The preview is what makes the start page a live view of the session rather
// than a name on a list. It reuses the JSONL the daemon persists, so a running
// session's most recent turns are visible.
func (m *Model) loadStartPreview() {
	if len(m.startRows) == 0 || m.dataDir == "" {
		return
	}
	sel := clamp(m.startSelected, 0, len(m.startRows)-1)
	row := &m.startRows[sel]
	if row.Preview != nil {
		return
	}
	msgs, err := session.Messages(filepath.Join(session.Dir(m.dataDir), row.Info.Name+".jsonl"))
	if err != nil {
		// Distinguish "loaded and empty" from "not loaded yet", or an empty
		// session re-reads on every frame.
		row.Preview = []Block{}
		return
	}
	if len(msgs) > PreviewMessages {
		msgs = msgs[len(msgs)-PreviewMessages:]
	}
	blocks := BlocksFromMessages(msgs)
	if blocks == nil {
		blocks = []Block{}
	}
	row.Preview = blocks
}
