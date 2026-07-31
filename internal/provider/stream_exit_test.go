package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// H3.1: the stream producer emitted a parse-error chunk and then continued
// reading, but the consumer returns on the first chunk carrying an error. The
// next send has nobody to receive it, so the producer blocks there for the rest
// of the turn — holding the response body, the connection and its own
// goroutine, while a retry opens another.
func TestAParseErrorEndsTheStream(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		open func(url string) (<-chan Chunk, error)
	}{
		{
			name: "openai",
			body: "data: {not json}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"more\"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{\"content\":\"and more\"}}]}\n\ndata: [DONE]\n\n",
			open: func(url string) (<-chan Chunk, error) {
				return NewOpenAI("oai", url, "k").ChatStream(context.Background(),
					Req{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
			},
		},
		{
			name: "ollama",
			body: "{not json}\n{\"message\":{\"content\":\"more\"}}\n{\"done\":true}\n",
			open: func(url string) (<-chan Chunk, error) {
				return NewOllama("ollama", url, "").ChatStream(context.Background(),
					Req{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			ch, err := tc.open(srv.URL)
			if err != nil {
				t.Fatal(err)
			}

			// Consume exactly as the agent does: stop at the first error.
			var got Chunk
			for c := range ch {
				got = c
				if c.Err != nil {
					break
				}
			}
			if got.Err == nil {
				t.Fatal("the malformed line was not reported")
			}

			// The producer must now be finished. If it is still trying to send,
			// the channel stays open and this blocks — which is exactly the
			// goroutine leak, made observable.
			select {
			case _, open := <-ch:
				if open {
					t.Error("the producer kept sending after the terminal error")
				}
			case <-time.After(2 * time.Second):
				t.Error("the producer is still blocked on a send nobody will receive; " +
					"it holds the response body and its goroutine for the rest of the turn")
			}
		})
	}
}
