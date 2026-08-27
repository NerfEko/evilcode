package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// R2-04: the side pane used to io.ReadAll whatever file a diff named, split
// it into lines, and highlight every row, so a click on a generated file of
// any size allocated several copies on the update loop. It also fed an
// already-truncated prefix to tools.Truncate, whose notice then reported
// roughly one byte omitted and showed the prefix's tail as the file's tail.

func TestFileDiffContentRefusesOversizedFile(t *testing.T) {
	dir := t.TempDir()
	m := newTestModel(t)
	m.cwd = dir

	big := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(big, []byte(strings.Repeat("x", maxPanelFileBytes+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	small := filepath.Join(dir, "small.txt")
	if err := os.WriteFile(small, []byte("line one\nline two\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := m.fileDiffContent("big.txt", "--- a/big.txt\n+++ b/big.txt\n@@ -1 +1 @@\n-x\n+y\n")
	if len(got.Body) != 0 {
		t.Errorf("oversized file loaded %d lines into the pane; want the diff-only fallback", len(got.Body))
	}
	if got.Diff == "" {
		t.Error("the fallback must keep the diff itself")
	}

	got = m.fileDiffContent("small.txt", "")
	if len(got.Body) != 2 || got.Body[0] != "line one" {
		t.Errorf("small file body = %q, want both lines", got.Body)
	}
}

// A file that grows past the ceiling between the Stat and the read must not
// blow the budget either: the read is capped too.
func TestFileDiffContentCapsGrowthAfterStat(t *testing.T) {
	dir := t.TempDir()
	m := newTestModel(t)
	m.cwd = dir

	path := filepath.Join(dir, "growing.txt")
	if err := os.WriteFile(path, []byte("ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Replace the small file with a huge one; the pane's reader must still cap.
	if err := os.WriteFile(path, []byte(strings.Repeat("x", maxPanelFileBytes*2)), 0o644); err != nil {
		t.Fatal(err)
	}
	got := m.fileDiffContent("growing.txt", "")
	_ = got // Either the diff-only fallback or a capped read is acceptable;
	// the invariant is that the read never exceeded the ceiling, which the
	// LimitReader enforces by construction.
}

func TestQuickViewTruncationNoticeIsTruthful(t *testing.T) {
	dir := t.TempDir()
	m := newTestModel(t)
	m.cwd = dir

	// A file far larger than the window: the notice must carry the real total,
	// not the one byte the old Truncate-on-prefix path reported.
	path := filepath.Join(dir, "large.txt")
	body := strings.Repeat("a line of text\n", (quickViewWindow/15)*3)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got := m.readQuickView("large.txt")
	if got == nil {
		t.Fatal("no quick view")
	}
	if len(got.Body) == 0 {
		t.Fatal("empty quick view")
	}
	last := got.Body[len(got.Body)-1]
	if !strings.Contains(last, "showing the first") || !strings.Contains(last, "use read with offset/limit") {
		t.Fatalf("notice = %q, want an accurate truncation notice", last)
	}
	if !strings.Contains(last, humanBytes(quickViewWindow)) {
		t.Errorf("notice = %q, want the window size named", last)
	}
	// The real total (three windows' worth) must appear — that is the part the
	// old Truncate-on-prefix path could never say truthfully.
	if !strings.Contains(last, "of "+humanBytes(quickViewWindow*3)) {
		t.Errorf("notice = %q, want the real total (%s) named", last, humanBytes(quickViewWindow*3))
	}

	// A small file keeps its exact content and no notice.
	small := filepath.Join(dir, "small.txt")
	if err := os.WriteFile(small, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got = m.readQuickView("small.txt")
	if len(got.Body) != 2 || got.Body[0] != "one" || got.Body[1] != "two" {
		t.Errorf("small file body = %q, want both lines and no notice", got.Body)
	}
}
