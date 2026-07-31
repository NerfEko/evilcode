package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"
)

// H3.14: a malformed frame mid-stream leaves no goroutine behind.
//
// The producer used to emit its parse error and keep reading, blocking on a
// send into a channel the consumer had already abandoned — one goroutine and
// one held connection per turn that hit a bad frame, for the rest of the turn.
func TestMalformedStreamsLeaveNoGoroutines(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A bad frame, then plenty more the producer would otherwise sit on.
		w.Write([]byte("data: {not json}\n\n"))
		for range 50 {
			w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n"))
		}
	}))
	defer srv.Close()

	settle()
	before := runtime.NumGoroutine()

	for range 25 {
		ch, err := NewOpenAI("oai", srv.URL, "k").ChatStream(context.Background(),
			Req{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
		if err != nil {
			t.Fatal(err)
		}
		// Consume as the agent does: stop at the first error and walk away.
		for c := range ch {
			if c.Err != nil {
				break
			}
		}
	}

	settle()
	if grew := runtime.NumGoroutine() - before; grew > 5 {
		t.Errorf("25 malformed streams left %d goroutines behind, each holding a connection", grew)
	}
}

func settle() {
	for range 20 {
		runtime.Gosched()
		time.Sleep(10 * time.Millisecond)
	}
}
