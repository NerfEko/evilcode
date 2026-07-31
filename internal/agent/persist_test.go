package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"evilcode/internal/provider"
)

// H1.7a: the persistence sink could not report a failure, so a full disk or a
// closed store left the durable transcript behind the in-memory conversation
// with nothing said about it. The session looks fine until it is resumed.
func TestPersistFailureIsSurfaced(t *testing.T) {
	a := newTestAgent(t, provider.NewMock("mock", "chat"), nil)
	a.Conv.Persist(func(m provider.Message) error {
		return fmt.Errorf("no space left on device")
	})

	evs, err := collect(t, a, func() error { return a.Run(context.Background(), "hi") })
	if err != nil {
		t.Fatal(err)
	}
	var said bool
	for _, e := range evs {
		if strings.Contains(e.Text, "no space left on device") {
			said = true
		}
		if e.Err != nil && strings.Contains(e.Err.Error(), "no space left on device") {
			said = true
		}
	}
	if !said {
		t.Errorf("a failing session write was never reported; events: %v", kinds(evs))
	}
}

// H1.7b: the conversation lock was released before the sink ran, so two
// concurrent appends could reach memory in one order and disk in the other. A
// resumed session then replays a conversation that never happened.
func TestPersistOrderMatchesMemoryOrder(t *testing.T) {
	conv := NewConversation("system")
	var mu sync.Mutex
	var written []string
	conv.Persist(func(m provider.Message) error {
		mu.Lock()
		defer mu.Unlock()
		written = append(written, m.Content)
		return nil
	})

	var wg sync.WaitGroup
	for i := range 200 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conv.Append(provider.Message{Role: provider.RoleUser, Content: fmt.Sprintf("m%03d", i)})
		}()
	}
	wg.Wait()

	var inMemory []string
	for _, m := range conv.Messages() {
		if m.Role == provider.RoleUser {
			inMemory = append(inMemory, m.Content)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(written) != len(inMemory) {
		t.Fatalf("persisted %d messages, conversation holds %d", len(written), len(inMemory))
	}
	for i := range inMemory {
		if written[i] != inMemory[i] {
			t.Fatalf("disk order diverges from memory order at %d: disk %q, memory %q",
				i, written[i], inMemory[i])
		}
	}
}
