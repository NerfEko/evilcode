package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// AskOption is one offered answer.
type AskOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// AskRequest is a pending question for the user.
type AskRequest struct {
	Question string
	Options  []AskOption
	Multi    bool

	// Reply carries the chosen labels back. It is buffered so the asker never
	// blocks on a UI that has already gone away.
	Reply chan []string
}

// Asker is the seam between the tool and whatever surface presents the
// question. The TUI renders an inline picker; a headless run answers
// immediately with the default.
type Asker interface {
	// Ask presents the question and blocks until answered or the context ends.
	Ask(ctx context.Context, req *AskRequest) ([]string, error)
}

// AskFunc adapts a function to Asker.
type AskFunc func(ctx context.Context, req *AskRequest) ([]string, error)

func (f AskFunc) Ask(ctx context.Context, req *AskRequest) ([]string, error) {
	return f(ctx, req)
}

// NewAsk builds the ask tool over a presenter.
func NewAsk(asker Asker) Tool {
	return Tool{
		Name: "ask",
		Desc: "Ask the user a question with specific options, when the answer would change " +
			"what you build and you cannot reasonably pick for them. Prefer deciding yourself " +
			"and saying which way you went; use this for genuine forks, not for permission.",
		Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "question": {"type": "string", "description": "The question, as one clear sentence"},
    "options": {
      "type": "array",
      "description": "The choices. Put your recommendation first.",
      "items": {
        "type": "object",
        "properties": {
          "label":       {"type": "string", "description": "Short choice text"},
          "description": {"type": "string", "description": "What choosing this means"}
        },
        "required": ["label"]
      }
    },
    "multi": {"type": "boolean", "description": "Allow selecting more than one option"}
  },
  "required": ["question", "options"]
}`),
		Run: func(ctx context.Context, raw json.RawMessage) (Result, error) {
			var a struct {
				Question string      `json:"question"`
				Options  []AskOption `json:"options"`
				Multi    bool        `json:"multi,omitempty"`
			}
			if err := unmarshalArgs(raw, &a); err != nil {
				return Result{}, err
			}
			if strings.TrimSpace(a.Question) == "" {
				return Result{}, fmt.Errorf("question is required")
			}
			if len(a.Options) < 2 {
				return Result{}, fmt.Errorf(
					"ask needs at least two options; with one there is nothing to choose")
			}
			if asker == nil {
				return Result{}, fmt.Errorf("no way to ask the user in this mode")
			}

			req := &AskRequest{
				Question: a.Question,
				Options:  a.Options,
				Multi:    a.Multi,
				Reply:    make(chan []string, 1),
			}
			chosen, err := asker.Ask(ctx, req)
			if err != nil {
				return Result{}, err
			}
			if len(chosen) == 0 {
				return Result{Output: "The user did not answer."}, nil
			}
			return Result{
				Output: "The user chose: " + strings.Join(chosen, ", "),
				Intent: truncate(strings.Join(chosen, ", "), 40),
			}, nil
		},
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// PendingAsk holds the question the UI is currently showing. It is a single
// slot rather than a queue: two questions on screen at once is a worse
// experience than serializing them, and the tool batch already bounds how many
// can be in flight.
type PendingAsk struct {
	mu  sync.Mutex
	req *AskRequest
}

// Set stores the pending question.
func (p *PendingAsk) Set(req *AskRequest) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.req = req
}

// Get returns the pending question, if any.
func (p *PendingAsk) Get() *AskRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.req
}

// Answer resolves the pending question and clears the slot.
func (p *PendingAsk) Answer(labels []string) {
	p.mu.Lock()
	req := p.req
	p.req = nil
	p.mu.Unlock()
	if req != nil {
		select {
		case req.Reply <- labels:
		default:
		}
	}
}
