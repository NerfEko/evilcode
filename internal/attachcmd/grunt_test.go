package attachcmd

import (
	"testing"

	"evilcode/internal/daemon"
	"evilcode/internal/tui"
)

// The grunt chain end to end: daemon roster rows carry worker identity, the
// descriptors keep it, and the picker rows mark the crew — so a grunt never
// renders as a normal session anywhere down the line.
func TestGruntIdentitySurvivesToPickerRows(t *testing.T) {
	rows := sessionDescriptors([]daemon.SessionInfo{
		{Name: "wisp-13", Model: "m@mock", Live: true},
		{
			Name: "grunt-3", Model: "small@mock", Live: true, Running: true,
			Worker: true, Task: "wire auth", Tokens: 1500,
			Spawner: "wisp-13", Tail: []string{"reading auth.go"},
		},
	})
	picker := tui.SessionRows(rows)
	if len(picker) != 2 {
		t.Fatalf("mapped %d rows, want 2", len(picker))
	}
	if picker[0].Worker {
		t.Error("normal session row marks Worker")
	}
	grunt := picker[1]
	if !grunt.Worker {
		t.Fatal("grunt row lost the Worker flag through descriptors")
	}
	if grunt.Task != "wire auth" {
		t.Errorf("grunt task = %q", grunt.Task)
	}
}
