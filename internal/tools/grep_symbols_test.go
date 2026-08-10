package tools

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"evilcode/internal/lsp"
)

type blockingLSPServer struct {
	entered chan string
	release chan struct{}
}

func (s *blockingLSPServer) For(ctx context.Context, path string) (*lsp.Client, error) {
	select {
	case s.entered <- path:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-s.release:
		return nil, errors.New("test server unavailable")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestParseRGRecordsKeepsMatchAndContextLinesDistinct(t *testing.T) {
	records := parseRGRecords("dir/a-file.go:12:match\ndir/a-file.go-13-context\n--\n")
	if len(records) != 2 {
		t.Fatalf("records = %#v, want two", records)
	}
	if records[0].Path != "dir/a-file.go" || records[0].Line != 12 || records[0].Context {
		t.Errorf("match record = %#v", records[0])
	}
	if records[1].Path != "dir/a-file.go" || records[1].Line != 13 || !records[1].Context {
		t.Errorf("context record = %#v", records[1])
	}
}

func TestParseRGRecordsUsesNULPathBoundaryAndKeepsBinary(t *testing.T) {
	records := parseRGRecords("dir:123-part/a.go\x001:needle\nfoo:123:bar.bin: binary file matches (found \\\"x\\\" byte)\n")
	if len(records) != 2 {
		t.Fatalf("records = %#v, want match and binary records", records)
	}
	if records[0].Path != "dir:123-part/a.go" || records[0].Line != 1 || records[0].Text != "needle" {
		t.Errorf("NUL-delimited match = %#v", records[0])
	}
	if !records[1].Binary || records[1].Path != "foo:123:bar.bin" || records[1].Raw == "" {
		t.Errorf("binary record = %#v", records[1])
	}
}

func TestParseRGRecordsKeepsNewlineInNULDelimitedPath(t *testing.T) {
	records := parseRGRecords("dir/foo\nbar.go\x001:needle\n")
	if len(records) != 1 || records[0].Path != "dir/foo\nbar.go" || records[0].Line != 1 {
		t.Fatalf("newline-containing path record = %#v", records)
	}
}

func TestParseRGRecordsDoesNotSplitParseableNewlinePath(t *testing.T) {
	// The bytes before NUL happen to look like a plain path:line:text record,
	// but --null makes the NUL the authoritative path boundary.
	records := parseRGRecords("dir/1:2:fake\nname.go\x001:needle\n")
	if len(records) != 1 || records[0].Path != "dir/1:2:fake\nname.go" || records[0].Line != 1 {
		t.Fatalf("parseable newline-containing path = %#v", records)
	}
}

func TestParseRGRecordsSkipsContextSeparatorBeforeNULRecord(t *testing.T) {
	records := parseRGRecords("first.go\x001:one\n--\nsecond.go\x002:two\n")
	if len(records) != 2 || records[1].Path != "second.go" || records[1].Line != 2 {
		t.Fatalf("records around separator = %#v", records)
	}
	if records[0].Group == records[1].Group {
		t.Fatalf("records around separator share group %d", records[0].Group)
	}
}

func TestDeclarationSymbolDoesNotTreatJavaScriptFunctionAsGo(t *testing.T) {
	if symbol, ok := declarationSymbol("function render() {}", 1); !ok || symbol.Kind != "function" || symbol.Name != "render" {
		t.Fatalf("JavaScript declaration = %#v, %v", symbol, ok)
	}
	if symbol, ok := declarationSymbol("funcName() {}", 1); ok {
		t.Fatalf("identifier with func prefix was treated as declaration: %#v", symbol)
	}
}

func TestDeclarationSymbolUsesGenericGoReceiverType(t *testing.T) {
	got, ok := declarationSymbol("func (w *Widget[T]) Render() {}", 7)
	if !ok || got.Name != "Widget.Render" || got.Kind != "func" {
		t.Fatalf("generic receiver declaration = %#v, %v; want Widget.Render", got, ok)
	}
}

func TestEnclosingGrepSymbolChoosesInnermostLSPRange(t *testing.T) {
	symbols := flattenGrepSymbols([]lsp.Symbol{
		{
			Name:  "Widget",
			Kind:  5,
			Range: lsp.Range{Start: lsp.Position{Line: 0}, End: lsp.Position{Line: 9}},
			Children: []lsp.Symbol{{
				Name:  "Render",
				Kind:  6,
				Range: lsp.Range{Start: lsp.Position{Line: 3}, End: lsp.Position{Line: 5}},
			}},
		},
	})
	if got := enclosingGrepSymbol(symbols, 5); got != "method Render" {
		t.Fatalf("enclosing symbol = %q, want method Render", got)
	}
	if got := enclosingGrepSymbol(symbols, 9); got != "class Widget" {
		t.Fatalf("outer symbol = %q, want class Widget", got)
	}
}

func TestLSPExclusiveEndAtColumnZeroDoesNotIncludeNextLine(t *testing.T) {
	symbols := flattenGrepSymbols([]lsp.Symbol{{
		Name: "First", Kind: 12,
		Range: lsp.Range{Start: lsp.Position{Line: 0}, End: lsp.Position{Line: 3, Character: 0}},
	}})
	if got := enclosingGrepSymbol(symbols, 3); got != "function First" {
		t.Fatalf("last covered line = %q, want function First", got)
	}
	if got := enclosingGrepSymbol(symbols, 4); got != "" {
		t.Fatalf("exclusive end line = %q, want top level", got)
	}
}

func TestScanGrepSymbolsStopsAtHighestHitLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sample.go")
	if err := os.WriteFile(path, []byte("package sample\nfunc First() {}\n\nfunc Later() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	symbols := scanGrepSymbols(context.Background(), path, 2)
	if len(symbols) != 1 || symbols[0].Name != "First" {
		t.Fatalf("bounded symbols = %#v, want only First", symbols)
	}
}

func TestGrepResolvesDifferentLanguagesConcurrently(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep is not installed")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "sample.go"), []byte("package sample\nfunc GoNeedle() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sample.py"), []byte("def python_needle():\n    pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	server := &blockingLSPServer{
		entered: make(chan string, 2),
		release: make(chan struct{}),
	}
	runner := NewExec(root).WithLSP(server)
	runner.Timeout = 3 * time.Second
	done := make(chan error, 1)
	go func() {
		_, err := runner.grepTool().Run(context.Background(), []byte(`{"pattern":"(?i)needle"}`))
		done <- err
	}()

	select {
	case <-server.entered:
	case <-time.After(time.Second):
		close(server.release)
		t.Fatal("grep never requested symbols for the first file")
	}
	select {
	case <-server.entered:
		close(server.release)
	case <-time.After(250 * time.Millisecond):
		close(server.release)
		t.Fatal("symbol resolution was serial across hit files")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("grep did not finish after the language server was released")
	}
}

func TestBoundedCaptureRetainsOnlyConfiguredPrefix(t *testing.T) {
	capture := newBoundedCapture(5)
	if n, err := capture.Write([]byte("123456789")); err != nil || n != 9 {
		t.Fatalf("Write = %d, %v; want 9, nil", n, err)
	}
	got, truncated := capture.snapshot()
	if got != "12345" || !truncated {
		t.Fatalf("snapshot = %q, %v; want bounded prefix and truncation", got, truncated)
	}
}
