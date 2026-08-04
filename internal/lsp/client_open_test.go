package lsp

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type captureWriteCloser struct{ bytes.Buffer }

func (captureWriteCloser) Close() error { return nil }

type signalWriteCloser struct {
	bytes.Buffer
	wrote chan struct{}
}

func (w *signalWriteCloser) Write(p []byte) (int, error) {
	select {
	case <-w.wrote:
	default:
		close(w.wrote)
	}
	return w.Buffer.Write(p)
}

func (w *signalWriteCloser) Close() error { return nil }

type blockingWriteCloser struct{ closed chan struct{} }

func (w *blockingWriteCloser) Write([]byte) (int, error) {
	<-w.closed
	return 0, os.ErrClosed
}

func (w *blockingWriteCloser) Close() error {
	select {
	case <-w.closed:
	default:
		close(w.closed)
	}
	return nil
}

func TestOpenRefreshesChangedDocumentForLaterSymbols(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.go")
	if err := os.WriteFile(path, []byte("package sample\nfunc Old() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wire := &captureWriteCloser{}
	c := &Client{Name: "test", Root: dir, in: wire, opened: map[string]openedDocument{}, syncKind: incrementalDocumentSync, syncKnown: true}
	if err := c.Open(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package sample\nfunc New() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := c.Open(path); err != nil {
		t.Fatal(err)
	}
	got := wire.String()
	if strings.Count(got, `"method":"textDocument/didOpen"`) != 1 {
		t.Fatalf("didOpen frames = %q", got)
	}
	if strings.Count(got, `"method":"textDocument/didChange"`) != 1 ||
		!strings.Contains(got, `"version":2`) || !strings.Contains(got, `"range"`) || !strings.Contains(got, "func New()") {
		t.Fatalf("changed document was not sent as didChange: %q", got)
	}
}

func TestOpenRefreshInvalidatesCachedDiagnostics(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.go")
	if err := os.WriteFile(path, []byte("package sample\nfunc Old() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wire := &captureWriteCloser{}
	c := &Client{
		Name: "test", Root: dir, in: wire,
		opened: map[string]openedDocument{},
		diags:  map[string][]Diagnostic{}, syncKnown: true,
	}
	if err := c.Open(path); err != nil {
		t.Fatal(err)
	}
	abs, _ := filepath.Abs(path)
	c.diags[abs] = []Diagnostic{{Message: "stale"}}
	if err := os.WriteFile(path, []byte("package sample\nfunc New() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := c.Open(path); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := c.Diagnostics(ctx, path); got != nil {
		t.Fatalf("cached diagnostics survived refresh: %#v", got)
	}
}

func TestDocumentSyncKindAcceptsNumberAndOptionsObject(t *testing.T) {
	if got := documentSyncKind([]byte(`{"capabilities":{"textDocumentSync":2}}`)); got != 2 {
		t.Fatalf("numeric sync kind = %d, want 2", got)
	}
	if got := documentSyncKind([]byte(`{"capabilities":{"textDocumentSync":{"change":1}}}`)); got != 1 {
		t.Fatalf("object sync kind = %d, want 1", got)
	}
	if kind, known := documentSyncCapability([]byte(`{"capabilities":{"textDocumentSync":0}}`)); kind != 0 || !known {
		t.Fatalf("explicit no-sync capability = (%d, %v), want (0, true)", kind, known)
	}
	if _, known := documentSyncCapability([]byte(`{"capabilities":{}}`)); known {
		t.Fatal("omitted sync capability was treated as explicit no-sync")
	}
	if kind, known, openClose := documentSyncDetails([]byte(`{"capabilities":{"textDocumentSync":{"openClose":true}}}`)); kind != 0 || !known || !openClose {
		t.Fatalf("open/close-only capability = (%d, %v, %v), want (0, true, true)", kind, known, openClose)
	}
}

func TestOpenSkipsNotificationsForExplicitNoSync(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.go")
	if err := os.WriteFile(path, []byte("package sample\nfunc Old() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wire := &captureWriteCloser{}
	c := &Client{Name: "test", Root: dir, in: wire, opened: map[string]openedDocument{}, syncKnown: true}
	if err := c.Open(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package sample\nfunc New() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := c.Open(path); err != nil {
		t.Fatal(err)
	}
	if got := wire.String(); got != "" {
		t.Fatalf("no-sync server received notifications: %q", got)
	}
}

func TestOpenCloseOnlySyncReceivesOpenButNotChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.go")
	if err := os.WriteFile(path, []byte("package sample\nfunc Old() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wire := &captureWriteCloser{}
	c := &Client{Name: "test", Root: dir, in: wire, opened: map[string]openedDocument{}, syncKnown: true, syncOpenClose: true}
	if err := c.Open(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package sample\nfunc New() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := c.Open(path); err != nil {
		t.Fatal(err)
	}
	got := wire.String()
	if strings.Count(got, `"method":"textDocument/didOpen"`) != 1 || strings.Contains(got, `"method":"textDocument/didChange"`) {
		t.Fatalf("open/close-only notifications = %q", got)
	}
}

func TestStructuredSyncWithoutOpenCloseSkipsInitialOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.go")
	if err := os.WriteFile(path, []byte("package sample\nfunc Old() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wire := &captureWriteCloser{}
	c := &Client{
		Name: "test", Root: dir, in: wire, opened: map[string]openedDocument{},
		syncKnown: true, syncKind: incrementalDocumentSync, syncOptions: true,
	}
	if err := c.Open(path); err != nil {
		t.Fatal(err)
	}
	if got := wire.String(); got != "" {
		t.Fatalf("openClose:false server received didOpen: %q", got)
	}
	if err := os.WriteFile(path, []byte("package sample\nfunc New() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := c.Open(path); err != nil {
		t.Fatal(err)
	}
	got := wire.String()
	if strings.Count(got, `"method":"textDocument/didOpen"`) != 0 ||
		strings.Count(got, `"method":"textDocument/didChange"`) != 1 {
		t.Fatalf("structured openClose:false notifications = %q", got)
	}
}

func TestUnknownSyncCapabilityDoesNotSendChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.go")
	if err := os.WriteFile(path, []byte("package sample\nfunc Old() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wire := &captureWriteCloser{}
	c := &Client{Name: "test", Root: dir, in: wire, opened: map[string]openedDocument{}}
	if err := c.Open(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package sample\nfunc New() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := c.Open(path); err != nil {
		t.Fatal(err)
	}
	got := wire.String()
	if strings.Count(got, `"method":"textDocument/didOpen"`) != 1 ||
		strings.Contains(got, `"method":"textDocument/didChange"`) {
		t.Fatalf("unknown-sync notifications = %q", got)
	}
}

func TestOpenContextCanStopBehindAnotherDocumentOperation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.go")
	if err := os.WriteFile(path, []byte("package sample\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wire := &captureWriteCloser{}
	c := &Client{Name: "test", Root: dir, in: wire, opened: map[string]openedDocument{}}
	if err := c.lockDocuments(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.unlockDocuments()
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := c.OpenContext(ctx, path); err == nil {
		t.Fatal("OpenContext waited past its deadline for the document gate")
	}
}

func TestCallUsesRequestContextForBlockedWrite(t *testing.T) {
	wire := &blockingWriteCloser{closed: make(chan struct{})}
	c := &Client{Name: "test", in: wire, pending: map[int]chan json.RawMessage{}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if _, err := c.call(ctx, "documentSymbol", nil); err == nil {
		t.Fatal("call waited past its deadline for a blocked write")
	}
}

func TestQueuedWriteDoesNotRunAfterContextCancellation(t *testing.T) {
	wire := &signalWriteCloser{wrote: make(chan struct{})}
	c := &Client{Name: "test", in: wire}
	c.writeMu.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := c.writeContext(ctx, map[string]any{"method": "queued"}); err == nil {
		t.Fatal("queued write unexpectedly succeeded")
	}
	c.writeMu.Unlock()
	select {
	case <-wire.wrote:
		t.Fatal("timed-out write was emitted after the serializer became available")
	case <-time.After(20 * time.Millisecond):
	}
}

func TestLegacyPositionOperationsBoundDocumentOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.go")
	if err := os.WriteFile(path, []byte("package sample\nfunc F() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &Client{Name: "test", Root: dir, opened: map[string]openedDocument{}}
	if err := c.lockDocuments(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.unlockDocuments()
	for _, name := range []string{"definition", "references", "hover"} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		var err error
		switch name {
		case "definition":
			_, err = c.Definition(ctx, path, 1, 1)
		case "references":
			_, err = c.References(ctx, path, 1, 1)
		case "hover":
			_, err = c.Hover(ctx, path, 1, 1)
		}
		cancel()
		if err == nil {
			t.Errorf("%s unexpectedly passed a held document gate", name)
		}
	}
}
