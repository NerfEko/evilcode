package tui

import (
	"strings"
	"testing"
	"time"

	"evilcode/internal/provider"
	"evilcode/internal/session"

	tea "charm.land/bubbletea/v2"
)

// TestStartPagePreviewLoadsFromDisk is the end-to-end feedback loop for the
// start-page overhaul: it stands up a Model over a real data dir holding a
// session with messages, drives the same pipeline the live TUI uses
// (refreshStartSessions -> applyStartSessions -> loadStartPreview -> render),
// and asserts the preview box actually shows that session's conversation and
// (now-correct) message count — not "0 messages".
func TestStartPagePreviewLoadsFromDisk(t *testing.T) {
	dir := t.TempDir()
	// Write a real session with a user/assistant exchange to disk.
	store, err := session.CreateNamed(dir, "bat")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteMessage(provider.Message{Role: provider.RoleUser, Content: "wire the auth flow"}); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteMessage(provider.Message{Role: provider.RoleAssistant, Content: "Done — the refresh path is wired."}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	// A Model whose current session is a different, empty one, pointed at the
	// data dir. No remoteSessions, so the start page falls back to session.List.
	m := NewModel(nil, HeaderState{SessionName: "current", Model: "mock"})
	m.width, m.height = 90, 24
	m = m.WithSessions(dir, t.TempDir(), nil)

	// 1. Fetch the roster (local fallback).
	refresh := m.refreshStartSessions()
	msg := refresh()
	startMsg, ok := msg.(startSessionsMsg)
	if !ok {
		t.Fatalf("refreshStartSessions produced %T, want startSessionsMsg", msg)
	}
	if startMsg.err != nil {
		t.Fatalf("refresh errored: %v", startMsg.err)
	}
	if len(startMsg.rows) != 1 || startMsg.rows[0].Info.Name != "bat" {
		t.Fatalf("roster = %+v, want just bat", startMsg.rows)
	}
	if startMsg.rows[0].Info.Messages < 2 {
		t.Errorf("roster bat Messages = %d, want >= 2 (the 0-messages bug)", startMsg.rows[0].Info.Messages)
	}

	// 2. Apply through the update loop.
	model, _ := m.update(startMsg)
	m = model.(*Model)
	if len(m.startRows) != 1 {
		t.Fatalf("startRows = %d, want 1", len(m.startRows))
	}
	if m.startRows[0].Preview == nil {
		t.Error("applyStartSessions did not load the selected row's preview")
	}

	// 3. Render the view and assert the conversation + message count show up.
	frame := plain(m.View().Content)
	if !strings.Contains(frame, "wire the auth flow") {
		t.Errorf("preview missing the prompt:\n%s", frame)
	}
	if !strings.Contains(frame, "refresh path is wired") {
		t.Errorf("preview missing the reply:\n%s", frame)
	}
	if !strings.Contains(frame, "2 messages") {
		t.Errorf("preview missing the (non-zero) message count:\n%s", frame)
	}
	if strings.Contains(frame, "0 messages") {
		t.Errorf("preview shows '0 messages' (the bug):\n%s", frame)
	}

	// 4. Arrow navigation loads the preview for the newly selected row and Enter
	// resumes it. With one row, right is a no-op move but still activates; Enter
	// then resumes bat.
	m.startActive = false
	_, _ = m.handleKey(keyPressRight())
	if !m.startActive {
		t.Error("right arrow did not activate the start page selection")
	}
	resumed, _ := m.handleKey(keyPressEnter())
	rm := resumed.(*Model)
	if rm.ResumeTarget() != "bat" {
		t.Errorf("Enter did not resume bat, ResumeTarget=%q", rm.ResumeTarget())
	}

	// 5. Typing falls through to the composer and drops the selection.
	m2 := NewModel(nil, HeaderState{SessionName: "current", Model: "mock"})
	m2.width, m2.height = 90, 24
	m2 = m2.WithSessions(dir, t.TempDir(), nil)
	m2.startRows = m.startRows
	m2.startActive = true
	_, _ = m2.handleKey(keyPressText("h"))
	if m2.editor.Text != "h" {
		t.Errorf("typing did not reach the composer, editor.Text=%q", m2.editor.Text)
	}
	if m2.startActive {
		t.Error("typing did not drop the start-page selection")
	}
	_ = time.Now
}

func TestStartPageRefreshDoesNotReactivateWhileTyping(t *testing.T) {
	m := NewModel(nil, HeaderState{SessionName: "current", Model: "mock"})
	m.startRows = []SessionRow{{Info: session.Info{Name: "bat"}}}
	m.startSelected = 0
	m.startActive = true
	m.editor.Text = "typed prompt"

	m.applyStartSessions(startSessionsMsg{rows: []SessionRow{{Info: session.Info{Name: "bat"}}}})

	if m.startActive {
		t.Fatal("roster refresh reactivated the resume highlight while typing")
	}
}

func TestStartPageRefreshPausesWhileTyping(t *testing.T) {
	m := NewModel(nil, HeaderState{SessionName: "current", Model: "mock"})
	m.startLoadedAt = time.Now().Add(-StartPageRefresh)
	m.editor.Text = "typed prompt"

	if m.needsStartRefresh() {
		t.Fatal("start-page roster refresh stayed on the typing path")
	}
}

func TestStartPageRenderCacheSurvivesComposerEdits(t *testing.T) {
	m := NewModel(nil, HeaderState{SessionName: "current", Model: "mock"})
	m.width, m.height = 90, 24
	m.startRows = []SessionRow{{Info: session.Info{Name: "bat"}}}

	m.View()
	if !m.startPageCacheValid || len(m.startPageCache.Lines) == 0 {
		t.Fatal("start page render did not populate its cache")
	}
	version := m.startPageVersion
	lines := m.startPageCache.Lines

	_, _ = m.handleKey(keyPressText("h"))
	m.View()
	if m.startPageVersion != version {
		t.Fatalf("ordinary typing invalidated the start page cache: version %d -> %d", version, m.startPageVersion)
	}
	if &m.startPageCache.Lines[0] != &lines[0] {
		t.Fatal("ordinary typing rebuilt the start page instead of reusing its cached rows")
	}
}

func TestStartPageWaveUpdatesWithoutRebuildingPreview(t *testing.T) {
	m := NewModel(nil, HeaderState{SessionName: "current", Model: "mock"})
	m.width, m.height = 90, 24
	m.startRows = []SessionRow{{Info: session.Info{Name: "bat"}}}

	m.View()
	lines := m.startPageCache.Lines
	wordmark := lines[0]
	preview := lines[startPageWordmarkRows+1]
	m.startWaveFrame = 1
	m.View()

	if &m.startPageCache.Lines[0] != &lines[0] {
		t.Fatal("animating the wave rebuilt the cached start page")
	}
	if m.startPageCache.Lines[0] == wordmark {
		t.Fatal("animating the wave did not update the wordmark")
	}
	if m.startPageCache.Lines[startPageWordmarkRows+1] != preview {
		t.Fatal("animating the wave rebuilt the session preview")
	}
}

// keyPressRight builds a right-arrow KeyPressMsg.
func keyPressRight() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeyRight} }

// keyPressEnter builds an Enter KeyPressMsg.
func keyPressEnter() tea.KeyPressMsg { return tea.KeyPressMsg{Code: tea.KeyEnter} }

// keyPressText builds a KeyPressMsg carrying printable text.
func keyPressText(s string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: 0, Text: s}
}
