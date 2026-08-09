package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExposureMergesAndResets(t *testing.T) {
	exposure := NewExposure()
	path := filepath.Join(t.TempDir(), "sample.go")
	exposure.Record([]LineRange{{Path: path, Start: 2, End: 3}, {Path: path, Start: 4, End: 4}})
	if !exposure.Contains(path, 2) || !exposure.Contains(path, 4) || exposure.Contains(path, 5) {
		t.Fatalf("ranges = %#v", exposure.Snapshot())
	}
	exposure.Reset()
	if exposure.Contains(path, 2) || len(exposure.Snapshot()) != 0 {
		t.Fatalf("reset left ranges = %#v", exposure.Snapshot())
	}
}

func TestReadThenGrepCollapsesShownHit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.go")
	if err := os.WriteFile(path, []byte("package sample\n\nfunc Render() {\n\tneedle()\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	exposure := NewExposure()
	fs := NewFS(dir).WithExposure(exposure)
	execTools := NewExec(dir).WithExposure(exposure)
	if _, err := run(t, fs.Tools(), "read", map[string]any{"path": "sample.go"}); err != nil {
		t.Fatal(err)
	}
	res, err := run(t, execTools.Tools(), "grep", map[string]any{"pattern": "needle", "path": "sample.go"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "sample.go:4 — shown above") || strings.Contains(res.Output, "needle()") {
		t.Errorf("collapsed grep = %q", res.Output)
	}
}

func TestRepeatedGrepCollapsesTheSecondHit(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sample.go"), []byte("package sample\nfunc Render() { needle() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	execTools := NewExec(dir).WithExposure(NewExposure())
	if first, err := run(t, execTools.Tools(), "grep", map[string]any{"pattern": "needle"}); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(first.Output, "needle()") {
		t.Fatalf("first grep = %q", first.Output)
	}
	second, err := run(t, execTools.Tools(), "grep", map[string]any{"pattern": "needle"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(second.Output, "shown above") || strings.Contains(second.Output, "needle()") {
		t.Errorf("second grep = %q", second.Output)
	}
}

func TestBashOutputRecordsSourceRanges(t *testing.T) {
	dir := t.TempDir()
	exposure := NewExposure()
	e := NewExec(dir).WithExposure(exposure)
	res, err := run(t, e.Tools(), "bash", map[string]any{"cmd": "printf 'sample.go:2-4: compiler output\\n'"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "sample.go:2-4") || !exposure.Contains(filepath.Join(dir, "sample.go"), 3) {
		t.Errorf("bash output = %q, ranges = %#v", res.Output, exposure.Snapshot())
	}
}

func TestCompactionResetAllowsAHitAgain(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sample.go"), []byte("package sample\nfunc Render() { needle() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	exposure := NewExposure()
	e := NewExec(dir).WithExposure(exposure)
	for i := 0; i < 2; i++ {
		res, err := run(t, e.Tools(), "grep", map[string]any{"pattern": "needle"})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(res.Output, "shown above") {
			t.Fatalf("grep in fresh epoch collapsed: %q", res.Output)
		}
		if i == 0 {
			exposure.Reset()
		}
	}
	// A third call confirms the post-reset epoch is tracked normally.
	res, err := run(t, e.Tools(), "grep", map[string]any{"pattern": "needle"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "shown above") {
		t.Fatalf("post-reset hit was not tracked: %q", res.Output)
	}
}
