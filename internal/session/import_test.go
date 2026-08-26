package session

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"evilcode/internal/provider"
)

func TestCodexIdentityReadsOnlyTheHeaderLine(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("FIFO reproduction requires Unix")
	}
	path := filepath.Join(t.TempDir(), "codex-header.fifo")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		file, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			return
		}
		_, _ = file.Write([]byte(`{"type":"session_meta","payload":{"id":"codex-stream"}}` + "\n"))
		<-release
		_ = file.Close()
	}()

	result := make(chan struct {
		id  string
		err error
	}, 1)
	go func() {
		id, err := externalFileID(SourceCodex, path)
		result <- struct {
			id  string
			err error
		}{id: id, err: err}
	}()
	select {
	case got := <-result:
		close(release)
		<-writerDone
		if got.err != nil || got.id != "codex-stream" {
			t.Fatalf("header identity = %q, %v", got.id, got.err)
		}
	case <-time.After(250 * time.Millisecond):
		close(release)
		<-writerDone
		<-result
		t.Fatal("identity lookup waited for the entire transcript after reading its header")
	}
}

func TestImportClaudeCodeSessionAndResumeIt(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "evilcode-data")
	path := filepath.Join(root, "claude.jsonl")
	contents := `{"type":"user","sessionId":"claude-1","cwd":"/workspace/app","timestamp":"2026-08-01T10:00:00Z","message":{"role":"user","content":"fix the anchor parser"}}
{"type":"assistant","sessionId":"claude-1","timestamp":"2026-08-01T10:00:01Z","message":{"role":"assistant","model":"sonnet","content":[{"type":"text","text":"I fixed the anchor parser."}]}}
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	info, err := ImportExternalFile(dataDir, SourceClaude, path, "claude-1")
	if err != nil {
		t.Fatal(err)
	}
	if info.Messages != 2 || info.SourceID != "claude-1" {
		t.Fatalf("import info = %#v, want two Claude messages", info)
	}
	_, messages, err := Resume(dataDir, info.Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].Role != provider.RoleUser || messages[1].Role != provider.RoleAssistant {
		t.Fatalf("resumed messages = %#v, want user then assistant", messages)
	}
	if messages[0].Content != "fix the anchor parser" || messages[1].Content != "I fixed the anchor parser." {
		t.Fatalf("resumed content = %#v, want imported conversation", messages)
	}

	second, err := ImportExternalFile(dataDir, SourceClaude, path, "claude-1")
	if err != nil {
		t.Fatal(err)
	}
	if second.Name != info.Name || second.Messages != 2 {
		t.Fatalf("repeat import = %#v, want the stable existing session", second)
	}
}

func TestClaudeToolResultKeepsItsCallName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude-tools.jsonl")
	contents := `{"type":"assistant","sessionId":"claude-tools","timestamp":"2026-08-01T10:00:00Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"call-1","name":"read","input":{"path":"store.go"}}]}}
{"type":"user","sessionId":"claude-tools","timestamp":"2026-08-01T10:00:01Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","content":"package session"}]}}
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	external, err := ParseExternalFile(SourceClaude, path, "claude-tools")
	if err != nil {
		t.Fatal(err)
	}
	if len(external.Messages) != 2 {
		t.Fatalf("Claude tool transcript = %#v, want assistant call and tool result", external.Messages)
	}
	if len(external.Messages[0].Message.ToolCalls) != 1 || external.Messages[1].Message.ToolName != "read" {
		t.Fatalf("Claude tool mapping = %#v, want call name on its result", external.Messages)
	}
}

func TestExternalFileIdentityWinsOverPathHint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude-tools.jsonl")
	contents := `{"type":"user","sessionId":"embedded-id","message":{"role":"user","content":"hello"}}
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	external, err := ParseExternalFile(SourceClaude, path, path)
	if err != nil {
		t.Fatal(err)
	}
	if external.SourceID != "embedded-id" {
		t.Fatalf("source id = %q, want embedded transcript id", external.SourceID)
	}
}

func TestImportCodexResponseItems(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "rollout.jsonl")
	contents := `{"type":"session_meta","payload":{"id":"codex-1","cwd":"/workspace/app","model":"gpt-5"}}
{"timestamp":"2026-08-02T11:00:00Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"find the torn tail"}]}}
{"timestamp":"2026-08-02T11:00:01Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"the tail is recoverable"}]}}
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	external, err := ParseExternalFile(SourceCodex, path, "")
	if err != nil {
		t.Fatal(err)
	}
	if external.SourceID != "codex-1" || len(external.Messages) != 2 {
		t.Fatalf("Codex parse = %#v, want id and two messages", external)
	}
	if external.Messages[0].Message.Content != "find the torn tail" {
		t.Fatalf("Codex user message = %#v", external.Messages[0].Message)
	}

	// H1: the source model must land in the native session's model metadata so
	// a resume picks the model the conversation ran on.
	dataDir := filepath.Join(root, "evilcode-data")
	info, err := ImportExternalFile(dataDir, SourceCodex, path, "")
	if err != nil {
		t.Fatal(err)
	}
	desc, err := Describe(dataDir, info.Name)
	if err != nil {
		t.Fatal(err)
	}
	if desc.Model != "gpt-5@codex" {
		t.Errorf("imported model metadata = %q, want gpt-5@codex", desc.Model)
	}
}

func TestImportOpenCodeSessionParts(t *testing.T) {
	root := t.TempDir()
	storage := filepath.Join(root, "storage")
	sessionDir := filepath.Join(storage, "session")
	messageDir := filepath.Join(storage, "message", "ses-1")
	partDir := filepath.Join(storage, "part", "msg-1")
	for _, dir := range []string{sessionDir, messageDir, partDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	projectSessionDir := filepath.Join(sessionDir, "project-1")
	if err := os.MkdirAll(projectSessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sessionPath := filepath.Join(projectSessionDir, "ses-1.json")
	if err := os.WriteFile(sessionPath, []byte(`{"id":"ses-1","title":"Anchor work","directory":"/workspace/app","time":{"created":1785664800000,"updated":1785664801000}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(messageDir, "msg-1.json"), []byte(`{"id":"msg-1","role":"user","time":{"created":1785664800000}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(partDir, "part-1.json"), []byte(`{"type":"text","text":"continue the anchor work"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(messageDir, "msg-2.json"), []byte(`{"id":"msg-2","role":"assistant","time":{"created":1785664801000},"content":"I will continue it"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	info, err := ImportExternalFile(filepath.Join(root, "data"), SourceOpenCode, sessionPath, "ses-1")
	if err != nil {
		t.Fatal(err)
	}
	_, messages, err := Resume(filepath.Join(root, "data"), info.Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].Content != "continue the anchor work" || messages[1].Content != "I will continue it" {
		t.Fatalf("OpenCode resumed messages = %#v", messages)
	}
}
