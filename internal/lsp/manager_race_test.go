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

func TestCloseDoesNotPublishASlowStartingClient(t *testing.T) {
	started := make(chan struct{})
	finish := make(chan struct{})
	m := &Manager{
		Root:     t.TempDir(),
		Commands: map[string][]string{"go": {"gopls"}},
		clients:  map[string]*Client{},
		failed:   map[string]error{},
		start: func(context.Context, string, string, []string) (*Client, error) {
			close(started)
			<-finish
			return &Client{Name: "gopls"}, nil
		},
	}
	result := make(chan error, 1)
	go func() {
		_, err := m.For(context.Background(), "main.go")
		result <- err
	}()
	<-started
	m.Close()
	close(finish)
	if err := <-result; err == nil {
		t.Fatal("a client started after manager close")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.clients) != 0 {
		t.Fatalf("closed manager published %d clients", len(m.clients))
	}
}

func TestCanceledStartupIsRetriedForLaterLSPCall(t *testing.T) {
	var attempts int
	m := &Manager{
		Root:     t.TempDir(),
		Commands: map[string][]string{"go": {"gopls"}},
		clients:  map[string]*Client{},
		failed:   map[string]error{},
		start: func(ctx context.Context, name, root string, command []string) (*Client, error) {
			attempts++
			if attempts == 1 {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return &Client{Name: name, Root: root}, nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.For(ctx, "main.go"); err == nil {
		t.Fatal("a canceled startup unexpectedly succeeded")
	}
	if _, err := m.For(context.Background(), "main.go"); err != nil {
		t.Fatalf("later LSP call did not retry: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("startup attempts = %d, want one retry", attempts)
	}
}

func TestDeadlineStartupUsesCooldown(t *testing.T) {
	var attempts int
	m := &Manager{
		Root:       t.TempDir(),
		Commands:   map[string][]string{"go": {"gopls"}},
		clients:    map[string]*Client{},
		failed:     map[string]error{},
		retryAfter: map[string]time.Time{},
		start: func(ctx context.Context, name, root string, command []string) (*Client, error) {
			attempts++
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if _, err := m.For(ctx, "main.go"); err == nil {
		t.Fatal("a timed-out startup unexpectedly succeeded")
	}
	if _, err := m.For(context.Background(), "main.go"); err == nil {
		t.Fatal("cooldown lookup unexpectedly started a server")
	}
	if attempts != 1 {
		t.Fatalf("startup attempts = %d, want one attempt during cooldown", attempts)
	}
}

func TestTimedOutClientWriteIsReplaced(t *testing.T) {
	first := &Client{Name: "gopls", in: &blockingWriteCloser{closed: make(chan struct{})}}
	second := &Client{Name: "gopls"}
	attempts := 0
	m := &Manager{
		Root:     t.TempDir(),
		Commands: map[string][]string{"go": {"gopls"}},
		clients:  map[string]*Client{},
		failed:   map[string]error{},
		start: func(context.Context, string, string, []string) (*Client, error) {
			attempts++
			if attempts == 1 {
				return first, nil
			}
			return second, nil
		},
	}
	client, err := m.For(context.Background(), "main.go")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := client.writeContext(ctx, map[string]any{"method": "stuck"}); err == nil {
		t.Fatal("blocked write unexpectedly succeeded")
	}
	replacement, err := m.For(context.Background(), "main.go")
	if err != nil {
		t.Fatalf("replacement lookup failed: %v", err)
	}
	if replacement != second || attempts != 2 {
		t.Fatalf("replacement = %p, attempts = %d; want second client after one retry", replacement, attempts)
	}
}
