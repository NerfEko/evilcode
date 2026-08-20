package tui

import (
	"testing"
	"time"

	"evilcode/internal/session"
)

// TestStartPageOrderingAndSelectionFollows verifies the two roster-stability
// rules: active sessions move to the front (idle/completed to the right), and
// when a refresh reorders the list the selection follows the same session
// instead of snapping to whatever lands at the old index.
func TestStartPageOrderingAndSelectionFollows(t *testing.T) {
	m := NewModel(nil, HeaderState{SessionName: "current", Model: "mock"})
	m.width, m.height = 90, 24
	// No dataDir so loadStartPreview is a no-op; this test is about order/selection.

	info := func(name string, mod time.Time) session.Info {
		return session.Info{Name: name, Modified: mod}
	}

	// First roster: an idle session, a completed session, and a running one.
	// Expected order after sort: running (active) first, then idle (live), then completed.
	first := []SessionRow{
		{Info: info("moth", time.Now().Add(-10*time.Minute)), Live: false},
		{Info: info("fox", time.Now().Add(-1*time.Minute)), Live: true},
		{Info: info("bat", time.Now().Add(-2*time.Minute)), Live: true, Running: true},
	}
	m.applyStartSessions(startSessionsMsg{rows: first})
	got := rowNames(m.startRows)
	want := []string{"bat", "fox", "moth"} // active, live-idle, completed
	if !equalSlices(got, want) {
		t.Fatalf("first sort = %v, want %v", got, want)
	}

	// User selects fox (the live-idle one) and activates it.
	m.startSelected = 1
	m.startActive = true

	// Second roster: bat stops running (becomes idle), fox starts running.
	// Active tier is now {fox}; live-idle {bat}; completed {moth}. The selection
	// was on fox, which moves from index 1 to index 0 — it must stay on fox.
	second := []SessionRow{
		{Info: info("moth", time.Now().Add(-10*time.Minute)), Live: false},
		{Info: info("bat", time.Now().Add(-2*time.Minute)), Live: true},
		{Info: info("fox", time.Now().Add(-30*time.Second)), Live: true, Running: true},
	}
	m.applyStartSessions(startSessionsMsg{rows: second})
	got = rowNames(m.startRows)
	want = []string{"fox", "bat", "moth"} // fox active, bat live-idle, moth completed
	if !equalSlices(got, want) {
		t.Fatalf("second sort = %v, want %v", got, want)
	}
	if m.startRows[m.startSelected].Info.Name != "fox" {
		t.Errorf("selection followed the index, not the session: selected=%q, want fox",
			m.startRows[m.startSelected].Info.Name)
	}
	if !m.startActive {
		t.Error("selection lost its active highlight across the refresh")
	}

	// Third roster: fox drops off entirely. The selection must not jump to a
	// random session; the active highlight drops so a reflexive Enter is safe.
	third := []SessionRow{
		{Info: info("bat", time.Now().Add(-2*time.Minute)), Live: true},
		{Info: info("moth", time.Now().Add(-10*time.Minute)), Live: false},
	}
	m.applyStartSessions(startSessionsMsg{rows: third})
	if m.startActive {
		t.Error("active highlight should drop when the selected session disappears")
	}

	// A waiting-on-answer session outranks a plain live-idle one even if older.
	waiting := []SessionRow{
		{Info: info("old", time.Now().Add(-3*time.Hour)), Live: true, Pending: 1},
		{Info: info("fresh", time.Now()), Live: true},
	}
	m.applyStartSessions(startSessionsMsg{rows: waiting})
	got = rowNames(m.startRows)
	want = []string{"old", "fresh"} // pending is active, so it leads despite age
	if !equalSlices(got, want) {
		t.Fatalf("pending sort = %v, want %v", got, want)
	}
}

func rowNames(rows []SessionRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Info.Name
	}
	return out
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
