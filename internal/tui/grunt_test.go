package tui

import (
	"strings"
	"testing"

	"evilcode/internal/session"
	"evilcode/internal/theme"
)

func TestGruntNameMarksCrew(t *testing.T) {
	for _, name := range []string{"grunt-1", "grunt-42"} {
		if !session.IsGruntName(name) {
			t.Errorf("%q not recognized as a grunt", name)
		}
	}
	for _, name := range []string{"wisp-13", "grunt", "gruntly"} {
		if session.IsGruntName(name) {
			t.Errorf("%q recognized as a grunt", name)
		}
	}
}

func gruntRow(name string) SessionRow {
	return SessionRow{Info: sessionInfo(name, ""), Worker: session.IsGruntName(name)}
}

func TestSortStartRowsSectionsGruntsLast(t *testing.T) {
	rows := []SessionRow{
		{Info: sessionInfo("grunt-2", ""), Running: true, Live: true, Worker: true},
		{Info: sessionInfo("wisp-13", ""), Running: true, Live: true},
		{Info: sessionInfo("grunt-1", ""), Live: true, Worker: true},
		{Info: sessionInfo("ed", ""), Live: true},
	}
	got := sortStartRows(rows)
	var names []string
	for _, r := range got {
		names = append(names, r.Info.Name)
	}
	// Normal sessions keep their relative order up front; grunts follow.
	if names[0] != "wisp-13" || names[1] != "ed" {
		t.Fatalf("normal sessions not first: %v", names)
	}
	if names[2] != "grunt-2" || names[3] != "grunt-1" {
		t.Fatalf("grunt section wrong: %v", names)
	}
}

func TestSessionRowMarksGrunts(t *testing.T) {
	r := NewRenderer(theme.Dracula(), 60)
	row := gruntRow("grunt-3")
	row.Task = "wire auth"
	plain := strings.Join(plainLines(r.sessionRow(row, false, "", 60)), "\n")
	if !strings.Contains(plain, "grunt-3") || !strings.Contains(plain, "grunt") {
		t.Fatalf("grunt row = %q, want the name and the grunt tag", plain)
	}
	if !strings.Contains(plain, "wire auth") {
		t.Fatalf("grunt row = %q, want the task", plain)
	}
	normal := strings.Join(plainLines(r.sessionRow(gruntRow("wisp-13"), false, "", 60)), "\n")
	if strings.Contains(normal, "grunt") {
		t.Fatalf("normal row carries the grunt tag: %q", normal)
	}
}

func TestStartPageButtonRowSectionsGrunts(t *testing.T) {
	r := NewRenderer(theme.Dracula(), 80)
	rows := sortStartRows([]SessionRow{
		{Info: sessionInfo("grunt-1", ""), Live: true, Worker: true},
		{Info: sessionInfo("wisp-13", ""), Live: true},
	})
	line := plainText(r.startPageButtonRow(rows, 0, false, 80))
	if !strings.Contains(line, "grunts") {
		t.Fatalf("button row = %q, want the grunt section divider", line)
	}
	if strings.Index(line, "wisp-13") > strings.Index(line, "grunt-1") {
		t.Fatalf("grunt pill sorts before the normal pill: %q", line)
	}
}

func TestObserveModeHidesComposerUntilTakeover(t *testing.T) {
	m := newTestModel(t)
	m.EnterObserveMode("grunt-3", "wire auth", "small@mock")
	if !m.observeGrunt {
		t.Fatal("EnterObserveMode did not arm observe mode")
	}
	if m.editor.Text != "" {
		t.Fatal("entering observe mode left text in the hidden composer")
	}
	bar := m.bottomBar()
	joined := strings.Join(plainLines(bar), "\n")
	if !strings.Contains(joined, "grunt-3") || !strings.Contains(joined, "wire auth") {
		t.Fatalf("details bar = %q, want name and task", joined)
	}
	if !strings.Contains(strings.ToLower(joined), "alt+o") {
		t.Fatalf("details bar = %q, want the takeover keybind", joined)
	}

	// Composer-bound keys are swallowed; navigation passes through.
	if !m.observeGate("enter", "") {
		t.Error("enter not swallowed while observing")
	}
	if !m.observeGate("x", "x") {
		t.Error("printable text not swallowed while observing")
	}
	if m.observeGate("pgup", "") {
		t.Error("pgup swallowed while observing; scroll must keep working")
	}
	if m.observeGate("ctrl+c", "") {
		t.Error("ctrl+c swallowed while observing; detach must keep working")
	}

	// No prompt leaves an observe window even through a path that
	// bypasses handleKey.
	m.editor.Text = "should not send"
	if _, _ = m.send(); m.editor.Text != "should not send" {
		t.Fatal("send consumed text while observing")
	}

	// Alt+O promotes to a normal session.
	if !m.observeGate("alt+o", "") {
		t.Fatal("alt+o did not promote")
	}
	if m.observeGrunt {
		t.Fatal("still observing after takeover")
	}
}

func TestGruntBarRendersDetails(t *testing.T) {
	r := NewRenderer(theme.Dracula(), 60)
	lines := plainLines(r.RenderGruntBar(GruntDetails{
		Name: "grunt-3", Task: "wire auth", Model: "small@mock", Running: true,
	}))
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"grunt-3", "wire auth", "small@mock", "working"} {
		if !strings.Contains(joined, want) {
			t.Errorf("grunt bar = %q, want %q", joined, want)
		}
	}
	idle := strings.Join(plainLines(r.RenderGruntBar(GruntDetails{Name: "grunt-3"})), "\n")
	if !strings.Contains(idle, "idle") || !strings.Contains(idle, "no brief recorded") {
		t.Errorf("empty grunt bar = %q", idle)
	}
}
