package daemon

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"evilcode/internal/agent"
	"evilcode/internal/config"
	"evilcode/internal/provider"
)

// Batch3 (EC-005 + EC-F-001 + EC-013 + EC-I-001) battery. Each test names the
// criterion it pins; all run against real Servers through the same entries a
// client would use.

// --- EC-005: input status ----------------------------------------------------

// TestInputWhitespaceRefusedAtBothEntries pins the EC-001 agreement: a
// whitespace-only prompt with no images is refused at both entries before the
// queue or a turn could take ownership of anything.
func TestInputWhitespaceRefusedAtBothEntries(t *testing.T) {
	srv, addr := webTestServer(t)
	sess, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}

	resp := webPostJSON(t, srv, addr, "/api/sessions/"+sess.Name+"/input", map[string]any{"text": "   \t\n "})
	webErrorOf(t, resp, http.StatusBadRequest, "/input")

	client, err := DialPath(srv.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	snap, err := client.Attach(sess.Name, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Send(ClientMsg{Kind: MsgInput, Session: snap.Session, Text: "  "}); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	type result struct {
		msg ServerMsg
		err error
	}
	got := make(chan result, 1)
	go func() {
		msg, err := client.Recv()
		got <- result{msg, err}
	}()
	select {
	case <-deadline:
		t.Fatal("timed out waiting for the whitespace refusal")
	case r := <-got:
		if r.err != nil {
			t.Fatalf("whitespace input dropped the connection instead of MsgError: %v", r.err)
		}
		if r.msg.Kind != MsgError || !strings.Contains(r.msg.Err, "text or at least one image") {
			t.Fatalf("whitespace frame = %+v, want the actionable MsgError", r.msg)
		}
	}
}

// TestWebInputImageOnlyAccepted pins the other half of the EC-001 agreement:
// an image-only prompt (blank text, real image bytes) is accepted and runs.
func TestWebInputImageOnlyAccepted(t *testing.T) {
	srv, addr := webTestServer(t)
	sess, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	png := []byte("\x89PNG image-only web turn")
	resp := webPostJSON(t, srv, addr, "/api/sessions/"+sess.Name+"/input", map[string]any{
		"text":   "   ",
		"images": []string{base64.StdEncoding.EncodeToString(png)},
	})
	webOKOf(t, resp, "/input")
	waitIdle(t, sess, "the image-only turn to end")

	msgs, err := srv.sessionLogMessages(sess.Name)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if m.Role == provider.RoleUser && len(m.Images) == 1 && string(m.Images[0]) == string(png) {
			return
		}
	}
	t.Fatalf("durable log carries no image-only user message: %+v", msgs)
}

// TestWebInputStartedQueuedFull pins the EC-005 HTTP mapping: an idle session
// starts (200), a busy session queues (200, FIFO order preserved), and a full
// queue is 429 with the uniform shape. Bounds and FIFO are unchanged.
func TestWebInputStartedQueuedFull(t *testing.T) {
	srv, addr := webScenarioServer(t, "ask")
	sess, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	tap := tapEvents(t, sess)

	// Started: idle session, 200.
	resp := webPostJSON(t, srv, addr, "/api/sessions/"+sess.Name+"/input", map[string]any{"text": "first-input"})
	webOKOf(t, resp, "/input")
	ask := tap.next(func(e *agent.Event) bool { return e.Kind == agent.EventAsk }, "the pending ask")
	if ask.Ask == nil || ask.Ask.ID == "" {
		t.Fatalf("ask event carries no id: %+v", ask)
	}

	// Queued: the turn is blocked on the ask, so the next prompt waits.
	resp = webPostJSON(t, srv, addr, "/api/sessions/"+sess.Name+"/input", map[string]any{"text": "second-input"})
	webOKOf(t, resp, "/input")
	sess.mu.Lock()
	if got := len(sess.queued); got != 1 || sess.queued[0].text != "second-input" {
		sess.mu.Unlock()
		t.Fatalf("queued = %+v, want the one waiting prompt", sess.queued)
	}
	// Fill to the bound while the turn is still blocked.
	for i := 0; i < MaxQueuedInputs-1; i++ {
		sess.queued = append(sess.queued, queuedInput{text: fmt.Sprintf("pad-%d", i)})
	}
	sess.mu.Unlock()

	// Full: 429 with the uniform error shape, bounds untouched.
	resp = webPostJSON(t, srv, addr, "/api/sessions/"+sess.Name+"/input", map[string]any{"text": "overflow-input"})
	msg := webErrorOf(t, resp, http.StatusTooManyRequests, "/input")
	if !strings.Contains(msg, "queue is full") {
		t.Errorf("429 body %q does not name the full queue", msg)
	}
	sess.mu.Lock()
	if got := len(sess.queued); got != MaxQueuedInputs {
		t.Errorf("queue = %d after refusal, want the bound %d kept", got, MaxQueuedInputs)
	}
	// Drop the artificial pads; the genuinely queued prompt stays first.
	sess.queued = append([]queuedInput(nil), sess.queued[:1]...)
	sess.mu.Unlock()

	// Answering lets the blocked turn end, and the queued prompt runs after
	// it: FIFO order across the started/queued seam.
	resp = webPostJSON(t, srv, addr, "/api/sessions/"+sess.Name+"/answer", map[string]any{
		"request_id": ask.Ask.ID, "answers": []string{"Exponential backoff"},
	})
	webOKOf(t, resp, "/answer")
	waitIdle(t, sess, "both turns to finish")

	msgs, err := srv.sessionLogMessages(sess.Name)
	if err != nil {
		t.Fatal(err)
	}
	first, second := -1, -1
	for i, m := range msgs {
		if m.Role != provider.RoleUser {
			continue
		}
		if strings.Contains(m.Content, "first-input") && first < 0 {
			first = i
		}
		if strings.Contains(m.Content, "second-input") && second < 0 {
			second = i
		}
	}
	if first < 0 || second < 0 {
		t.Fatalf("durable log lost a turn (first %d second %d): %+v", first, second, msgs)
	}
	if first > second {
		t.Errorf("FIFO broken: queued prompt ran at %d before the started one at %d", second, first)
	}
}

// TestWebInputClosingIsConflict pins the EC-005 closing mapping: a session
// that is tearing down answers 409, and the socket answers MsgError.
func TestWebInputClosingIsConflict(t *testing.T) {
	srv, addr := webTestServer(t)
	sess, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	sess.mu.Lock()
	sess.closing = true
	sess.mu.Unlock()

	resp := webPostJSON(t, srv, addr, "/api/sessions/"+sess.Name+"/input", map[string]any{"text": "too late"})
	webErrorOf(t, resp, http.StatusConflict, "/input")
}

// TestSocketInputRefusalsAreMsgError pins the EC-005 socket half: whitespace
// without images, a full queue, and a closing session are all MsgError
// frames, never silence and never a dropped connection.
func TestSocketInputRefusalsAreMsgError(t *testing.T) {
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
	sess, err := srv.Open(snap.Session)
	if err != nil {
		t.Fatal(err)
	}
	recvErr := func(desc string) ServerMsg {
		t.Helper()
		// A refusal is both an async notice for observers and a synchronous
		// MsgError for the sender; the two race down different paths, so
		// skip observer echoes until the refusal arrives.
		deadline := time.After(5 * time.Second)
		for {
			type result struct {
				msg ServerMsg
				err error
			}
			got := make(chan result, 1)
			go func() {
				msg, err := client.Recv()
				got <- result{msg, err}
			}()
			select {
			case <-deadline:
				t.Fatalf("timed out waiting for the %s refusal", desc)
			case r := <-got:
				if r.err != nil {
					t.Fatalf("%s: connection dropped instead of MsgError: %v", desc, r.err)
				}
				if r.msg.Kind == MsgError && r.msg.Err != "" {
					return r.msg
				}
			}
		}
	}

	// Whitespace-only with no images.
	if err := client.Send(ClientMsg{Kind: MsgInput, Session: snap.Session, Text: "   "}); err != nil {
		t.Fatal(err)
	}
	if msg := recvErr("whitespace"); !strings.Contains(msg.Err, "text or at least one image") {
		t.Errorf("whitespace refusal = %q, want the actionable error", msg.Err)
	}

	// Full queue: hold the reservation and fill to the bound.
	done, ok := sess.beginTurn(func() {})
	if !ok {
		t.Fatal("could not hold the turn reservation")
	}
	sess.mu.Lock()
	for i := 0; i < MaxQueuedInputs; i++ {
		sess.queued = append(sess.queued, queuedInput{text: fmt.Sprintf("pad-%d", i)})
	}
	sess.mu.Unlock()
	if err := client.Send(ClientMsg{Kind: MsgInput, Session: snap.Session, Text: "overflow"}); err != nil {
		t.Fatal(err)
	}
	if msg := recvErr("queue-full"); !strings.Contains(msg.Err, "queue is full") {
		t.Errorf("queue-full refusal = %q, want the full-queue error", msg.Err)
	}
	sess.mu.Lock()
	sess.queued = nil
	sess.running = false
	sess.cancel, sess.turnDone = nil, nil
	close(done)
	sess.closing = true
	sess.mu.Unlock()

	// Closing session.
	if err := client.Send(ClientMsg{Kind: MsgInput, Session: snap.Session, Text: "too late"}); err != nil {
		t.Fatal(err)
	}
	if msg := recvErr("closing"); !strings.Contains(msg.Err, "closing") {
		t.Errorf("closing refusal = %q, want the closing error", msg.Err)
	}
}

// --- EC-F-001: Oldest --------------------------------------------------------

// TestSnapshotTruncationTracksOldest pins the fitter contract: a truncated
// snapshot reports 0 < Oldest <= original length with Messages[0] at that
// shaped index, the envelope lands Oldest on the original length, and a
// snapshot that fits keeps Oldest 0.
func TestSnapshotTruncationTracksOldest(t *testing.T) {
	half := MaxServerFrameBytes/2 + 2048
	orig := []Message{
		{Role: "user", Content: "oldest"},
		{Role: "user", Content: strings.Repeat("x", half)},
		{Role: "user", Content: strings.Repeat("y", half)},
	}
	out, _, ok := fitServerFrame(ServerMsg{Kind: MsgSnapshot, Snapshot: &Snapshot{Session: "s", Messages: orig}})
	if !ok {
		t.Fatal("snapshot could not be fitted at all")
	}
	if !out.Snapshot.Truncated {
		t.Fatal("truncated snapshot was not flagged")
	}
	if o := out.Snapshot.Oldest; o <= 0 || o > len(orig) {
		t.Fatalf("Oldest = %d, want 0 < Oldest <= %d", o, len(orig))
	}
	if out.Snapshot.Messages[0].Content != orig[out.Snapshot.Oldest].Content {
		t.Error("Messages[0] is not the shaped message at Oldest")
	}
	if n := len(out.Snapshot.Messages); n != len(orig)-out.Snapshot.Oldest {
		t.Errorf("kept %d messages from Oldest %d of %d, want the newest tail", n, out.Snapshot.Oldest, len(orig))
	}

	// The last removal lands Oldest on the original length.
	single := &Snapshot{Session: "s", Messages: []Message{
		{Role: "user", Content: strings.Repeat("x", MaxServerFrameBytes)},
	}}
	out, _, ok = fitServerFrame(ServerMsg{Kind: MsgSnapshot, Snapshot: single})
	if !ok {
		t.Fatal("envelope-only downgrade failed")
	}
	if out.Snapshot.Oldest != 1 {
		t.Errorf("envelope Oldest = %d, want the original length 1", out.Snapshot.Oldest)
	}
	if len(out.Snapshot.Messages) != 0 || !out.Snapshot.Truncated {
		t.Errorf("envelope = %d msgs truncated %v, want empty and flagged", len(out.Snapshot.Messages), out.Snapshot.Truncated)
	}

	// A snapshot that fits is untouched.
	small := &Snapshot{Session: "s", Messages: []Message{{Role: "user", Content: "hi"}}}
	out, _, ok = fitServerFrame(ServerMsg{Kind: MsgSnapshot, Snapshot: small})
	if !ok {
		t.Fatal("small snapshot could not be fitted")
	}
	if out.Snapshot.Truncated || out.Snapshot.Oldest != 0 {
		t.Errorf("fitting clean snapshot set truncated=%v oldest=%d, want false/0",
			out.Snapshot.Truncated, out.Snapshot.Oldest)
	}
}

// shapedContent names the content bigStoredSession-equivalent shaped index i
// holds: even indexes are questions, odd ones answers.
func shapedPairContent(i int) string {
	if i%2 == 0 {
		return fmt.Sprintf("q%d", i/2)
	}
	return fmt.Sprintf("a%d", i/2)
}

// TestHistoryPageSeamsWithTruncatedSnapshot pins the pager agreement: with a
// truncated snapshot at Oldest, /messages?before=Oldest returns the adjacent
// page — tiling exactly (oldest+len == before) with no overlap.
func TestHistoryPageSeamsWithTruncatedSnapshot(t *testing.T) {
	srv, addr := webTestServer(t)
	bigStoredSession(t, "seamlog", 60) // 120 shaped messages q0/a0..q59/a59

	// The same 120 shaped messages, padded outside Content so the pager and
	// the snapshot agree on every content while the frame still overflows.
	orig := make([]Message, 0, 120)
	for i := 0; i < 120; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		orig = append(orig, Message{
			Role: role, Content: shapedPairContent(i),
			Reasoning: strings.Repeat("r", 64<<10),
		})
	}
	out, buf, ok := fitServerFrame(ServerMsg{Kind: MsgSnapshot, Snapshot: &Snapshot{Session: "seam", Messages: orig}})
	if !ok {
		t.Fatal("snapshot could not be fitted at all")
	}
	if !out.Snapshot.Truncated {
		t.Fatal("padded snapshot was not truncated; the seam test needs a truncation")
	}
	oldest := out.Snapshot.Oldest
	if oldest <= 0 || oldest > len(orig) {
		t.Fatalf("Oldest = %d, want 0 < Oldest <= %d", oldest, len(orig))
	}
	if len(buf) > MaxServerFrameBytes {
		t.Fatalf("fitted frame is %d bytes, over the cap", len(buf))
	}
	if got := out.Snapshot.Messages[0].Content; got != shapedPairContent(oldest) {
		t.Fatalf("snapshot head = %q, want shaped[%d] = %q", got, oldest, shapedPairContent(oldest))
	}

	page := fetchPage(t, srv, addr, "seamlog", fmt.Sprintf("?before=%d&limit=50", oldest))
	if got := page.Oldest + len(page.Messages); got != oldest {
		t.Fatalf("page seam broken: oldest %d + %d msgs != snapshot Oldest %d",
			page.Oldest, len(page.Messages), oldest)
	}
	if n := len(page.Messages); n == 0 {
		t.Fatal("page above a truncation is empty; want the adjacent messages")
	} else if got := page.Messages[n-1].Content; got != shapedPairContent(oldest-1) {
		t.Errorf("page tail = %q, want shaped[%d] = %q (adjacent, no gap)",
			got, oldest-1, shapedPairContent(oldest-1))
	}
	for _, m := range page.Messages {
		if m.Content == shapedPairContent(oldest) {
			t.Errorf("page overlaps the snapshot head %q", m.Content)
			break
		}
	}
}

// TestInitialSnapshotsObeyByteCap pins the wire half: a conversation that
// overflows the frame budget still attaches over the socket and still opens
// over SSE, truncated with Oldest set, and neither initial frame exceeds the
// cap.
func TestInitialSnapshotsObeyByteCap(t *testing.T) {
	srv, spath := testServer(t)
	sess, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	name := sess.Name
	for i := 0; i < 3; i++ {
		sess.built.Agent.Conv.Append(provider.Message{
			Role: provider.RoleUser, Content: fmt.Sprintf("big-%d %s", i, strings.Repeat("x", 3<<20)),
		})
	}

	client, err := DialPath(spath)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	snap, err := client.Attach(name, 0)
	if err != nil {
		t.Fatalf("attach to an oversized conversation failed: %v", err)
	}
	if !snap.Truncated || snap.Oldest <= 0 {
		t.Errorf("socket snapshot truncated=%v oldest=%d, want flagged with Oldest>0",
			snap.Truncated, snap.Oldest)
	}
	if buf, err := json.Marshal(snap); err != nil || len(buf) > MaxServerFrameBytes {
		t.Errorf("socket snapshot frame is %d bytes (err %v), over the cap", len(buf), err)
	}

	wsrv, waddr := webTestServer(t)
	wsess, err := wsrv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		wsess.built.Agent.Conv.Append(provider.Message{
			Role: provider.RoleUser, Content: fmt.Sprintf("big-%d %s", i, strings.Repeat("x", 3<<20)),
		})
	}
	stream := openSSE(t, wsrv, waddr, wsess.Name, "", "")
	_, kind, data := stream.next()
	if kind != "snapshot" {
		t.Fatalf("first SSE frame = %q, want snapshot", kind)
	}
	if len(data) > MaxServerFrameBytes {
		t.Errorf("SSE snapshot data is %d bytes, over the cap", len(data))
	}
	var frame ServerMsg
	if err := json.Unmarshal([]byte(data), &frame); err != nil {
		t.Fatalf("SSE snapshot data is not a ServerMsg: %v", err)
	}
	if frame.Snapshot == nil || !frame.Snapshot.Truncated || frame.Snapshot.Oldest <= 0 {
		t.Errorf("SSE snapshot = %+v, want truncated with Oldest>0", frame.Snapshot)
	}
}

// --- EC-013: body read deadline ------------------------------------------------

// TestWebBodyDeadlineClosesStalledPost pins the read-side bound: a client
// that stalls mid-body is closed or answered within the (shrunk) deadline
// instead of holding a handler goroutine.
func TestWebBodyDeadlineClosesStalledPost(t *testing.T) {
	srv, addr := webTestServer(t)
	old := webBodyReadTimeout
	webBodyReadTimeout = 300 * time.Millisecond
	t.Cleanup(func() { webBodyReadTimeout = old })
	sess, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}

	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	head := fmt.Sprintf("POST /api/sessions/%s/input HTTP/1.1\r\nHost: %s\r\n"+
		"Authorization: Bearer %s\r\nOrigin: http://%s\r\n"+
		"Content-Type: application/json\r\nContent-Length: 64\r\nConnection: close\r\n\r\n",
		sess.Name, addr, webTokenOf(t, srv), addr)
	if _, err := io.WriteString(conn, head); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(conn, `{"text": "hel`); err != nil {
		t.Fatal(err)
	}
	// Stall: the rest of the body never arrives.
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	start := time.Now()
	buf := make([]byte, 4096)
	n, rerr := conn.Read(buf)
	elapsed := time.Since(start)
	if elapsed > 4*time.Second {
		t.Fatalf("stalled POST held the handler %v, past the shortened deadline", elapsed)
	}
	body := string(buf[:n])
	if rerr != nil {
		return // closed: the deadline fired
	}
	if len(body) >= 12 && body[8] == ' ' && (body[9] == '4' || body[9] == '5') {
		return // answered 4xx/5xx within the deadline
	}
	t.Fatalf("stalled POST answered %q, want a close or a 4xx/5xx", body)
}

// TestWebBodyDeadlineLeavesSSEAlive pins the untouched half: a stream that
// idles past the (shrunk) body deadline still delivers live events, so the
// bound never grew a WriteTimeout.
func TestWebBodyDeadlineLeavesSSEAlive(t *testing.T) {
	srv, addr := webTestServer(t)
	old := webBodyReadTimeout
	webBodyReadTimeout = 200 * time.Millisecond
	t.Cleanup(func() { webBodyReadTimeout = old })

	sess, err := srv.Open("")
	if err != nil {
		t.Fatal(err)
	}
	stream := openSSE(t, srv, addr, sess.Name, "", "")
	if _, kind, _ := stream.next(); kind != "snapshot" {
		t.Fatalf("first frame = %q, want snapshot", kind)
	}
	time.Sleep(600 * time.Millisecond) // well past the body deadline
	publish(t, sess, "stream survives the body deadline")
	_, kind, data := stream.next()
	if kind != "event" || !strings.Contains(data, "stream survives the body deadline") {
		t.Fatalf("post-deadline frame = %q %q, want the live event", kind, data)
	}
}

// --- EC-I-001: models singleflight --------------------------------------------

// TestWebModelsSingleFlight pins the in-flight dedup: 20 concurrent cache
// misses against a slow counting fake produce one upstream aggregation and
// identical answers, with no lock held across the fetch.
func TestWebModelsSingleFlight(t *testing.T) {
	var tagsHits atomic.Int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		tagsHits.Add(1)
		time.Sleep(250 * time.Millisecond) // force the burst to overlap
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"models":[{"name":"fake-small","size":1234,"details":{"parameter_size":"1B"}}]}`)
	}))
	t.Cleanup(fake.Close)

	srv, addr := webTestServer(t)
	srv.mu.Lock()
	srv.Cfg.Providers = []config.ProviderConfig{{Name: "fake", Kind: config.KindOllama, BaseURL: fake.URL}}
	srv.mu.Unlock()
	oldTTL := webModelsCacheTTL
	webModelsCacheTTL = time.Hour
	t.Cleanup(func() { webModelsCacheTTL = oldTTL })

	const burst = 20
	bodies := make([]string, burst)
	codes := make([]int, burst)
	var wg sync.WaitGroup
	for i := 0; i < burst; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req, err := http.NewRequest(http.MethodGet, "http://"+addr+"/api/models", nil)
			if err != nil {
				return
			}
			req.Header.Set("Authorization", "Bearer "+webTokenOf(t, srv))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return
			}
			defer resp.Body.Close()
			raw, err := io.ReadAll(resp.Body)
			if err != nil {
				return
			}
			codes[i], bodies[i] = resp.StatusCode, string(raw)
		}(i)
	}
	wg.Wait()
	for i := 0; i < burst; i++ {
		if codes[i] != http.StatusOK {
			t.Fatalf("request %d: status %d, want 200 (body %q)", i, codes[i], bodies[i])
		}
		if bodies[i] != bodies[0] {
			t.Fatalf("request %d body differs from request 0:\n%q\n%q", i, bodies[i], bodies[0])
		}
	}
	if got := tagsHits.Load(); got != 1 {
		t.Errorf("upstream /api/tags hit %d times for %d concurrent misses, want 1", got, burst)
	}
	var entries []webModelEntry
	if err := json.Unmarshal([]byte(bodies[0]), &entries); err != nil {
		t.Fatalf("catalog is not JSON: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "fake-small" {
		t.Errorf("catalog = %+v, want the fake's one model", entries)
	}
}
