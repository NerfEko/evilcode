package session

import (
	"sync"
	"testing"
)

// H2.12: Create picked a name no *existing* session held and then opened it
// without O_EXCL. Two creators running together both list, both see the name
// free, and both append to one log — two conversations interleaved in one file,
// and each store believing it owns it.
func TestConcurrentCreatesGetDistinctSessions(t *testing.T) {
	dir := t.TempDir()

	const creators = 16
	names := make(chan string, creators)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range creators {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			st, err := Create(dir)
			if err != nil {
				return
			}
			names <- st.Name
			st.Close()
		}()
	}
	close(start)
	wg.Wait()
	close(names)

	seen := map[string]bool{}
	for n := range names {
		if seen[n] {
			t.Fatalf("two sessions were created as %q — they share one log", n)
		}
		seen[n] = true
	}
	if len(seen) != creators {
		t.Errorf("%d of %d creators got a session", len(seen), creators)
	}
}
