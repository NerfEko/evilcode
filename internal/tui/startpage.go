package tui

import (
	"path/filepath"
	"sort"
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

// applyStartSessions stores a fetched roster. It re-sorts the rows so active
// sessions sit at the front and idle/completed ones fall to the right, and it
// keeps the selection following the same session across the reorder rather
// than snapping to whatever lands at the old index.
func (m *Model) applyStartSessions(msg startSessionsMsg) {
	m.startLoading = false
	m.startLoadedAt = time.Now()
	if msg.err != nil {
		// A failed poll leaves the previous rows in place rather than blanking
		// the start page — a transient daemon hiccup should not erase the menu.
		return
	}

	// Remember the session under the selection so it can be re-selected after
	// the roster is replaced and re-sorted.
	var prevName string
	if len(m.startRows) > 0 {
		prevName = m.startRows[clamp(m.startSelected, 0, len(m.startRows)-1)].Info.Name
	}

	m.startRows = sortStartRows(msg.rows)

	// Re-select the same session if it is still present. If it has dropped off
	// the roster, drop the active highlight too so a reflexive Enter does not
	// resume a different session than the one the user had highlighted.
	m.startSelected = 0
	m.startActive = false
	// Refreshes must not steal focus back from the composer. While the user is
	// typing a new prompt the resume row is intentionally inactive; otherwise
	// the three-second roster poll makes the green pill reappear between
	// keystrokes and the next printable key removes it again.
	if prevName != "" && m.editor.Text == "" {
		for i, r := range m.startRows {
			if r.Info.Name == prevName {
				m.startSelected = i
				m.startActive = true
				break
			}
		}
	}
	if m.startSelected >= len(m.startRows) {
		m.startSelected = max(len(m.startRows)-1, 0)
	}
	m.invalidateStartPageCache()

	// Load the preview for the now-selected row so the box is populated on the
	// first frame after the roster arrives, not just after the first arrow.
	m.loadStartPreview()
}

// sortStartRows orders the start page: active sessions (a turn in flight or a
// question waiting on an answer) to the front, then live-but-idle sessions,
// then completed/stored ones. Within each tier the most recently modified
// session wins, so a session that just became inactive slides to the head of
// the inactive group rather than to the very end.
func sortStartRows(rows []SessionRow) []SessionRow {
	tier := func(r SessionRow) int {
		switch {
		case r.Running || r.Pending > 0:
			return 0 // active: in flight or waiting on an answer
		case r.Live:
			return 1 // ready: hydrated but idle
		default:
			return 2 // completed/stored
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		ti, tj := tier(rows[i]), tier(rows[j])
		if ti != tj {
			return ti < tj
		}
		mi, mj := rows[i].Info.Modified, rows[j].Info.Modified
		if mi.IsZero() {
			mi = time.Unix(0, 0)
		}
		if mj.IsZero() {
			mj = time.Unix(0, 0)
		}
		return mi.After(mj)
	})
	return rows
}

// startPageVisible reports whether the empty-transcript start page is showing.
func (m *Model) startPageVisible() bool {
	return len(m.blocks) == 0
}

// needsStartRefresh reports whether the start page should poll the roster now.
func (m *Model) needsStartRefresh() bool {
	// Once the composer has text, the user has chosen a new prompt rather than
	// browsing the resume menu. Pause roster I/O until the editor is empty again;
	// reading and previewing a JSONL session on the typing path is avoidable
	// latency, and a refresh cannot change anything the user can currently use.
	return m.startPageVisible() && m.editor.Text == "" && !m.startLoading &&
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

// loadStartPreview fills the selected row's conversation preview from disk and
// records its real message count. It reuses the JSONL the daemon persists, so a
// running session's most recent turns are visible. The count is taken from the
// same read (rather than trusted from the roster) so it is correct even when the
// daemon is a stale process that does not report Messages — the "0 messages"
// case. The preview is re-read on each refresh so a running session's preview
// stays live rather than freezing at first load.
func (m *Model) loadStartPreview() {
	if len(m.startRows) == 0 || m.dataDir == "" {
		return
	}
	sel := clamp(m.startSelected, 0, len(m.startRows)-1)
	row := &m.startRows[sel]
	msgs, err := session.Messages(filepath.Join(session.Dir(m.dataDir), row.Info.Name+".jsonl"))
	if err != nil {
		// Distinguish "loaded and empty" from "not loaded yet", or an empty
		// session re-reads on every frame.
		row.Preview = []Block{}
		m.invalidateStartPageCache()
		return
	}
	// The count is the full conversation length, before tailing to the preview
	// window. This is what makes the title say "42 messages" instead of "0".
	row.Info.Messages = len(msgs)
	tail := msgs
	if len(tail) > PreviewMessages {
		tail = tail[len(tail)-PreviewMessages:]
	}
	blocks := BlocksFromMessages(tail, m.cwd)
	if blocks == nil {
		blocks = []Block{}
	}
	row.Preview = blocks
	m.invalidateStartPageCache()
}
