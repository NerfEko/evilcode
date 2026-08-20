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
	// ID is set by a remote runtime. Local TUI requests leave it empty.
	ID       string
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

// PendingAsk holds the question the UI is showing, and the ones behind it.
//
// One on screen at a time — two questions at once is a worse experience than
// answering them in turn — but the ones waiting are queued rather than
// discarded. They used to be: a second question overwrote the first, whose tool
// call was left blocked on a channel nobody held any more until the user
// interrupted the turn. The comment claiming the tool batch bounded this was
// wrong in the ordinary case, since a batch runs its calls concurrently.
type PendingAsk struct {
	mu     sync.Mutex
	req    *AskRequest
	queued []*AskRequest
}

// Set shows the question, or queues it behind the one already showing.
func (p *PendingAsk) Set(req *AskRequest) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.req == nil {
		p.req = req
		return
	}
	p.queued = append(p.queued, req)
}

// Get returns the question on screen, if any.
func (p *PendingAsk) Get() *AskRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.req
}

// Answer resolves the question on screen and shows the next one.
func (p *PendingAsk) Answer(labels []string) {
	p.mu.Lock()
	req := p.req
	p.req = nil
	if len(p.queued) > 0 {
		p.req, p.queued = p.queued[0], p.queued[1:]
	}
	p.mu.Unlock()
	reply(req, labels)
}

// Remove resolves one specific question with no answer, wherever it is in the
// queue. The tool calls this when its own call is cancelled, which is not the
// same as the user dismissing whatever happens to be on screen.
func (p *PendingAsk) Remove(req *AskRequest) {
	p.mu.Lock()
	if p.req == req {
		p.req = nil
		if len(p.queued) > 0 {
			p.req, p.queued = p.queued[0], p.queued[1:]
		}
	} else {
		for i, q := range p.queued {
			if q == req {
				p.queued = append(p.queued[:i], p.queued[i+1:]...)
				break
			}
		}
	}
	p.mu.Unlock()
	reply(req, nil)
}

// Cancel resolves every outstanding question with no answer.
func (p *PendingAsk) Cancel() {
	p.mu.Lock()
	reqs := append([]*AskRequest{p.req}, p.queued...)
	p.req, p.queued = nil, nil
	p.mu.Unlock()
	for _, req := range reqs {
		reply(req, nil)
	}
}

// reply hands an answer back without blocking: the Reply channel is buffered,
// and a UI that has gone away must not wedge the tool.
func reply(req *AskRequest, labels []string) {
	if req == nil {
		return
	}
	select {
	case req.Reply <- labels:
	default:
	}
}
