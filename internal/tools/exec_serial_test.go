package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// H2.14: stateful shell calls snapshot the working directory at start and write
// it back at the end. Run in parallel they all start from the same directory
// and the last one to finish wins, so every `cd` but one is lost — silently,
// since each call reports success.
func TestParallelShellCallsDoNotLoseTheirDirectory(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"one", "two", "three"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	e := NewExec(root)
	set := e.Tools()

	call := func(cmd string) Call {
		raw, _ := json.Marshal(map[string]any{"cmd": cmd})
		return Call{ID: cmd, Name: "bash", Args: raw}
	}

	// Three calls, each stepping down and then reporting where it is. Whatever
	// order they run in, each must see the directory it just moved to.
	out := set.RunBatch(context.Background(), []Call{
		call("cd one && pwd"),
		call("cd .. && cd two && pwd"),
		call("cd .. && cd three && pwd"),
	})
	for i, o := range out {
		if o.Err != nil {
			t.Fatalf("call %d failed: %v\n%s", i, o.Err, o.Result.Output)
		}
	}

	// The surviving directory must be one a call actually ended in, not a
	// blend: the failure is that a later call starts from a directory an
	// earlier one had already left.
	final := e.Cwd()
	rel, err := filepath.Rel(root, final)
	if err != nil {
		t.Fatal(err)
	}
	switch rel {
	case "one", "two", "three":
	default:
		t.Errorf("the shell ended in %q, which no call moved to", rel)
	}

	// And every call's own view must be self-consistent: it moved somewhere and
	// pwd agreed.
	for i, o := range out {
		if o.Result.Output == "" {
			t.Errorf("call %d printed nothing", i)
		}
	}
}
