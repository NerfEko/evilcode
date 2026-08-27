package tui

import (
	"strings"
	"testing"
)

func TestKvCacheWidget(t *testing.T) {
	r := testRenderer(80)

	// No data yet: the widget is absent, not an empty box claiming space.
	if w := r.KvCacheWidget(0, 0); len(w.Lines) != 0 {
		t.Errorf("zero cache should produce no widget, got %v", w.Lines)
	}

	// 80 hit of 100 cached tokens -> 80%.
	rows := plainLines(r.KvCacheWidget(8000, 2000).Lines)
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "80%") {
		t.Errorf("8000 read / 2000 write should read 80%%:\n%s", joined)
	}
	if !strings.Contains(joined, "8.0k") {
		t.Errorf("should show human-readable read tokens:\n%s", joined)
	}
	if !strings.Contains(joined, "2.0k") {
		t.Errorf("should show human-readable write tokens:\n%s", joined)
	}

	// All misses, zero hits -> 0%, no crash on zero read.
	rows = plainLines(r.KvCacheWidget(0, 5000).Lines)
	joined = strings.Join(rows, "\n")
	if !strings.Contains(joined, "0%") {
		t.Errorf("0 read / 5000 write should read 0%%:\n%s", joined)
	}
}
