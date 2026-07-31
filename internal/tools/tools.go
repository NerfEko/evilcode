// Package tools implements the agent's tool set (plan.md §17). Tools are a
// plain slice, not a registry: there is one process, one user, and a handful of
// tools — an indirection layer would buy nothing.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// MaxResultBytes caps what a tool may return. A tool that dumps a whole
// repository into the context window is a cost bug, not a feature.
const MaxResultBytes = 50 * 1024

// DiffStat counts changed lines, for the `(+8 -5)` badge on edit rows (§9.5).
type DiffStat struct {
	Added   int `json:"added"`
	Removed int `json:"removed"`
}

// Result is what a tool produces. Output is what the model sees; the rest is
// display metadata the TUI renders and the model never pays for.
type Result struct {
	// Output is the text handed back to the model.
	Output string

	// Diff is a unified diff for tools that changed a file, rendered by §9.3.
	Diff string

	// DiffStat accompanies Diff.
	DiffStat *DiffStat

	// Intent is a short human-readable summary for the tool row, when the tool
	// can describe itself better than its arguments do.
	Intent string
}

// Tool is one callable capability.
type Tool struct {
	Name   string
	Desc   string
	Schema json.RawMessage

	// Run executes the tool. A returned error becomes an error tool result the
	// model can read and recover from — it does not abort the turn.
	Run func(ctx context.Context, args json.RawMessage) (Result, error)
}

// Set is the collection of tools available to a turn.
type Set []Tool

// Find returns the tool with the given name.
func (s Set) Find(name string) (Tool, bool) {
	for _, t := range s {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}

// Names lists the tool names in order.
func (s Set) Names() []string {
	out := make([]string, len(s))
	for i, t := range s {
		out[i] = t.Name
	}
	return out
}

// Call is one requested invocation.
type Call struct {
	ID   string
	Name string
	Args json.RawMessage
}

// Outcome pairs a call with what running it produced.
type Outcome struct {
	Call   Call
	Result Result
	Err    error
}

// MaxConcurrent bounds parallel tool execution. Tools shell out and touch the
// filesystem; unbounded fan-out on a model that requests twenty calls would
// thrash the machine for no gain.
const MaxConcurrent = 8

// RunBatch executes calls concurrently and returns outcomes in the original
// order, so the transcript reads in the order the model asked rather than the
// order things happened to finish.
func (s Set) RunBatch(ctx context.Context, calls []Call) []Outcome {
	out := make([]Outcome, len(calls))
	sem := make(chan struct{}, MaxConcurrent)
	var wg sync.WaitGroup

	for i, call := range calls {
		wg.Add(1)
		go func(i int, call Call) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				out[i] = Outcome{Call: call, Err: ctx.Err()}
				return
			}
			out[i] = s.RunOne(ctx, call)
		}(i, call)
	}
	wg.Wait()
	return out
}

// RunOne executes a single call, converting a panic into an error rather than
// taking the process down: a bad tool argument must not kill a live session.
func (s Set) RunOne(ctx context.Context, call Call) (outcome Outcome) {
	outcome.Call = call
	defer func() {
		if r := recover(); r != nil {
			outcome.Err = fmt.Errorf("tool %q panicked: %v", call.Name, r)
		}
	}()

	tool, ok := s.Find(call.Name)
	if !ok {
		outcome.Err = fmt.Errorf("unknown tool %q (available: %s)",
			call.Name, strings.Join(s.Names(), ", "))
		return outcome
	}

	res, err := tool.Run(ctx, call.Args)
	res.Output = Truncate(res.Output)
	outcome.Result = res
	outcome.Err = err
	return outcome
}

// Truncate caps a result, keeping both ends: the head says what the thing is
// and the tail usually holds the error or the summary, so cutting only the
// middle keeps a truncated result useful.
func Truncate(s string) string {
	if len(s) <= MaxResultBytes {
		return s
	}
	const note = "\n\n[... %d bytes truncated; narrow the request to see the rest ...]\n\n"
	head := MaxResultBytes * 2 / 3
	tail := MaxResultBytes - head
	// Cut on rune boundaries so truncation never emits a broken sequence.
	head = backToRuneBoundary(s, head)
	tailStart := forwardToRuneBoundary(s, len(s)-tail)
	return s[:head] + fmt.Sprintf(note, tailStart-head) + s[tailStart:]
}

func backToRuneBoundary(s string, i int) int {
	for i > 0 && !utf8Start(s[i]) {
		i--
	}
	return i
}

func forwardToRuneBoundary(s string, i int) int {
	for i < len(s) && !utf8Start(s[i]) {
		i++
	}
	return i
}

// utf8Start reports whether b can begin a UTF-8 sequence (i.e. is not a
// continuation byte).
func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// unmarshalArgs decodes tool arguments, rejecting unknown fields so a model
// that misspells a parameter is told rather than silently getting a default.
func unmarshalArgs(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("bad arguments: %w", err)
	}
	return nil
}
