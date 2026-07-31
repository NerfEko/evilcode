package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// H1.5: write and edit truncated the destination in place, so the file is
// observably empty or half-written for as long as the write takes. A crash, a
// short write or a full disk in that window leaves the truncated version on
// disk and the original nowhere.
//
// The window is what the test observes: a reader running alongside the writer
// must never see a file that is neither the old contents nor the new ones.
func TestWriteIsAtomicForAReader(t *testing.T) {
	f := tempFS(t, nil)
	path := filepath.Join(f.Root, "big.txt")

	old := strings.Repeat("a\n", 100_000)
	fresh := strings.Repeat("b\n", 100_000)
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var bad string
	var mu sync.Mutex
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			if s := string(data); s != old && s != fresh {
				mu.Lock()
				if bad == "" {
					bad = describe(s)
				}
				mu.Unlock()
				return
			}
		}
	}()

	for range 8 {
		if _, err := run(t, f.Tools(), "write", map[string]any{
			"path": "big.txt", "content": fresh,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := run(t, f.Tools(), "write", map[string]any{
			"path": "big.txt", "content": old,
		}); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if bad != "" {
		t.Fatalf("a reader saw a partially written file: %s", bad)
	}
}

func describe(s string) string {
	if s == "" {
		return "the file was empty — truncated, not yet rewritten"
	}
	return fmt.Sprintf("the file held %d bytes, matching neither version", len(s))
}

// A replacement must not quietly widen a file's permissions.
func TestWriteAndEditPreservePermissions(t *testing.T) {
	f := tempFS(t, map[string]string{"secret.txt": "one\n"})
	path := filepath.Join(f.Root, "secret.txt")
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := run(t, f.Tools(), "write", map[string]any{
		"path": "secret.txt", "content": "two\n",
	}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode after write = %v, want 0600", info.Mode().Perm())
	}

	if _, err := run(t, f.Tools(), "edit", map[string]any{
		"path": "secret.txt", "old": "two", "new": "three",
	}); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode after edit = %v, want 0600", info.Mode().Perm())
	}
}
