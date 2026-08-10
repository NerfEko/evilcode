package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	run.AddToolCheck(OvernightToolCheck{Name: "bash", Command: "go test ./...", Success: true})
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

func TestOvernightReportContainsStopReasonDiffstatAndUnvalidatedTodo(t *testing.T) {
	repo := t.TempDir()
	runGitTest(t, repo, "init", "-q")
	runGitTest(t, repo, "config", "user.email", "test@example.invalid")
	runGitTest(t, repo, "config", "user.name", "J10 test")
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
	run.AddToolCheck(OvernightToolCheck{Name: "bash", Command: "go test ./...", Success: true})
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
		"1 files",
		"+1 additions",
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

func runGitTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}
