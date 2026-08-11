package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"evilcode/internal/todo"
)

func TestOvernightTaskCardsRequireEvidenceToValidateDoneWork(t *testing.T) {
	done := uint8(100)
	start := []todo.Item{
		{ID: "validated", Content: "run the test suite", Status: todo.StatusPending},
		{ID: "unvalidated", Content: "write the release note", Status: todo.StatusPending},
		{ID: "untouched", Content: "leave this alone", Status: todo.StatusPending},
	}
	after := []todo.Item{
		{ID: "validated", Content: "run the test suite", Status: todo.StatusCompleted, CompletionConfidence: &done},
		{ID: "unvalidated", Content: "write the release note", Status: todo.StatusCompleted},
		start[2],
	}

	var run Overnight
	run.StartWithSnapshot(time.Date(2026, 8, 10, 1, 0, 0, 0, time.UTC), GitSnapshot{}, start)
	run.BeginTurn()
	run.AddToolCheck(OvernightToolCheck{Name: "bash", Command: "go test ./...", Intent: "validated", Success: true})
	run.RecordTurn(time.Now(), 123, after)

	if len(run.Cards) != 2 {
		t.Fatalf("got %d task cards, want 2", len(run.Cards))
	}
	for _, card := range run.Cards {
		switch card.ID {
		case "validated":
			if !card.Validated {
				t.Errorf("validated card was not validated: %s", card.Validation)
			}
		case "unvalidated":
			if card.Validated || !strings.Contains(card.Validation, "completion-confidence") {
				t.Errorf("done card without evidence = validated=%v validation=%q", card.Validated, card.Validation)
			}
		default:
			t.Errorf("unexpected card %q", card.ID)
		}
	}
}

func TestOvernightDoesNotShareOneCheckAcrossMultipleCompletedTodos(t *testing.T) {
	done := uint8(100)
	start := []todo.Item{
		{ID: "api", Content: "test the API", Status: todo.StatusPending},
		{ID: "ui", Content: "test the UI", Status: todo.StatusPending},
	}
	after := []todo.Item{
		{ID: "api", Content: "test the API", Status: todo.StatusCompleted, CompletionConfidence: &done},
		{ID: "ui", Content: "test the UI", Status: todo.StatusCompleted, CompletionConfidence: &done},
	}
	var run Overnight
	run.StartWithSnapshot(time.Now(), GitSnapshot{}, start)
	run.AddToolCheck(OvernightToolCheck{Name: "bash", Command: "go test ./internal/api", Success: true})
	run.RecordTurn(time.Now(), 1, after)
	validated := 0
	for _, card := range run.Cards {
		if card.Validated {
			validated++
		}
		if card.ID == "ui" && card.Validated {
			t.Fatalf("the API command was attributed to the UI todo: %+v", run.Cards)
		}
	}
	if validated != 1 {
		t.Fatalf("one API check should validate exactly one todo, got %+v", run.Cards)
	}
}

func TestTruncateReportKeepsUTF8Valid(t *testing.T) {
	got := truncateReport(strings.Repeat("a", 8)+"é"+strings.Repeat("b", 8), 9)
	if !utf8.ValidString(got) {
		t.Fatalf("truncated report is invalid UTF-8: %q", got)
	}
}

func TestGitOutputCaptureIsBounded(t *testing.T) {
	var output boundedGitOutput
	payload := []byte(strings.Repeat("x", gitOutputLimit+1024))
	if n, err := output.Write(payload); err != nil || n != len(payload) {
		t.Fatalf("Write = %d, %v; want %d, nil", n, err, len(payload))
	}
	if output.Len() != gitOutputLimit || !output.truncated {
		t.Fatalf("capture = %d bytes, truncated=%v", output.Len(), output.truncated)
	}
}

func TestReportLineCountingSkipsLargeArtifacts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.bin")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(reportFileScanLimit + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	lines, skipped, err := countReportFileLines(path)
	if err != nil || lines != 0 || !skipped {
		t.Fatalf("count = %d, skipped=%v, err=%v", lines, skipped, err)
	}
}

func TestGitDiffStatExcludesPreflightUntrackedFilesExactly(t *testing.T) {
	repo := initOvernightGitRepo(t)
	oldName := "already here "
	if err := os.WriteFile(filepath.Join(repo, oldName), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	start := captureGit(repo)
	if err := os.WriteFile(filepath.Join(repo, "created.txt"), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stat := captureGitDiffStat(repo, start.Head, start.Dirty)
	if stat.Error != "" || stat.Files != 1 || stat.Added != 1 {
		t.Fatalf("diffstat = %+v", stat)
	}
	if strings.Contains(stat.Summary, oldName) || !strings.Contains(stat.Summary, "created.txt") {
		t.Fatalf("untracked summary = %q", stat.Summary)
	}
}

func TestFinishOvernightPublishesReportCompletion(t *testing.T) {
	repo := initOvernightGitRepo(t)
	model := &Model{cwd: repo, dataDir: t.TempDir()}
	model.overnight.StartWithSnapshot(time.Now(), captureGit(repo), nil)
	model.finishOvernight("test complete")

	deadline := time.Now().Add(3 * time.Second)
	var done *overnightReportCompletion
	for done == nil && time.Now().Before(deadline) {
		done = model.overnightReportDone.Swap(nil)
		if done == nil {
			time.Sleep(time.Millisecond)
		}
	}
	if done == nil {
		t.Fatal("timed out waiting for asynchronous report")
	}
	model.applyOvernightReportCompletion(done)
	if model.overnight.ReportPath == "" {
		t.Fatalf("completion did not publish report path: %+v", done)
	}
	if _, err := os.Stat(model.overnight.ReportPath); err != nil {
		t.Fatal(err)
	}
}

func TestOvernightReportContainsStopReasonDiffstatAndUnvalidatedTodo(t *testing.T) {
	repo := initOvernightGitRepo(t)
	file := filepath.Join(repo, "tracked.txt")
	if err := os.WriteFile(file, []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "add", "tracked.txt")
	runGitTest(t, repo, "commit", "-qm", "base")
	start := captureGit(repo)
	if start.Head == "" || start.Error != "" {
		t.Fatalf("preflight git snapshot = %+v", start)
	}
	if err := os.WriteFile(file, []byte("base\nchanged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("new\nfile\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	done := uint8(100)
	items := []todo.Item{
		{ID: "tests", Content: "run the test suite", Status: todo.StatusPending},
		{ID: "release", Content: "prepare the release note", Status: todo.StatusPending},
		{ID: "docs", Content: "update the changelog", Status: todo.StatusPending},
	}
	completed := []todo.Item{
		{ID: "tests", Content: "run the test suite", Status: todo.StatusCompleted, CompletionConfidence: &done},
		{ID: "release", Content: "prepare the release note", Status: todo.StatusCompleted},
		items[2],
	}
	var run Overnight
	run.StartWithSnapshot(time.Date(2026, 8, 10, 2, 0, 0, 0, time.UTC), start, items)
	run.BeginTurn()
	run.AddToolCheck(OvernightToolCheck{Name: "bash", Command: "go test ./...", Intent: "tests", Success: true})
	run.RecordTurn(time.Date(2026, 8, 10, 2, 1, 0, 0, time.UTC), 321, completed)
	run.Stop("reached the test stop cap")

	dataDir := t.TempDir()
	path, err := run.WriteReport(dataDir, repo, completed)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	html := string(contents)
	for _, want := range []string{
		"reached the test stop cap",
		"2 files",
		"+3 additions",
		"new.txt",
		"unvalidated",
		"prepare the release note",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("report does not contain %q", want)
		}
	}
	if strings.Contains(html, "<link") || strings.Contains(html, "src=") {
		t.Error("report references an external asset; it must be self-contained")
	}
	if got := strings.Count(html, `badge unvalidated`); got != 1 {
		t.Errorf("report has %d unvalidated task badges, want 1", got)
	}
	if got := LatestOvernightReport(dataDir); got != path {
		t.Errorf("latest report = %q, want %q", got, path)
	}
}

func initOvernightGitRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runGitTest(t, repo, "init", "-q")
	runGitTest(t, repo, "config", "user.email", "test@example.invalid")
	runGitTest(t, repo, "config", "user.name", "J10 test")
	return repo
}

func runGitTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}
