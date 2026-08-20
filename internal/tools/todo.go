package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"evilcode/internal/todo"
)

// todoArgs is the wire shape of a todo-tool call. History fields are absent by
// design: they are tool-owned, and a model that could write them could
// manufacture the evidence trail the gates read (plan.md §12.2).
type todoArgs struct {
	Items []todoItemArg `json:"items"`
	Plan  *todoPlanArg  `json:"plan,omitempty"`
	Goals []todoGoalArg `json:"goals,omitempty"`
}

type todoItemArg struct {
	ID                   string   `json:"id"`
	Content              string   `json:"content"`
	Status               string   `json:"status"`
	Priority             string   `json:"priority,omitempty"`
	Group                *string  `json:"group,omitempty"`
	BlockedBy            []string `json:"blocked_by,omitempty"`
	Confidence           *uint8   `json:"confidence,omitempty"`
	CompletionConfidence *uint8   `json:"completion_confidence,omitempty"`
}

type todoPlanArg struct {
	UserIntention         *string `json:"user_intention,omitempty"`
	UnderstandsUserIntent *uint8  `json:"understands_user_intent,omitempty"`
}

type todoGoalArg struct {
	Group              string  `json:"group"`
	FeedbackLoop       *string `json:"feedback_loop,omitempty"`
	ClosedFeedbackLoop *uint8  `json:"closed_feedback_loop,omitempty"`
	EndToEndOwnership  *uint8  `json:"end_to_end_ownership,omitempty"`
}

// todoDesc teaches the scoring vocabulary. The tool is only as good as the
// model's understanding of what the three scores mean, so the description
// spends its tokens there rather than on the schema, which the schema already
// covers.
const todoDesc = `Track genuinely multi-stage work as a todo list, and record how confident you are in it.

Do not call this just to make a plan visible. A todo update is not progress: after
creating or changing the list, immediately execute the next actionable item. For
a small task that fits in one or two actions, skip this tool.

Send the COMPLETE list every time; it replaces the stored one. Keep ids stable
so progress can be tracked across writes. Keep exactly one current item
in_progress, and do not mark an item completed until its result is observed.

Three scores drive the harness's quality gates. Be honest with them — they are
used to decide whether you get asked to double-check your work, and inflating
them to avoid that just produces wrong work with a confident label.

- confidence (per item, 0-100): how sure you are the PLAN for this item is
  right, before doing it.
- completion_confidence (per item, 0-100): how sure you are the work is
  actually DONE and correct. Base it on something you observed — a test that
  passed, output you read — not on having made the edit.
- understands_user_intent (plan, 0-100): how sure you are you know what the
  user actually wants. Low early is normal and fine; say so rather than
  guessing high.
- closed_feedback_loop (per goal, 0-100): whether something reported back on
  whether the work satisfies the requirement. Record feedback_loop as the
  concrete command and metric, e.g. "go test ./internal/auth/...".
- end_to_end_ownership (per goal, 0-100): whether you verified the whole goal
  works together, not just that each item was edited. A group cannot be
  completed until this is at least 96.

Group related items with the same group name so their goal can be scored.`

// NewTodo builds the todo tool over a store.
func NewTodo(store *todo.Store, onWrite func(todo.Result)) Tool {
	return Tool{
		Name: "todo",
		Desc: todoDesc,
		Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "items": {
      "type": "array",
      "description": "The complete todo list, replacing the stored one",
      "items": {
        "type": "object",
        "properties": {
          "id":       {"type": "string",  "description": "Stable identifier, kept across writes"},
          "content":  {"type": "string",  "description": "What the task is"},
          "status":   {"type": "string",  "enum": ["pending", "in_progress", "completed", "cancelled"]},
          "priority": {"type": "string",  "enum": ["high", "medium", "low"]},
          "group":    {"type": "string",  "description": "Goal this item belongs to"},
          "blocked_by": {"type": "array", "items": {"type": "string"}, "description": "Item ids that must finish first"},
          "confidence": {"type": "integer", "minimum": 0, "maximum": 100,
                         "description": "How sure you are the plan for this item is right"},
          "completion_confidence": {"type": "integer", "minimum": 0, "maximum": 100,
                         "description": "How sure you are it is actually done and correct, based on something observed"}
        },
        "required": ["id", "content", "status"]
      }
    },
    "plan": {
      "type": "object",
      "properties": {
        "user_intention": {"type": "string", "description": "What you believe the user wants"},
        "understands_user_intent": {"type": "integer", "minimum": 0, "maximum": 100}
      }
    },
    "goals": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "group":                {"type": "string"},
          "feedback_loop":        {"type": "string", "description": "The concrete command and metric"},
          "closed_feedback_loop": {"type": "integer", "minimum": 0, "maximum": 100},
          "end_to_end_ownership": {"type": "integer", "minimum": 0, "maximum": 100}
        },
        "required": ["group"]
      }
    }
  },
  "required": ["items"]
}`),
		Run: func(ctx context.Context, raw json.RawMessage) (Result, error) {
			var a todoArgs
			if err := unmarshalArgs(raw, &a); err != nil {
				return Result{}, err
			}

			w := todo.Write{Items: make([]todo.Item, 0, len(a.Items))}
			for _, in := range a.Items {
				w.Items = append(w.Items, todo.Item{
					ID:                   in.ID,
					Content:              in.Content,
					Status:               todo.Status(in.Status),
					Priority:             todo.Priority(in.Priority),
					Group:                in.Group,
					BlockedBy:            in.BlockedBy,
					Confidence:           in.Confidence,
					CompletionConfidence: in.CompletionConfidence,
				})
			}
			if a.Plan != nil {
				w.Plan = &todo.Plan{
					UserIntention:         a.Plan.UserIntention,
					UnderstandsUserIntent: a.Plan.UnderstandsUserIntent,
				}
			}
			for _, g := range a.Goals {
				w.Goals = append(w.Goals, todo.Goal{
					Group:              g.Group,
					FeedbackLoop:       g.FeedbackLoop,
					ClosedFeedbackLoop: g.ClosedFeedbackLoop,
					EndToEndOwnership:  g.EndToEndOwnership,
				})
			}

			res, err := store.Apply(w)
			if err != nil {
				return Result{}, err
			}
			if res.Rejected {
				// A rejection is an error the model can act on, and the stored
				// list is unchanged.
				return Result{}, fmt.Errorf("%s", res.Explanation)
			}
			if onWrite != nil {
				onWrite(res)
			}

			out := summarizeList(store.Items())
			if res.Immediate != "" {
				out += "\n\n" + res.Immediate
			}
			return Result{
				Output:  out,
				Intent:  res.Delta.Summary(),
				Display: res.Delta,
			}, nil
		},
	}
}

// summarizeList is what the model reads back. It is deliberately compact: the
// model just wrote this list, so echoing it in full is paid-for redundancy.
func summarizeList(items []todo.Item) string {
	if len(items) == 0 {
		return "todo list cleared"
	}
	done, total := 0, len(items)
	for _, i := range items {
		if i.Status == todo.StatusCompleted {
			done++
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d/%d complete", done, total)

	var open []string
	for _, i := range todo.SortItems(items) {
		if i.Status == todo.StatusPending || i.Status == todo.StatusInProgress {
			open = append(open, i.Content)
		}
	}
	if len(open) > 0 {
		shown := open
		if len(shown) > 5 {
			shown = shown[:5]
		}
		fmt.Fprintf(&b, "\nremaining: %s", strings.Join(shown, "; "))
		if len(open) > len(shown) {
			fmt.Fprintf(&b, " (+%d more)", len(open)-len(shown))
		}
	}
	return b.String()
}
