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
	f := tempFS(t, map[string]string{"a.txt": "x"}).WithConfine(true)
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
	f := NewFS(dir).WithConfine(true)
	if _, err := run(t, f.Tools(), "read", map[string]any{"path": "link.txt"}); err == nil {
		t.Error("a symlink pointing outside the workspace must be refused")
	}
}

func TestWorkspaceReachableThroughSymlink(t *testing.T) {
	// A workspace opened through a symlinked path must still accept its own
	// files, including ones that do not exist yet. Comparing an unresolved
	// path against a resolved root rejects everything.
	real := t.TempDir()
	if err := os.WriteFile(filepath.Join(real, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "workspace")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	f := NewFS(link).WithConfine(true)
	if _, err := run(t, f.Tools(), "read", map[string]any{"path": "a.txt"}); err != nil {
		t.Errorf("reading an existing file through a symlinked root: %v", err)
	}
	// A file whose parent directories do not exist yet is the harder case.
	if _, err := run(t, f.Tools(), "write", map[string]any{
		"path": "new/deep/file.txt", "content": "hi",
	}); err != nil {
		t.Errorf("writing a new nested file through a symlinked root: %v", err)
	}
	// Escaping must still be refused.
	if _, err := run(t, f.Tools(), "read", map[string]any{"path": "../../etc/passwd"}); err == nil {
		t.Error("escape through a symlinked root must still be refused")
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

func TestAnchoredReadOutput(t *testing.T) {
	f := tempFS(t, map[string]string{"a.go": "package main\n\nfunc main() {}\n"})
	f.WithAnchors(true)
	res, err := run(t, f.Tools(), "read", map[string]any{"path": "a.go"})
	if err != nil {
		t.Fatal(err)
	}
	// Each line carries `anchor|number| content`.
	for _, line := range strings.Split(strings.TrimRight(res.Output, "\n"), "\n") {
		parts := strings.SplitN(line, "|", 3)
		if len(parts) != 3 {
			t.Fatalf("line %q is not anchored", line)
		}
		if len(parts[0]) != AnchorLen {
			t.Errorf("anchor %q is %d chars, want %d", parts[0], len(parts[0]), AnchorLen)
		}
	}
	// Without anchors the classic numbering is used, so a model that cannot
	// handle anchors is unaffected.
	plain := tempFS(t, map[string]string{"a.go": "package main\n"})
	res, _ = run(t, plain.Tools(), "read", map[string]any{"path": "a.go"})
	if strings.Contains(res.Output, "|") {
		t.Errorf("anchors leaked into classic output: %q", res.Output)
	}
}

func TestAnchoredEditReplace(t *testing.T) {
	f := tempFS(t, map[string]string{"a.go": "package main\n\nfunc main() {}\n"})
	f.WithAnchors(true)
	set := f.Tools()

	if _, err := run(t, set, "read", map[string]any{"path": "a.go"}); err != nil {
		t.Fatal(err)
	}
	anchor := LineAnchor("func main() {}")

	res, err := run(t, set, "edit", map[string]any{
		"path": "a.go",
		"patches": []map[string]any{{
			"anchor": anchor, "op": "replace",
			"lines": []string{"func main() {", "\tprintln(\"hi\")", "}"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(f.Root, "a.go"))
	want := "package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n"
	if string(got) != want {
		t.Errorf("file =\n%q\nwant\n%q", got, want)
	}
	if res.DiffStat == nil || res.DiffStat.Added != 3 || res.DiffStat.Removed != 1 {
		t.Errorf("DiffStat = %+v, want +3 -1", res.DiffStat)
	}
}

func TestAnchoredEditInsertAndDelete(t *testing.T) {
	f := tempFS(t, map[string]string{"a.txt": "one\ntwo\nthree\n"})
	f.WithAnchors(true)
	set := f.Tools()
	run(t, set, "read", map[string]any{"path": "a.txt"})

	if _, err := run(t, set, "edit", map[string]any{
		"path": "a.txt",
		"patches": []map[string]any{
			{"anchor": LineAnchor("one"), "op": "insert_after", "lines": []string{"one-and-a-half"}},
			{"anchor": LineAnchor("three"), "op": "delete"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(f.Root, "a.txt"))
	if string(got) != "one\none-and-a-half\ntwo\n" {
		t.Errorf("file = %q", got)
	}
}

func TestStaleAnchorRefusedAfterExternalChange(t *testing.T) {
	// The corruption case: the file moved under the model between read and
	// edit. Applying a best-effort patch here is strictly worse than the retry
	// anchors were meant to save.
	f := tempFS(t, map[string]string{"a.txt": "one\ntwo\n"})
	f.WithAnchors(true)
	set := f.Tools()
	run(t, set, "read", map[string]any{"path": "a.txt"})
	anchor := LineAnchor("two")

	// Something else rewrites the file.
	time.Sleep(10 * time.Millisecond)
	os.WriteFile(filepath.Join(f.Root, "a.txt"), []byte("completely\ndifferent\ncontent\n"), 0o644)

	_, err := run(t, set, "edit", map[string]any{
		"path":    "a.txt",
		"patches": []map[string]any{{"anchor": anchor, "op": "delete"}},
	})
	if err == nil {
		t.Fatal("a stale anchor must be refused")
	}
	if !strings.Contains(err.Error(), "Re-read") {
		t.Errorf("err = %q, want it to say to re-read", err)
	}
	// Nothing may have been written.
	got, _ := os.ReadFile(filepath.Join(f.Root, "a.txt"))
	if string(got) != "completely\ndifferent\ncontent\n" {
		t.Errorf("the refused edit modified the file: %q", got)
	}
}

func TestAnchorWithoutReadRefused(t *testing.T) {
	f := tempFS(t, map[string]string{"a.txt": "one\n"})
	f.WithAnchors(true)
	_, err := run(t, f.Tools(), "edit", map[string]any{
		"path":    "a.txt",
		"patches": []map[string]any{{"anchor": "abcd", "op": "delete"}},
	})
	if err == nil || !strings.Contains(err.Error(), "not read this file") {
		t.Errorf("err = %v, want a refusal naming the missing read", err)
	}
}

func TestUnknownAnchorRefused(t *testing.T) {
	f := tempFS(t, map[string]string{"a.txt": "one\ntwo\n"})
	f.WithAnchors(true)
	set := f.Tools()
	run(t, set, "read", map[string]any{"path": "a.txt"})

	_, err := run(t, set, "edit", map[string]any{
		"path":    "a.txt",
		"patches": []map[string]any{{"anchor": "ffff", "op": "delete"}},
	})
	if err == nil || !strings.Contains(err.Error(), "not in the version you read") {
		t.Errorf("err = %v", err)
	}
}

func TestAmbiguousAnchorRefused(t *testing.T) {
	// Two identical lines share an anchor. Guessing which one was meant is how
	// an anchored edit silently changes the wrong line.
	f := tempFS(t, map[string]string{"a.txt": "same\nother\nsame\n"})
	f.WithAnchors(true)
	set := f.Tools()
	run(t, set, "read", map[string]any{"path": "a.txt"})

	_, err := run(t, set, "edit", map[string]any{
		"path":    "a.txt",
		"patches": []map[string]any{{"anchor": LineAnchor("same"), "op": "delete"}},
	})
	if err == nil || !strings.Contains(err.Error(), "identical lines") {
		t.Errorf("err = %v, want an ambiguity refusal", err)
	}
	got, _ := os.ReadFile(filepath.Join(f.Root, "a.txt"))
	if string(got) != "same\nother\nsame\n" {
		t.Errorf("the refused edit modified the file: %q", got)
	}
}

func TestPartlyInvalidPatchSetChangesNothing(t *testing.T) {
	// Resolution happens fully before mutation, so a set with one bad patch
	// leaves the file untouched rather than half-applied.
	f := tempFS(t, map[string]string{"a.txt": "one\ntwo\nthree\n"})
	f.WithAnchors(true)
	set := f.Tools()
	run(t, set, "read", map[string]any{"path": "a.txt"})

	_, err := run(t, set, "edit", map[string]any{
		"path": "a.txt",
		"patches": []map[string]any{
			{"anchor": LineAnchor("one"), "op": "delete"},
			{"anchor": "ffff", "op": "delete"},
		},
	})
	if err == nil {
		t.Fatal("want a refusal")
	}
	got, _ := os.ReadFile(filepath.Join(f.Root, "a.txt"))
	if string(got) != "one\ntwo\nthree\n" {
		t.Errorf("file was partly modified: %q", got)
	}
}

func TestClassicEditStillWorksWithAnchorsOn(t *testing.T) {
	// Anchors are additive; the exact-string form must keep working for models
	// that do not use them.
	f := tempFS(t, map[string]string{"a.txt": "hello\n"})
	f.WithAnchors(true)
	if _, err := run(t, f.Tools(), "edit", map[string]any{
		"path": "a.txt", "old": "hello", "new": "goodbye",
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(f.Root, "a.txt"))
	if string(got) != "goodbye\n" {
		t.Errorf("file = %q", got)
	}
}

func TestEditWithNeitherFormRefused(t *testing.T) {
	f := tempFS(t, map[string]string{"a.txt": "x\n"})
	_, err := run(t, f.Tools(), "edit", map[string]any{"path": "a.txt"})
	if err == nil || !strings.Contains(err.Error(), "either patches") {
		t.Errorf("err = %v", err)
	}
}

func TestLineAnchorIsWhitespaceSensitive(t *testing.T) {
	// Two lines differing only in indentation are different lines to an edit,
	// so they must not share an anchor.
	if LineAnchor("func main() {") == LineAnchor("\tfunc main() {") {
		t.Error("indentation must affect the anchor")
	}
	if LineAnchor("a") != LineAnchor("a") {
		t.Error("anchors must be stable")
	}
}

func TestUnconfinedIsTheDefault(t *testing.T) {
	// evilcode runs on the user's own machine as the user. Refusing to read a
	// file one directory over is friction, not protection, so confinement is
	// opt-in.
	outside := t.TempDir()
	target := filepath.Join(outside, "neighbour.txt")
	if err := os.WriteFile(target, []byte("readable"), 0o644); err != nil {
		t.Fatal(err)
	}

	f := tempFS(t, nil)
	if f.Confine {
		t.Error("confinement must be off unless asked for")
	}
	res, err := run(t, f.Tools(), "read", map[string]any{"path": target})
	if err != nil {
		t.Fatalf("an unconfined session should read any readable path: %v", err)
	}
	if !strings.Contains(res.Output, "readable") {
		t.Errorf("output = %q", res.Output)
	}
}

func TestConfineExplainsHowToTurnItOff(t *testing.T) {
	// A refusal the user cannot act on is just an obstacle.
	f := tempFS(t, nil).WithConfine(true)
	_, err := run(t, f.Tools(), "read", map[string]any{"path": "/etc/hostname"})
	if err == nil {
		t.Fatal("want a refusal")
	}
	if !strings.Contains(err.Error(), "confine_to_workspace") {
		t.Errorf("err = %q, want it to name the setting", err)
	}
}
