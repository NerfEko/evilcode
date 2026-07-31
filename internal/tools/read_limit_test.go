package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// H3.2: FS.MaxReadBytes is declared, documented as capping a single read,
// initialized in NewFS — and never referenced. read did an unbounded
// os.ReadFile plus a full line split, so the whole file is resident before any
// truncation happens. A multi-gigabyte file takes the process with it.
func TestReadRefusesAFileLargerThanTheCap(t *testing.T) {
	f := tempFS(t, nil)
	f.MaxReadBytes = 4096

	big := filepath.Join(f.Root, "big.txt")
	if err := os.WriteFile(big, []byte(strings.Repeat("a\n", 40_000)), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := run(t, f.Tools(), "read", map[string]any{"path": "big.txt"})
	if err == nil {
		t.Fatalf("an %d-byte file was read whole against a %d-byte cap",
			80_000, f.MaxReadBytes)
	}
	if !strings.Contains(err.Error(), "offset") && !strings.Contains(err.Error(), "limit") {
		t.Errorf("error = %q, want it to say how to read the file in pieces", err)
	}
	if res.Output != "" {
		t.Errorf("a refused read still produced %d bytes of output", len(res.Output))
	}
}

// Paging through the same file with offset and limit still works: the cap is on
// what one call loads, not on what the file may be.
func TestAPagedReadOfALargeFileWorks(t *testing.T) {
	f := tempFS(t, nil)
	f.MaxReadBytes = 4096

	var b strings.Builder
	for i := range 40_000 {
		b.WriteString("line ")
		b.WriteString(strings.Repeat("x", 10))
		b.WriteByte('\n')
		_ = i
	}
	if err := os.WriteFile(filepath.Join(f.Root, "big.txt"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := run(t, f.Tools(), "read", map[string]any{
		"path": "big.txt", "offset": 1, "limit": 20,
	})
	if err != nil {
		t.Fatalf("a paged read of a large file must work: %v", err)
	}
	if lines := strings.Count(res.Output, "\n"); lines > 25 {
		t.Errorf("a 20-line window returned %d lines", lines)
	}
}
