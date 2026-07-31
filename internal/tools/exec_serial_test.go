package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// H2.14: stateful shell calls snapshot the working directory at start and write
// it back at the end. Run in parallel they overlap, so every `cd` but one is
// lost and a call does its work in a directory another call has already moved
// away from — silently, since each reports success.
//
// The property is mutual exclusion, and each call proves it about itself: it
// claims a marker, holds it briefly, and reports if it ever sees another call's
// marker alongside its own.
func TestStatefulShellCallsDoNotOverlap(t *testing.T) {
	dir := t.TempDir()
	e := NewExec(dir)
	e.Timeout = 30 * time.Second

	call := func(id string) Call {
		script := "touch " + id + ".claim; sleep 0.2; " +
			"n=$(ls *.claim | wc -l); if [ \"$n\" != 1 ]; then echo OVERLAP; fi; " +
			"rm -f " + id + ".claim"
		raw, _ := json.Marshal(map[string]any{"cmd": script})
		return Call{ID: id, Name: "bash", Args: raw}
	}

	out := e.Tools().RunBatch(context.Background(),
		[]Call{call("a"), call("b"), call("c"), call("d")})

	for i, o := range out {
		if o.Err != nil {
			t.Fatalf("call %d failed: %v\n%s", i, o.Err, o.Result.Output)
		}
		if strings.Contains(o.Result.Output, "OVERLAP") {
			t.Errorf("call %d ran while another was still going; a stateful shell "+
				"cannot run in parallel with itself", i)
		}
	}
}

// And the directory a call moves to is the one the next call starts in, which
// is the behaviour the serialization exists to protect.
func TestTheWorkingDirectoryCarriesToTheNextCall(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	e := NewExec(dir)
	e.Timeout = 30 * time.Second
	set := e.Tools()

	run := func(cmd string) Result {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"cmd": cmd})
		out := set.RunOne(context.Background(), Call{ID: cmd, Name: "bash", Args: raw})
		if out.Err != nil {
			t.Fatalf("%s: %v\n%s", cmd, out.Err, out.Result.Output)
		}
		return out.Result
	}

	run("cd sub")
	if got := strings.TrimSpace(run("pwd").Output); !strings.HasSuffix(got, "/sub") {
		t.Errorf("the next call started in %q, not where the previous one moved to", got)
	}
}
