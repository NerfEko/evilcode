package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"evilcode/internal/memory"
)

// MemoryDisplay is the payload the recall tile renders from (plan.md §9.5).
type MemoryDisplay struct {
	Hits   []memory.Hit
	Tokens int
}

// NewMemory returns the remember, recall, and reflect tools (plan.md §19).
func NewMemory(m *memory.Manager) Set {
	return Set{rememberTool(m), recallTool(m), reflectTool(m)}
}

type rememberArgs struct {
	Text  string `json:"text"`
	Kind  string `json:"kind,omitempty"`
	Scope string `json:"scope,omitempty"`
}

const rememberDesc = `Store something worth remembering after this conversation ends.

Use it for things that stay true: a stated preference, a project convention, a
constraint, a decision and its reason. Do not use it for file contents, command
output, or anything you can read again in a second — a memory bank full of those
buries the memories that matter.

A near-identical memory is merged rather than duplicated, so restating a fact to
correct it is the right move.

Memories are project-scoped by default when a workspace is known. Pass scope
"global" for a preference or fact that should follow the user everywhere.`

func rememberTool(m *memory.Manager) Tool {
	return Tool{
		Name: "remember",
		Desc: rememberDesc,
		Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "text": {"type": "string", "description": "The fact, stated so it makes sense out of context"},
    "kind": {"type": "string", "enum": ["fact", "preference", "project", "episode"],
             "description": "fact: durable truth. preference: how the user wants things done. project: a convention of this codebase. episode: something that happened."},
    "scope": {"type": "string", "enum": ["project", "global"],
              "description": "project (default in a workspace) or global (visible in every workspace)"}
  },
  "required": ["text"]
}`),
		Run: func(ctx context.Context, raw json.RawMessage) (Result, error) {
			var a rememberArgs
			if err := unmarshalArgs(raw, &a); err != nil {
				return Result{}, err
			}
			if strings.TrimSpace(a.Text) == "" {
				return Result{}, fmt.Errorf("text is required")
			}
			kind := memory.Kind(a.Kind)
			if a.Kind == "" {
				kind = memory.KindFact
			}
			scope := memory.Scope(a.Scope)
			if scope != "" && !scope.Valid() {
				return Result{}, fmt.Errorf("unknown memory scope %q (want project or global)", a.Scope)
			}
			rec, merged, err := m.RememberWithScope(ctx, a.Text, kind, scope)
			if err != nil {
				return Result{}, err
			}
			if merged {
				return Result{
					Output: fmt.Sprintf("merged into an existing memory (#%d)", rec.ID),
					Intent: "merged a memory",
				}, nil
			}
			return Result{
				Output: fmt.Sprintf("remembered as #%d (%s)", rec.ID, rec.Kind),
				Intent: memory.Truncate(a.Text, 48),
			}, nil
		},
	}
}

type recallArgs struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

func recallTool(m *memory.Manager) Tool {
	return Tool{
		Name: "recall",
		Desc: "Search long-term memory. Relevant memories are already injected " +
			"automatically each turn, so reach for this only when you need something " +
			"the current turn did not surface.",
		Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "query": {"type": "string",  "description": "What to look for"},
    "limit": {"type": "integer", "description": "Maximum memories to return; defaults to 8"}
  },
  "required": ["query"]
}`),
		Run: func(ctx context.Context, raw json.RawMessage) (Result, error) {
			var a recallArgs
			if err := unmarshalArgs(raw, &a); err != nil {
				return Result{}, err
			}
			if strings.TrimSpace(a.Query) == "" {
				return Result{}, fmt.Errorf("query is required")
			}
			if !m.Enabled() {
				return Result{}, fmt.Errorf("memory is off; turn it on with /memory on")
			}
			limit := a.Limit
			if limit <= 0 {
				limit = 8
			}
			hits := m.Search(a.Query, m.QueryVector(ctx, a.Query), limit, memory.RecallThreshold)
			if len(hits) == 0 {
				return Result{Output: "no memories match " + a.Query}, nil
			}

			var b strings.Builder
			for _, h := range hits {
				fmt.Fprintf(&b, "#%d (%s) %s\n", h.ID, h.Kind, h.Text)
			}
			out := strings.TrimRight(b.String(), "\n")
			return Result{
				Output:  out,
				Intent:  fmt.Sprintf("%d memories for %q", len(hits), memory.Truncate(a.Query, 32)),
				Display: MemoryDisplay{Hits: hits, Tokens: memory.EstimateTokens(out)},
			}, nil
		},
	}
}

type reflectArgs struct {
	Question string `json:"question"`
}

func reflectTool(m *memory.Manager) Tool {
	return Tool{
		Name: "reflect",
		Desc: "Ask a question of the whole memory bank and get a synthesized answer. " +
			"Prefer recall for a single lookup; use reflect only when the answer spans " +
			"several memories rather than sitting in one.",
		Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "question": {"type": "string", "description": "The question to answer from memory"}
  },
  "required": ["question"]
}`),
		Run: func(ctx context.Context, raw json.RawMessage) (Result, error) {
			var a reflectArgs
			if err := unmarshalArgs(raw, &a); err != nil {
				return Result{}, err
			}
			if strings.TrimSpace(a.Question) == "" {
				return Result{}, fmt.Errorf("question is required")
			}
			answer, err := m.Reflect(ctx, a.Question)
			if err != nil {
				return Result{}, err
			}
			return Result{Output: answer, Intent: "reflected on memory"}, nil
		},
	}
}
