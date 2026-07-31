package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func tempFS(t *testing.T, files map[string]string) *FS {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return NewFS(dir)
}

func run(t *testing.T, set Set, name string, args any) (Result, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	out := set.RunOne(context.Background(), Call{ID: "c1", Name: name, Args: raw})
	return out.Result, out.Err
}

func TestReadNumbersLines(t *testing.T) {
	f := tempFS(t, map[string]string{"a.txt": "one\ntwo\nthree\n"})
	res, err := run(t, f.Tools(), "read", map[string]any{"path": "a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	want := "1\tone\n2\ttwo\n3\tthree\n"
	if res.Output != want {
		t.Errorf("output = %q, want %q", res.Output, want)
	}
}

func TestReadOffsetAndLimit(t *testing.T) {
	f := tempFS(t, map[string]string{"a.txt": "1\n2\n3\n4\n5\n"})
	res, err := run(t, f.Tools(), "read", map[string]any{"path": "a.txt", "offset": 2, "limit": 2})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "2\t2") || !strings.Contains(res.Output, "3\t3") {
		t.Errorf("output = %q, want lines 2 and 3", res.Output)
	}
	if strings.Contains(res.Output, "4\t4") {
		t.Errorf("output = %q, want the limit respected", res.Output)
	}
	if !strings.Contains(res.Output, "more lines") {
		t.Error("a truncated read must say how to get the rest")
	}
}

func TestReadRejectsBinaryAndDirectories(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "bin"), []byte{0x7f, 'E', 'L', 'F', 0, 0}, 0o644)
	os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	f := NewFS(dir)

	if _, err := run(t, f.Tools(), "read", map[string]any{"path": "bin"}); err == nil {
		t.Error("want an error for a binary file")
	}
	if _, err := run(t, f.Tools(), "read", map[string]any{"path": "sub"}); err == nil {
		t.Error("want an error for a directory")
	}
}

func TestPathEscapeIsRefused(t *testing.T) {
	f := tempFS(t, map[string]string{"a.txt": "x"})
	for _, path := range []string{"../outside.txt", "../../etc/passwd", "/etc/passwd"} {
		if _, err := run(t, f.Tools(), "read", map[string]any{"path": path}); err == nil {
			t.Errorf("reading %q must be refused", path)
		}
	}
}

func TestSymlinkEscapeIsRefused(t *testing.T) {
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("classified"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(secret, filepath.Join(dir, "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	f := NewFS(dir)
	if _, err := run(t, f.Tools(), "read", map[string]any{"path": "link.txt"}); err == nil {
		t.Error("a symlink pointing outside the workspace must be refused")
	}
}

func TestWriteCreatesAndReportsDiff(t *testing.T) {
	f := tempFS(t, nil)
	res, err := run(t, f.Tools(), "write", map[string]any{
		"path": "new/deep/file.txt", "content": "hello\nworld\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(f.Root, "new/deep/file.txt"))
	if err != nil {
		t.Fatalf("write must create parent directories: %v", err)
	}
	if string(got) != "hello\nworld\n" {
		t.Errorf("file = %q", got)
	}
	if res.DiffStat == nil || res.DiffStat.Added != 2 {
		t.Errorf("DiffStat = %+v, want 2 added", res.DiffStat)
	}
	if !strings.Contains(res.Output, "created") {
		t.Errorf("output = %q, want it to say created", res.Output)
	}
}

func TestEditReplacesAndCounts(t *testing.T) {
	f := tempFS(t, map[string]string{"a.go": "package main\n\nfunc main() {}\n"})
	res, err := run(t, f.Tools(), "edit", map[string]any{
		"path": "a.go", "old": "func main() {}", "new": "func main() {\n\tprintln(\"hi\")\n}",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(f.Root, "a.go"))
	if !strings.Contains(string(got), `println("hi")`) {
		t.Errorf("file = %q", got)
	}
	if res.DiffStat == nil || res.DiffStat.Added != 3 || res.DiffStat.Removed != 1 {
		t.Errorf("DiffStat = %+v, want +3 -1", res.DiffStat)
	}
	if !strings.Contains(res.Diff, "+++") {
		t.Errorf("Diff = %q, want a unified diff", res.Diff)
	}
}

func TestEditMissingStringExplainsItself(t *testing.T) {
	f := tempFS(t, map[string]string{"a.txt": "hello"})
	_, err := run(t, f.Tools(), "edit", map[string]any{"path": "a.txt", "old": "nope", "new": "x"})
	if err == nil {
		t.Fatal("want an error")
	}
	// The message must push toward re-reading; a bare "not found" is what
	// starts edit-retry loops (plan.md §17).
	if !strings.Contains(err.Error(), "Re-read") {
		t.Errorf("err = %q, want it to suggest re-reading the file", err)
	}
}

func TestEditAmbiguousStringIsRefused(t *testing.T) {
	f := tempFS(t, map[string]string{"a.txt": "x\nx\n"})
	_, err := run(t, f.Tools(), "edit", map[string]any{"path": "a.txt", "old": "x", "new": "y"})
	if err == nil {
		t.Fatal("want an error for an ambiguous match")
	}
	if !strings.Contains(err.Error(), "2 times") {
		t.Errorf("err = %q, want the occurrence count", err)
	}
	// Nothing may have been written.
	got, _ := os.ReadFile(filepath.Join(f.Root, "a.txt"))
	if string(got) != "x\nx\n" {
		t.Errorf("file = %q, want it untouched after a refused edit", got)
	}
}

func TestEditAllReplacesEveryOccurrence(t *testing.T) {
	f := tempFS(t, map[string]string{"a.txt": "x\nx\n"})
	if _, err := run(t, f.Tools(), "edit", map[string]any{
		"path": "a.txt", "old": "x", "new": "y", "all": true,
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(f.Root, "a.txt"))
	if string(got) != "y\ny\n" {
		t.Errorf("file = %q", got)
	}
}

func TestEditIdenticalStringsRefused(t *testing.T) {
	f := tempFS(t, map[string]string{"a.txt": "x"})
	if _, err := run(t, f.Tools(), "edit", map[string]any{"path": "a.txt", "old": "x", "new": "x"}); err == nil {
		t.Error("want an error when old and new are identical")
	}
}

func TestGlob(t *testing.T) {
	f := tempFS(t, map[string]string{
		"main.go":                 "",
		"internal/a/a.go":         "",
		"internal/a/a_test.go":    "",
		"internal/b/c/deep.go":    "",
		"node_modules/pkg/bad.go": "",
		"README.md":               "",
	})
	tests := []struct {
		pattern string
		want    []string
		absent  []string
	}{
		{"*.go", []string{"main.go", "internal/a/a.go"}, []string{"README.md"}},
		{"**/*_test.go", []string{"internal/a/a_test.go"}, []string{"main.go"}},
		{"internal/**/*.go", []string{"internal/a/a.go", "internal/b/c/deep.go"}, []string{"main.go"}},
		{"internal/a/*.go", []string{"internal/a/a.go"}, []string{"internal/b/c/deep.go"}},
	}
	for _, tt := range tests {
		t.Run(tt.pattern, func(t *testing.T) {
			res, err := run(t, f.Tools(), "glob", map[string]any{"pattern": tt.pattern})
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tt.want {
				if !strings.Contains(res.Output, want) {
					t.Errorf("output missing %q:\n%s", want, res.Output)
				}
			}
			for _, absent := range tt.absent {
				if strings.Contains(res.Output, absent) {
					t.Errorf("output should not contain %q:\n%s", absent, res.Output)
				}
			}
			if strings.Contains(res.Output, "node_modules") {
				t.Error("node_modules must never be walked")
			}
		})
	}
}

func TestGlobNoMatches(t *testing.T) {
	f := tempFS(t, map[string]string{"a.txt": ""})
	res, err := run(t, f.Tools(), "glob", map[string]any{"pattern": "*.rs"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "no files matched") {
		t.Errorf("output = %q", res.Output)
	}
}

func TestUnknownArgumentIsReported(t *testing.T) {
	// A misspelled parameter must be an error, not a silent default — the model
	// otherwise never learns it got the call wrong.
	f := tempFS(t, map[string]string{"a.txt": "x"})
	_, err := run(t, f.Tools(), "read", map[string]any{"path": "a.txt", "pathh": "typo"})
	if err == nil {
		t.Error("want an error for an unknown argument")
	}
}

func TestUnknownToolNamesTheAlternatives(t *testing.T) {
	f := tempFS(t, nil)
	out := f.Tools().RunOne(context.Background(), Call{Name: "nope", Args: json.RawMessage(`{}`)})
	if out.Err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(out.Err.Error(), "read") {
		t.Errorf("err = %q, want it to list the available tools", out.Err)
	}
}

func TestPanicInToolBecomesError(t *testing.T) {
	set := Set{{
		Name: "boom",
		Run: func(ctx context.Context, raw json.RawMessage) (Result, error) {
			panic("kaboom")
		},
	}}
	out := set.RunOne(context.Background(), Call{Name: "boom"})
	if out.Err == nil || !strings.Contains(out.Err.Error(), "kaboom") {
		t.Fatalf("err = %v, want the panic converted to an error", out.Err)
	}
}

func TestBashRunsAndPersistsCwd(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	e := NewExec(dir)
	set := e.Tools()

	res, err := run(t, set, "bash", map[string]any{"cmd": "echo hello"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(res.Output) != "hello" {
		t.Errorf("output = %q", res.Output)
	}

	if _, err := run(t, set, "bash", map[string]any{"cmd": "cd sub"}); err != nil {
		t.Fatal(err)
	}
	res, err = run(t, set, "bash", map[string]any{"cmd": "pwd"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "sub") {
		t.Errorf("pwd = %q, want the cd from the previous call to persist", res.Output)
	}
}

func TestBashNonZeroExitKeepsOutput(t *testing.T) {
	e := NewExec(t.TempDir())
	res, err := run(t, e.Tools(), "bash", map[string]any{"cmd": "echo problem >&2; exit 3"})
	if err == nil {
		t.Fatal("want an error for a non-zero exit")
	}
	if !strings.Contains(err.Error(), "3") {
		t.Errorf("err = %q, want the exit code", err)
	}
	// The model needs the output to act on the failure.
	if !strings.Contains(res.Output, "problem") {
		t.Errorf("output = %q, want stderr preserved on failure", res.Output)
	}
}

func TestBashTimeout(t *testing.T) {
	e := NewExec(t.TempDir())
	e.Timeout = 100 * time.Millisecond
	start := time.Now()
	_, err := run(t, e.Tools(), "bash", map[string]any{"cmd": "sleep 10"})
	if err == nil {
		t.Fatal("want a timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("err = %q", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("took %s; the timeout did not fire", elapsed)
	}
}

func TestBashEmptyCommandRefused(t *testing.T) {
	e := NewExec(t.TempDir())
	if _, err := run(t, e.Tools(), "bash", map[string]any{"cmd": "   "}); err == nil {
		t.Error("want an error for an empty command")
	}
}

func TestGrep(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\nfunc NewThing() {}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("func NewOther() {}\n"), 0o644)
	e := NewExec(dir)

	res, err := run(t, e.Tools(), "grep", map[string]any{"pattern": "func New"})
	if err != nil {
		if strings.Contains(err.Error(), "not installed") {
			t.Skip("ripgrep not installed")
		}
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "a.go") || !strings.Contains(res.Output, "b.txt") {
		t.Errorf("output = %q, want both files", res.Output)
	}
	if strings.Contains(res.Output, dir) {
		t.Errorf("output = %q, want paths relative to the root", res.Output)
	}

	res, err = run(t, e.Tools(), "grep", map[string]any{"pattern": "func New", "glob": "*.go"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Output, "b.txt") {
		t.Errorf("output = %q, want the glob to exclude b.txt", res.Output)
	}
}

func TestGrepNoMatchesIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("nothing here\n"), 0o644)
	e := NewExec(dir)
	res, err := run(t, e.Tools(), "grep", map[string]any{"pattern": "zzzznotpresent"})
	if err != nil {
		if strings.Contains(err.Error(), "not installed") {
			t.Skip("ripgrep not installed")
		}
		t.Fatalf("no matches is an answer, not a failure: %v", err)
	}
	if !strings.Contains(res.Output, "no matches") {
		t.Errorf("output = %q", res.Output)
	}
}

func TestRunBatchPreservesOrder(t *testing.T) {
	var running, maxRunning int64
	set := Set{{
		Name: "slow",
		Run: func(ctx context.Context, raw json.RawMessage) (Result, error) {
			n := atomic.AddInt64(&running, 1)
			for {
				m := atomic.LoadInt64(&maxRunning)
				if n <= m || atomic.CompareAndSwapInt64(&maxRunning, m, n) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			atomic.AddInt64(&running, -1)
			var a struct {
				N int `json:"n"`
			}
			json.Unmarshal(raw, &a)
			return Result{Output: strings.Repeat("x", a.N)}, nil
		},
	}}

	var calls []Call
	for i := 1; i <= 6; i++ {
		calls = append(calls, Call{
			ID:   string(rune('a' + i)),
			Name: "slow",
			Args: json.RawMessage(`{"n":` + string(rune('0'+i)) + `}`),
		})
	}
	start := time.Now()
	outs := set.RunBatch(context.Background(), calls)
	elapsed := time.Since(start)

	if len(outs) != 6 {
		t.Fatalf("outcomes = %d", len(outs))
	}
	for i, o := range outs {
		if len(o.Result.Output) != i+1 {
			t.Errorf("outcome %d = %q, want results in call order", i, o.Result.Output)
		}
	}
	if atomic.LoadInt64(&maxRunning) < 2 {
		t.Error("batch did not run concurrently")
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("batch took %s; six 20ms calls should overlap", elapsed)
	}
}

func TestRunBatchBoundsConcurrency(t *testing.T) {
	var running, maxRunning int64
	set := Set{{
		Name: "count",
		Run: func(ctx context.Context, raw json.RawMessage) (Result, error) {
			n := atomic.AddInt64(&running, 1)
			for {
				m := atomic.LoadInt64(&maxRunning)
				if n <= m || atomic.CompareAndSwapInt64(&maxRunning, m, n) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			atomic.AddInt64(&running, -1)
			return Result{}, nil
		},
	}}
	calls := make([]Call, 40)
	for i := range calls {
		calls[i] = Call{Name: "count", Args: json.RawMessage(`{}`)}
	}
	set.RunBatch(context.Background(), calls)
	if got := atomic.LoadInt64(&maxRunning); got > MaxConcurrent {
		t.Errorf("peak concurrency = %d, want at most %d", got, MaxConcurrent)
	}
}

func TestTruncate(t *testing.T) {
	if got := Truncate("short"); got != "short" {
		t.Errorf("short strings must pass through unchanged, got %q", got)
	}

	big := strings.Repeat("a", MaxResultBytes*2)
	got := Truncate(big)
	if len(got) >= len(big) {
		t.Errorf("len = %d, want it shorter than %d", len(got), len(big))
	}
	if !strings.Contains(got, "truncated") {
		t.Error("a truncated result must say so")
	}
}

func TestTruncateKeepsBothEnds(t *testing.T) {
	body := "HEAD" + strings.Repeat("-", MaxResultBytes*2) + "TAIL"
	got := Truncate(body)
	if !strings.HasPrefix(got, "HEAD") {
		t.Error("truncation must keep the head")
	}
	if !strings.HasSuffix(got, "TAIL") {
		t.Error("truncation must keep the tail — the error is usually there")
	}
}

func TestTruncateDoesNotSplitRunes(t *testing.T) {
	// Cutting mid-rune would emit invalid UTF-8 into the context window.
	body := strings.Repeat("🦇", MaxResultBytes)
	got := Truncate(body)
	for i, r := range got {
		if r == '\uFFFD' {
			t.Fatalf("truncation split a rune at byte %d", i)
		}
	}
}

func TestSetFindAndNames(t *testing.T) {
	set := NewFS(t.TempDir()).Tools()
	if _, ok := set.Find("read"); !ok {
		t.Error("read should be present")
	}
	if _, ok := set.Find("nope"); ok {
		t.Error("nope should not be present")
	}
	names := set.Names()
	if len(names) != len(set) {
		t.Errorf("Names() = %v", names)
	}
}

func TestSchemasAreValidJSON(t *testing.T) {
	// A malformed schema is rejected by the provider mid-turn, which is a
	// miserable place to find out.
	set := append(NewFS(t.TempDir()).Tools(), NewExec(t.TempDir()).Tools()...)
	for _, tool := range set {
		t.Run(tool.Name, func(t *testing.T) {
			if tool.Desc == "" {
				t.Error("tool has no description")
			}
			var schema map[string]any
			if err := json.Unmarshal(tool.Schema, &schema); err != nil {
				t.Fatalf("schema does not parse: %v", err)
			}
			if schema["type"] != "object" {
				t.Errorf("schema type = %v, want object", schema["type"])
			}
			if _, ok := schema["properties"]; !ok {
				t.Error("schema has no properties")
			}
		})
	}
}

func TestMatchGlob(t *testing.T) {
	tests := []struct {
		pattern, path string
		want          bool
	}{
		{"*.go", "main.go", true},
		{"*.go", "internal/a/main.go", true}, // bare pattern matches the basename
		{"*.go", "main.rs", false},
		{"**/*.go", "a/b/c.go", true},
		{"**/*.go", "c.go", true}, // ** may consume zero segments
		{"a/**/b.go", "a/b.go", true},
		{"a/**/b.go", "a/x/y/b.go", true},
		{"a/**/b.go", "z/x/b.go", false},
		{"a/*/c.go", "a/b/c.go", true},
		{"a/*/c.go", "a/b/x/c.go", false}, // a single * must not cross separators
	}
	for _, tt := range tests {
		if got := matchGlob(tt.pattern, tt.path); got != tt.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", tt.pattern, tt.path, got, tt.want)
		}
	}
}
