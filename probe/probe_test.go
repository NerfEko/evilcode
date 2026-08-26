//go:build probe

// Package probe runs the scenario files in probe/scenarios against a live
// evilcode under tmux and diffs the captured plain-text frames against
// probe/goldens (plan.md §14).
//
// It is behind the `probe` build tag because it needs tmux and a built binary,
// which a plain `go test ./...` should not require:
//
//	go build -o evilcode ./cmd/evilcode
//	go test -tags probe ./probe/...
//	UPDATE_GOLDENS=1 go test -tags probe ./probe/...
package probe

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(wd)
}

// runProbe shells out to probe.sh, which owns all tmux interaction. Driving the
// same script the agent drives by hand keeps one code path, not two.
func runProbe(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command(filepath.Join(root, "probe", "probe.sh"), args...)
	cmd.Dir = root
	// Its own tmux socket and frame directory, so a second probe run — another
	// `go test`, or an agent driving probe.sh by hand — cannot interleave with
	// this one's pane. The failure that motivates this is silent: the golden
	// comes out holding a different scenario's transcript.
	cmd.Env = append(os.Environ(),
		"PROBE_SCENARIO=",
		"PROBE_ID="+strconv.Itoa(os.Getpid()),
		"PROBE_FRAMES="+framesDir(root))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("probe.sh %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// framesDir is this test process's private capture directory. It stays under
// probe/frames so `probe.sh png` still finds a frame after a failing run.
func framesDir(root string) string {
	return filepath.Join(root, "probe", "frames", "run-"+strconv.Itoa(os.Getpid()))
}

func probeBinary(root string) string {
	if path := strings.TrimSpace(os.Getenv("EVILCODE_BIN")); path != "" {
		return path
	}
	return filepath.Join(root, "evilcode")
}

func TestScenarios(t *testing.T) {
	root := repoRoot(t)

	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed; probe scenarios need it")
	}
	if _, err := os.Stat(probeBinary(root)); err != nil {
		t.Skip("no evilcode binary; run: go build -o evilcode ./cmd/evilcode or set EVILCODE_BIN")
	}

	files, err := filepath.Glob(filepath.Join(root, "probe", "scenarios", "*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no scenario files found")
	}

	// Frames are diagnostic output for a failing run, so they survive it and are
	// cleared on the next one rather than deleted at the end.
	os.RemoveAll(framesDir(root))

	for _, file := range files {
		name := strings.TrimSuffix(filepath.Base(file), ".txt")
		t.Run(name, func(t *testing.T) {
			runScenario(t, root, file)
		})
	}
}

func runScenario(t *testing.T, root, file string) {
	t.Helper()
	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	// Always tear the session down, including on a failed assertion, so one
	// broken scenario cannot wedge the next.
	t.Cleanup(func() { runProbe(t, root, "kill") })

	scenario := ""

	for lineNo, line := range strings.Split(string(src), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		verb, rest, _ := strings.Cut(line, " ")
		rest = strings.TrimSpace(rest)

		switch verb {
		case "boot":
			args := []string{"boot"}
			if scenario != "" {
				args = append(args, "--scenario="+scenario)
			}
			runProbe(t, root, append(args, strings.Fields(rest)...)...)
		case "scenario":
			// Passed explicitly to the next boot rather than exported. An
			// environment variable leaked between subtests and produced
			// goldens containing a different scenario's transcript.
			scenario = rest
		case "keys":
			runProbe(t, root, append([]string{"keys"}, splitArgs(rest)...)...)
		case "serve":
			args := []string{"serve"}
			if scenario != "" {
				args = append(args, "--scenario="+scenario)
			}
			runProbe(t, root, args...)
		case "attach":
			runProbe(t, root, append([]string{"attach"}, splitArgs(rest)...)...)
		case "wait":
			runProbe(t, root, append([]string{"wait"}, splitArgs(rest)...)...)
		case "wait-pane":
			runProbe(t, root, append([]string{"wait-pane"}, splitArgs(rest)...)...)
		case "sleep":
			runProbe(t, root, append([]string{"sleep"}, splitArgs(rest)...)...)
		case "kill":
			runProbe(t, root, "kill")
		case "capture":
			checkGolden(t, root, rest)
		default:
			t.Fatalf("%s:%d: unknown verb %q", filepath.Base(file), lineNo+1, verb)
		}
	}
}

// splitArgs splits a scenario line into arguments, honoring double quotes so a
// key sequence containing spaces stays one argument. Splitting on whitespace
// would send "fix the clamp" as three separate key sequences, which tmux
// concatenates without the spaces.
func splitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote, has := false, false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			has = true
		case r == ' ' && !inQuote:
			if has {
				out = append(out, cur.String())
				cur.Reset()
				has = false
			}
		default:
			cur.WriteRune(r)
			has = true
		}
	}
	if has {
		out = append(out, cur.String())
	}
	return out
}

// scrub removes machine-specific text so a golden compares the same anywhere.
// The repo can be reachable by more than one path (a symlinked or bind-mounted
// home), and the TUI prints whichever one it was launched with, so both the
// literal and the resolved form are replaced. The build version is scrubbed
// too: the header badge changes on every release, and a golden that embeds it
// goes stale the moment anything else in the frame is updated (E10).
func scrub(s, root string) string {
	forms := []string{root}
	if resolved, err := filepath.EvalSymlinks(root); err == nil && resolved != root {
		forms = append(forms, resolved)
	}
	// Longest first, so a prefix does not shadow a longer match.
	sort.Slice(forms, func(i, j int) bool { return len(forms[i]) > len(forms[j]) })
	for _, f := range forms {
		s = strings.ReplaceAll(s, f, "<repo>")
	}
	s = versionBadge.ReplaceAllString(s, "evilcode · vX.Y.Z")
	return s
}

// versionBadge matches the header's `evilcode · v1.2.3` version badge.
var versionBadge = regexp.MustCompile(`evilcode · v[0-9]+\.[0-9]+\.[0-9]+`)

func checkGolden(t *testing.T, root, name string) {
	t.Helper()
	if name == "" {
		t.Fatal("capture needs a golden name")
	}
	runProbe(t, root, "frame", name)

	framePath := filepath.Join(framesDir(root), name+".txt")
	got, err := os.ReadFile(framePath)
	if err != nil {
		t.Fatalf("reading captured frame: %v", err)
	}
	// tmux pads every pane row out to the full height; trailing blank rows carry
	// no information and would make goldens churn on any pane-size change.
	gotText := scrub(strings.TrimRight(string(got), "\n \t"), root)

	goldenPath := filepath.Join(root, "probe", "goldens", name+".txt")
	if os.Getenv("UPDATE_GOLDENS") == "1" {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, []byte(gotText+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("updated golden %s", name)
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("no golden for %q (%v)\nrun: UPDATE_GOLDENS=1 go test -tags probe ./probe/...\ncaptured:\n%s",
			name, err, gotText)
	}
	wantText := scrub(strings.TrimRight(string(want), "\n \t"), root)
	if gotText != wantText {
		t.Errorf("frame %q does not match its golden\n--- want ---\n%s\n--- got ---\n%s\n\n"+
			"if the change is intended:\n  UPDATE_GOLDENS=1 go test -tags probe ./probe/...",
			name, wantText, gotText)
	}
}

// TestPaletteReservesNoLayoutHeight enforces plan.md invariant 3: opening the
// slash palette must never move the transcript. The palette is drawn over the
// finished frame, so every row above the composer has to be byte-identical
// before and after it opens.
//
// This is checked against captured frames rather than unit-tested because the
// failure mode is a layout interaction, which only exists once everything is
// composed together.
func TestPaletteReservesNoLayoutHeight(t *testing.T) {
	root := repoRoot(t)
	closed, err := os.ReadFile(filepath.Join(root, "probe", "goldens", "palette-closed.txt"))
	if err != nil {
		t.Skipf("no golden yet: %v", err)
	}
	open, err := os.ReadFile(filepath.Join(root, "probe", "goldens", "palette-open.txt"))
	if err != nil {
		t.Skipf("no golden yet: %v", err)
	}

	closedLines := strings.Split(strings.TrimRight(string(closed), "\n"), "\n")
	openLines := strings.Split(strings.TrimRight(string(open), "\n"), "\n")

	// Everything up to the composer row must be untouched. The composer itself
	// changes, because a "/" was typed into it.
	composer := -1
	for i, l := range closedLines {
		if strings.Contains(l, "1>") {
			composer = i
			break
		}
	}
	if composer < 0 {
		t.Fatalf("could not find the composer row in:\n%s", closed)
	}

	for i := 0; i < composer; i++ {
		if i >= len(openLines) {
			t.Fatalf("opening the palette removed row %d (%q)", i, closedLines[i])
		}
		if closedLines[i] != openLines[i] {
			t.Errorf("opening the palette moved the transcript at row %d:\n  closed: %q\n  open:   %q",
				i, closedLines[i], openLines[i])
		}
	}
}
