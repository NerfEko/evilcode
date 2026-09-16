package daemon

import (
	"bufio"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"evilcode/internal/agent"
)

// sseStream is a live SSE response being read frame by frame.
type sseStream struct {
	t    *testing.T
	resp *http.Response
	br   *bufio.Reader
}

// openSSE connects to the events endpoint with the daemon's bearer token.
func openSSE(t *testing.T, srv *Server, addr, name, query string, lastEventID string) *sseStream {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://"+addr+"/api/sessions/"+name+"/events"+query, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+webTokenOf(t, srv))
	req.Header.Set("Accept", "text/event-stream")
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := webClient.Do(req)
	if err != nil {
		t.Fatalf("SSE connect: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("SSE connect: status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("SSE Content-Type = %q", ct)
	}
	return &sseStream{t: t, resp: resp, br: bufio.NewReader(resp.Body)}
}

// next reads one frame, skipping comments. It fails the test on timeout.
func (s *sseStream) next() (id int, kind string, data string) {
	s.t.Helper()
	type result struct {
		id         int
		kind, data string
	}
	done := make(chan result, 1)
	go func() {
		var id int
		var kind, data string
		for {
			line, err := s.br.ReadString('\n')
			if err != nil {
				done <- result{-1, "eof", err.Error()}
				return
			}
			line = strings.TrimRight(line, "\n")
			switch {
			case strings.HasPrefix(line, ":"):
				continue // heartbeat comment
			case strings.HasPrefix(line, "id: "):
				id, _ = strconv.Atoi(strings.TrimPrefix(line, "id: "))
			case strings.HasPrefix(line, "event: "):
				kind = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = strings.TrimPrefix(line, "data: ")
			case line == "":
				if kind != "" || data != "" {
					done <- result{id, kind, data}
					return
				}
			}
		}
	}()
	select {
	case r := <-done:
		if r.kind == "eof" {
			s.t.Fatalf("SSE stream ended: %v", r.data)
		}
		return r.id, r.kind, r.data
	case <-time.After(5 * time.Second):
		s.t.Fatal("timed out waiting for an SSE frame")
		return 0, "", ""
	}
}

func publish(t *testing.T, sess *Session, text string) int {
	t.Helper()
	sess.publishEvent(agent.Event{Kind: agent.EventNotice, Text: text})
	return sess.ring.Seq()
}

func TestWebEventsConnectSequenceMirrorsAttach(t *testing.T) {
	srv, addr := webTestServer(t)
	sess, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}

	// Events published before the connect: a fresh SSE connect replays only
	// the turn in flight (nothing here), so the snapshot is the whole story.
	publish(t, sess, "before connect")
	stream := openSSE(t, srv, addr, sess.Name, "", "")

	id, kind, data := stream.next()
	if kind != "snapshot" {
		t.Fatalf("first frame kind = %q, want snapshot", kind)
	}
	if !strings.Contains(data, `"session":"`+sess.Name+`"`) {
		t.Errorf("snapshot data %q does not name the session", data)
	}
	if !strings.Contains(data, `"kind":"snapshot"`) {
		t.Errorf("data %q is not a ServerMsg frame", data)
	}

	// A live event arrives as an event frame carrying the ring sequence.
	want := publish(t, sess, "live notice")
	id, kind, data = stream.next()
	if kind != "event" || !strings.Contains(data, "live notice") {
		t.Fatalf("live frame = %q %q, want the notice event", kind, data)
	}
	if id != want {
		t.Errorf("frame id = %d, want the ring sequence %d", id, want)
	}
}

func TestWebEventsSinceReplaysExactlyTheGap(t *testing.T) {
	srv, addr := webTestServer(t)
	sess, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}

	seqs := []int{}
	for _, text := range []string{"one", "two", "three", "four"} {
		seqs = append(seqs, publish(t, sess, text))
	}

	// A reconnecting client that saw seqs[1] ("two") gets exactly three and
	// four — the gap after what it rendered, not the whole ring.
	stream := openSSE(t, srv, addr, sess.Name, "?since="+strconv.Itoa(seqs[1]), "")
	id, kind, data := stream.next()
	if kind != "snapshot" {
		t.Fatalf("first frame = %q, want the snapshot first (self-healing)", kind)
	}
	id, kind, data = stream.next()
	if kind != "event" || !strings.Contains(data, `"three"`) {
		t.Fatalf("first replayed event = %q %q, want three", kind, data)
	}
	if id <= seqs[1] {
		t.Errorf("replayed id %d is not past since=%d", id, seqs[1])
	}
	_, kind, data = stream.next()
	if !strings.Contains(data, `"four"`) {
		t.Fatalf("second replayed event = %q, want four", data)
	}

	// Last-Event-ID behaves like since, so a browser reconnect needs nothing
	// but its automatic header.
	stream2 := openSSE(t, srv, addr, sess.Name, "", strconv.Itoa(seqs[2]))
	_, kind, _ = stream2.next() // snapshot always
	id, kind, data = stream2.next()
	if kind != "event" || !strings.Contains(data, `"four"`) {
		t.Fatalf("Last-Event-ID replay = %q %q, want only four", kind, data)
	}
}

func TestWebEventsHeartbeatKeepsTheStreamWarm(t *testing.T) {
	srv, addr := webTestServer(t)
	sess, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	oldHB, oldWT := webSSEHeartbeat, webSSEWriteTimeout
	webSSEHeartbeat, webSSEWriteTimeout = 50*time.Millisecond, 2*time.Second
	t.Cleanup(func() { webSSEHeartbeat, webSSEWriteTimeout = oldHB, oldWT })

	stream := openSSE(t, srv, addr, sess.Name, "", "")
	stream.next() // snapshot

	// No events are published; the next bytes must be the ping comment.
	line := make(chan string, 1)
	go func() {
		l, err := stream.br.ReadString('\n')
		if err != nil {
			line <- "err: " + err.Error()
			return
		}
		line <- strings.TrimRight(l, "\n")
	}()
	select {
	case got := <-line:
		if !strings.HasPrefix(got, ": ping") {
			t.Errorf("idle stream produced %q, want the ping comment", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no heartbeat arrived")
	}
}

func TestWebEventsSlowClientDoesNotStallTheSession(t *testing.T) {
	srv, addr := webTestServer(t)
	sess, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}

	baseSeq := publish(t, sess, "base-marker")
	stream := openSSE(t, srv, addr, sess.Name, "", "")
	if _, kind, _ := stream.next(); kind != "snapshot" {
		t.Fatalf("first frame kind = %q, want snapshot", kind)
	}

	// The §5 rule: a web client that cannot keep up must never stall the
	// session's event pump. broadcast evicts on the first full send instead
	// of blocking; publish a burst far past the subscription capacity and
	// bound the whole thing with a deadline.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 2000; i++ {
			sess.publishEvent(agent.Event{Kind: agent.EventTextDelta, Text: strings.Repeat("x", 64)})
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("publishing stalled: the slow client is blocking the session pump")
	}

	// Fill-queue observes termination: the overflowed subscription is evicted.
	waitFor(t, "overflow eviction", func() bool {
		sess.mu.Lock()
		defer sess.mu.Unlock()
		return len(sess.subs) == 0
	})
	// The SSE loop exits, terminating the stream; draining the buffered bytes
	// must reach EOF instead of hanging on a live stream.
	terminated := make(chan struct{})
	go func() {
		defer close(terminated)
		for {
			if _, err := stream.br.ReadString('\n'); err != nil {
				return
			}
		}
	}()
	select {
	case <-terminated:
	case <-time.After(5 * time.Second):
		t.Fatal("slow SSE stream did not terminate after overflow")
	}

	// Clients decremented once on eviction; the deferred unsubscribe after the
	// handler returns is a no-op.
	for _, info := range srv.Sessions() {
		if info.Name == sess.Name && info.Clients != 0 {
			t.Errorf("Clients = %d after overflow, want 0 (evicted once)", info.Clients)
		}
	}

	// Reconnect with the last sequence from before the burst: the ring replays
	// the gap the terminated stream missed.
	reconnect := openSSE(t, srv, addr, sess.Name, "?since="+strconv.Itoa(baseSeq), "")
	if _, kind, _ := reconnect.next(); kind != "snapshot" {
		t.Fatalf("reconnect first frame kind = %q, want snapshot", kind)
	}
	id, kind, data := reconnect.next()
	if kind != "event" {
		t.Fatalf("reconnect replay kind = %q, want event", kind)
	}
	if id <= baseSeq {
		t.Errorf("replayed id %d is not past since=%d", id, baseSeq)
	}
	if !strings.Contains(data, strings.Repeat("x", 16)) {
		t.Errorf("reconnect replay data does not carry the burst gap: %q", data)
	}
}

func TestWebEventsUnsubscribesOnDisconnect(t *testing.T) {
	srv, addr := webTestServer(t)
	sess, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}

	// A client that hangs up must leave the session's subscriber set, or the
	// idle watchdog would keep a dead window alive forever (decision 9).
	stream := openSSE(t, srv, addr, sess.Name, "", "")
	stream.next() // snapshot, proving the subscription exists
	stream.resp.Body.Close()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		sess.mu.Lock()
		subs := len(sess.subs)
		sess.mu.Unlock()
		if subs == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the SSE subscription outlived its connection")
}

func TestWebEventsStoredSessionIs404(t *testing.T) {
	srv, addr := webTestServer(t)
	bigStoredSession(t, "ghost", 1)

	resp := authedWebGet(t, srv, addr, "/api/sessions/ghost/events")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("stored session events: status %d, want 404 (no live ring to tail)", resp.StatusCode)
	}
}

// TestWebSubscriptionAccountsForIdleExpiry follows the existing watchdog
// harness (session_idle_test.go): the SSE subscription's presence in
// sess.subs is what makes a browser tab a window, so an open stream keeps the
// hydrated runtime alive and a closed one starts the countdown (decision 9).
func TestWebSubscriptionAccountsForIdleExpiry(t *testing.T) {
	srv, addr := webTestServer(t)
	sess, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	name := sess.Name

	stream := openSSE(t, srv, addr, name, "", "")
	stream.next() // snapshot: the subscription is established

	now := time.Now()
	expired := func() bool {
		sess.mu.Lock()
		sess.idleSince = now.Add(-SessionIdleTimeout - time.Second)
		sess.mu.Unlock()
		srv.expireIdleSessions(now)
		srv.mu.Lock()
		_, live := srv.sessions[name]
		srv.mu.Unlock()
		return live
	}
	if live := expired(); !live {
		t.Fatal("the watchdog expired a session with an open web stream")
	}

	// The tab goes away: the request context ends, the handler unsubscribes,
	// and the next sweep tears the runtime down like any windowless session.
	stream.resp.Body.Close()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		sess.mu.Lock()
		subs := len(sess.subs)
		sess.mu.Unlock()
		if subs == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if live := expired(); live {
		t.Fatal("a closed web subscription kept the session hydrated past its idle timeout")
	}
}
