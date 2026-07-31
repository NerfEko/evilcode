package lsp

import (
	"context"
	"sync"
	"testing"
	"time"
)

// H2.11: For dropped the lock across Start and never rechecked, so concurrent
// callers each launched a server. The last one into the map wins; the others
// are a running process, a pipe pair and two goroutines each, leaked until the
// process exits.
func TestConcurrentForStartsOneServerPerLanguage(t *testing.T) {
	var started int
	var mu sync.Mutex
	m := &Manager{
		Root:     t.TempDir(),
		Commands: map[string][]string{"go": {"gopls"}},
		clients:  map[string]*Client{},
		failed:   map[string]error{},
		start: func(ctx context.Context, name, root string, command []string) (*Client, error) {
			mu.Lock()
			started++
			mu.Unlock()
			// A real server takes seconds to come up and index. The window
			// between releasing the lock and recording the client is that
			// whole time, not the nanoseconds a stub would take.
			time.Sleep(20 * time.Millisecond)
			return &Client{Name: name, Root: root}, nil
		},
	}

	const callers = 12
	got := make(chan *Client, callers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			c, err := m.For(context.Background(), "main.go")
			if err != nil {
				return
			}
			got <- c
		}()
	}
	close(start)
	wg.Wait()
	close(got)

	mu.Lock()
	n := started
	mu.Unlock()
	if n != 1 {
		t.Errorf("%d servers were started for one language; %d of them leak", n, n-1)
	}

	var first *Client
	for c := range got {
		if first == nil {
			first = c
			continue
		}
		if c != first {
			t.Error("callers were handed different clients for one language")
		}
	}
}
