package tools

import (
	"strings"
	"testing"
)

// D2: an oversized unified diff is cut with a note that the change itself is
// complete, and DiffStat keeps the real counts — only the rendered text loses
// its middle.
func TestOversizedDiffIsTruncated(t *testing.T) {
	f := tempFS(t, map[string]string{"a.txt": "old\n"})
	big := strings.Repeat("new content line\n", MaxDiffBytes/16) // ~4 MiB of changes

	res, err := run(t, f.Tools(), "write", map[string]any{
		"path":    "a.txt",
		"content": big,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Diff) > MaxDiffBytes {
		t.Errorf("diff retained %d bytes, want at most MaxDiffBytes (%d)", len(res.Diff), MaxDiffBytes)
	}
	if !strings.Contains(res.Diff, "diff truncated") {
		t.Error("truncated diff must say so")
	}
	if res.DiffStat == nil || res.DiffStat.Added == 0 {
		t.Errorf("DiffStat = %+v, want the real counts preserved", res.DiffStat)
	}
}
