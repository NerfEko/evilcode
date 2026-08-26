package daemon

import (
	"errors"
	"strings"
	"testing"
)

// D1: a client frame that would overflow the server scanner must be refused
// with a typed, actionable error before it is written — the connection stays
// usable and the caller can tell the user why, instead of the daemon dropping
// the connection mid-prompt.
func TestSendRefusesOversizedFrame(t *testing.T) {
	srv, path := testServer(t)
	defer srv.Close()

	client, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	big := make([]byte, MaxClientFrameBytes+1)
	err = client.Send(ClientMsg{Kind: MsgInput, Text: "x", Images: [][]byte{big}})
	if err == nil {
		t.Fatal("oversized frame was accepted")
	}
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("err = %v, want ErrFrameTooLarge", err)
	}
	if !strings.Contains(err.Error(), "image") {
		t.Errorf("err = %v, want it to point at the images", err)
	}

	// The connection must still work after the refusal.
	if _, err := client.Attach("", 0); err != nil {
		t.Fatalf("connection unusable after frame refusal: %v", err)
	}
}

// D2: the queued-input queue is bounded in count and bytes. Filling a busy
// session's queue past the cap is rejected instead of growing memory without
// limit.
func TestQueuedInputQueueIsBounded(t *testing.T) {
	srv, path := testServer(t)
	defer srv.Close()

	client, err := DialPath(path)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	snap, err := client.Attach("", 0)
	if err != nil {
		t.Fatal(err)
	}

	srv.mu.Lock()
	sess := srv.sessions[snap.Session]
	srv.mu.Unlock()
	if sess == nil {
		t.Fatalf("session %q not registered", snap.Session)
	}

	// Fill the queue to the count cap while the session is busy.
	sess.mu.Lock()
	for i := 0; i < MaxQueuedInputs; i++ {
		sess.queued = append(sess.queued, queuedInput{text: "x"})
	}
	sess.mu.Unlock()

	// One more prompt must be refused, not appended.
	sess.inputRequest("", "one more", false)
	sess.mu.Lock()
	got := len(sess.queued)
	sess.mu.Unlock()
	if got != MaxQueuedInputs {
		t.Fatalf("queue grew to %d after the cap of %d", got, MaxQueuedInputs)
	}
}
