package tuicmd

import (
	"fmt"
	"strings"
	"testing"
)

// H3.6: a session switch called Run from inside itself, so the outer frame's
// defers — the session store, the MCP client, the LSP manager, the agent, the
// memory bank — did not run until the final unwind. Twenty switches meant
// twenty live sets of every MCP server process and language server.
//
// The property is teardown ordering: session N+1 must not begin until session N
// has finished.
func TestASessionSwitchTearsDownBeforeReentering(t *testing.T) {
	var events []string
	sessions := []string{"wisp", "owl", ""}
	i := 0

	err := runSessions([]string{"-m", "some-model"}, func(args []string) (string, error) {
		n := i
		i++
		events = append(events, fmt.Sprintf("enter %d %s", n, strings.Join(args, " ")))
		defer func() { events = append(events, fmt.Sprintf("leave %d", n)) }()
		return sessions[n], nil
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"enter 0 -m some-model",
		"leave 0",
		"enter 1 -resume wisp -m some-model",
		"leave 1",
		"enter 2 -resume owl -m some-model",
		"leave 2",
	}
	if len(events) != len(want) {
		t.Fatalf("events = %v", events)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("event %d = %q, want %q\nall: %v", i, events[i], want[i], events)
		}
	}
}

// An error stops the loop rather than switching again.
func TestASessionErrorEndsTheLoop(t *testing.T) {
	calls := 0
	err := runSessions(nil, func(args []string) (string, error) {
		calls++
		return "next", fmt.Errorf("boom")
	})
	if err == nil {
		t.Fatal("want the error back")
	}
	if calls != 1 {
		t.Errorf("ran %d sessions after an error", calls)
	}
}
