package daemon

import (
	"strings"
	"testing"

	"evilcode/internal/provider"
	"evilcode/internal/session"
)

// H1.9: closing a session cancelled the turn and closed the store in the same
// breath, without waiting for the turn to unwind. Whatever the turn wrote on its
// way out — the partial answer, the interrupted-tool stubs — reached a closed
// store and was dropped, leaving a session file that is missing its own ending.
func TestClosingASessionWaitsForItsTurn(t *testing.T) {
	srv, _ := testServer(t)
	defer srv.Close()

	sess, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	path := sess.built.Store.Path

	sess.Input("hello")
	sess.close()

	msgs, err := session.Messages(path)
	if err != nil {
		t.Fatal(err)
	}
	var onDisk []string
	for _, m := range msgs {
		onDisk = append(onDisk, string(m.Role)+":"+m.Content)
	}

	// Everything the conversation holds must be in the file: the store is
	// closed, so anything missing now is missing for good.
	var inMemory []string
	for _, m := range sess.built.Agent.Conv.Messages() {
		if m.Role == provider.RoleSystem {
			continue
		}
		inMemory = append(inMemory, string(m.Role)+":"+m.Content)
	}
	if len(inMemory) == 0 {
		t.Fatal("the turn never appended anything, so the test proves nothing")
	}
	if len(onDisk) != len(inMemory) {
		t.Fatalf("the session file holds %d messages, the conversation holds %d:\n disk: %v\n mem:  %v",
			len(onDisk), len(inMemory), trunc(onDisk), trunc(inMemory))
	}
	if !strings.Contains(strings.Join(onDisk, "\n"), "hello") {
		t.Error("the prompt never reached the session file")
	}
}

func trunc(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		if len(s) > 60 {
			s = s[:60] + "…"
		}
		out[i] = s
	}
	return out
}
