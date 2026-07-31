package agent

import "context"

// Hooks is the post-turn seam. Auto-poke, the todo quality gates, and the
// advisor all attach here (plan.md §12.4, §21) rather than being wired into the
// loop, so the loop stays readable and each of them can be tested alone.
type Hooks interface {
	// PostTurn runs when the model has stopped asking for tools. Returning
	// true means something was appended to the conversation and the loop
	// should run another iteration; false ends the turn.
	//
	// Every implementation needs a circuit breaker (plan.md §12.6): a hook
	// that always returns true is an infinite loop, and every one of those
	// listed in the plan happened somewhere for real.
	PostTurn(ctx context.Context, a *Agent) (appended bool, err error)
}

// HookFunc adapts a function to Hooks.
type HookFunc func(ctx context.Context, a *Agent) (bool, error)

func (f HookFunc) PostTurn(ctx context.Context, a *Agent) (bool, error) { return f(ctx, a) }

// Chain runs hooks in order and stops at the first that appends. Stopping
// early matters: two hooks both appending in one turn means two voices arguing
// with the model at once, which reads as noise.
type Chain []Hooks

func (c Chain) PostTurn(ctx context.Context, a *Agent) (bool, error) {
	for _, h := range c {
		appended, err := h.PostTurn(ctx, a)
		if err != nil {
			return appended, err
		}
		if appended {
			return true, nil
		}
	}
	return false, nil
}
