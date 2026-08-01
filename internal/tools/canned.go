package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// CannedResult pairs one tool call's exact arguments with the Result it
// produced, for Canned below.
type CannedResult struct {
	Name   string
	Args   json.RawMessage
	Result Result
}

// Canned builds a Set for screen recordings: each tool's Run returns a
// pre-recorded Result instead of touching the real filesystem or running real
// commands. It exists so a recording can replay a real captured transcript —
// real tool call arguments, real tool output — without depending on a
// particular repo checkout being present at record time.
//
// Lookup is keyed by name and the exact argument bytes, not call order, so
// concurrent calls within one batch (RunBatch's worker pool does not preserve
// dispatch order) each still resolve to their own recorded result.
func Canned(entries []CannedResult) Set {
	byKey := make(map[string]Result, len(entries))
	var names []string
	seen := make(map[string]bool)
	for _, e := range entries {
		byKey[cannedKey(e.Name, e.Args)] = e.Result
		if !seen[e.Name] {
			seen[e.Name] = true
			names = append(names, e.Name)
		}
	}
	set := make(Set, 0, len(names))
	for _, name := range names {
		set = append(set, Tool{
			Name: name,
			Run: func(ctx context.Context, args json.RawMessage) (Result, error) {
				r, ok := byKey[cannedKey(name, args)]
				if !ok {
					return Result{}, fmt.Errorf("canned: no recorded result for %s %s", name, args)
				}
				return r, nil
			},
		})
	}
	return set
}

// cannedKey canonicalizes args before keying: json.Marshal on a Go map
// always sorts keys alphabetically, but a canned entry's Args came from a
// captured session log that preserved whatever order the model originally
// emitted them in. Comparing raw bytes would miss a match every time that
// order wasn't already alphabetical.
func cannedKey(name string, args json.RawMessage) string {
	var v any
	if err := json.Unmarshal(args, &v); err == nil {
		if canon, err := json.Marshal(v); err == nil {
			args = canon
		}
	}
	return name + "\x00" + string(args)
}
