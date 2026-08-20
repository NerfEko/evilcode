package daemon

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"evilcode/internal/agent"
	"evilcode/internal/tools"
)

// askBroker is the server-owned implementation of the ask tool. The request
// remains here, not in a TUI goroutine, so closing every window does not leave
// the agent waiting on a channel that no process can answer.
type askBroker struct {
	mu       sync.Mutex
	next     uint64
	pending  map[string]*tools.AskRequest
	requests map[string]agent.AskEvent
	publish  func(agent.Event)
}

func newAskBroker() *askBroker {
	return &askBroker{
		pending:  map[string]*tools.AskRequest{},
		requests: map[string]agent.AskEvent{},
	}
}

func (b *askBroker) SetPublisher(publish func(agent.Event)) {
	b.mu.Lock()
	b.publish = publish
	b.mu.Unlock()
}

func (b *askBroker) Ask(ctx context.Context, req *tools.AskRequest) ([]string, error) {
	b.mu.Lock()
	b.next++
	id := fmt.Sprintf("ask-%d", b.next)
	req.ID = id
	e := agent.AskEvent{ID: id, Question: req.Question, Options: req.Options, Multi: req.Multi}
	b.pending[id] = req
	b.requests[id] = e
	publish := b.publish
	b.mu.Unlock()

	if publish != nil {
		publish(agent.Event{Kind: agent.EventAsk, Ask: &e})
	}

	select {
	case answer := <-req.Reply:
		return answer, nil
	case <-ctx.Done():
		b.mu.Lock()
		delete(b.pending, id)
		delete(b.requests, id)
		publish := b.publish
		b.mu.Unlock()
		if publish != nil {
			publish(agent.Event{Kind: agent.EventAskResolved, RequestID: id})
		}
		return nil, ctx.Err()
	}
}

func (b *askBroker) Answer(id string, labels []string) error {
	b.mu.Lock()
	req, ok := b.pending[id]
	if ok {
		delete(b.pending, id)
		delete(b.requests, id)
	}
	publish := b.publish
	b.mu.Unlock()
	if !ok {
		return fmt.Errorf("pending request %q was not found", id)
	}
	sent := false
	select {
	case req.Reply <- labels:
		sent = true
	default:
	}
	if publish != nil {
		publish(agent.Event{Kind: agent.EventAskResolved, RequestID: id})
	}
	if !sent {
		return fmt.Errorf("pending request %q is no longer accepting an answer", id)
	}
	return nil
}

func (b *askBroker) Snapshot() []agent.AskEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	ids := make([]string, 0, len(b.requests))
	for id := range b.requests {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]agent.AskEvent, 0, len(ids))
	for _, id := range ids {
		out = append(out, b.requests[id])
	}
	return out
}

// Cancel resolves every pending request when its session is shutting down.
// Otherwise a provider/tool goroutine could remain blocked on an ask after the
// daemon has already stopped accepting clients.
func (b *askBroker) Cancel() {
	b.mu.Lock()
	requests := make([]*tools.AskRequest, 0, len(b.pending))
	for id, req := range b.pending {
		delete(b.pending, id)
		delete(b.requests, id)
		requests = append(requests, req)
	}
	b.mu.Unlock()
	for _, req := range requests {
		select {
		case req.Reply <- nil:
		default:
		}
	}
}
